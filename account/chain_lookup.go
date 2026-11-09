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
// an error, because "not mined" may release money.
//
// It asks GET /chain/transactions/{hash}, not /chain/transactions/reference/{hash}: the node's
// reference handler maps every indexer read error to 204 (dvote api/chain.go chainTxRefByHashHandler
// returns ErrTransactionNotFound.WithErr(err)), so a failing indexer would look like "not mined",
// while chainTxByHashHandler runs the same indexer query and answers 204 only when the tx is not
// found and 500 (ErrVochainGetTxFailed) on a read error. apiclient.TransactionReference is not used
// either: it maps every non-200 to "does not exist".
func (a *Account) TxMined(hash internal.HexBytes) (bool, error) {
	resp, code, err := a.client.Request(apiclient.HTTPGET, nil, "chain", "transactions", hash.String())
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

// ChainHeight returns the current height of the chain (GET /chain/info).
func (a *Account) ChainHeight() (uint32, error) {
	info, err := a.client.ChainInfo()
	if err != nil {
		return 0, fmt.Errorf("could not read the chain info: %w", err)
	}
	return info.Height, nil
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
