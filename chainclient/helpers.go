package chainclient

import (
	"crypto/rand"
	"fmt"
	"math"
	"math/big"

	ethcommon "github.com/ethereum/go-ethereum/common"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/proto/build/go/models"
	"google.golang.org/protobuf/proto"
)

// GenerateNullifier returns the vote nullifier of a non-anonymous voter in an election:
// keccak256(address || processID). It matches the node's state.GenerateNullifier byte for byte.
func GenerateNullifier(address ethcommon.Address, processID []byte) []byte {
	data := make([]byte, 0, len(address)+len(processID))
	data = append(data, address.Bytes()...)
	data = append(data, processID...)
	return ethereum.HashRaw(data)
}

// GenerateFaucetPackage builds a faucet package, signed by from, that lets to spend amount tokens
// from the signer's account. The payload identifier is random so each package is single-use.
func GenerateFaucetPackage(from *ethereum.SignKeys, to ethcommon.Address, amount uint64) (*models.FaucetPackage, error) {
	id, err := rand.Int(rand.Reader, big.NewInt(math.MaxInt32))
	if err != nil {
		return nil, fmt.Errorf("generating faucet package identifier: %w", err)
	}
	return faucetPackageWithID(from, to, amount, id.Uint64())
}

// faucetPackageWithID is GenerateFaucetPackage with a fixed payload identifier.
func faucetPackageWithID(from *ethereum.SignKeys, to ethcommon.Address, amount, id uint64) (*models.FaucetPackage, error) {
	payload, err := proto.Marshal(&models.FaucetPayload{
		Identifier: id,
		To:         to.Bytes(),
		Amount:     amount,
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling faucet payload: %w", err)
	}
	signature, err := from.SignEthereum(payload)
	if err != nil {
		return nil, fmt.Errorf("signing faucet payload: %w", err)
	}
	return &models.FaucetPackage{Payload: payload, Signature: signature}, nil
}

// txCostNames maps the transaction cost names the node uses in GET /chain/transactions/cost to
// their transaction types.
var txCostNames = map[string]models.TxType{
	"SetProcessStatus":        models.TxType_SET_PROCESS_STATUS,
	"SetProcessCensus":        models.TxType_SET_PROCESS_CENSUS,
	"SetProcessDuration":      models.TxType_SET_PROCESS_DURATION,
	"SetProcessMetadata":      models.TxType_SET_PROCESS_METADATA,
	"SetProcessQuestionIndex": models.TxType_SET_PROCESS_QUESTION_INDEX,
	"SendTokens":              models.TxType_SEND_TOKENS,
	"SetAccountInfoURI":       models.TxType_SET_ACCOUNT_INFO_URI,
	"SetAccountValidator":     models.TxType_SET_ACCOUNT_VALIDATOR,
	"CreateAccount":           models.TxType_CREATE_ACCOUNT,
	"RegisterKey":             models.TxType_REGISTER_VOTER_KEY,
	"NewProcess":              models.TxType_NEW_PROCESS,
	"AddDelegateForAccount":   models.TxType_ADD_DELEGATE_FOR_ACCOUNT,
	"DelDelegateForAccount":   models.TxType_DEL_DELEGATE_FOR_ACCOUNT,
	"CollectFaucet":           models.TxType_COLLECT_FAUCET,
}

// TxCostNameToTxType returns the transaction type of a node cost name, or TX_UNKNOWN for a name it
// does not know.
func TxCostNameToTxType(name string) models.TxType {
	if t, ok := txCostNames[name]; ok {
		return t
	}
	return models.TxType_TX_UNKNOWN
}
