package account

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
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
	acc, err := a.client.Account(p.OrgAddress.String())
	if err != nil {
		return nil, fmt.Errorf("could not fetch organization account: %w", err)
	}

	var duration, startTime uint32
	switch {
	case !ep.StartDate.IsZero() && !ep.EndDate.IsZero():
		if !ep.EndDate.After(ep.StartDate) {
			return nil, fmt.Errorf("endDate must be after startDate")
		}
		duration = uint32(ep.EndDate.Sub(ep.StartDate).Seconds())
		if ep.StartDate.After(time.Now()) {
			startTime = uint32(ep.StartDate.Unix())
		}
	case !ep.EndDate.IsZero():
		d := time.Until(ep.EndDate)
		if d <= 0 {
			return nil, fmt.Errorf("endDate must be in the future")
		}
		duration = uint32(d.Seconds())
	default:
		return nil, fmt.Errorf("endDate is required")
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
				Nonce:   acc.Nonce,
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
