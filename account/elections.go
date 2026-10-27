package account

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/vochain/processid"
	"go.vocdoni.io/proto/build/go/models"
)

// AccountElectionIndex returns the number of elections the organization account has created on
// chain. It is read from the account state, so it counts an election as soon as it is mined.
func (a *Account) AccountElectionIndex(org common.Address) (uint32, error) {
	acc, err := a.client.Account(org.String())
	if err != nil {
		return 0, fmt.Errorf("could not fetch organization account: %w", err)
	}
	return acc.ElectionIndex, nil
}

// ElectionCensusRoot returns the census root of the on-chain election with the given id. found
// is false, with a nil error, when the node does not know the election (yet).
func (a *Account) ElectionCensusRoot(processID internal.HexBytes) (root internal.HexBytes, found bool, err error) {
	resp, code, err := a.client.Request(http.MethodGet, nil, "elections", processID.String())
	if err != nil {
		return nil, false, fmt.Errorf("could not fetch election %x: %w", []byte(processID), err)
	}
	switch code {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, false, nil
	default:
		return nil, false, fmt.Errorf("could not fetch election %x: status %d", []byte(processID), code)
	}
	var election api.Election
	if err := json.Unmarshal(resp, &election); err != nil {
		return nil, false, fmt.Errorf("could not decode election %x: %w", []byte(processID), err)
	}
	if election.Census == nil {
		return nil, false, fmt.Errorf("election %x has no census", []byte(processID))
	}
	return internal.HexBytes(election.Census.CensusRoot), true, nil
}

// ElectionIDParams identifies the election an organization creates at a given election index.
type ElectionIDParams struct {
	OrgAddress   common.Address
	CensusOrigin models.CensusOrigin
	Params       *db.ElectionParams
	// Index is the organization's election index when the election is created: the
	// AccountElectionIndex read just before its NEW_PROCESS transaction mines.
	Index uint32
}

// ElectionID returns the id the chain assigns to an election created from p, computed the way
// the chain derives it from the chain id, organization, census origin, envelope type and index.
func (a *Account) ElectionID(p *ElectionIDParams) (internal.HexBytes, error) {
	pid := new(processid.ProcessID)
	pid.SetChainID(a.client.ChainID())
	pid.SetAddr(p.OrgAddress)
	pid.SetNonce(p.Index)
	if err := pid.SetCensusOrigin(p.CensusOrigin); err != nil {
		return nil, fmt.Errorf("could not compute election id: %w", err)
	}
	if err := pid.SetEnvelopeType(EnvelopeType(p.Params)); err != nil {
		return nil, fmt.Errorf("could not compute election id: %w", err)
	}
	return pid.Marshal(), nil
}
