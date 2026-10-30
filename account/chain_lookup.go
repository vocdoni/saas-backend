package account

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/internal"
	"go.vocdoni.io/dvote/api"
	"go.vocdoni.io/dvote/apiclient"
)

// TxMined reports whether the tx with the given hash is in a block. Only the node's 204 ("not
// found") answers false with no error; any other answer, a 5xx or a transport failure included, is
// an error, because "not mined" may release money (apiclient.TransactionReference maps every
// non-200 to "does not exist", so it is not used here).
func (a *Account) TxMined(hash internal.HexBytes) (bool, error) {
	resp, code, err := a.client.Request(apiclient.HTTPGET, nil, "chain", "transactions", "reference", hash.String())
	if err != nil {
		return false, fmt.Errorf("could not look up tx %s: %w", hash.String(), err)
	}
	switch code {
	case http.StatusOK:
		return true, nil
	case http.StatusNoContent:
		return false, nil
	default:
		return false, fmt.Errorf("could not look up tx %s: status %d (%s)", hash.String(), code, resp)
	}
}

// OrgElectionIDs returns one page (from 0) of the ids of the elections an organization created,
// newest first. A page past the last one is empty.
func (a *Account) OrgElectionIDs(org common.Address, page int) ([]internal.HexBytes, error) {
	resp, code, err := a.client.Request(apiclient.HTTPGET, nil,
		"accounts", hex.EncodeToString(org.Bytes()), "elections", "page", strconv.Itoa(page))
	if err != nil {
		return nil, fmt.Errorf("could not list elections of %s: %w", org.Hex(), err)
	}
	if code != http.StatusOK {
		return nil, fmt.Errorf("could not list elections of %s: status %d (%s)", org.Hex(), code, resp)
	}
	// the node answers {} past the last page
	var list api.ElectionsList
	if err := json.Unmarshal(resp, &list); err != nil {
		return nil, fmt.Errorf("could not decode elections of %s: %w", org.Hex(), err)
	}
	ids := make([]internal.HexBytes, 0, len(list.Elections))
	for _, e := range list.Elections {
		if e != nil {
			ids = append(ids, internal.HexBytes(e.ElectionID))
		}
	}
	return ids, nil
}
