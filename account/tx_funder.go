package account

import (
	"bytes"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/errors"
	"go.vocdoni.io/dvote/vochain/state/electionprice"
	"go.vocdoni.io/proto/build/go/models"
)

// FundTransaction funds the tx with the required amount for the tx type
// and returns the tx with the faucet package included.
// Returns the tx, the type and an error if any.
func (a *Account) FundTransaction(tx *models.Tx, targetAddr common.Address) (*models.Tx, *models.TxType, error) {
	var txType models.TxType
	switch tx.Payload.(type) {
	case *models.Tx_SetAccount:
		txSetAccount := tx.GetSetAccount()
		// check the tx fields
		if txSetAccount == nil || txSetAccount.Account == nil || txSetAccount.InfoURI == nil {
			return nil, nil, errors.ErrInvalidTxFormat.With("missing fields")
		}
		// check the account is the same as the user
		if !bytes.Equal(txSetAccount.GetAccount(), targetAddr.Bytes()) {
			return nil, nil, errors.ErrUnauthorized.With("invalid account")
		}
		// check the tx subtype
		switch txSetAccount.Txtype {
		case models.TxType_CREATE_ACCOUNT:
			txType = models.TxType_CREATE_ACCOUNT
			// get the tx cost for the tx type
			amount, ok := a.TxCosts[txType]
			if !ok {
				panic("invalid tx type")
			}
			// generate the faucet package with the calculated amount
			faucetPkg, err := a.FaucetPackage(targetAddr, amount)
			if err != nil {
				return nil, nil, errors.ErrCouldNotCreateFaucetPackage.WithErr(err)
			}
			// include the faucet package in the tx
			txSetAccount.FaucetPackage = faucetPkg
			tx = &models.Tx{
				Payload: &models.Tx_SetAccount{
					SetAccount: txSetAccount,
				},
			}
		case models.TxType_SET_ACCOUNT_INFO_URI:
			txType = models.TxType_SET_ACCOUNT_INFO_URI
			// get the tx cost for the tx type
			amount, ok := a.TxCosts[txType]
			if !ok {
				panic("invalid tx type")
			}
			// generate the faucet package with the calculated amount
			faucetPkg, err := a.FaucetPackage(targetAddr, amount)
			if err != nil {
				return nil, nil, errors.ErrCouldNotCreateFaucetPackage.WithErr(err)
			}
			// include the faucet package in the tx
			txSetAccount.FaucetPackage = faucetPkg
			tx = &models.Tx{
				Payload: &models.Tx_SetAccount{
					SetAccount: txSetAccount,
				},
			}

		}
	case *models.Tx_NewProcess:
		txNewProcess := tx.GetNewProcess()
		// check the tx fields
		if txNewProcess == nil || txNewProcess.Process == nil {
			return nil, nil, errors.ErrInvalidTxFormat.With("missing fields")
		}
		// check the tx subtype
		switch txNewProcess.Txtype {
		case models.TxType_NEW_PROCESS:
			txType = models.TxType_NEW_PROCESS
			// get the tx cost for the tx type
			amount, ok := a.TxCosts[txType]
			if !ok {
				panic("invalid tx type")
			}
			// increment the amount with the election price to fund the
			// faucet package with the required amount for this type of
			// election
			amount += a.ElectionPriceCalc.Price(&electionprice.ElectionParameters{
				MaxCensusSize:           txNewProcess.Process.MaxCensusSize,
				ElectionDurationSeconds: txNewProcess.Process.Duration,
				EncryptedVotes:          txNewProcess.Process.EnvelopeType.EncryptedVotes,
				AnonymousVotes:          txNewProcess.Process.EnvelopeType.Anonymous,
				MaxVoteOverwrite:        txNewProcess.Process.VoteOptions.MaxVoteOverwrites,
			})
			// generate the faucet package with the calculated amount
			faucetPkg, err := a.FaucetPackage(targetAddr, amount)
			if err != nil {
				return nil, nil, errors.ErrCouldNotCreateFaucetPackage.WithErr(err)
			}
			// include the faucet package in the tx
			txNewProcess.FaucetPackage = faucetPkg
			tx = &models.Tx{
				Payload: &models.Tx_NewProcess{
					NewProcess: txNewProcess,
				},
			}
		}
	case *models.Tx_SetProcess:
		txSetProcess := tx.GetSetProcess()
		// check the tx fields
		if txSetProcess == nil || txSetProcess.ProcessId == nil {
			return nil, nil, errors.ErrInvalidTxFormat.With("missing fields")
		}
		// get the tx cost for the tx type
		amount, ok := a.TxCosts[txSetProcess.Txtype]
		if !ok {
			return nil, nil, errors.ErrInvalidTxFormat.With("invalid tx type")
		}

		// check the tx subtype
		switch txSetProcess.Txtype {
		case models.TxType_SET_PROCESS_STATUS:
			txType = models.TxType_SET_PROCESS_STATUS
			// check if the process status is in the tx
			if txSetProcess.Status == nil {
				return nil, nil, errors.ErrInvalidTxFormat.With("missing status field")
			}
		case models.TxType_SET_PROCESS_CENSUS:
			txType = models.TxType_SET_PROCESS_CENSUS
			// check if the process census is in the tx
			if (txSetProcess.CensusRoot == nil || txSetProcess.CensusURI == nil) && txSetProcess.CensusSize == nil {
				return nil, nil, errors.ErrInvalidTxFormat.With("missing census fields")
			}
			// get the current process to fill the missing fields in the tx to
			// calculate the election price
			currentProcess, err := a.client.Election(txSetProcess.ProcessId)
			if err != nil {
				return nil, nil, errors.ErrVochainRequestFailed.WithErr(err)
			}
			// increment the amount with the election price to fund the
			// faucet package with the required amount for this type of
			// election update
			amount += a.ElectionPriceCalc.Price(&electionprice.ElectionParameters{
				MaxCensusSize:           txSetProcess.GetCensusSize(),
				ElectionDurationSeconds: uint32(currentProcess.EndDate.Sub(currentProcess.StartDate).Seconds()),
				EncryptedVotes:          currentProcess.VoteMode.EncryptedVotes,
				AnonymousVotes:          currentProcess.VoteMode.Anonymous,
				MaxVoteOverwrite:        currentProcess.TallyMode.MaxVoteOverwrites,
			})
		case models.TxType_SET_PROCESS_RESULTS:
			txType = models.TxType_SET_PROCESS_RESULTS
			// check if the process results are in the tx
			if txSetProcess.Results == nil {
				return nil, nil, errors.ErrInvalidTxFormat.With("missing results field")
			}
		case models.TxType_SET_PROCESS_DURATION:
			txType = models.TxType_SET_PROCESS_DURATION
			// check if the process duration is in the tx
			if txSetProcess.Duration == nil {
				return nil, nil, errors.ErrInvalidTxFormat.With("missing duration field")
			}
			// get the current process to fill the missing fields in the tx to
			// calculate the election price
			currentProcess, err := a.client.Election(txSetProcess.ProcessId)
			if err != nil {
				return nil, nil, errors.ErrVochainRequestFailed.WithErr(err)
			}
			// increment the amount with the election price to fund the
			// faucet package with the required amount for this type of
			// election update
			amount += a.ElectionPriceCalc.Price(&electionprice.ElectionParameters{
				MaxCensusSize:           currentProcess.Census.MaxCensusSize,
				ElectionDurationSeconds: txSetProcess.GetDuration(),
				EncryptedVotes:          currentProcess.VoteMode.EncryptedVotes,
				AnonymousVotes:          currentProcess.VoteMode.Anonymous,
				MaxVoteOverwrite:        currentProcess.TallyMode.MaxVoteOverwrites,
			})
		}
		// generate the faucet package with the calculated amount
		faucetPkg, err := a.FaucetPackage(targetAddr, amount)
		if err != nil {
			return nil, nil, errors.ErrCouldNotCreateFaucetPackage.WithErr(err)
		}
		// include the faucet package in the tx
		txSetProcess.FaucetPackage = faucetPkg
		tx = &models.Tx{
			Payload: &models.Tx_SetProcess{
				SetProcess: txSetProcess,
			},
		}
	case *models.Tx_SetSIK, *models.Tx_DelSIK:
		txSetSIK := tx.GetSetSIK()
		// check the tx fields
		if txSetSIK == nil || txSetSIK.SIK == nil {
			return nil, nil, errors.ErrInvalidTxFormat.With("missing fields")
		}
		txType = models.TxType_SET_ACCOUNT_SIK
		// get the tx cost for the tx type
		amount, ok := a.TxCosts[txType]
		if !ok {
			return nil, nil, errors.ErrInvalidTxFormat.With("invalid tx type")
		}
		// generate the faucet package with the calculated amount
		faucetPkg, err := a.FaucetPackage(targetAddr, amount)
		if err != nil {
			return nil, nil, errors.ErrCouldNotCreateFaucetPackage.WithErr(err)
		}
		// include the faucet package in the tx
		txSetSIK.FaucetPackage = faucetPkg
		tx = &models.Tx{
			Payload: &models.Tx_SetSIK{
				SetSIK: txSetSIK,
			},
		}
	case *models.Tx_CollectFaucet:
		// no need to fund the faucet package
		txCollectFaucet := tx.GetCollectFaucet()
		txType = models.TxType_COLLECT_FAUCET
		tx = &models.Tx{
			Payload: &models.Tx_CollectFaucet{
				CollectFaucet: txCollectFaucet,
			},
		}
	default:
		return nil, nil, errors.ErrTxTypeNotAllowed
	}

	return tx, &txType, nil
}
