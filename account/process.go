package account

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/apiclient"
	"go.vocdoni.io/proto/build/go/models"
)

// electionMetadataVersion is the schema version written into ElectionMetadata.
const electionMetadataVersion = "1.0"

// defaultElectionType is the metadata type used when ElectionParams.TypeMetadata is nil.
const defaultElectionType = "single-choice-multiquestion"

// BuildElectionMetadata maps the high-level ElectionParams into an on-chain
// ElectionMetadata document and returns its JSON encoding. The returned bytes are
// stored content-addressed; their public URL becomes the on-chain process metadata.
func BuildElectionMetadata(params *db.ElectionParams) ([]byte, error) {
	if params == nil {
		return nil, fmt.Errorf("nil election params")
	}
	meta := &api.ElectionMetadata{
		Title:       api.LanguageString(params.Title),
		Version:     electionMetadataVersion,
		Description: api.LanguageString(params.Description),
		Media: api.ProcessMedia{
			Header:    params.Header,
			StreamURI: params.StreamURI,
		},
		Questions: make([]api.Question, 0, len(params.Questions)),
	}
	if params.TypeMetadata != nil {
		meta.Type = api.ElectionProperties{Name: params.TypeMetadata.Name, Properties: params.TypeMetadata.Properties}
	} else {
		meta.Type = api.ElectionProperties{Name: defaultElectionType}
	}
	for _, q := range params.Questions {
		question := api.Question{
			Title:       api.LanguageString(q.Title),
			Description: api.LanguageString(q.Description),
			Choices:     make([]api.ChoiceMetadata, 0, len(q.Choices)),
		}
		for _, ch := range q.Choices {
			question.Choices = append(question.Choices, api.ChoiceMetadata{
				Title: api.LanguageString(ch.Title),
				Value: ch.Value,
			})
		}
		meta.Questions = append(meta.Questions, question)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("could not marshal election metadata: %w", err)
	}
	return data, nil
}

// NewProcessParams bundles the inputs required to build a NewProcess transaction.
type NewProcessParams struct {
	OrgAddress common.Address // organization (entity) that owns the election
	Params     *db.ElectionParams
	CensusRoot []byte // census root: the CSP ECDSA public key, or its blind public key when Anonymous
	CensusURI  string // census endpoint (SaaS CSP base URL)
	// Anonymous selects blind CSP: the election is published with census origin OFF_CHAIN_CA_V2
	// (the CSP blind-signs ballots so it cannot link a signature to the voter). CensusRoot must then
	// be the CSP blind public key. This is unrelated to EnvelopeType.Anonymous (the zk-SNARK flag),
	// which stays false — the Vochain routes both CA origins to the CSP verifier regardless of it.
	Anonymous   bool
	MetadataURL string // public https URL of the stored ElectionMetadata JSON
	// Nonce, when set, is used as the tx account nonce instead of reading the current
	// on-chain nonce. Batch publishing sets explicit consecutive nonces so N txs can be
	// signed and submitted together; single publishes leave it nil to read the nonce.
	Nonce *uint32
	// InitialStatus is the on-chain status the election is created with. Only READY (the
	// default when unset / PROCESS_UNKNOWN) and PAUSED are accepted, matching the vochain
	// whitelist for NewProcess. PAUSED requires Mode.Interruptible so the admin can later
	// unpause; BuildNewProcessTx forces the flag on when this is PAUSED, since a paused
	// election that can never be started would be permanently unreachable.
	InitialStatus models.ProcessStatus
}

// resolveInitialStatus validates s and returns the on-chain status to publish an election
// with. The zero value (PROCESS_UNKNOWN) resolves to READY, preserving the historical
// behaviour for callers that do not set InitialStatus. Any status other than READY or
// PAUSED is rejected — the vochain refuses the transaction otherwise (see
// vocdoni-node vochain/transaction/election_tx.go).
func resolveInitialStatus(s models.ProcessStatus) (models.ProcessStatus, error) {
	switch s {
	case models.ProcessStatus_PROCESS_UNKNOWN, models.ProcessStatus_READY:
		return models.ProcessStatus_READY, nil
	case models.ProcessStatus_PAUSED:
		return models.ProcessStatus_PAUSED, nil
	default:
		return 0, fmt.Errorf("initialStatus must be READY or PAUSED, got %s", s)
	}
}

// electionStartDuration maps the high-level start/end dates to the on-chain
// StartTime and Duration (seconds). A zero startTime means "start immediately".
// When startDate is already in the past the election starts now and must still
// end at endDate, so the duration is measured from now rather than from
// startDate (which would otherwise overrun endDate).
func electionStartDuration(startDate, endDate time.Time) (startTime, duration uint32, err error) {
	if endDate.IsZero() {
		return 0, 0, fmt.Errorf("endDate is required")
	}
	if !startDate.IsZero() {
		if !endDate.After(startDate) {
			return 0, 0, fmt.Errorf("endDate must be after startDate")
		}
		if startDate.After(time.Now()) {
			// range-check before truncating to the on-chain uint32 types: a wrapped
			// start/duration would otherwise set a bogus election window and could slip a
			// too-long duration under the plan limit (which compares the already-cast value).
			startSec := startDate.Unix()
			dur := endDate.Sub(startDate).Seconds()
			if startSec < 0 || startSec > math.MaxUint32 {
				return 0, 0, fmt.Errorf("startDate out of range")
			}
			if dur < 0 || dur > math.MaxUint32 {
				return 0, 0, fmt.Errorf("duration out of range")
			}
			return uint32(startSec), uint32(dur), nil
		}
	}
	d := time.Until(endDate)
	if d <= 0 {
		return 0, 0, fmt.Errorf("endDate must be in the future")
	}
	if secs := d.Seconds(); secs > math.MaxUint32 {
		return 0, 0, fmt.Errorf("duration out of range")
	}
	return 0, uint32(d.Seconds()), nil
}

// BuildNewProcessTx constructs an unsigned Tx_NewProcess from high-level election
// params. It reads the organization account's current on-chain nonce, maps
// ElectionParams into the on-chain models.Process using a CSP census, and always
// sets EnvelopeType and VoteOptions (the funder needs them to price the tx).
func (a *Account) BuildNewProcessTx(p *NewProcessParams) (*models.Tx, error) {
	if p == nil || p.Params == nil {
		return nil, fmt.Errorf("nil new process params")
	}
	ep := p.Params
	if ep.MaxCensusSize == 0 {
		return nil, fmt.Errorf("maxCensusSize must be greater than zero")
	}
	initialStatus, err := resolveInitialStatus(p.InitialStatus)
	if err != nil {
		return nil, err
	}
	nonce := p.Nonce
	if nonce == nil {
		acc, err := a.client.Account(p.OrgAddress.String())
		if err != nil {
			return nil, fmt.Errorf("could not fetch organization account: %w", err)
		}
		nonce = &acc.Nonce
	}

	startTime, duration, err := electionStartDuration(ep.StartDate, ep.EndDate)
	if err != nil {
		return nil, err
	}

	// blind CSP publishes under the V2 origin (weight-binding salt); the plain ECDSA CSP stays on
	// the legacy origin. Both are verified on-chain by the CSP verifier; the flag never touches
	// EnvelopeType.Anonymous.
	censusOrigin := models.CensusOrigin_OFF_CHAIN_CA
	if p.Anonymous {
		censusOrigin = models.CensusOrigin_OFF_CHAIN_CA_V2
	}

	// a PAUSED election can only be resumed by SET_PROCESS_STATUS, which the vochain refuses
	// unless the process is interruptible; a paused non-interruptible election would be
	// permanently stuck, so force the flag on when publishing paused (whatever the caller set).
	interruptible := ep.ElectionType.Interruptible
	if initialStatus == models.ProcessStatus_PAUSED {
		interruptible = true
	}

	metadataURL := p.MetadataURL
	process := &models.Process{
		EntityId:      p.OrgAddress.Bytes(),
		Status:        initialStatus,
		StartTime:     startTime,
		Duration:      duration,
		CensusOrigin:  censusOrigin,
		CensusRoot:    p.CensusRoot,
		MaxCensusSize: ep.MaxCensusSize,
		Metadata:      &metadataURL,
		EnvelopeType: &models.EnvelopeType{
			Serial:         false,
			Anonymous:      ep.ElectionType.Anonymous,
			EncryptedVotes: ep.ElectionType.SecretUntilTheEnd,
			UniqueValues:   ep.VoteType.UniqueChoices,
			CostFromWeight: ep.VoteType.CostFromWeight,
		},
		VoteOptions: &models.ProcessVoteOptions{
			MaxCount:          ep.VoteType.MaxCount,
			MaxValue:          ep.VoteType.MaxValue,
			MaxVoteOverwrites: ep.VoteType.MaxVoteOverwrites,
			MaxTotalCost:      ep.VoteType.MaxTotalCost,
			CostExponent:      ep.VoteType.CostExponent,
		},
		Mode: &models.ProcessMode{
			AutoStart:     ep.ElectionType.Autostart,
			Interruptible: interruptible,
			DynamicCensus: ep.ElectionType.DynamicCensus,
		},
	}
	if p.CensusURI != "" {
		censusURI := p.CensusURI
		process.CensusURI = &censusURI
	}
	return &models.Tx{
		Payload: &models.Tx_NewProcess{
			NewProcess: &models.NewProcessTx{
				Txtype:  models.TxType_NEW_PROCESS,
				Nonce:   *nonce,
				Process: process,
			},
		},
	}, nil
}

// BuildSetProcessStatusTx builds an unsigned SET_PROCESS_STATUS transaction that
// moves the on-chain election identified by processID to the given status. It reads
// the organization account's current nonce so the funder/signer can complete it.
func (a *Account) BuildSetProcessStatusTx(
	orgAddress common.Address, processID []byte, status models.ProcessStatus,
) (*models.Tx, error) {
	if len(processID) == 0 {
		return nil, fmt.Errorf("empty process id")
	}
	acc, err := a.client.Account(orgAddress.String())
	if err != nil {
		return nil, fmt.Errorf("could not fetch organization account: %w", err)
	}
	st := status
	return &models.Tx{
		Payload: &models.Tx_SetProcess{
			SetProcess: &models.SetProcessTx{
				Txtype:    models.TxType_SET_PROCESS_STATUS,
				Nonce:     acc.Nonce,
				ProcessId: processID,
				Status:    &st,
			},
		},
	}, nil
}

// BuildSetProcessCensusTx builds an unsigned SET_PROCESS_CENSUS transaction that raises a published
// election's maxCensusSize (keeping its census root/URI). The chain accepts a size increase without
// DynamicCensus as long as the new size is not smaller than the current one; resend the existing
// censusRoot/censusURI so they are preserved rather than cleared.
func (a *Account) BuildSetProcessCensusTx(
	orgAddress common.Address, processID, censusRoot []byte, censusURI string, maxCensusSize uint64,
) (*models.Tx, error) {
	if len(processID) == 0 {
		return nil, fmt.Errorf("empty process id")
	}
	acc, err := a.client.Account(orgAddress.String())
	if err != nil {
		return nil, fmt.Errorf("could not fetch organization account: %w", err)
	}
	uri := censusURI
	size := maxCensusSize
	return &models.Tx{
		Payload: &models.Tx_SetProcess{
			SetProcess: &models.SetProcessTx{
				Txtype:     models.TxType_SET_PROCESS_CENSUS,
				Nonce:      acc.Nonce,
				ProcessId:  processID,
				CensusRoot: censusRoot,
				CensusURI:  &uri,
				CensusSize: &size,
			},
		},
	}, nil
}

// SubmitSignedTx submits an already-signed transaction to the chain and waits
// (up to 40s) until it is mined. It returns the transaction response data — for a
// NewProcess transaction this is the on-chain process id.
func (a *Account) SubmitSignedTx(stx []byte) ([]byte, error) {
	hash, data, err := a.client.SendTx(stx)
	if err != nil {
		return nil, fmt.Errorf("could not submit signed tx: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*40)
	defer cancel()
	if _, err := a.client.WaitUntilTxIsMined(ctx, hash); err != nil {
		return nil, fmt.Errorf("could not wait for tx to be mined: %w", err)
	}
	return data, nil
}

// Election fetches the current on-chain state of the election (process) with
// the given id from the Vochain.
func (a *Account) Election(processID []byte) (*api.Election, error) {
	election, err := a.client.Election(processID)
	if err != nil {
		return nil, fmt.Errorf("could not fetch election %x: %w", processID, err)
	}
	return election, nil
}

// ElectionMemos pages every vote of the on-chain election with the given id and returns each
// non-empty voter memo cast alongside the open choice, one entry per matching vote (so a memo
// cast N times appears N times). A memo rides on the whole vote, so we correlate it to a choice
// via the vote package: the votes-list page carries the memo but not the package, so for each
// memo-carrying vote we fetch the single vote (which the node decrypts at RESULTS) and keep the
// memo only if selectsOpen reports its decoded values selected the open choice (the predicate
// owns the ballot layout — see OpenChoiceMatcher). The node's votes-list endpoint returns an
// empty page past the last one, which terminates the loop.
//
// ponytail: pages the whole election and does one extra GET per memo-carrying vote (N+1); fine for the
// manager-only memo resolution folded into the results reads. Add saas-side pagination / a bounded
// fetch pool if a process ever accumulates memos in the millions.
func (a *Account) ElectionMemos(electionID []byte, selectsOpen func(votes []int) bool) ([]string, error) {
	var memos []string
	for page := 0; ; page++ {
		resp, code, err := a.client.Request(http.MethodGet, nil,
			"elections", internal.HexBytes(electionID).String(), "votes", "page", strconv.Itoa(page))
		if err != nil {
			return nil, fmt.Errorf("could not fetch votes of election %x: %w", electionID, err)
		}
		if code != http.StatusOK {
			return nil, fmt.Errorf("could not fetch votes of election %x: unexpected status %d (%s)",
				electionID, code, resp)
		}
		var list api.VotesList
		if err := json.Unmarshal(resp, &list); err != nil {
			return nil, fmt.Errorf("could not decode votes of election %x: %w", electionID, err)
		}
		if len(list.Votes) == 0 {
			return memos, nil
		}
		for _, v := range list.Votes {
			if len(v.Memo) == 0 {
				continue
			}
			votes, err := a.voteSelectedValues(internal.HexBytes(v.VoteID))
			if err != nil {
				// the list page and the per-vote fetch can disagree transiently (indexing lag), so a
				// vote the chain does not know yet only drops its own memo, not the whole list.
				if errors.Is(err, ErrVoteNotFound) {
					continue
				}
				return nil, err
			}
			if selectsOpen(votes) {
				memos = append(memos, string(v.Memo))
			}
		}
	}
}

// voteSelectedValues fetches a single vote by its id and returns the choice values it selected
// (VotePackage.Votes). The node decrypts the package for encrypted elections once keys are revealed
// (RESULTS); if the package is still opaque/absent it returns no values, so the caller drops the memo.
func (a *Account) voteSelectedValues(voteID internal.HexBytes) ([]int, error) {
	vote, err := a.VoteByNullifier(voteID)
	if err != nil {
		return nil, err
	}
	if len(vote.VotePackage) == 0 {
		return nil, nil
	}
	var pkg struct {
		Votes []int `json:"votes"`
	}
	if err := json.Unmarshal(vote.VotePackage, &pkg); err != nil {
		return nil, nil // opaque/encrypted package: no selectable values, memo is dropped
	}
	return pkg.Votes, nil
}

// ErrVoteNotFound is returned by VoteByNullifier when the chain has no vote with the
// given nullifier. It is a distinct sentinel because "the chain does not know this vote"
// is an answer, while any other failure means the chain could not be asked.
var ErrVoteNotFound = fmt.Errorf("vote not found")

// VoteByNullifier fetches an on-chain vote by its nullifier (voteID) from the Vochain,
// returning ErrVoteNotFound when the chain does not know it. The node keys this lookup on
// the nullifier alone — unlike its /votes/verify route, which also needs the election id —
// so a caller holding only a nullifier can still resolve the vote and the election it
// belongs to. There is no apiclient wrapper for it, hence the raw request.
func (a *Account) VoteByNullifier(nullifier []byte) (*api.Vote, error) {
	resp, code, err := a.client.Request(apiclient.HTTPGET, nil, "votes", hex.EncodeToString(nullifier))
	if err != nil {
		return nil, fmt.Errorf("could not fetch vote %x: %w", nullifier, err)
	}
	switch code {
	case http.StatusOK:
		vote := &api.Vote{}
		if err := json.Unmarshal(resp, vote); err != nil {
			return nil, fmt.Errorf("could not decode vote %x: %w", nullifier, err)
		}
		return vote, nil
	case http.StatusNotFound:
		return nil, ErrVoteNotFound
	default:
		return nil, fmt.Errorf("could not fetch vote %x: status %d (%s)", nullifier, code, resp)
	}
}

// ElectionEncryptionKeys fetches the encryption public keys of the on-chain election with
// the given id. Only encrypted (secretUntilTheEnd) elections publish keys, and only after
// the keykeepers have done so, so the returned slice may be empty for a freshly created
// election. The node also returns private keys once the election ends; those are never
// needed for voting and are deliberately ignored here.
func (a *Account) ElectionEncryptionKeys(processID []byte) ([]db.EncryptionKey, error) {
	ek, err := a.client.ElectionKeys(processID)
	if err != nil {
		return nil, fmt.Errorf("could not fetch election keys %x: %w", processID, err)
	}
	keys := make([]db.EncryptionKey, 0, len(ek.PublicKeys))
	for _, k := range ek.PublicKeys {
		keys = append(keys, db.EncryptionKey{Index: k.Index, Key: internal.HexBytes(k.Key)})
	}
	return keys, nil
}
