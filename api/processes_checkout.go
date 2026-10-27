package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"go.mongodb.org/mongo-driver/v2/bson"
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

// refusePaymentLocked refuses draft mutations while a payment is processing: money is in
// flight and its outcome is unknown, so the priced inputs must not move under it. A paid
// draft is not locked — every publish path re-prices against what was paid, refusing when
// the draft grew worth more, so an edit can only fix a draft that failed publish preflight,
// never under-charge. Pending payments do not lock either: the open session is expired and
// replaced at the next checkout. A paid draft is not refused by DELETE either: the money is
// returned first (see refundPaidProcess). Callers naming extra statuses in alsoLocked refuse
// those too.
// A payment state it cannot read is refused rather than assumed absent: nothing further
// down the write path consults payment state, so failing open here would let a transient
// Mongo error unlock exactly the draft this guard exists to protect.
func (a *API) refusePaymentLocked(w http.ResponseWriter, oid bson.ObjectID, alsoLocked ...db.ProcessPaymentStatus) bool {
	payment, err := a.db.ProcessPayment(oid)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return false // no payment: nothing to lock
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return true
	}
	if payment.Status == db.ProcessPaymentProcessing {
		errors.ErrPaymentSessionConflict.
			Withf("the draft is locked while its payment is being processed").Write(w)
		return true
	}
	if slices.Contains(alsoLocked, payment.Status) {
		errors.ErrPaymentSessionConflict.
			Withf("the draft payment is %s; edit the draft and publish it instead", payment.Status).Write(w)
		return true
	}
	return false
}

// refuseOrgAddOnsAfterPayment refuses an edit that turns a once-per-organization add-on on for a
// draft already paid without it, writing the refusal. A paid process's once-per-organization
// add-ons are fixed by its payment (processQuote), and there is no endpoint that sells one
// afterwards. An add-on the organization already paid changes no price and is let through.
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
	payment, err := a.db.ProcessPayment(vp.ID)
	if errors.Is(err, db.ErrNotFound) {
		return false
	}
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return true
	}
	if payment.Status != db.ProcessPaymentPaid {
		return false
	}
	org, err := a.db.Organization(vp.OrgAddress)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return true
	}
	for _, addOn := range added {
		if !payment.Charged(addOn) && org.AddOnPaidAt(addOn).IsZero() {
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
//	@Description	expired and replaced. 400 when the process is free or the wallet already covers it
//	@Description	(publish directly). 403 (40181) for a managed organization: its integrator's wallet
//	@Description	pays at publish. 409 (40177) when the draft is already paid (buy census headroom
//	@Description	instead) or its last checkout completed and is being credited. 422 (40176) above
//	@Description	50 000 voters (quote-only). Requires Admin role.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string								true	"Process ID"
//	@Param			request		body		apicommon.ProcessCheckoutRequest	true	"Checkout parameters"
//	@Success		200			{object}	apicommon.ProcessCheckoutResponse
//	@Failure		400			{object}	errors.Error	"Free process, wallet covers the price, or empty census"
//	@Failure		401			{object}	errors.Error
//	@Failure		403			{object}	errors.Error	"Managed organization (no wallet of its own)"
//	@Failure		404			{object}	errors.Error
//	@Failure		409			{object}	errors.Error	"Draft already paid, checkout completing, or the draft is busy"
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
	claimed, err := a.db.ClaimVotingProcessForPublish(oid)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !claimed {
		errors.ErrPublishInProgress.Write(w)
		return
	}
	defer func() {
		if e := a.db.ClearVotingProcessPublishing(oid); e != nil {
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
	quote, input, err := a.processQuote(vp)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	// win the add-on claims before the top-up is priced, so the price it is opened for is the
	// price this process is entitled to charge
	quote, claims, err := a.claimOrgAddOnsForPayment(vp, org, &input)
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
	if quote.TotalCents == 0 {
		errors.ErrMalformedBody.Withf("process is free, publish it directly").Write(w)
		return
	}
	// a payment state we cannot read is never assumed absent
	payment, err := a.db.ProcessPayment(oid)
	if err != nil && !errors.Is(err, db.ErrNotFound) {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	previousSessionID := ""
	if err == nil {
		switch payment.Status {
		case db.ProcessPaymentPending, db.ProcessPaymentFailed:
			previousSessionID = payment.CheckoutSessionID
		default:
			// paid (a draft whose census outgrew its price buys the difference as headroom), or
			// refunded by a delete that did not finish
			errors.ErrPaymentSessionConflict.Withf("the process payment is %s; a paid draft buys more voters "+
				"with POST /processes/{processId}/census/checkout", payment.Status).Write(w)
			return
		}
	}
	wallet, err := a.db.Wallet(org.Address)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	shortfall := quote.TotalCents - wallet.BalanceCents
	if shortfall <= 0 {
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
			// paid and not credited yet: a second top-up now would be priced from a stale balance
			errors.ErrPaymentSessionConflict.Withf("the last checkout completed and is being credited; retry shortly").Write(w)
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
		Payer:             org.Address,
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
		errors.ErrPaymentSessionConflict.Write(w)
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
//	@Description	never fulfills.
//	@Description	Requires Manager/Admin of the organization.
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{object}	apicommon.ProcessPaymentStatusResponse
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error	"Process not found, or it has no payment"
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
	payment, err := a.db.ProcessPayment(oid)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			errors.ErrProcessNotFound.Withf("process has no payment").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	resp := &apicommon.ProcessPaymentStatusResponse{
		Status:      payment.Status,
		AmountCents: payment.AmountCents,
		Currency:    payment.Currency,
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
//	@Description	lands in the wallet regardless. 404 when there is no pending payment; 409 (40177) when
//	@Description	the payment is paid or refunded. Requires Admin role.
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Success		200			{string}	string	"OK"
//	@Failure		401			{object}	errors.Error
//	@Failure		404			{object}	errors.Error	"Process not found, or it has no payment"
//	@Failure		409			{object}	errors.Error	"Payment paid or refunded, checkout changed, or draft busy"
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
			errors.ErrProcessNotFound.Withf("process has no payment").Write(w)
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
	claimed, err := a.db.ClaimVotingProcessForPublish(oid)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !claimed {
		errors.ErrPublishInProgress.Write(w)
		return
	}
	defer func() {
		if e := a.db.ClearVotingProcessPublishing(oid); e != nil {
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
	if !payment.Consumed && !a.releasePendingCheckout(w, payment) {
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

// releasePendingCheckout makes sure a pending payment's open checkout session can no longer be
// paid once the payment goes (cancelled, or deleted with its draft), writing the error and
// reporting false when it cannot. A session already completed or expired needs nothing: a
// completed one credits the wallet whatever happens to the process, and nothing else.
func (a *API) releasePendingCheckout(w http.ResponseWriter, payment *db.ProcessPayment) bool {
	if a.paymentGW == nil {
		errors.ErrPaymentSessionConflict.
			Withf("the payment gateway is unavailable; the checkout session cannot be released").Write(w)
		return false
	}
	session, err := a.paymentGW.GetPaymentSession(payment.CheckoutSessionID)
	if err != nil {
		errors.ErrStripeError.Withf("cannot reconcile checkout session").WithErr(err).Write(w)
		return false
	}
	if session.Status != stripe.SessionStatusOpen {
		return true
	}
	if err := a.paymentGW.ExpirePaymentSession(payment.CheckoutSessionID); err != nil {
		errors.ErrStripeError.Withf("cannot expire checkout session").WithErr(err).Write(w)
		return false
	}
	return true
}

// defaultPaymentStatuses are the payments that still need action — an open checkout, or one
// being settled — which is what the payments list answers when no status is asked for.
var defaultPaymentStatuses = []db.ProcessPaymentStatus{db.ProcessPaymentPending, db.ProcessPaymentProcessing}

// organizationProcessPaymentsHandler godoc
//
//	@Summary		List the process payments of an organization
//	@Description	The organization's process payments as stored, filtered by status — by default the
//	@Description	ones still open (pending, processing), which is how the drafts whose checkout
//	@Description	blocks publication are found. No live Stripe state is read: poll
//	@Description	GET /processes/{processId}/checkout for that. A payment whose process was deleted
//	@Description	(refunded) is listed with an empty title. Requires Manager/Admin of the organization.
//	@Tags			organizations
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string	true	"Organization address"
//	@Param			status		query		string	false	"Comma-separated statuses (default: pending,processing)"
//	@Param			sortOrder	query		string	false	"Order by creation time (default: desc)"	Enums(asc, desc)
//	@Param			page		query		integer	false	"Page number (default: 1)"
//	@Param			limit		query		integer	false	"Number of items per page (default: 10)"
//	@Success		200			{object}	apicommon.OrganizationProcessPaymentsResponse
//	@Failure		400			{object}	errors.Error	"Invalid status, sortOrder or pagination"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/organizations/{orgAddress}/payments [get]
func (a *API) organizationProcessPaymentsHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	org, _, ok := a.organizationFromRequest(r)
	if !ok {
		errors.ErrNoOrganizationProvided.Write(w)
		return
	}
	if !user.HasRoleFor(org.Address, db.AdminRole) && !user.HasRoleFor(org.Address, db.ManagerRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the organization").Write(w)
		return
	}
	query := r.URL.Query()
	statuses := defaultPaymentStatuses
	if raw := query.Get(ParamStatus); raw != "" {
		statuses = nil
		for s := range strings.SplitSeq(raw, ",") {
			status := db.ProcessPaymentStatus(strings.TrimSpace(s))
			if !status.IsValid() {
				errors.ErrMalformedURLParam.Withf("invalid %s %q", ParamStatus, status).Write(w)
				return
			}
			statuses = append(statuses, status)
		}
	}
	ascending := false
	switch sortOrder := query.Get(ParamSortOrder); sortOrder {
	case "", "desc":
	case "asc":
		ascending = true
	default:
		errors.ErrMalformedURLParam.Withf("invalid %s %q", ParamSortOrder, sortOrder).Write(w)
		return
	}
	params, err := parsePaginationParams(query.Get(ParamPage), query.Get(ParamLimit))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	total, payments, err := a.db.OrganizationProcessPayments(org.Address, db.ProcessPaymentsQuery{
		Statuses:  statuses,
		Ascending: ascending,
		Page:      params.Page,
		Limit:     params.Limit,
	})
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not get process payments: %v", err).Write(w)
		return
	}
	pagination, err := calculatePagination(params.Page, params.Limit, total)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	resp := &apicommon.OrganizationProcessPaymentsResponse{
		Payments:   make([]apicommon.OrganizationProcessPayment, 0, len(payments)),
		Pagination: pagination,
	}
	ids := make([]bson.ObjectID, len(payments))
	for i := range payments {
		ids[i] = payments[i].ProcessID
	}
	processes, err := a.db.VotingProcessesByIDs(ids)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	for i := range payments {
		p := &payments[i]
		item := apicommon.OrganizationProcessPayment{
			ProcessID:   p.ProcessID.Hex(),
			Status:      p.Status,
			AmountCents: p.AmountCents,
			Currency:    p.Currency,
			CreatedAt:   p.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		}
		if !p.PaidAt.IsZero() {
			item.PaidAt = p.PaidAt.UTC().Format("2006-01-02T15:04:05Z")
		}
		// a process no longer there is listed with an empty title
		if vp, ok := processes[p.ProcessID]; ok {
			item.Title = vp.Title
			item.Published = vp.Published
		}
		resp.Payments = append(resp.Payments, item)
	}
	apicommon.HTTPWriteJSON(w, resp)
}
