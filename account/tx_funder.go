package account

import (
	"bytes"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/errors"
	"go.vocdoni.io/dvote/vochain/state/electionprice"
	"go.vocdoni.io/proto/build/go/models"
)

// createFaucetPackage creates a faucet package for the given address and amount.
func (a *Account) createFaucetPackage(targetAddr common.Address, amount uint64) (*models.FaucetPackage, error) {
	faucetPkg, err := a.FaucetPackage(targetAddr, amount)
	if err != nil {
		return nil, errors.ErrCouldNotCreateFaucetPackage.WithErr(err)
	}
	return faucetPkg, nil
}

// getTxCost gets the transaction cost for the given transaction type.
func (a *Account) getTxCost(txType models.TxType) (uint64, error) {
	amount, ok := a.TxCosts[txType]
	if !ok {
		return 0, errors.ErrInvalidTxFormat.With("invalid tx type")
	}
	return amount, nil
}

// handleSetAccountTx handles a SetAccount transaction.
func (a *Account) handleSetAccountTx(tx *models.Tx, targetAddr common.Address) (*models.Tx, *models.TxType, error) {
	txSetAccount := tx.GetSetAccount()

	// Check the tx fields
	if txSetAccount == nil || txSetAccount.Account == nil || txSetAccount.InfoURI == nil {
		return nil, nil, errors.ErrInvalidTxFormat.With("missing fields")
	}

	// Check the account is the same as the user
	if !bytes.Equal(txSetAccount.GetAccount(), targetAddr.Bytes()) {
		return nil, nil, errors.ErrUnauthorized.With("invalid account")
	}

	var txType models.TxType

	// Handle different subtypes
	switch txSetAccount.Txtype {
	case models.TxType_CREATE_ACCOUNT:
		txType = txSetAccount.Txtype

		// Get the tx cost and create faucet package
		amount, err := a.getTxCost(txType)
		if err != nil {
			return nil, nil, err
		}

		faucetPkg, err := a.createFaucetPackage(targetAddr, amount)
		if err != nil {
			return nil, nil, err
		}

		// Include the faucet package in the tx
		txSetAccount.FaucetPackage = faucetPkg
		tx = &models.Tx{
			Payload: &models.Tx_SetAccount{
				SetAccount: txSetAccount,
			},
		}
	default:
		return nil, nil, errors.ErrInvalidTxFormat.With("invalid SetAccount tx type")
	}

	return tx, &txType, nil
}

// handleNewProcessTx handles a NewProcess transaction.
func (a *Account) handleNewProcessTx(tx *models.Tx, targetAddr common.Address) (*models.Tx, *models.TxType, error) {
	txNewProcess := tx.GetNewProcess()

	// Check the tx fields
	if txNewProcess == nil || txNewProcess.Process == nil {
		return nil, nil, errors.ErrInvalidTxFormat.With("missing fields")
	}

	// Only handle NEW_PROCESS type
	if txNewProcess.Txtype != models.TxType_NEW_PROCESS {
		return nil, nil, errors.ErrInvalidTxFormat.With("invalid NewProcess tx type")
	}

	txType := models.TxType_NEW_PROCESS

	// Get the tx cost
	amount, err := a.getTxCost(txType)
	if err != nil {
		return nil, nil, err
	}

	// Add election price, computed like the chain does: a metadata-only process has no envelope
	// type or vote options, so the getters (nil-safe) price it as an election with no census.
	p := txNewProcess.Process
	amount += a.ElectionPriceCalc.Price(&electionprice.ElectionParameters{
		MaxCensusSize:           p.GetMaxCensusSize(),
		ElectionDurationSeconds: p.GetDuration(),
		EncryptedVotes:          p.GetEnvelopeType().GetEncryptedVotes(),
		AnonymousVotes:          p.GetEnvelopeType().GetAnonymous(),
		MaxVoteOverwrite:        p.GetVoteOptions().GetMaxVoteOverwrites(),
	})

	// Create faucet package
	faucetPkg, err := a.createFaucetPackage(targetAddr, amount)
	if err != nil {
		return nil, nil, err
	}

	// Include the faucet package in the tx
	txNewProcess.FaucetPackage = faucetPkg
	tx = &models.Tx{
		Payload: &models.Tx_NewProcess{
			NewProcess: txNewProcess,
		},
	}

	return tx, &txType, nil
}

// calculateSetProcessElectionPrice calculates the election price for a SetProcess transaction.
func (a *Account) calculateSetProcessElectionPrice(
	txSetProcess *models.SetProcessTx,
	currentProcess any,
) uint64 {
	// Extract the necessary fields based on the actual structure
	var durationSeconds uint32
	var encryptedVotes bool
	var anonymous bool
	var maxVoteOverwrite uint32

	// Extract fields from the election object using type assertion and reflection
	// This is a more generic approach that doesn't assume a specific type
	type electionLike interface {
		// We're just checking if these methods exist
		GetEndBlock() uint64
		GetStartBlock() uint64
		GetEncryptedVotes() bool
		GetAnonymous() bool
		GetMaxVoteOverwrites() uint32
		GetCensus() any
	}

	// Try to access the fields directly first
	if election, ok := currentProcess.(electionLike); ok {
		durationSeconds = uint32(election.GetEndBlock() - election.GetStartBlock())
		encryptedVotes = election.GetEncryptedVotes()
		anonymous = election.GetAnonymous()
		maxVoteOverwrite = election.GetMaxVoteOverwrites()
	} else { //nolint empty branch
		// Fallback: use default values
		// In a real implementation, you might want to use reflection to extract values
		// from the currentProcess object, but for now we'll use defaults

		// We could log a warning here that we're using default values
		// log.Warn("Using default values for election parameters")
	}

	switch txSetProcess.Txtype {
	case models.TxType_SET_PROCESS_CENSUS:
		return a.ElectionPriceCalc.Price(&electionprice.ElectionParameters{
			MaxCensusSize:           txSetProcess.GetCensusSize(),
			ElectionDurationSeconds: durationSeconds,
			EncryptedVotes:          encryptedVotes,
			AnonymousVotes:          anonymous,
			MaxVoteOverwrite:        maxVoteOverwrite,
		})
	default:
		return 0
	}
}

// validateSetProcessTx validates a SetProcess transaction.
func (*Account) validateSetProcessTx(txSetProcess *models.SetProcessTx) error {
	if txSetProcess == nil || txSetProcess.ProcessId == nil {
		return errors.ErrInvalidTxFormat.With("missing fields")
	}

	switch txSetProcess.Txtype {
	case models.TxType_SET_PROCESS_STATUS:
		if txSetProcess.Status == nil {
			return errors.ErrInvalidTxFormat.With("missing status field")
		}
	case models.TxType_SET_PROCESS_CENSUS:
		if (txSetProcess.CensusRoot == nil || txSetProcess.CensusURI == nil) && txSetProcess.CensusSize == nil {
			return errors.ErrInvalidTxFormat.With("missing census fields")
		}
	case models.TxType_SET_PROCESS_METADATA:
		if txSetProcess.GetMetadata() == "" || len(txSetProcess.MetadataHash) == 0 {
			return errors.ErrInvalidTxFormat.With("missing metadata fields")
		}
	default:
		return errors.ErrInvalidTxFormat.With("unsupported txtype")
	}
	return nil
}

// handleSetProcessTx handles a SetProcess transaction.
func (a *Account) handleSetProcessTx(tx *models.Tx, targetAddr common.Address) (*models.Tx, *models.TxType, error) {
	txSetProcess := tx.GetSetProcess()

	// Validate the transaction
	if err := a.validateSetProcessTx(txSetProcess); err != nil {
		return nil, nil, err
	}

	// Get the tx cost
	amount, err := a.getTxCost(txSetProcess.Txtype)
	if err != nil {
		return nil, nil, err
	}

	// For certain types, we need to get the current process and calculate additional costs
	if txSetProcess.Txtype == models.TxType_SET_PROCESS_CENSUS {
		election, err := a.client.Election(txSetProcess.ProcessId)
		if err != nil {
			return nil, nil, errors.ErrVochainRequestFailed.WithErr(err)
		}

		// Add election price
		amount += a.calculateSetProcessElectionPrice(txSetProcess, election)
	}

	// Create faucet package
	faucetPkg, err := a.createFaucetPackage(targetAddr, amount)
	if err != nil {
		return nil, nil, err
	}

	// Include the faucet package in the tx
	txSetProcess.FaucetPackage = faucetPkg
	tx = &models.Tx{
		Payload: &models.Tx_SetProcess{
			SetProcess: txSetProcess,
		},
	}

	return tx, &txSetProcess.Txtype, nil
}

// FundTransaction funds the tx with the required amount for the tx type
// and returns the tx with the faucet package included.
// Returns the tx, the type and an error if any.
func (a *Account) FundTransaction(tx *models.Tx, targetAddr common.Address) (*models.Tx, *models.TxType, error) {
	switch tx.Payload.(type) {
	case *models.Tx_SetAccount:
		return a.handleSetAccountTx(tx, targetAddr)
	case *models.Tx_NewProcess:
		return a.handleNewProcessTx(tx, targetAddr)
	case *models.Tx_SetProcess:
		return a.handleSetProcessTx(tx, targetAddr)
	default:
		return nil, nil, errors.ErrTxTypeNotAllowed
	}
}
