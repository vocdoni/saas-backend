// Package chainclient is a thin HTTP client for the subset of the vocdoni-node API the service uses
// (chain info, accounts, transactions, elections and votes), plus the few chain helpers that would
// otherwise require linking vocdoni-node's chain packages (nullifiers, faucet packages, tx cost
// names). It deliberately depends only on lightweight packages so the service binary does not pull
// in the node (cometbft, IPFS, ZK).
package chainclient

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"time"

	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
	"go.vocdoni.io/dvote/types"
	"go.vocdoni.io/dvote/vochain/state/electionprice"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

const (
	// DefaultTimeout bounds every HTTP request to the node.
	DefaultTimeout = 10 * time.Second
	// DefaultRetries is how many times a request is sent while the node answers 503 (mempool full),
	// waiting for the next block between attempts.
	DefaultRetries = 3
	// blockTime is the node's target block time; it paces the block and tx polling loops.
	blockTime = 10 * time.Second
	// pollInterval is how often block height and tx inclusion are polled.
	pollInterval = blockTime / 2
	// nextBlockTimeout bounds the wait for the next block before retrying a 503.
	nextBlockTimeout = blockTime * 3
	userAgent        = "Vocdoni API client / 1.0"
)

// StatusError is returned when the node answers with an unexpected HTTP status.
type StatusError struct {
	Code int
	Body []byte
}

// Error implements the error interface.
func (e *StatusError) Error() string {
	return fmt.Sprintf("API error: %d (%s)", e.Code, e.Body)
}

// ErrTransactionDoesNotExist is returned by TransactionReference when the node does not know the
// transaction (yet).
var ErrTransactionDoesNotExist = fmt.Errorf("transaction does not exist")

// Client talks to a vocdoni-node HTTP API.
type Client struct {
	c       *http.Client
	addr    *url.URL
	chainID string
	retries int
}

// New returns a client for the node API at host and fetches the chain ID. As with the node's own
// client, a non-200 answer to the chain info probe is logged and leaves ChainID empty instead of
// failing, while a transport error fails.
func New(host string) (*Client, error) {
	addr, err := url.Parse(host)
	if err != nil {
		return nil, fmt.Errorf("parsing node api url: %w", err)
	}
	c := &Client{
		c: &http.Client{
			Transport: &http.Transport{
				IdleConnTimeout: DefaultTimeout,
				WriteBufferSize: 1 << 20,
				ReadBufferSize:  1 << 20,
			},
			Timeout: DefaultTimeout,
		},
		addr:    addr,
		retries: DefaultRetries,
	}
	data, code, err := c.request(http.MethodGet, nil, "chain", "info")
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		log.Warnw("cannot get chain info from API server", "status", code, "data", data)
		return c, nil
	}
	info := &ChainInfo{}
	if err := json.Unmarshal(data, info); err != nil {
		return nil, fmt.Errorf("cannot get chain ID from API server: %w", err)
	}
	c.chainID = info.ID
	return c, nil
}

// ChainID returns the identifier of the chain the node runs.
func (c *Client) ChainID() string {
	return c.chainID
}

// request sends a request to the node at the given path segments, JSON-encoding jsonBody when it
// is not nil, and returns the raw response body and status code. A 503 (mempool full) is retried
// up to the client's retry count, waiting for the next block in between.
func (c *Client) request(method string, jsonBody any, urlPath ...string) ([]byte, int, error) {
	return c.send(method, jsonBody, c.retries, urlPath...)
}

func (c *Client) send(method string, jsonBody any, attempts int, urlPath ...string) ([]byte, int, error) {
	var body []byte
	if jsonBody != nil {
		var err error
		if body, err = json.Marshal(jsonBody); err != nil {
			return nil, 0, fmt.Errorf("encoding request body: %w", err)
		}
	}
	newBody := func() io.Reader {
		if body == nil {
			return nil
		}
		return bytes.NewReader(body)
	}
	u := *c.addr
	u.Path = path.Join(u.Path, path.Join(urlPath...))
	for i := 1; ; i++ {
		req, err := http.NewRequestWithContext(context.Background(), method, u.String(), newBody())
		if err != nil {
			return nil, 0, fmt.Errorf("building request: %w", err)
		}
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Content-Type", "application/json")
		resp, err := c.c.Do(req)
		if err != nil {
			return nil, 0, err
		}
		data, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			return nil, 0, fmt.Errorf("reading response: %w", err)
		}
		if resp.StatusCode == http.StatusServiceUnavailable && i < attempts {
			log.Warnf("mempool is full, will wait and retry (%d/%d)", i, attempts)
			_ = c.waitNextBlock()
			continue
		}
		return data, resp.StatusCode, nil
	}
}

// get sends a GET and decodes a 200 answer into out; any other status is a *StatusError.
func (c *Client) get(out any, urlPath ...string) error {
	return c.do(http.MethodGet, nil, out, urlPath...)
}

func (c *Client) do(method string, jsonBody, out any, urlPath ...string) error {
	resp, code, err := c.request(method, jsonBody, urlPath...)
	if err != nil {
		return err
	}
	if code != http.StatusOK {
		return &StatusError{Code: code, Body: resp}
	}
	if err := json.Unmarshal(resp, out); err != nil {
		return fmt.Errorf("could not decode response: %w", err)
	}
	return nil
}

// ChainInfo returns the current chain info.
func (c *Client) ChainInfo() (*ChainInfo, error) {
	info := &ChainInfo{}
	if err := c.get(info, "chain", "info"); err != nil {
		return nil, err
	}
	return info, nil
}

// waitNextBlock waits (bounded by nextBlockTimeout) until the chain height increases. It polls
// without the 503 retry so it never recurses into itself.
func (c *Client) waitNextBlock() error {
	ctx, cancel := context.WithTimeout(context.Background(), nextBlockTimeout)
	defer cancel()
	start := uint32(0)
	for {
		data, code, err := c.send(http.MethodGet, nil, 1, "chain", "info")
		if err == nil && code == http.StatusOK {
			info := &ChainInfo{}
			if json.Unmarshal(data, info) == nil {
				if start == 0 {
					start = info.Height
				} else if info.Height > start {
					return nil
				}
			}
		}
		select {
		case <-time.After(pollInterval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Account returns the on-chain account at address (hex).
func (c *Client) Account(address string) (*Account, error) {
	acc := &Account{}
	if err := c.get(acc, "accounts", address); err != nil {
		return nil, err
	}
	return acc, nil
}

// SendTx submits a protobuf-marshaled models.SignedTx and returns its hash and the chain response
// data (for a NewProcess, the process id).
func (c *Client) SendTx(signedTx []byte) (types.HexBytes, []byte, error) {
	tx := &Transaction{}
	if err := c.do(http.MethodPost, &Transaction{Payload: signedTx}, tx, "chain", "transactions"); err != nil {
		return nil, nil, err
	}
	return tx.Hash, tx.Response, nil
}

// SendTxBatch submits several signed transactions at once, in order, to the batch endpoint. The
// transactions must carry contiguous account nonces. "Submitted" in the result means
// mempool-accepted, not confirmed.
func (c *Client) SendTxBatch(txs []TransactionPayload) (*TransactionBatchResult, error) {
	res := &TransactionBatchResult{}
	if err := c.do(http.MethodPost, &TransactionBatch{Transactions: txs}, res, "chain", "transactions", "batch"); err != nil {
		return nil, err
	}
	return res, nil
}

// TransactionReference returns where a transaction was mined, or ErrTransactionDoesNotExist when
// the node answers anything but 200.
func (c *Client) TransactionReference(txHash types.HexBytes) (*TransactionReference, error) {
	resp, code, err := c.request(http.MethodGet, nil, "chain", "transactions", "reference", txHash.String())
	if err != nil {
		return nil, err
	}
	if code != http.StatusOK {
		return nil, ErrTransactionDoesNotExist
	}
	ref := &TransactionReference{}
	if err := json.Unmarshal(resp, ref); err != nil {
		return nil, fmt.Errorf("could not decode response: %w", err)
	}
	return ref, nil
}

// WaitUntilTxIsMined polls until the transaction is included in a block or ctx is done. Once seen,
// it waits a further half poll interval so the block is committed before callers read state.
func (c *Client) WaitUntilTxIsMined(ctx context.Context, txHash types.HexBytes) (*TransactionReference, error) {
	start := time.Now()
	for {
		ref, err := c.TransactionReference(txHash)
		if err == nil {
			time.Sleep(pollInterval / 2)
			log.Infow("transaction mined", "tx", txHash.String(), "duration", time.Since(start).String())
			return ref, nil
		}
		select {
		case <-time.After(pollInterval):
		case <-ctx.Done():
			return nil, fmt.Errorf("transaction %s never mined after %s: %w",
				txHash.String(), time.Since(start).String(), ctx.Err())
		}
	}
}

// Election returns the on-chain election with the given id.
func (c *Client) Election(electionID types.HexBytes) (*Election, error) {
	if electionID == nil {
		return nil, fmt.Errorf("passed electionID is nil")
	}
	election := &Election{}
	if err := c.get(election, "elections", electionID.String()); err != nil {
		return nil, err
	}
	return election, nil
}

// ElectionKeys returns the encryption keys of an election. Only encrypted elections have them.
func (c *Client) ElectionKeys(electionID types.HexBytes) (*ElectionKeys, error) {
	keys := &ElectionKeys{}
	if err := c.get(keys, "elections", electionID.String(), "keys"); err != nil {
		return nil, err
	}
	return keys, nil
}

// ElectionVotesPage returns one page of an election's votes. Past the last page the node returns an
// empty list.
func (c *Client) ElectionVotesPage(electionID types.HexBytes, page int) (*VotesList, error) {
	list := &VotesList{}
	if err := c.get(list, "elections", electionID.String(), "votes", "page", strconv.Itoa(page)); err != nil {
		return nil, err
	}
	return list, nil
}

// Vote returns the vote with the given nullifier. A vote the node does not know is a *StatusError
// with Code 404.
func (c *Client) Vote(nullifier []byte) (*Vote, error) {
	vote := &Vote{}
	if err := c.get(vote, "votes", hex.EncodeToString(nullifier)); err != nil {
		return nil, err
	}
	return vote, nil
}

// ElectionPriceFactors returns the node's election price calculator parameters (base price,
// capacity and factors).
func (c *Client) ElectionPriceFactors() (*electionprice.Calculator, error) {
	calc := &electionprice.Calculator{}
	if err := c.get(calc, "chain", "info", "electionPriceFactors"); err != nil {
		return nil, err
	}
	return calc, nil
}

// TransactionCosts returns the base cost of each transaction type, keyed by the node's cost names
// (see TxCostNameToTxType).
func (c *Client) TransactionCosts() (map[string]uint64, error) {
	tx := &Transaction{}
	if err := c.get(tx, "chain", "transactions", "cost"); err != nil {
		return nil, err
	}
	return tx.Costs, nil
}

// FaucetPackage claims a faucet package for address from the node's open faucet
// ({api}/open/claim/{address}).
func (c *Client) FaucetPackage(address string) (*models.FaucetPackage, error) {
	fresp := &faucetResponse{}
	if err := c.get(fresp, "open", "claim", address); err != nil {
		return nil, fmt.Errorf("faucet request failed: %w", err)
	}
	if fresp.Amount == "" {
		return nil, fmt.Errorf("faucet response is missing amount")
	}
	if fresp.FaucetPackage == nil {
		return nil, fmt.Errorf("faucet response is missing package")
	}
	pkg := &faucetPackage{}
	if err := json.Unmarshal(fresp.FaucetPackage, pkg); err != nil {
		return nil, fmt.Errorf("could not decode faucet package: %w", err)
	}
	return &models.FaucetPackage{Payload: pkg.FaucetPayload, Signature: pkg.Signature}, nil
}

// AccountBootstrap creates the signer's account on chain (POST /accounts), paying the transaction
// with faucetPkg. The account gets the given name and no metadata URI. It returns the tx hash.
func (c *Client) AccountBootstrap(signer *ethereum.SignKeys, faucetPkg *models.FaucetPackage, name string) (
	types.HexBytes, error,
) {
	tx, err := proto.Marshal(&models.Tx{
		Payload: &models.Tx_SetAccount{
			SetAccount: &models.SetAccountTx{
				Txtype:        models.TxType_CREATE_ACCOUNT,
				Nonce:         new(uint32),
				Account:       signer.Address().Bytes(),
				FaucetPackage: faucetPkg,
				Name:          &name,
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("could not marshal create account tx: %w", err)
	}
	signature, err := signer.SignVocdoniTx(tx, c.chainID)
	if err != nil {
		return nil, fmt.Errorf("could not sign create account tx: %w", err)
	}
	stx, err := proto.Marshal(&models.SignedTx{Tx: tx, Signature: signature})
	if err != nil {
		return nil, fmt.Errorf("could not marshal signed tx: %w", err)
	}
	res := &accountSet{}
	if err := c.do(http.MethodPost, &accountSet{TxPayload: stx}, res, "accounts"); err != nil {
		return nil, err
	}
	return res.TxHash, nil
}
