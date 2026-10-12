package chainclient

import (
	"encoding/json"
	"time"

	"go.vocdoni.io/dvote/types"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/encoding/protojson"
)

// The types below mirror the subset of the vocdoni-node API (go.vocdoni.io/dvote/api) that this
// service reads or writes. Field names and JSON tags match the node's so the wire format is
// identical; fields the service never touches are left out. One struct per node payload is the
// point of this file, hence the public struct count.
//
//revive:disable:max-public-structs

// ChainInfo is the subset of GET /chain/info the service needs.
type ChainInfo struct {
	ID     string `json:"chainId"`
	Height uint32 `json:"height"`
}

// Account is the on-chain account returned by GET /accounts/{address}.
type Account struct {
	Address types.HexBytes `json:"address"`
	Nonce   uint32         `json:"nonce"`
	Balance uint64         `json:"balance"`
	InfoURL string         `json:"infoURL,omitempty"`
}

// Transaction is the request and response body of POST /chain/transactions, and the response of
// GET /chain/transactions/cost (which only fills Costs).
type Transaction struct {
	Payload   []byte            `json:"payload,omitempty"`
	Hash      types.HexBytes    `json:"hash,omitempty"`
	Response  []byte            `json:"response,omitempty"`
	Code      *uint32           `json:"code,omitempty"`
	Costs     map[string]uint64 `json:"costs,omitempty"`
	Address   types.HexBytes    `json:"address,omitempty"`
	ProcessID types.HexBytes    `json:"processId,omitempty"`
}

// TransactionPayload is one item of a POST /chain/transactions/batch request: a protobuf-marshaled
// models.SignedTx plus, optionally, the raw election metadata of a NewProcess.
type TransactionPayload struct {
	Payload  []byte `json:"payload"`
	Metadata []byte `json:"metadata,omitempty"`
}

// TransactionBatch is the body of POST /chain/transactions/batch.
type TransactionBatch struct {
	Transactions []TransactionPayload `json:"transactions"`
}

// TransactionBatchResult groups every input transaction of a batch by outcome, each group in input
// order: Submitted were accepted by the mempool (not confirmed), Failed holds the first failure
// (submission stops there) and Pending the remainder that was not sent.
type TransactionBatchResult struct {
	Submitted []TransactionBatchItem `json:"submitted"`
	Failed    []TransactionBatchItem `json:"failed"`
	Pending   []TransactionBatchItem `json:"pending"`
}

// TransactionBatchItem is the outcome of one transaction of a batch.
type TransactionBatchItem struct {
	Hash        types.HexBytes `json:"hash,omitempty"`
	ProcessID   types.HexBytes `json:"processId,omitempty"`
	Code        *uint32        `json:"code,omitempty"`
	Error       string         `json:"error,omitempty"`
	MetadataURL string         `json:"metadataURL,omitempty"`
}

// TransactionReference locates a mined transaction (GET /chain/transactions/reference/{hash}).
type TransactionReference struct {
	Height uint32 `json:"blockHeight"`
	Index  uint32 `json:"transactionIndex"`
}

// Election is an on-chain election as returned by GET /elections/{id}.
type Election struct {
	ElectionSummary
	Census       *ElectionCensus `json:"census,omitempty"`
	MetadataURL  string          `json:"metadataURL"`
	CreationTime time.Time       `json:"creationTime"`
	VoteMode     VoteMode        `json:"voteMode,omitempty"`
	ElectionMode ElectionMode    `json:"electionMode,omitempty"`
	TallyMode    TallyMode       `json:"tallyMode,omitempty"`
	// Metadata is the decoded metadata document, inlined by the node only when it could resolve it.
	Metadata any `json:"metadata,omitempty"`
}

// ElectionSummary holds the election fields shared by the node's list and detail endpoints.
type ElectionSummary struct {
	ElectionID     types.HexBytes    `json:"electionId"`
	OrganizationID types.HexBytes    `json:"organizationId"`
	Status         string            `json:"status"`
	StartDate      time.Time         `json:"startDate"`
	EndDate        time.Time         `json:"endDate"`
	VoteCount      uint64            `json:"voteCount"`
	FinalResults   bool              `json:"finalResults"`
	Results        [][]*types.BigInt `json:"result,omitempty"`
	ManuallyEnded  bool              `json:"manuallyEnded"`
	ChainID        string            `json:"chainId"`
	Title          string            `json:"title,omitempty"`
}

// ElectionCensus is the census block of an election.
type ElectionCensus struct {
	CensusOrigin           string         `json:"censusOrigin"`
	CensusRoot             types.HexBytes `json:"censusRoot"`
	PostRegisterCensusRoot types.HexBytes `json:"postRegisterCensusRoot"`
	CensusURL              string         `json:"censusURL"`
	MaxCensusSize          uint64         `json:"maxCensusSize"`
}

// VoteMode wraps the election's envelope type. Reading a promoted field panics when it is nil.
type VoteMode struct {
	*models.EnvelopeType
}

// MarshalJSON encodes the envelope type as the node does: protojson with unpopulated fields.
func (v VoteMode) MarshalJSON() ([]byte, error) {
	return protoJSON.Marshal(&v)
}

// ElectionMode wraps the election's process mode. Reading a promoted field panics when it is nil.
type ElectionMode struct {
	*models.ProcessMode
}

// MarshalJSON encodes the process mode as the node does: protojson with unpopulated fields.
func (e ElectionMode) MarshalJSON() ([]byte, error) {
	return protoJSON.Marshal(&e)
}

// TallyMode wraps the election's vote options. Reading a promoted field panics when it is nil.
type TallyMode struct {
	*models.ProcessVoteOptions
}

// MarshalJSON encodes the vote options as the node does: protojson with unpopulated fields.
func (t TallyMode) MarshalJSON() ([]byte, error) {
	return protoJSON.Marshal(&t)
}

// protoJSON is the encoding the node uses for the protobuf-backed election modes.
var protoJSON = protojson.MarshalOptions{EmitUnpopulated: true, UseEnumNumbers: false}

// ElectionKeys holds the encryption keys of an election (GET /elections/{id}/keys). Only the
// public keys are read; the node also returns private keys once the election ends.
type ElectionKeys struct {
	PublicKeys []Key `json:"publicKeys,omitempty"`
}

// Key is one indexed encryption key of an election.
type Key struct {
	Index int            `json:"index"`
	Key   types.HexBytes `json:"key"`
}

// Vote is an on-chain vote (GET /votes/{nullifier}, and the rows of a votes page).
type Vote struct {
	TxHash               types.HexBytes  `json:"txHash,omitempty"`
	VoteID               types.HexBytes  `json:"voteID,omitempty"`
	EncryptionKeyIndexes []uint32        `json:"encryptionKeys,omitempty"`
	VotePackage          json.RawMessage `json:"package,omitempty"`
	VoteWeight           string          `json:"weight,omitempty"`
	VoteNumber           *uint32         `json:"number,omitempty"`
	ElectionID           types.HexBytes  `json:"electionID,omitempty"`
	VoterID              types.HexBytes  `json:"voterID,omitempty"`
	BlockHeight          uint32          `json:"blockHeight,omitempty"`
	TransactionIndex     *int32          `json:"transactionIndex,omitempty"`
	OverwriteCount       *uint32         `json:"overwriteCount,omitempty"`
	Memo                 []byte          `json:"memo,omitempty"`
	Date                 *time.Time      `json:"date,omitempty"`
}

// VotesList is one page of an election's votes (GET /elections/{id}/votes/page/{n}).
type VotesList struct {
	Votes []*Vote `json:"votes"`
}

// ElectionMetadata is the off-chain metadata document an election's metadata URL points at.
type ElectionMetadata struct {
	Title       LanguageString     `json:"title"`
	Version     string             `json:"version"`
	Description LanguageString     `json:"description"`
	Media       ProcessMedia       `json:"media,omitempty"`
	Meta        any                `json:"meta,omitempty"`
	Questions   []Question         `json:"questions,omitempty"`
	Type        ElectionProperties `json:"type,omitempty"`
}

// LanguageString maps a language code to the text in that language.
type LanguageString map[string]string

// ProcessMedia holds the media references of an election.
type ProcessMedia struct {
	Header    string `json:"header,omitempty"`
	StreamURI string `json:"streamUri,omitempty"`
}

// Question is one question of an election metadata document.
type Question struct {
	Choices     []ChoiceMetadata `json:"choices"`
	Description LanguageString   `json:"description"`
	Title       LanguageString   `json:"title"`
	Meta        any              `json:"meta,omitempty"`
}

// ChoiceMetadata is one choice of a metadata question.
type ChoiceMetadata struct {
	Title LanguageString `json:"title"`
	Value uint32         `json:"value"`
	Meta  any            `json:"meta,omitempty"`
}

// ElectionProperties names the election type of a metadata document and carries its properties.
type ElectionProperties struct {
	Name       string `json:"name"`
	Properties any    `json:"properties"`
}

// accountSet is the request and response body of POST /accounts.
type accountSet struct {
	TxPayload []byte         `json:"txPayload,omitempty"`
	Metadata  []byte         `json:"metadata,omitempty"`
	TxHash    types.HexBytes `json:"txHash"`
}

// faucetResponse is the body returned by the node's faucet ({api}/open/claim/{address}).
type faucetResponse struct {
	Amount        string `json:"amount,omitempty"`
	FaucetPackage []byte `json:"faucetPackage,omitempty"`
}

// faucetPackage is the JSON envelope of a faucet package inside faucetResponse.
type faucetPackage struct {
	FaucetPayload []byte `json:"faucetPayload"`
	Signature     []byte `json:"signature"`
}
