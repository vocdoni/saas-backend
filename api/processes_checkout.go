package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"go.vocdoni.io/dvote/log"
)

// paymentGateway is what the pay-per-process handlers need from Stripe, defined on the
// consumer side so API tests can install a fake; *stripe.Service satisfies it. The service only
// ever opens wallet top-ups: it never refunds.
type paymentGateway interface {
	CreatePaymentSession(params *stripe.PaymentSessionParams) (*stripe.PaymentSessionInfo, error)
	GetPaymentSession(sessionID string) (*stripe.PaymentSessionInfo, error)
	ExpirePaymentSession(sessionID string) error
}

// refuseOrgAddOnsAfterPayment refuses an edit that turns a once-per-organization add-on on for a
// draft already paid without it, writing the refusal. A paid process's once-per-organization
// add-ons are fixed by its payment (processQuote), and there is no endpoint that sells one
// afterwards. An add-on the organization already paid changes no price and is let through. Only a
// courtesy, read outside the payer's wallet lock: a stamp released after it is refused at publish
// (unstampedOrgAddOnError).
func (a *API) refuseOrgAddOnsAfterPayment(w http.ResponseWriter, vp *db.VotingProcess, addOns db.ProcessAddOns) bool {
	var added []db.OrgAddOn
	for _, addOn := range db.OrgAddOns {
		if addOns.Has(addOn) && !vp.AddOns.Has(addOn) {
			added = append(added, addOn)
		}
	}
	if len(added) == 0 {
		return false
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return true
	}
	// a stamp a claimant's credit-back gave back is not the organization's, whatever the
	// cut-short release left on it
	if org, err = a.healOrgOwedReleases(org); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return true
	}
	paid, err := a.processPaidState(vp.ID, walletPayer(org))
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return true
	}
	if !paid.paid() {
		return false
	}
	for _, addOn := range added {
		if !paid.Charged(addOn) && org.AddOnPaidAt(addOn).IsZero() {
			errors.ErrPaymentSessionConflict.
				Withf("the draft is paid without %s, which cannot be added after payment", addOn).Write(w)
			return true
		}
	}
	return false
}

// createProcessCheckoutHandler godoc
//
//	@Summary		Top up the wallet to publish a voting process
//	@Description	Opens a one-time Stripe checkout that tops up the organization's wallet by what it
//	@Description	lacks to pay the draft's server-calculated price (the shortfall, at least the €1
//	@Description	minimum; VAT added by Stripe Tax at checkout), and returns its client secret. Once the
//	@Description	payment is verified by webhook the wallet is credited and the process is published as
//	@Description	the requesting user, paid from the wallet; if it cannot be (its price rose, it changed
//	@Description	or was deleted) the credit stays in the wallet and the process is published by hand.
//	@Description	An open session for the same price and shortfall is reused; an obsolete one is
//	@Description	expired and replaced. A draft publish would refuse is refused here first, with the same
//	@Description	error, before any money is taken. 400 when the process is free, the wallet already
//	@Description	covers it (publish directly; an open checkout of the draft is expired and dropped
//	@Description	first), or it is not ready to publish. 403 (40181) for a managed
//	@Description	organization: its integrator's wallet pays at publish. 409 (40177) when the draft is
//	@Description	already paid (buy census headroom instead), a delete refunded it and did not finish
//	@Description	(delete it again), its last checkout completed and is being credited, or another
//	@Description	draft's payment holds a once-per-organization add-on it selects.
//	@Description	422 (40176) above 50 000 voters (quote-only). Requires Admin role.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string								true	"Process ID"
//	@Param			request		body		apicommon.ProcessCheckoutRequest	true	"Checkout parameters"
//	@Success		200			{object}	apicommon.ProcessCheckoutResponse
//	@Failure		400			{object}	errors.Error	"Free process, wallet covers the price, empty census, or not ready to publish"
//	@Failure		401			{object}	errors.Error
//	@Failure		403			{object}	errors.Error	"Managed organization (no wallet), or an integrator's own organization (40174)"
//	@Failure		404			{object}	errors.Error
//	@Failure		409			{object}	errors.Error	"Paid, checkout completing, add-on held by another draft, questions out of sync, or busy"
//	@Failure		422			{object}	errors.Error	"Census size requires a custom quote"
//	@Failure		500			{object}	errors.Error
//	@Router			/processes/{processId}/checkout [post]
func (a *API) createProcessCheckoutHandler(w http.ResponseWriter, r *http.Request) {
	if a.paymentGW == nil {
		errors.ErrStripeError.Withf("stripe service not available").Write(w)
		return
	}
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	req := &apicommon.ProcessCheckoutRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		errors.ErrMalformedBody.Write(w)
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
	if vp.Published {
		errors.ErrDuplicateConflict.Withf("process already published").Write(w)
		return
	}
	// Checkout takes the claim publish, cancel and delete take, until its pending payment is
	// stored. A draft being published is priced already; a cancel landing between the claim
	// refresh below and that write would release the add-on claims of the session it stores; a
	// delete would drop the draft and leave a payable session behind for a process that is gone.
	// Winning the claim proves the draft is still an unpublished draft, so it is re-read under it.
	owner, claimed, err := a.db.ClaimVotingProcessForPublish(oid, time.Time{})
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !claimed {
		errors.ErrPublishInProgress.Write(w)
		return
	}
	defer func() {
		if e := a.db.ClearVotingProcessPublishing(oid, owner); e != nil {
			log.Warnw("could not release the checkout's claim on a draft", "processId", oid.Hex(), "error", e)
		}
	}()
	if vp, ok = a.loadVotingProcess(w, oid); !ok {
		return
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if org.ManagedBy != (common.Address{}) {
		errors.ErrManagedOrgHasNoWallet.
			Withf("managed organization processes are paid from the integrator wallet at publish time").Write(w)
		return
	}
	// a draft publish would refuse is refused before any money is taken: its top-up could only
	// end up in the wallet
	questions, err := a.db.QuestionsByProcess(oid)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	target := publishTarget{vp: vp, questions: questions, census: census, user: user}
	if err := preflightError(a.publishPreflightProblems(target)); err != nil {
		writeSubscriptionError(w, err)
		return
	}
	// what the draft paid is read off its wallet's ledger; a state we cannot read is never
	// assumed absent
	paid, err := a.processPaidState(oid, org.Address)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// a draft a delete refunded can only be deleted again, as publish answers it
	if paid.Refunded {
		writeSubscriptionError(w, errRefundedDraft)
		return
	}
	if paid.paid() {
		// a draft whose census outgrew its price buys the difference as headroom
		errors.ErrPaymentSessionConflict.Withf("the process is paid; a paid draft buys more voters " +
			"with POST /processes/{processId}/census/checkout").Write(w)
		return
	}
	payment := paid.row
	previousSessionID := ""
	if payment != nil {
		previousSessionID = payment.CheckoutSessionID
	}
	quote, input, err := a.processQuote(vp)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	if err := input.heldError(); err != nil {
		writeSubscriptionError(w, err)
		return
	}
	// win the add-on claims before the top-up is priced, so the price it is opened for is the
	// price this process is entitled to charge
	quote, claims, err := a.claimOrgAddOnsForPayment(vp, &input)
	if err != nil {
		a.releaseRefusedOrgAddOnClaims(vp, claims)
		writeSubscriptionError(w, err)
		return
	}
	// every refusal below leaves no payment behind the claims, so they are given back; only a
	// session the stored payment references keeps them
	kept := false
	defer func() {
		if !kept {
			a.releaseRefusedOrgAddOnClaims(vp, claims)
		}
	}()
	if quote.QuoteRequired {
		errors.ErrQuoteRequired.Write(w)
		return
	}
	wallet, err := a.db.Wallet(org.Address)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	shortfall := quote.TotalCents - wallet.BalanceCents
	// free, or covered by the wallet: nothing is left to charge, so an open checkout is obsolete and
	// goes, as a cancel drops it, or publish would refuse the draft for it. Free is checked on its
	// own: a negative balance would otherwise open a checkout for a free draft.
	if quote.TotalCents == 0 || shortfall <= 0 {
		if err := a.dropObsoleteCheckout(vp, payment); err != nil {
			writeSubscriptionError(w, err)
			return
		}
		if quote.TotalCents == 0 {
			errors.ErrMalformedBody.Withf("process is free, publish it directly").Write(w)
			return
		}
		errors.ErrMalformedBody.Withf("the wallet covers the price; publish the process directly").Write(w)
		return
	}
	chargeCents := max(shortfall, pricing.MinimumChargeCents)
	if payment != nil && payment.Status == db.ProcessPaymentPending && !payment.Consumed {
		session, err := a.paymentGW.GetPaymentSession(payment.CheckoutSessionID)
		if err != nil {
			errors.ErrStripeError.Withf("cannot reconcile checkout session").WithErr(err).Write(w)
			return
		}
		switch session.Status {
		case stripe.SessionStatusComplete:
			// paid and not credited yet: a second top-up now would be priced from a stale balance.
			// A delayed payment method (SEPA) can stay complete but unpaid for days: cancelling it is
			// the way out
			errors.ErrPaymentSessionConflict.Withf("the last checkout completed and is being credited; retry " +
				"shortly, or cancel it with DELETE /processes/{processId}/checkout if its payment is still clearing").Write(w)
			return
		case stripe.SessionStatusOpen:
			if payment.AmountCents == quote.TotalCents && payment.TopUpCents == chargeCents {
				kept = true
				apicommon.HTTPWriteJSON(w, &apicommon.ProcessCheckoutResponse{
					ClientSecret: session.ClientSecret,
					SessionID:    session.ID,
					AmountCents:  payment.TopUpCents,
					Currency:     payment.Currency,
				})
				return
			}
			// obsolete session: it must be gone before a replacement may exist, so a failure to
			// expire refuses the checkout rather than leaving two payable sessions
			if err := a.paymentGW.ExpirePaymentSession(session.ID); err != nil {
				errors.ErrStripeError.Withf("cannot expire obsolete checkout session").WithErr(err).Write(w)
				return
			}
		default:
		}
	}
	session, err := a.openWalletTopUp(walletTopUp{
		wallet:      *org,
		user:        user,
		amountCents: chargeCents,
		description: fmt.Sprintf("wallet top-up to publish voting process %s", oid.Hex()),
		metadata:    map[string]string{stripe.MetadataKeyProcessID: oid.Hex()},
		returnURL:   req.ReturnURL,
		locale:      req.Locale,
	})
	if err != nil {
		errors.ErrStripeError.Withf("cannot create checkout session").WithErr(err).Write(w)
		return
	}
	stored, err := a.db.SetProcessPaymentPending(&db.ProcessPayment{
		ProcessID:         oid,
		OrgAddress:        vp.OrgAddress,
		CheckoutSessionID: session.ID,
		AmountCents:       quote.TotalCents,
		TopUpCents:        chargeCents,
		Currency:          "eur",
		RequestedBy:       user.Email,
		Branding:          input.Branding,
		CustomURL:         input.CustomURL,
	}, previousSessionID)
	if err != nil || !stored {
		// the session exists but nothing references it: expire it so it is not paid for a
		// publish that will not follow (paid anyway, it would only credit the wallet)
		if e := a.paymentGW.ExpirePaymentSession(session.ID); e != nil {
			log.Warnw("could not expire orphaned checkout session; paying it only credits the wallet",
				"sessionId", session.ID, "error", e)
		}
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		// a concurrent checkout stored first, or the session this one replaces expired meanwhile
		// and took its row with it: either way the caller retries against the current state
		errors.ErrPaymentSessionConflict.Withf("the checkout changed meanwhile; retry").Write(w)
		return
	}
	kept = true
	if previousSessionID != "" && previousSessionID != session.ID {
		log.Infow("replaced obsolete checkout session",
			"processId", oid.Hex(), "previousSessionId", previousSessionID, "sessionId", session.ID)
	}
	apicommon.HTTPWriteJSON(w, &apicommon.ProcessCheckoutResponse{
		ClientSecret: session.ClientSecret,
		SessionID:    session.ID,
		AmountCents:  chargeCents,
		Currency:     "eur",
	})
}

// processCheckoutStatusHandler godoc
//
//	@Summary		Get the payment status of a voting process
//	@Description	The stored payment state (pending while a top-up checkout is open, paid, refunded)
//	@Description	combined with the live Stripe state of the top-up checkout opened for the process, if
//	@Description	any. Payment truth comes from webhooks — this endpoint is for polling the outcome, it
//	@Description	never fulfills. consumed is true while a top-up credited to the wallet waits for the
//	@Description	publish the webhook then runs. refusal is set when it could not (price rose,
//	@Description	wallet short, the draft cannot publish, the requester is no longer an admin...): the
//	@Description	credit stays in the wallet and the process is published by hand. consumed with no
//	@Description	refusal and status still pending means the webhook will retry the publish: the credit
//	@Description	is already in the wallet, so POST /processes/{processId}/publish may be called at once
//	@Description	instead of waiting, and the retry then finds the process published. status paid means
//	@Description	the process is published or its publish job is running; jobId names the latest publish
//	@Description	job (the webhook's included): poll GET /jobs/{jobId}. status failed means the publish
//	@Description	failed after the payment and was credited back; that job's errors say why.
//	@Description	404 with 40179 means the draft exists but has no payment: none was opened, or a top-up
//	@Description	checkout that expired or whose delayed payment failed was dropped (open a new one); 404
//	@Description	with 40038 means the process itself is gone.
//	@Description	Requires Manager/Admin of the organization.
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{object}	apicommon.ProcessPaymentStatusResponse
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error	"Process not found (40038), or no payment or checkout for it (40179)"
//	@Failure		500			{object}	errors.Error
//	@Router			/processes/{processId}/checkout [get]
func (a *API) processCheckoutStatusHandler(w http.ResponseWriter, r *http.Request) {
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
	if !user.HasRoleFor(vp.OrgAddress, db.ManagerRole) && !user.HasRoleFor(vp.OrgAddress, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the organization").Write(w)
		return
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	paid, err := a.processPaidState(oid, walletPayer(org))
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	payment := paid.row
	if payment == nil {
		errors.ErrCheckoutSessionNotFound.Withf("process has no payment or checkout").Write(w)
		return
	}
	// a paid process's amount is what its ledger and base say it paid, never the row's cache
	amountCents := payment.AmountCents
	if payment.Status == db.ProcessPaymentPaid {
		amountCents = paid.cents()
	}
	resp := &apicommon.ProcessPaymentStatusResponse{
		Status:      payment.Status,
		AmountCents: amountCents,
		Currency:    payment.Currency,
		Consumed:    payment.Consumed,
		Refusal:     payment.TopUpRefusal,
		JobID:       payment.PublishJobID,
	}
	if !payment.PaidAt.IsZero() {
		resp.PaidAt = payment.PaidAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if payment.CheckoutSessionID != "" && a.paymentGW != nil {
		if session, err := a.paymentGW.GetPaymentSession(payment.CheckoutSessionID); err == nil {
			resp.SessionStatus = string(session.Status)
			resp.SessionPaymentStatus = string(session.PaymentStatus)
		}
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// cancelProcessCheckoutHandler godoc
//
//	@Summary		Cancel the open top-up checkout of a voting process
//	@Description	Expires the draft's open top-up checkout so it can no longer be paid, drops its pending
//	@Description	payment and releases the add-on claims the checkout took. It is how a draft whose
//	@Description	checkout is open is published from the wallet instead (publish refuses it with 409
//	@Description	while the session is payable). A checkout already completed is not expired: its money
//	@Description	lands in the wallet regardless. 404 (40179) when there is no payment; 409 (40177) when
//	@Description	the payment is paid, refunded or failed, or when the checkout's top-up was already spent
//	@Description	on the process's publish (its ledger shows it paid): publish it again instead, which
//	@Description	charges nothing. Requires Admin role.
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{string}	string	"OK"
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error	"Process not found (40038), or no payment or checkout for it (40179)"
//	@Failure		409			{object}	errors.Error	"Paid, refunded or failed, top-up spent on its publish, checkout changed, or draft busy"
//	@Failure		500			{object}	errors.Error	"Stripe could not verify or expire the session"
//	@Router			/processes/{processId}/checkout [delete]
func (a *API) cancelProcessCheckoutHandler(w http.ResponseWriter, r *http.Request) {
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
	payment, err := a.db.ProcessPayment(oid)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			errors.ErrCheckoutSessionNotFound.Withf("process has no payment or checkout").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if payment.Status != db.ProcessPaymentPending {
		errors.ErrPaymentSessionConflict.
			Withf("the payment is %s and has no checkout to cancel", payment.Status).Write(w)
		return
	}
	// cancel takes the claim checkout takes, so a checkout of this draft cannot store a new
	// session — and take the add-on claims for it — between the reads below and the release
	owner, claimed, err := a.db.ClaimVotingProcessForPublish(oid, time.Time{})
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !claimed {
		errors.ErrPublishInProgress.Write(w)
		return
	}
	defer func() {
		if e := a.db.ClearVotingProcessPublishing(oid, owner); e != nil {
			log.Warnw("could not release the cancel's claim on a draft", "processId", oid.Hex(), "error", e)
		}
	}()
	cancelled := payment.CheckoutSessionID
	if payment, err = a.db.ProcessPayment(oid); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if payment.Status != db.ProcessPaymentPending || payment.CheckoutSessionID != cancelled {
		errors.ErrPaymentSessionConflict.Withf("the checkout changed while it was being cancelled; retry").Write(w)
		return
	}
	// read before the payment is dropped, so the claim released below is the one it held
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if payment.Consumed {
		// the row is a cache: a consumed top-up the ledger shows spent on this process's publish
		// (a debit whose publish has not finished) paid for its add-ons, so releasing their claim
		// would let a sibling buy them again; publishing it again finishes it for free
		paid, err := a.processPaidState(oid, walletPayer(org))
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		if paid.PaidCents > 0 {
			errors.ErrPaymentSessionConflict.
				Withf("the top-up was spent on this process's publish; publish it again instead").Write(w)
			return
		}
	} else if err := a.releasePendingCheckout(payment); err != nil {
		writeSubscriptionError(w, err)
		return
	}
	dropped, err := a.db.DeleteProcessPaymentCheckout(oid, cancelled)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if dropped {
		// the expiry webhook may have dropped it first and released the same claims
		a.releaseDroppedCheckoutClaim(vp, org)
	}
	apicommon.HTTPWriteOK(w)
}

// dropObsoleteCheckout drops a draft's open top-up checkout when there is nothing left for it to
// charge (the draft became free, or the wallet covers it), as DELETE /checkout drops it: the session
// expired if still open (a completed one is left to credit the wallet), the row dropped by a CAS on
// it, and the draft's add-on claims released. Anything but an open checkout is left alone. The
// caller holds the publish claim, so no other checkout of the draft runs; the organization is read
// after the drop, since the caller's own claim refresh moved the claim times the release is pinned
// to. Returns why it cannot as an errors.Error, or a plain error meaning 500.
func (a *API) dropObsoleteCheckout(vp *db.VotingProcess, payment *db.ProcessPayment) error {
	if payment == nil || payment.Status != db.ProcessPaymentPending || payment.Consumed {
		return nil
	}
	if err := a.releasePendingCheckout(payment); err != nil {
		return err
	}
	dropped, err := a.db.DeleteProcessPaymentCheckout(vp.ID, payment.CheckoutSessionID)
	if err != nil {
		return err
	}
	if !dropped {
		return nil
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		// the claims go stale on their own (db.OrgAddOnClaimStaleAfter)
		log.Warnw("could not release the add-on claims of a dropped checkout", "processId", vp.ID.Hex(), "error", err)
		return nil
	}
	a.releaseDroppedCheckoutClaim(vp, org)
	return nil
}

// releasePendingCheckout makes sure a pending payment's open checkout session can no longer be
// paid once the payment goes (cancelled, or deleted with its draft). A session already completed
// or expired needs nothing: a completed one credits the wallet whatever happens to the process.
// With no payment gateway (cmd/userdel) the session is left to Stripe's own expiry: paying it only
// credits the wallet. Returns why it cannot as an errors.Error.
func (a *API) releasePendingCheckout(payment *db.ProcessPayment) error {
	if a.paymentGW == nil {
		log.Warnw("no payment gateway: the open checkout stays payable until stripe expires it; paying it only credits the wallet",
			"processId", payment.ProcessID.Hex(), "sessionId", payment.CheckoutSessionID)
		return nil
	}
	session, err := a.paymentGW.GetPaymentSession(payment.CheckoutSessionID)
	if err != nil {
		return errors.ErrStripeError.Withf("cannot reconcile checkout session").WithErr(err)
	}
	if session.Status != stripe.SessionStatusOpen {
		return nil
	}
	if err := a.paymentGW.ExpirePaymentSession(payment.CheckoutSessionID); err != nil {
		return errors.ErrStripeError.Withf("cannot expire checkout session").WithErr(err)
	}
	return nil
}
