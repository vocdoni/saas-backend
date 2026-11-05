package api

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/crypto/ethereum"
	"go.vocdoni.io/dvote/log"
)

// deleteVotingProcessHandler godoc
//
//	@Summary		Delete a voting process draft
//	@Description	Delete an unpublished voting process draft together with its inline census. A
//	@Description	published process has on-chain elections and cannot be deleted. Requires
//	@Description	Manager/Admin role of the organization that owns the process.
//	@Description
//	@Description	A paid draft is credited back to the wallet that paid it before it is deleted (the
//	@Description	organization's own, or its integrator's for a managed organization); nothing is ever
//	@Description	refunded to a card. The payment record goes with the draft: the credit-back row in the
//	@Description	wallet history (GET /organizations/{orgAddress}/wallet) is the audit trail. The
//	@Description	once-per-organization add-ons it paid (branding, custom URL) are released so the
//	@Description	organization's next process can buy them — except one another process relies on,
//	@Description	which is withheld from the credit and stays the organization's. An open top-up
//	@Description	checkout of the draft is expired first; one already completed still credits the
//	@Description	wallet when its webhook arrives.
//	@Description
//	@Description	409 (40903) also answers while an election a failed publish submitted is not settled on
//	@Description	chain yet (it may still be mined): publish the draft again (it adopts or resubmits at no
//	@Description	new charge) or retry later. One that was mined is adopted, and the draft then answers 409
//	@Description	(40904) like any partially published one.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `voting:write`).
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string			true	"Process ID"
//	@Success		200			{string}	string			"OK"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		409			{object}	errors.Error	"Already published, partially published (40904), being published, or payment changed"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Failure		503			{object}	errors.Error	"Busy (50302), retry after Retry-After"
//	@Router			/processes/{processId} [delete]
func (a *API) deleteVotingProcessHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	// loads the process + questions and gates on Manager/Admin of the owning org.
	vp, _, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	if err := a.deleteDraftProcess(vp, nil); err != nil {
		writeSubscriptionError(w, err)
		return
	}
	apicommon.HTTPWriteOK(w)
}

// deleteDraftProcess deletes a draft, returning its money first: what it paid is credited back to
// the wallet that paid it, an open top-up checkout is expired so it can no longer be paid, and the
// add-on release a credit-back still owes is applied. Its payment row goes with it: the wallet
// ledger is the record of what it paid and got back. It is what DELETE /processes/{processId}
// does after authorization, and what an organization teardown does to each of its drafts. Errors are
// errors.Error values carrying their HTTP semantics (write with writeSubscriptionError) or plain
// errors meaning 500; the draft is then kept and the delete can be retried. A non-nil teardown (an
// organization teardown, deleteOrgDrafts) drops a partially published draft too: its mined
// elections stay on chain like a published process's, nothing is credited back (they were
// delivered) and the payment row stays as their audit trail; and an add-on the draft paid is
// withheld from its credit-back only when the teardown found it delivered (refundPaidProcess),
// since every other draft goes too. DELETE /processes/{processId} passes nil and refuses a
// partially published draft with 40904 so a publish can resume it.
func (a *API) deleteDraftProcess(vp *db.VotingProcess, teardown *orgTeardown) error {
	oid := vp.ID
	// only a draft can be deleted; a published process lives on-chain and is immutable.
	if vp.Published {
		return errors.ErrDuplicateConflict.Withf("process already published and not in draft mode")
	}
	// Deleting mid-publish would orphan on chain whatever the worker has already mined, and
	// refund it. So delete takes the claim a publish takes, and the two exclude each other:
	// checking the marker on the read above would let a publish claim the draft right after.
	// Every refusal below releases it; a deleted draft takes the marker with it.
	owner, claimed, err := a.db.ClaimVotingProcessForPublish(oid, time.Time{})
	if err != nil {
		return fmt.Errorf("failed to claim the draft: %w", err)
	}
	if !claimed {
		return errors.ErrPublishInProgress
	}
	deleted := false
	defer func() {
		if deleted {
			return
		}
		if e := a.db.ClearVotingProcessPublishing(oid, owner); e != nil {
			log.Warnw("could not release the delete's claim on a draft", "processId", oid.Hex(), "error", e)
		}
	}()
	// a partially published draft (an earlier publish mined some elections before failing) is
	// not deletable on its own: dropping it would erase the on-chain ids and orphan the mined
	// elections, while publishing again resumes the remaining questions. Only a teardown
	// drops it. Reloaded under the claim, so no publish worker can be minting
	// elections concurrently.
	questions, err := a.db.QuestionsByProcess(oid)
	if err != nil {
		return fmt.Errorf("failed to get process questions: %w", err)
	}
	current, err := a.db.VotingProcess(oid)
	if err != nil {
		return fmt.Errorf("failed to get the draft: %w", err)
	}
	// a submitted publish tx is settled first: a mined one is adopted, and counts as mined below;
	// one that may still mine, or that the chain cannot tell about (or no chain: cmd/userdel),
	// keeps the draft, which would otherwise be refunded or dropped under a live election
	res, err := a.resolvePublishTxs(current, questions)
	if err != nil {
		return fmt.Errorf("failed to resolve submitted publish txs: %w", err)
	}
	if res.unresolved {
		return errPublishNotSettled
	}
	// the flag is set before a mined election id is stored, so it also covers an election whose
	// id never reached its question
	if anyMined(questions) || current.HasMinedElections {
		if teardown == nil {
			return errors.ErrProcessPartiallyPublished
		}
		if err := a.db.DeleteVotingProcess(oid); err != nil {
			return fmt.Errorf("failed to delete the draft: %w", err)
		}
		deleted = true
		if !vp.CensusID.IsZero() {
			_ = a.db.DelCensus(vp.CensusID.Hex())
		}
		return nil
	}
	// an open top-up checkout is expired first, outside the wallet lock (it calls Stripe), so it
	// can no longer be paid; a consumed one is in the wallet already
	payment, err := a.db.ProcessPayment(oid)
	if err != nil {
		// a payment state it cannot read is never taken as absent: the draft may hold money
		if !errors.Is(err, db.ErrNotFound) {
			return fmt.Errorf("failed to get process payment: %w", err)
		}
		payment = nil // free process, or never quoted
	}
	// read before the checkout's row is dropped, so the add-on claims released below are its own
	var claimHolder *db.Organization
	if payment != nil && payment.Status == db.ProcessPaymentPending {
		if claimHolder, err = a.db.Organization(vp.OrgAddress); err != nil {
			return fmt.Errorf("failed to get organization: %w", err)
		}
		if !payment.Consumed {
			if err := a.releasePendingCheckout(payment); err != nil {
				return err
			}
		}
	}
	// what the draft paid goes back before the draft goes away; the ledger is the record of it
	if err := a.refundPaidProcess(vp, teardown); err != nil {
		return err
	}
	if err := atMoneyStep(moneyStepPaymentDrop, oid); err != nil {
		return err
	}
	if err := a.db.DeleteProcessPayment(oid); err != nil {
		return err
	}
	if claimHolder != nil {
		a.releaseDroppedCheckoutClaim(vp, claimHolder)
	}
	if err := atMoneyStep(moneyStepDraftDelete, oid); err != nil {
		return err
	}
	if err := a.db.DeleteVotingProcess(oid); err != nil {
		return fmt.Errorf("failed to delete the draft: %w", err)
	}
	deleted = true
	// best-effort: drop the draft's inline census so it is not orphaned.
	if !vp.CensusID.IsZero() {
		_ = a.db.DelCensus(vp.CensusID.Hex())
	}
	return nil
}

// orgTeardown is what an organization teardown decides its credit-backs by: the
// once-per-organization add-ons a published or mined process of the organization received.
type orgTeardown struct {
	delivered []db.OrgAddOn
}

// withDraftID names the draft a teardown stopped on. A typed API error keeps its code, status and
// data and gains the id in its client-visible message; any other error becomes a 500 whose body
// names the draft and whose internal detail stays log-only.
func withDraftID(err error, id string) error {
	var apiErr errors.Error
	if errors.As(err, &apiErr) {
		return apiErr.With("draft " + id)
	}
	return errors.ErrGenericInternalServerError.WithErr(err).With("draft " + id)
}

// deleteOrgDrafts deletes every draft of an organization with deleteDraftProcess, so the paid
// ones are credited back before they go, and then credits back every once-per-organization add-on
// an earlier credit-back withheld that no process received (creditUndeliveredAddOns). Before it
// drops anything it records, under the payer's wallet lock, the add-ons a published or mined process
// received (db.RecordOrganizationAddOnsDelivered); every credit-back decides by that set, so what
// comes back depends neither on the order the drafts go in nor on what a crashed attempt already
// removed. It stops at the first draft it cannot delete, returning that error and leaving it and
// the rest in place, so a teardown aborts with no money stranded and a retry resumes where it
// stopped.
func (a *API) deleteOrgDrafts(org common.Address) error {
	teardown, payer, err := a.startOrgTeardown(org)
	if err != nil {
		return err
	}
	for {
		// always the first page: each pass deletes what it read, and stops on a failure
		_, drafts, err := a.db.ListVotingProcesses(org, "", db.DraftOnly, 1, 100)
		if err != nil {
			return fmt.Errorf("failed to list drafts: %w", err)
		}
		if len(drafts) == 0 {
			return a.creditUndeliveredAddOns(org, payer, teardown)
		}
		for i := range drafts {
			if err := a.deleteDraftProcess(&drafts[i], teardown); err != nil {
				log.Warnw("could not delete a draft of an organization being torn down",
					"org", org.Hex(), "processId", drafts[i].ID.Hex(), "error", err)
				return withDraftID(err, drafts[i].ID.Hex())
			}
		}
	}
}

// startOrgTeardown records the add-ons a published or mined process of org received, under its
// payer's wallet lock, and returns them with the payer.
func (a *API) startOrgTeardown(org common.Address) (*orgTeardown, common.Address, error) {
	o, err := a.db.Organization(org)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("failed to get organization: %w", err)
	}
	payer := walletPayer(o)
	walletLock, err := a.walletLocks.lockOrBusy(context.Background(), payer)
	if err != nil {
		return nil, common.Address{}, err
	}
	defer walletLock.Unlock()
	delivered, err := a.db.RecordOrganizationAddOnsDelivered(org)
	if err != nil {
		return nil, common.Address{}, err
	}
	return &orgTeardown{delivered: delivered}, payer, nil
}

// creditUndeliveredAddOns credits back, once all of org's drafts are gone, each
// once-per-organization add-on an earlier credit-back of the organization withheld (its price
// stayed the organization's stamp) that no published or mined process received. Each withheld
// price is credited once ever (db.TeardownAddOnCreditKey, per withholding row) to the wallet that
// paid it, as a creditBack naming the withholding process. The stamp goes first, in the same
// organization write that records the credit as owed (db.OweOrganizationAddOnCredit), so a cut
// before the credit leaves no stamp a branded publish could ride for free; a retry credits every
// owed price whatever the organization received since, and settles the record after the credit;
// an add-on bought and withheld again after such a cut is a second price, credited too. A row
// withholding several add-ons
// splits its withheld cents in its order, each at most its list price.
func (a *API) creditUndeliveredAddOns(org, payer common.Address, teardown *orgTeardown) error {
	walletLock, err := a.walletLocks.lockOrBusy(context.Background(), payer)
	if err != nil {
		return err
	}
	defer walletLock.Unlock()
	rows, err := a.db.OrganizationAddOnWithholdings(payer, org)
	if err != nil {
		return err
	}
	o, err := a.db.Organization(org)
	if err != nil {
		return fmt.Errorf("failed to get organization: %w", err)
	}
	var spentBy common.Address
	if payer != org {
		spentBy = org
	}
	for i := range rows {
		row := &rows[i]
		left := row.WithheldCents
		for _, addOn := range row.WithheldAddOns {
			cents := min(orgAddOnCents(addOn), left)
			left -= cents
			if cents <= 0 {
				continue
			}
			key := db.TeardownAddOnCreditKey(org, addOn, row.ProcessID)
			if !slices.Contains(o.AddOnsOwedCredit, key) {
				if slices.Contains(teardown.delivered, addOn) {
					continue
				}
				if err := a.db.OweOrganizationAddOnCredit(org, addOn, row); err != nil {
					return err
				}
			}
			if err := a.db.CreditWallet(db.WalletCredit{
				OrgAddress:        payer,
				AmountCents:       cents,
				IdempotencyKey:    key,
				Kind:              db.WalletEntryCreditBack,
				ProcessID:         row.ProcessID,
				ProcessOrgAddress: org,
				SpentBy:           spentBy,
			}); err != nil {
				return fmt.Errorf("failed to credit back the withheld %s: %w", addOn, err)
			}
			if err := a.db.SettleOrganizationAddOnCredit(org, addOn, row.ProcessID); err != nil {
				return err
			}
			log.Infow("withheld add-on no process received credited back at teardown",
				"org", org.Hex(), "addOn", addOn, "amountCents", cents, "payer", payer.Hex())
		}
	}
	return nil
}

// DeleteOrgDrafts deletes every draft of the organization the way DELETE /processes/{processId}
// does — a paid one credited back first to the wallet that paid it — for a caller outside the API
// server: cmd/userdel, whose erasure must not strand money when it tears an organization down. It
// has no payment gateway: an open top-up checkout is dropped with its draft and left to Stripe's
// own expiry, and paying it meanwhile only credits the wallet. It stops at the first draft it
// cannot delete, leaving it and the rest in place. It has no chain client either, so a draft with a
// submitted publish tx not settled yet (db.VotingProcess.PublishSubmitted) stops it with 409
// ErrPublishInProgress: run the service, which settles it, or delete the draft through the API.
//
// ponytail: the wallet lock it takes is its own, not the running server's — a census headroom
// charge on the same payer landing between a draft's refund read and its credit stays debited for
// the deleted draft (logged as an error); a DB lease if erasures ever run against live traffic.
func DeleteOrgDrafts(database *db.MongoStorage, org common.Address) error {
	if database == nil {
		return fmt.Errorf("a database is required")
	}
	a := &API{db: database, walletLocks: newOrgTxMutex()}
	return a.deleteOrgDrafts(org)
}

// DeleteUserOrgDrafts posts the unposted wallet movements, then deletes the drafts of every org the
// erasure of userID will tear down (sole admin), crediting the paid ones back (DeleteOrgDrafts). It
// returns the balance each torn-down org's own wallet keeps (managed orgs are omitted: their
// integrator pays). It refuses while the user's db.UserErasurePlan is Blocked, since EraseUser would
// then tear nothing down, and stops at the first failure; nothing but drafts is deleted. Every draft
// goes before any balance is read: a managed org's paid draft is credited to its integrator's wallet.
func DeleteUserOrgDrafts(database *db.MongoStorage, userID uint64) (map[common.Address]int64, error) {
	if database == nil {
		return nil, fmt.Errorf("a database is required")
	}
	if err := database.DrainUnpostedWalletEntries(); err != nil {
		return nil, fmt.Errorf("could not post unposted wallet movements, nothing was erased: %w", err)
	}
	plan, err := database.UserErasurePlan(userID)
	if err != nil {
		return nil, fmt.Errorf("could not plan the erasure, nothing was erased: %w", err)
	}
	if len(plan.Blocked) > 0 {
		return nil, fmt.Errorf("orgs %v have sub-orgs or managed orgs that would survive them, nothing was erased",
			plan.Blocked)
	}
	for _, planOrg := range plan.Orgs {
		if !planOrg.DeletedEntirely {
			continue
		}
		if err := DeleteOrgDrafts(database, planOrg.Address); err != nil {
			return nil, fmt.Errorf("could not delete the drafts of org %s, nothing else was erased: %w", planOrg.Address, err)
		}
	}
	balances := map[common.Address]int64{}
	for _, planOrg := range plan.Orgs {
		if !planOrg.DeletedEntirely || planOrg.ManagedBy != (common.Address{}) {
			continue
		}
		wallet, err := database.Wallet(planOrg.Address)
		if err != nil {
			return nil, fmt.Errorf("could not read the wallet of org %s: %w", planOrg.Address, err)
		}
		balances[planOrg.Address] = int64(wallet.BalanceCents)
	}
	return balances, nil
}

// votingProcessParticipantsHandler godoc
//
//	@Summary		List voted participants of a voting process
//	@Description	Manager/Admin lookup of organization members by a single field (email, phone,
//	@Description	memberNumber, nationalId), intersected with the process census, reporting each
//	@Description	matched member's per-question vote status: hasSigned when the CSP signed the member's
//	@Description	ballot (the vote may not be on chain), hasVoted only when the vote is on chain. In an
//	@Description	anonymous (blind) census hasVoted is always false and hasSigned is the only signal. When
//	@Description	the chain cannot be asked about a signed question, that question is reported with
//	@Description	hasVotedUnknown=true (and hasVoted=false) instead of failing the whole list. For `phone`
//	@Description	pass the plaintext number; it is hashed server-side. Requires Manager/Admin of the
//	@Description	owning organization.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `voting:write`).
//	@Tags			processes
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string	true	"Process ID"
//	@Param			field		query		string	true	"Lookup field: email, phone, memberNumber or nationalId"
//	@Param			value		query		string	true	"Value to match for the given field"
//	@Success		200			{object}	apicommon.ProcessParticipantsResponse
//	@Failure		400			{object}	errors.Error	"Invalid input data"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Failure		503			{object}	errors.Error	"Server busy hashing the phone, retry after Retry-After (50302)"
//	@Router			/processes/{processId}/participants [get]
func (a *API) votingProcessParticipantsHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	vp, questions, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	field := db.OrgMemberLookupField(r.URL.Query().Get("field"))
	if !field.IsValid() {
		errors.ErrMalformedBody.Withf("invalid field: must be one of email, phone, memberNumber, nationalId").Write(w)
		return
	}
	value := r.URL.Query().Get("value")
	if value == "" {
		errors.ErrMalformedBody.Withf("missing value").Write(w)
		return
	}
	// phone is stored hashed, so hash the plaintext before looking up.
	var lookupValue any = value
	if field == db.OrgMemberLookupFieldPhone {
		org, err := a.db.Organization(vp.OrgAddress)
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		hashed, err := db.NewHashedPhoneContext(r.Context(), value, org)
		if errors.Is(err, internal.ErrHashingBusy) {
			writeHashingBusy(w, err)
			return
		}
		if err != nil || hashed.IsEmpty() {
			errors.ErrMalformedBody.Withf("invalid phone").Write(w)
			return
		}
		lookupValue = hashed
	}

	resp := apicommon.ProcessParticipantsResponse{Participants: []apicommon.ProcessParticipantEntry{}}
	members, err := a.db.OrgMembersByField(vp.OrgAddress, field, lookupValue)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if len(members) == 0 {
		apicommon.HTTPWriteJSON(w, resp)
		return
	}
	memberIDs := make([]string, 0, len(members))
	membersByID := make(map[string]*db.OrgMember, len(members))
	for _, m := range members {
		id := m.ID.Hex()
		memberIDs = append(memberIDs, id)
		membersByID[id] = m
	}
	participants, err := a.db.CensusParticipantsByMemberIDs(vp.CensusID.Hex(), memberIDs)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if len(participants) == 0 {
		apicommon.HTTPWriteJSON(w, resp)
		return
	}
	participantIDs := make([]string, 0, len(participants))
	for _, p := range participants {
		participantIDs = append(participantIDs, p.ParticipantID)
	}
	// per-question status: each question is its own on-chain election, keyed by upstreamId. One
	// query fetches the CSP rows of every (question, matched member) pair; a missing pair is
	// "not signed".
	upstreamIDs := make([]internal.HexBytes, 0, len(questions))
	for i := range questions {
		if len(questions[i].UpstreamID) > 0 { // a question not yet on chain has no CSP rows
			upstreamIDs = append(upstreamIDs, questions[i].UpstreamID)
		}
	}
	rows, err := a.db.CSPProcessesByMembers(upstreamIDs, participantIDs)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	statuses, err := a.resolveVoteStatuses(r.Context(), rows)
	// the caller gave up waiting: nobody is left to read an answer, and its cancelled lookups are
	// no node failure to log
	if r.Context().Err() != nil {
		return
	}
	if err != nil {
		// a lookup failed: the affected questions are reported with hasVotedUnknown rather than
		// failing the whole list, whose other data comes from the database
		log.Warnw("vote status lookup failed", "processId", oid.Hex(), "error", err)
	}
	for _, p := range participants {
		m, exists := membersByID[p.ParticipantID]
		if !exists {
			continue
		}
		entry := apicommon.ProcessParticipantEntry{
			MemberID:     m.ID.Hex(),
			Name:         m.Name,
			Surname:      m.Surname,
			Email:        m.Email,
			MemberNumber: m.MemberNumber,
		}
		for i := range questions {
			q := &questions[i]
			if len(q.UpstreamID) == 0 {
				continue
			}
			st := statuses[voteStatusKey(m.ID.Hex(), q.UpstreamID.String())]
			entry.Questions = append(entry.Questions, apicommon.ProcessParticipantQuestionVote{
				QuestionID:      q.ID.Hex(),
				UpstreamID:      q.UpstreamID,
				HasSigned:       st.Signed,
				HasVoted:        st.Voted,
				HasVotedUnknown: st.unknown,
			})
		}
		resp.Participants = append(resp.Participants, entry)
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// voteStatus is a member's resolved status on one election (see handlers.VoteStatusResolver);
// unknown marks a status whose on-chain lookup failed.
type voteStatus struct {
	handlers.VoteStatus
	unknown bool
}

// voteStatusKey keys a voteStatus by member id hex and election (upstream) id hex.
func voteStatusKey(memberID, upstreamID string) string { return memberID + "/" + upstreamID }

// resolveVoteStatuses resolves every CSP row concurrently (one goroutine per row; only uncached,
// non-blind rows reach the chain, through the semaphore-bounded lookup). A row whose lookup fails
// keeps its signed status and is marked unknown; the failures are returned joined.
func (a *API) resolveVoteStatuses(ctx context.Context, rows []db.CSPProcess) (map[string]voteStatus, error) {
	resolver := handlers.VoteStatusResolver{Store: a.db, Lookup: a.voteOnChain}
	results := make([]voteStatus, len(rows))
	errs := make([]error, len(rows))
	var wg sync.WaitGroup
	for i := range rows {
		wg.Go(func() {
			results[i].VoteStatus, errs[i] = resolver.Resolve(ctx, &rows[i])
			results[i].unknown = errs[i] != nil
		})
	}
	wg.Wait()
	statuses := make(map[string]voteStatus, len(rows))
	for i := range rows {
		statuses[voteStatusKey(rows[i].UserID.String(), rows[i].ProcessID.String())] = results[i]
	}
	return statuses, stderrors.Join(errs...)
}

// updateVotingProcessCensusHandler godoc
//
//	@Summary		Add members to a published process's census
//	@Description	Add existing organization members to the census of an already-published voting process
//	@Description	and raise each affected on-chain election's maxCensusSize so the new members can vote. Members are
//	@Description	added synchronously; the maxCensusSize update runs as an async job (poll GET /jobs/{jobId}).
//	@Description	Questions with an eligibility subset keep their fixed size and are unaffected. Requires
//	@Description	Manager/Admin role.
//	@Description
//	@Description	402 when the change would grow a published process's census beyond what it paid for:
//	@Description	40178 with the quote in data (its processId names the process to buy headroom for with
//	@Description	POST /processes/{processId}/census/checkout); for a managed organization, 40175 when its
//	@Description	integrator's wallet falls short (data: requiredCents, availableCents), or 40183 past the
//	@Description	spend cap its integrator set (data: capCents, spentCents, requiredCents).
//	@Description	422 when the grown census needs a custom quote.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string													true	"Process ID"
//	@Param			request		body		apicommon.AddCensusParticipantsRequest					true	"Member IDs to add"
//	@Success		200			{object}	apicommon.UpdateProcessCensusResponse					"Members added; no on-chain resize needed"
//	@Success		202			{object}	apicommon.UpdateProcessCensusResponse					"Members added; maxCensusSize update enqueued"
//	@Failure		400			{object}	errors.Error											"Invalid input data"
//	@Failure		401			{object}	errors.Error											"Unauthorized"
//	@Failure		404			{object}	errors.Error											"Process not found"
//	@Failure		402			{object}	errors.Error{data=apicommon.ProcessCensusGrowthQuote}	"Census growth beyond the price paid"
//	@Failure		422			{object}	errors.Error											"Census size requires a custom quote"
//	@Failure		409			{object}	errors.Error											"Process is not published"
//	@Failure		500			{object}	errors.Error											"Internal server error"
//	@Failure		503			{object}	errors.Error											"Busy (50302), retry after Retry-After"
//	@Router			/processes/{processId}/census [put]
func (a *API) updateVotingProcessCensusHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	// loads the process + questions and gates on Manager/Admin of the owning org.
	vp, questions, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	// only a published process can have its on-chain census extended; drafts use PUT /processes.
	if !vp.Published {
		errors.ErrDuplicateConflict.Withf("process is not published; edit the draft via PUT /processes/{processId}").Write(w)
		return
	}
	var req apicommon.AddCensusParticipantsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Withf("couldn't decode participant IDs").Write(w)
		return
	}
	if len(req.MemberIDs) == 0 {
		apicommon.HTTPWriteJSON(w, &apicommon.UpdateProcessCensusResponse{Added: 0})
		return
	}
	grown, err := a.addProcessCensusMembers(r.Context(), vp, req.MemberIDs)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	census := grown.census

	// only whole-census questions grow when participants are added; one that names an eligibility
	// subset is unaffected by who else joined the census. A terminal question has no room to take
	// them, as in census propagation.
	targets := make([]censusSizeTarget, 0, len(questions))
	for i := range questions {
		if isOpenStatus(questions[i].Status) && len(questions[i].EligibleMemberIDs) == 0 {
			targets = append(targets, censusSizeTarget{
				question: questions[i], census: census, size: uint64(census.Size),
			})
		}
	}
	jobID, err := a.enqueueSetProcessCensus(vp.OrgAddress, targets)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	resp := &apicommon.UpdateProcessCensusResponse{JobID: jobID, Added: uint32(grown.added), Errors: grown.memberErrs}
	if jobID == "" {
		// no whole-census election to resize (subset-only questions): nothing async is pending, so 200.
		apicommon.HTTPWriteJSON(w, resp)
		return
	}
	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, resp)
}

// processCensusGrowth is what addProcessCensusMembers did to a process census.
type processCensusGrowth struct {
	census     *db.Census // with its recounted size stored
	added      int
	memberErrs []string
}

// addProcessCensusMembers adds memberIDs to the census of the published process vp and stores
// its recounted size. Its errors are errors.Error values (write them with writeSubscriptionError),
// or plain errors meaning 500.
//
// The growth check and the write it allows are one step per organization (censusGrowthLocks),
// and the census is read inside it: two parallel requests would otherwise each pass against
// the same size and together grow past the price paid. Memberbase propagation takes the same
// lock. It is released before the caller enqueues the on-chain resize, which waits on orgTxLocks.
func (a *API) addProcessCensusMembers(
	ctx context.Context, vp *db.VotingProcess, memberIDs []string,
) (*processCensusGrowth, error) {
	growthLock, err := a.censusGrowthLocks.lockOrBusy(ctx, vp.OrgAddress)
	if err != nil {
		return nil, err
	}
	defer growthLock.Unlock()
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		return nil, err
	}
	// pay-per-process: the price is a function of the census size, so the census may grow
	// only as far as the price already paid reaches. Past that the 402 carries what the
	// growth costs, which POST /processes/{processId}/census/checkout sells — by card, or
	// from the integrator wallet for a managed organization.
	if err := a.censusGrowthRefusal(vp, census, memberIDs); err != nil {
		return nil, err
	}

	added, memberErrs, err := a.db.AddCensusParticipantsByMemberIDs(census.ID.Hex(), memberIDs)
	switch {
	case err == nil:
	case stderrors.Is(err, db.ErrInvalidData), stderrors.Is(err, db.ErrUpdateWouldCreateDuplicates):
		return nil, errors.ErrInvalidData.WithErr(err)
	case stderrors.Is(err, db.ErrNotFound):
		return nil, errors.ErrCensusNotFound
	default:
		return nil, err
	}

	// recount and persist the census size so the on-chain maxCensusSize set next is correct.
	size, err := a.db.CountCensusParticipants(census.ID.Hex())
	if err != nil {
		return nil, err
	}
	census.Size = size
	if _, err := a.db.SetCensus(census); err != nil {
		return nil, err
	}
	return &processCensusGrowth{census: census, added: added, memberErrs: memberErrs}, nil
}

// updateVotingProcessQuestionCensusHandler godoc
//
//	@Summary		Set a question's eligibility list
//	@Description	Replace the set of members eligible to vote one question of a voting process, for a
//	@Description	draft or a published process (closes #611). Requires Manager/Admin role.
//	@Description
//	@Description	The body is the **complete desired list, not a delta**, so the request is idempotent:
//	@Description	resend the whole list to change it. Every id must already be a participant of the
//	@Description	process census. Input order is preserved and duplicates are dropped, so a client can
//	@Description	diff what it reads back against its next request.
//	@Description
//	@Description	**An empty list does not mean "nobody": it is the encoding for "no restriction" and
//	@Description	opens the question to every member of the census.** A response of `eligible: 0`
//	@Description	therefore means the question is open to everyone.
//	@Description
//	@Description	Because reopening a restricted question can multiply its electorate while its election
//	@Description	was sized on chain for the old subset, a maxCensusSize increase is enqueued as an async
//	@Description	job whenever the question needs more room than it was published with (202; poll GET
//	@Description	/jobs/{jobId}).
//	@Description
//	@Description	Removing a member the CSP has already signed for, while a question of this process is
//	@Description	still READY or PAUSED, is refused with 409 and the offending ids in
//	@Description	`data.signedMemberIds`.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `voting:write`).
//	@Description
//	@Description	402 when the change would grow a published process's census beyond what it paid for:
//	@Description	40178 with the quote in data (its processId names the process to buy headroom for with
//	@Description	POST /processes/{processId}/census/checkout); for a managed organization, 40175 when its
//	@Description	integrator's wallet falls short (data: requiredCents, availableCents), or 40183 past the
//	@Description	spend cap its integrator set (data: capCents, spentCents, requiredCents).
//	@Description	422 when the grown census needs a custom quote.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string													true	"Process ID"
//	@Param			questionId	path		string													true	"Question ID"
//	@Param			request		body		apicommon.UpdateQuestionCensusRequest					true	"Complete desired eligibility list"
//	@Success		200			{object}	apicommon.UpdateQuestionCensusResponse					"Eligibility updated; no on-chain resize needed"
//	@Success		202			{object}	apicommon.UpdateQuestionCensusResponse					"Eligibility updated; maxCensusSize update enqueued"
//	@Failure		400			{object}	errors.Error											"Invalid input data, or a member is not part of the census"
//	@Failure		401			{object}	errors.Error											"Unauthorized"
//	@Failure		404			{object}	errors.Error											"Process or question not found"
//	@Failure		409			{object}	errors.Error											"Publish in progress, member already signed for, or list changed"
//	@Failure		402			{object}	errors.Error{data=apicommon.ProcessCensusGrowthQuote}	"Census growth beyond the price paid"
//	@Failure		422			{object}	errors.Error											"Census size requires a custom quote"
//	@Failure		500			{object}	errors.Error											"Internal server error"
//	@Router			/processes/{processId}/questions/{questionId}/census [put]
func (a *API) updateVotingProcessQuestionCensusHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	// loads the process + questions and gates on Manager/Admin of the owning org.
	vp, questions, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	// while a publish worker holds the process, its questions all look like drafts: an eligibility
	// write would take the draft path, skip the resize, and the worker would then mint an election
	// sized from its older snapshot. So an unpublished process is changed under the claim a publish
	// takes, as delete does, and the two exclude each other; checking the marker on the read above
	// would let a publish claim the process right after. The questions are reloaded under the
	// claim. A published process is never claimed again, so it needs none.
	if !vp.Published {
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
				log.Warnw("could not release the eligibility update's claim", "processId", oid.Hex(), "error", e)
			}
		}()
		if questions, err = a.db.QuestionsByProcess(oid); err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
	}
	question, ok := questionOfProcess(w, questions, chi.URLParam(r, "questionId"))
	if !ok {
		return
	}

	var req apicommon.UpdateQuestionCensusRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Withf("couldn't decode member IDs").Write(w)
		return
	}
	census, err := a.db.Census(vp.CensusID.Hex())
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	eligible, err := a.validateCensusParticipants(census.ID.Hex(), req.MemberIDs)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}

	previous := question.EligibleMemberIDs
	added, removed := diffMemberIDs(previous, eligible)
	// Members losing eligibility lose their ability to vote this question, so the refusal has to
	// happen before the write. It is keyed off who has been signed for, not off `removed`: a
	// question open to the whole census names nobody, so restricting it drops voters that no diff
	// against the stored list can report.
	if a.refuseVotersLosingEligibility(w, question, eligible) {
		return
	}
	if len(added) == 0 && len(removed) == 0 && len(question.UpstreamID) == 0 {
		// a draft that already says this: nothing to store, and no election to size
		apicommon.HTTPWriteJSON(w, &apicommon.UpdateQuestionCensusResponse{Eligible: uint32(len(eligible))})
		return
	}

	// What the election needs, with an empty list meaning the whole census (see the resize below).
	needed := uint64(len(eligible))
	if len(eligible) == 0 {
		needed = uint64(census.Size)
	}
	// the resize below pushes needed on chain; past what the process paid for it is refused here,
	// before the list is stored — refusing at the resize would strand it undersized. Pricing needed
	// rather than the census lets a question narrow even once the census has outgrown its price.
	if len(question.UpstreamID) > 0 {
		if err := a.censusGrowthPaymentError(censusGrowth{vp: vp, census: census, size: int64(needed)}); err != nil {
			writeSubscriptionError(w, err)
			return
		}
	}
	won, err := a.db.SetQuestionEligibleMemberIDs(question.ID, previous, eligible)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if !won {
		errors.ErrDuplicateConflict.Withf("the eligibility list changed while this request was being served").Write(w)
		return
	}

	resp := &apicommon.UpdateQuestionCensusResponse{
		Eligible: uint32(len(eligible)),
		Added:    uint32(len(added)),
		Removed:  uint32(len(removed)),
	}

	// needed, computed above, is always handed to enqueueSetProcessCensus rather than pre-filtered
	// against what the stored list implies the election was sized for. That proxy is wrong in exactly the case
	// that matters: the list is committed above before the resize is enqueued, so a queue-full 503
	// leaves the two disagreeing — and the retry, which is the whole recovery since nothing sweeps
	// failed SetProcessCensus jobs, diffs to nothing and would conclude the election already has
	// the room it never got. enqueueSetProcessCensus reads the election itself and enqueues only a
	// genuine increase, so it is the one place that can tell a completed request from an
	// interrupted one. The cost is a chain read on an idempotent replay.
	//
	// Note this is not keyed off "were members added" either: reopening a restricted question adds
	// nobody by name yet can multiply the electorate, and account.ComputeMaxCensusSize stamped that
	// election at exactly the old subset size — zero headroom. The CSP would sign and the chain
	// would reject.
	question.EligibleMemberIDs = eligible
	jobID, err := a.enqueueSetProcessCensus(vp.OrgAddress, []censusSizeTarget{
		{question: *question, census: census, size: needed},
	})
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	if jobID == "" {
		apicommon.HTTPWriteJSON(w, resp)
		return
	}
	resp.JobID = jobID
	apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, resp)
}

// questionOfProcess resolves a question id against the process's own questions, so a question of
// another process is a 404 rather than a cross-process write.
func questionOfProcess(
	w http.ResponseWriter, questions []db.VotingProcessQuestion, questionID string,
) (*db.VotingProcessQuestion, bool) {
	qid, err := bson.ObjectIDFromHex(questionID)
	if err != nil {
		errors.ErrMalformedURLParam.Withf("invalid question ID").Write(w)
		return nil, false
	}
	for i := range questions {
		if questions[i].ID == qid {
			return &questions[i], true
		}
	}
	errors.ErrProcessNotFound.Withf("question not found").Write(w)
	return nil, false
}

// diffMemberIDs reports which ids next gains over previous, and which it drops.
func diffMemberIDs(previous, next []string) (added, removed []string) {
	prev := make(map[string]bool, len(previous))
	for _, id := range previous {
		prev[id] = true
	}
	cur := make(map[string]bool, len(next))
	for _, id := range next {
		cur[id] = true
		if !prev[id] {
			added = append(added, id)
		}
	}
	for _, id := range previous {
		if !cur[id] {
			removed = append(removed, id)
		}
	}
	return added, removed
}

// removeVotingProcessCensusHandler godoc
//
//	@Summary		Remove members from a published process census
//	@Description	Remove members from a published voting process's census and from every question
//	@Description	eligibility list built on it, so the CSP stops signing for them. Refused with 409 for
//	@Description	any member the CSP has already signed for while a question of the process is still
//	@Description	READY or PAUSED; once voting closes on those questions the removal succeeds. The
//	@Description	offending ids come back in `data.signedMemberIds`. Members no longer in the census are
//	@Description	removed as a no-op rather than refused.
//	@Description
//	@Description	Pruning a question's eligibility list to empty opens it to the whole census, so a
//	@Description	maxCensusSize increase may be enqueued as an async job (poll GET /jobs/{jobId}).
//	@Description	Requires Manager/Admin role.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `voting:write`).
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			processId	path		string									true	"Process ID"
//	@Param			request		body		apicommon.AddCensusParticipantsRequest	true	"Member IDs to remove"
//	@Success		200			{object}	apicommon.UpdateProcessCensusResponse	"Members removed"
//	@Success		202			{object}	apicommon.UpdateProcessCensusResponse	"Members removed; maxCensusSize update enqueued"
//	@Failure		400			{object}	errors.Error							"Invalid input data"
//	@Failure		401			{object}	errors.Error							"Unauthorized"
//	@Failure		404			{object}	errors.Error							"Process not found"
//	@Failure		409			{object}	errors.Error							"Process is not published, or a member has already been signed for"
//	@Failure		500			{object}	errors.Error							"Internal server error"
//	@Router			/processes/{processId}/census [delete]
func (a *API) removeVotingProcessCensusHandler(w http.ResponseWriter, r *http.Request) {
	oid, ok := a.votingProcessID(w, r)
	if !ok {
		return
	}
	// loads the process + questions and gates on Manager/Admin of the owning org.
	vp, _, ok := a.authorizeStatusChange(w, r, oid)
	if !ok {
		return
	}
	if !vp.Published {
		errors.ErrDuplicateConflict.Withf(
			"process is not published; edit the draft via PUT /processes/{processId}",
		).Write(w)
		return
	}

	var req apicommon.AddCensusParticipantsRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		errors.ErrMalformedBody.Withf("couldn't decode participant IDs").Write(w)
		return
	}
	if len(req.MemberIDs) == 0 {
		apicommon.HTTPWriteJSON(w, &apicommon.UpdateProcessCensusResponse{Removed: 0})
		return
	}
	if len(req.MemberIDs) > maxCensusRemoval {
		errors.ErrInvalidData.Withf(
			"too many member ids: %d, the maximum per request is %d", len(req.MemberIDs), maxCensusRemoval,
		).Write(w)
		return
	}

	// scoped before the guard and the write, exactly as deleteOrganizationMembersHandler does. The
	// cascade derives the censuses to touch from the ids themselves and deletes CSP sessions by
	// userid alone, so an id that reaches it unscoped revokes a member of another organization —
	// being a Manager of *this* process's org is not authority over an id this org never owned.
	// Unknown and foreign ids are dropped rather than rejected, so `removed` stays the count of
	// rows actually deleted.
	targetIDs, err := a.db.FilterOrgMemberIDs(vp.OrgAddress, req.MemberIDs)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not resolve org members: %v", err).Write(w)
		return
	}
	if len(targetIDs) == 0 {
		apicommon.HTTPWriteJSON(w, &apicommon.UpdateProcessCensusResponse{Removed: 0})
		return
	}

	censusID := vp.CensusID.Hex()
	// before any write, so a refusal leaves the census exactly as it was
	if a.refuseBlockedVoters(w, []string{censusID}, targetIDs) {
		return
	}

	removed, emptied, err := a.db.RevokeMembersFromCensuses([]string{censusID}, targetIDs)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}

	// the participant rows actually deleted, not the ids submitted: an id naming nobody, or the
	// same id twice, must not be reported as a removal
	resp := &apicommon.UpdateProcessCensusResponse{Removed: uint32(removed)}
	jobID, resizeErrs := a.resizeEmptiedQuestions(vp.OrgAddress, emptied)
	resp.Errors = resizeErrs
	if jobID != "" {
		resp.JobID = jobID
		apicommon.HTTPWriteJSONStatus(w, http.StatusAccepted, resp)
		return
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// maxCensusRemoval bounds one DELETE /processes/{processId}/census body. Every id travels as a
// single $in filter through the guard and the three revocation writes, so an unbounded list is an
// unbounded query; a caller with more to remove pages through.
const maxCensusRemoval = 1000

// censusSizeTarget is one published question whose on-chain election may need its maxCensusSize
// raised, with the census the new size is read from. A single group census can back several
// processes, so one request can produce targets spread across them.
type censusSizeTarget struct {
	question db.VotingProcessQuestion
	census   *db.Census
	// size is what the election has to hold from now on: the eligibility subset when the question
	// names one, otherwise the whole census.
	size uint64
}

// electionSizeAndStatus reads a published question's current on-chain maxCensusSize and status,
// from one read of the election.
func (a *API) electionSizeAndStatus(q *db.VotingProcessQuestion) (uint64, string, error) {
	election, err := a.account.Election(q.UpstreamID)
	if err != nil {
		return 0, "", err
	}
	if election.Census == nil {
		return 0, "", fmt.Errorf("election %s carries no census", q.UpstreamID.String())
	}
	return election.Census.MaxCensusSize, election.Status, nil
}

// isOpenStatus reports whether an election, on chain or stored, still lets its census change:
// READY or PAUSED. ENDED, CANCELED and RESULTS are terminal and refuse SET_PROCESS_CENSUS.
func isOpenStatus(status string) bool {
	return status == db.QuestionStatusReady || status == db.QuestionStatusPaused
}

// enqueueSetProcessCensus submits one SET_PROCESS_CENSUS tx per target, as a single background job,
// to raise those elections' maxCensusSize. It returns an empty job id when nothing needs raising.
//
// It returns an error rather than writing a response, so one request can drive several processes —
// a group census can back more than one. Callers map it with writeSubscriptionError, which
// preserves ErrTxQueueFull's 503.
//
// It never enqueues a shrink. The chain accepts an increase only, and a census can now lose members,
// so blindly submitting the census size would eventually fail the job; each election is read first
// and only a genuine increase is sent.
//
// It never answers ErrOrgTxBusy: when the organization lock is held, the job is created anyway and
// the resize waits for the lock in the background (see below).
//
// A failed election read does not fail the request. By the time this runs, the participant or
// eligibility write it follows has already committed, so reporting failure would report it for work
// that landed — the tx goes out anyway and a chain refusal surfaces on the job with its real reason.
func (a *API) enqueueSetProcessCensus(orgAddress common.Address, targets []censusSizeTarget) (string, error) {
	growing := make([]censusSizeTarget, 0, len(targets))
	for _, t := range targets {
		if len(t.question.UpstreamID) == 0 || t.size == 0 {
			continue // a draft has no election yet, and there is nothing to size against
		}
		current, status, err := a.electionSizeAndStatus(&t.question)
		switch {
		case err != nil:
			log.Warnw("could not read election size, enqueueing the resize anyway",
				"question", t.question.ID.Hex(),
				"upstreamId", t.question.UpstreamID.String(), "error", err)
		case t.size <= current:
			continue // the election already has the room
		case !isOpenStatus(status):
			continue // an ended election cannot take a resize, so there is no job to make
		default:
			// a genuine increase
		}
		growing = append(growing, t)
	}
	if len(growing) == 0 {
		return "", nil
	}

	org, err := a.db.Organization(orgAddress)
	if err != nil {
		return "", fmt.Errorf("could not load organization: %w", err)
	}
	orgSigner, err := account.OrganizationSigner(a.secret, org.SignerSeedValue(), org.Nonce)
	if err != nil {
		return "", fmt.Errorf("could not restore organization signer: %w", err)
	}
	jobID, err := apicommon.NewJobID()
	if err != nil {
		return "", fmt.Errorf("could not create job id: %w", err)
	}
	if err := a.db.CreateTxJob(jobID, db.JobTypeSetProcessCensus, org.Address); err != nil {
		return "", fmt.Errorf("could not create tx job: %w", err)
	}
	// record keeps the per-question outcomes on the job when it fails as a whole; the default
	// recorder would drop them.
	record := func(result *db.JobResult, runErr error) {
		jobStatus, errMsg := db.JobStatusCompleted, ""
		if runErr != nil {
			jobStatus, errMsg = db.JobStatusFailed, runErr.Error()
		}
		if e := a.db.SetJobStatus(jobID, jobStatus, result, errMsg); e != nil {
			log.Warnw("could not record census resize job", "jobId", jobID, "error", e)
		}
	}
	submit := func(orgLock *orgTxLock) error {
		if !a.enqueueTx(txTask{jobID: jobID, record: record, run: func() (*db.JobResult, error) {
			defer orgLock.Unlock()
			return a.setProcessCensusSizes(orgSigner, growing)
		}}) {
			orgLock.Unlock()
			if e := a.db.SetJobStatus(jobID, db.JobStatusFailed, nil, "tx queue full"); e != nil {
				log.Warnw("could not mark job failed after full queue", "error", e)
			}
			return errors.ErrTxQueueFull
		}
		return nil
	}
	if orgLock, ok := a.orgTxLocks.tryLock(org.Address); ok {
		if err := submit(orgLock); err != nil {
			return "", err
		}
		return jobID, nil
	}
	// The organization has a tx in flight, and a publish holds the lock through every mining
	// round. The participant or eligibility write this follows has already committed and no
	// caller retries the resize, so it waits for the lock in the background instead of being
	// dropped; the job stays pending meanwhile and is what the caller polls. The lock is always
	// released once its holder's tx is done, so the wait ends.
	a.heldTxJobs.hold(jobID)
	go func() {
		defer a.heldTxJobs.release(jobID)
		if err := submit(a.orgTxLocks.lock(org.Address)); err != nil {
			log.Warnw("could not enqueue a deferred census resize", "org", org.Address, "jobId", jobID, "error", err)
		}
	}()
	return jobID, nil
}

// setProcessCensusSizes submits one SET_PROCESS_CENSUS tx per target, recording a no-op instead for
// the elections that already have the room (a resize that waited for the organization lock may have
// been overtaken by a larger one, and the chain refuses a shrink) and for those the chain no longer
// has open (a stored status can lag the chain, and an ended election refuses a resize).
//
// Every target is attempted even after one fails, so one election cannot hold back the others. The
// job fails when any attempted target failed, and its error names each failed question; the
// per-question outcomes are kept on the job either way, as the status-change job does.
func (a *API) setProcessCensusSizes(orgSigner *ethereum.SignKeys, targets []censusSizeTarget) (*db.JobResult, error) {
	entries := make([]db.QuestionJobResult, 0, len(targets))
	attempted := 0
	var failures []string
	for i := range targets {
		t := &targets[i]
		questionID := t.question.ID.Hex()
		current, status, err := a.electionSizeAndStatus(&t.question)
		if err == nil && t.size <= current {
			entries = append(entries, db.QuestionJobResult{QuestionID: questionID, Status: db.JobStatusCompleted, NoOp: true})
			continue
		}
		if err == nil && !isOpenStatus(status) {
			log.Infow("skipping census resize of an election that is no longer open",
				"question", questionID, "upstreamId", t.question.UpstreamID.String(), "status", status)
			entries = append(entries, db.QuestionJobResult{QuestionID: questionID, Status: db.JobStatusCompleted, NoOp: true})
			continue
		}
		attempted++
		entry := db.QuestionJobResult{QuestionID: questionID, Status: db.JobStatusCompleted}
		if err := a.submitSetProcessCensus(orgSigner, t); err != nil {
			log.Warnw("could not resize census of an election", "question", questionID, "error", err)
			entry.Status = db.JobStatusFailed
			entry.Error = err.Error()
			failures = append(failures, fmt.Sprintf("question %s: %v", questionID, err))
		}
		entries = append(entries, entry)
	}
	result := &db.JobResult{Questions: entries}
	if len(failures) > 0 {
		return result, fmt.Errorf("%d of %d census resizes failed: %s",
			len(failures), attempted, strings.Join(failures, "; "))
	}
	result.Status = string(db.JobStatusCompleted)
	return result, nil
}

// submitSetProcessCensus builds, funds, signs and submits one SET_PROCESS_CENSUS tx for a target.
func (a *API) submitSetProcessCensus(orgSigner *ethereum.SignKeys, t *censusSizeTarget) error {
	tx, err := a.account.BuildSetProcessCensusTx(
		orgSigner.Address(), t.question.UpstreamID,
		t.census.Published.Root, t.census.Published.URI, t.size,
	)
	if err != nil {
		return err
	}
	fundedTx, _, err := a.account.FundTransaction(tx, orgSigner.Address())
	if err != nil {
		return err
	}
	stx, err := a.account.SignTransaction(fundedTx, orgSigner)
	if err != nil {
		return err
	}
	_, err = a.account.SubmitSignedTx(stx)
	return err
}
