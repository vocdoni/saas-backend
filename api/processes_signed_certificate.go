package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"go.vocdoni.io/dvote/log"
)

// buyProcessSignedCertificateHandler godoc
//
//	@Summary		Buy the signed results certificate of a published process
//	@Description	Debits the signed results certificate add-on (€39, flat: never re-priced from the census)
//	@Description	from the payer's wallet for a published process, open or ended, quote-only included, and
//	@Description	turns addOns.signedCertificate on. A process that already has it is debited nothing
//	@Description	(amountCents 0). A draft selects the add-on with PUT /processes/{processId} instead (400).
//	@Description	No checkout is opened: when the wallet falls short a standard organization gets 402 (40178)
//	@Description	with the shortfall, tops its wallet up by at least shortfallCents and calls again; a managed
//	@Description	organization gets 402 (40175) or, past its spend cap, 402 (40183).
//	@Description	GET /processes/{processId}/price tells whether it is paid (signedCertificatePaid).
//	@Description	Requires Admin role. Also callable with a scoped API key (scope: `voting:write`).
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{object}	apicommon.ProcessCheckoutResponse
//	@Failure		400			{object}	errors.Error	"The process is a draft"
//	@Failure		401			{object}	errors.Error
//	@Failure		402			{object}	errors.Error	"Wallet short (40178, 40175), or spend cap reached (40183)"
//	@Failure		404			{object}	errors.Error
//	@Failure		409			{object}	errors.Error	"Open checkout or refunded (40177), or organization being deleted (40901)"
//	@Failure		500			{object}	errors.Error
//	@Failure		503			{object}	errors.Error	"Busy (50302), retry after Retry-After"
//	@Router			/processes/{processId}/signed-certificate [post]
func (a *API) buyProcessSignedCertificateHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	vp, ok := a.loadVotingProcess(w, oid)
	if !ok {
		return
	}
	if !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of the organization").Write(w)
		return
	}
	if !vp.Published {
		errors.ErrMalformedBody.Withf("the process is a draft: select the add-on with PUT /processes/{processId}").Write(w)
		return
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// the base is written from the stored process before any certificate debit, so a
	// grandfathered base never prices the certificate in
	p, projected, err := a.paidProcessProjection(vp, census, nil)
	if err == nil && !projected {
		err = errors.ErrGenericInternalServerError.Withf("published process %s has no payment base", oid.Hex())
	}
	if err != nil {
		writeAPIError(w, err)
		return
	}
	debited, err := a.buySignedCertificate(vp, p.org)
	if err != nil {
		writeAPIError(w, err)
		return
	}
	if debited > 0 {
		log.Infow("signed certificate bought", "processId", oid.Hex(), "debitedCents", debited)
	}
	apicommon.HTTPWriteJSON(w, &apicommon.ProcessCheckoutResponse{AmountCents: debited, Currency: "eur"})
}

// buySignedCertificate debits the certificate of the published vp from org's payer's wallet unless
// it is paid already, and returns what it debited. The ledger row is the record that it is paid,
// so it is read and written under the payer's wallet lock; the flag is its echo, redone by a retry.
func (a *API) buySignedCertificate(vp *db.VotingProcess, org *db.Organization) (pricing.Cents, error) {
	payer := walletPayer(org)
	walletLock, err := a.walletLocks.lockOrBusy(context.Background(), payer)
	if err != nil {
		return 0, err
	}
	defer walletLock.Unlock()
	if err := a.refuseTearingDown(vp.OrgAddress); err != nil {
		return 0, err
	}
	paid, err := a.processPaidState(vp.ID, payer)
	if err != nil {
		return 0, err
	}
	if paid.Refunded {
		return 0, errRefundedDraft
	}
	if paid.openCheckout() {
		return 0, errors.ErrPaymentSessionConflict.Withf(
			"process %s has an open top-up checkout the wallet cannot pay for", vp.ID.Hex())
	}
	var debited pricing.Cents
	if !vp.AddOns.SignedCertificate && paid.SignedCertificateCents == 0 {
		var managedOrg common.Address
		if org.ManagedBy != (common.Address{}) {
			managedOrg = vp.OrgAddress
		}
		if err := a.db.DebitWalletForSignedCertificate(db.WalletDebit{
			OrgAddress:        payer,
			ProcessID:         vp.ID,
			ProcessOrgAddress: vp.OrgAddress,
			AmountCents:       pricing.SignedCertCents,
			ManagedOrg:        managedOrg,
		}); err != nil {
			line := pricing.QuoteLine{
				Kind: pricing.LineSignedCert, Description: "signed results certificate", AmountCents: pricing.SignedCertCents,
			}
			return 0, a.walletDebitError(err, walletShort{
				org:      org,
				quote:    pricing.Quote{Lines: []pricing.QuoteLine{line}, TotalCents: pricing.SignedCertCents},
				dueCents: pricing.SignedCertCents,
				message: "the wallet does not cover the signed results certificate; " +
					"top it up by at least shortfallCents and call again",
			})
		}
		debited = pricing.SignedCertCents
	}
	if err := atMoneyStep(moneyStepSignedCert, vp.ID); err != nil {
		return 0, err
	}
	if err := a.db.SetVotingProcessSignedCertificate(vp.ID); err != nil {
		return 0, fmt.Errorf("failed to set the signed certificate add-on: %w", err)
	}
	a.refreshPaymentCache(vp, db.ProcessPaymentPaid, paid.cents()+debited)
	return debited, nil
}
