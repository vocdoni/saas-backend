package account

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/api"
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
	OrgAddress  common.Address // organization (entity) that owns the election
	Params      *db.ElectionParams
	CensusRoot  []byte // census root (CSP public key)
	CensusURI   string // census endpoint (SaaS CSP base URL)
	MetadataURL string // public https URL of the stored ElectionMetadata JSON
	// Nonce, when set, is used as the tx account nonce instead of reading the current
	// on-chain nonce. Batch publishing sets explicit consecutive nonces so N txs can be
	// signed and submitted together; single publishes leave it nil to read the nonce.
	Nonce *uint32
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
			start := startDate.Unix()
			dur := endDate.Sub(startDate).Seconds()
			if start < 0 || start > math.MaxUint32 {
				return 0, 0, fmt.Errorf("startDate out of range")
			}
			if dur < 0 || dur > math.MaxUint32 {
				return 0, 0, fmt.Errorf("duration out of range")
			}
			return uint32(start), uint32(dur), nil
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

	metadataURL := p.MetadataURL
	process := &models.Process{
		EntityId:      p.OrgAddress.Bytes(),
		Status:        models.ProcessStatus_READY,
		StartTime:     startTime,
		Duration:      duration,
		CensusOrigin:  models.CensusOrigin_OFF_CHAIN_CA,
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
			Interruptible: ep.ElectionType.Interruptible,
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
