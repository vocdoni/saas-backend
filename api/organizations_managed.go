package api

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/account"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"github.com/vocdoni/saas-backend/pricing"
	"go.vocdoni.io/dvote/log"
)

// integratorAddress resolves the integrator organization for an integrator-scoped request. The
// integrator endpoints are path-less: the organization is taken from the authenticated principal,
// never from the URL. With an API key it is the key's own org. With a user session (JWT) it is the
// integrator organization the user administers — an integrator manages a single organization (the UI
// does not let an integrator create more), and the user may also administer the managed orgs it
// created, so we select the one that is itself an integrator rather than the first org in the list.
// Writes ErrNotAnIntegrator and returns ok=false when the user has no integrator organization. The
// handlers still enforce the concrete role and quota eligibility on the resolved org.
func (a *API) integratorAddress(w http.ResponseWriter, r *http.Request) (common.Address, bool) {
	if key, ok := apicommon.APIKeyFromContext(r.Context()); ok {
		return key.OrgAddress, true
	}
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return common.Address{}, false
	}
	for _, orgUser := range user.Organizations {
		if orgUser.Role != db.AdminRole && orgUser.Role != db.ManagerRole {
			continue
		}
		org, err := a.db.Organization(orgUser.Address)
		if err != nil {
			continue
		}
		if a.subscriptions.IsIntegrator(org) {
			return org.Address, true
		}
	}
	errors.ErrNotAnIntegrator.Write(w)
	return common.Address{}, false
}

// createManagedOrganizationHandler godoc
//
//	@Summary		Create a managed organization under an integrator
//	@Description	Create a new organization managed by the caller's integrator organization. The
//	@Description	integrator is resolved from the authenticated principal (the API key's org, or the
//	@Description	user's organization for a session) — no address is passed in the URL. The caller must
//	@Description	be an admin of that integrator organization, which must be enabled as an integrator.
//	@Description	Until the integrator has topped up its wallet (top-ups less Stripe refunds and disputes
//	@Description	above zero) it may manage one organization: the next is refused with 402 (40184, data:
//	@Description	managedOrgs, managedOrgsCreated, netTopUpCents). Once it has, there is no limit on how many it manages,
//	@Description	whatever its current balance. The managed org's on-chain account is always
//	@Description	provisioned eagerly. The optional `ownerEmail` designates the managed org's owner/admin
//	@Description	(defaults to the calling user); the per-user MaxOrgsPerUser cap is bypassed for managed orgs.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `managed:write`).
//	@Tags			integrator
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			request	body		apicommon.CreateManagedOrganizationRequest	true	"Managed organization information"
//	@Success		200		{object}	apicommon.OrganizationInfo
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		402		{object}	errors.Error	"Top up the integrator wallet to create another managed organization (40184)"
//	@Failure		403		{object}	errors.Error	"Not an integrator"
//	@Failure		404		{object}	errors.Error	"Integrator organization not found"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Failure		503		{object}	errors.Error	"Busy (50302), retry after Retry-After"
//	@Router			/integrator/organizations [post]
func (a *API) createManagedOrganizationHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	integratorAddr, ok := a.integratorAddress(w, r)
	if !ok {
		return
	}
	if !user.HasRoleFor(integratorAddr, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of the integrator organization").Write(w)
		return
	}
	integrator, err := a.db.Organization(integratorAddr)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrOrganizationNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// eligibility: being an integrator (the cap of an unfunded one is checked under its lock below)
	if err := a.subscriptions.CanCreateManagedOrg(integrator); err != nil {
		var apiErr errors.Error
		if errors.As(err, &apiErr) {
			apiErr.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	req := &apicommon.CreateManagedOrganizationRequest{}
	if err := json.NewDecoder(r.Body).Decode(req); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	if !db.IsOrganizationTypeValid(req.Type) {
		errors.ErrMalformedBody.Withf("invalid organization type").Write(w)
		return
	}
	if !validateDefaultLang(w, req.DefaultLang) {
		return
	}
	if err := apicommon.ValidateMetaKeys(req.Meta); err != nil {
		errors.ErrInvalidUserData.WithErr(err).Write(w)
		return
	}
	if err := apicommon.ValidateMetaShorthands(req.Name, req.Logo, req.Description); err != nil {
		errors.ErrInvalidUserData.WithErr(err).Write(w)
		return
	}
	creatorEmail := user.Email
	if req.OwnerEmail != "" {
		// validate the owner exists up front, before provisioning an on-chain account, so a
		// bad ownerEmail fails fast with a 400 instead of orphaning a funded on-chain account.
		if _, err := a.db.UserByEmail(req.OwnerEmail); err != nil {
			if err == db.ErrNotFound {
				errors.ErrMalformedBody.Withf("owner email does not correspond to an existing user").Write(w)
				return
			}
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		creatorEmail = req.OwnerEmail
	}
	signer, nonce, err := account.NewSigner(a.secret, creatorEmail)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not create organization signer: %v", err).Write(w)
		return
	}
	defaultPlan, err := a.db.DefaultPlan()
	if err != nil || defaultPlan == nil {
		errors.ErrNoDefaultPlan.WithErr(err).Write(w)
		return
	}
	dbOrg := &db.Organization{
		Address:        signer.Address(),
		Website:        req.Website,
		Creator:        creatorEmail,
		SignerSeed:     creatorEmail,
		CreatedAt:      time.Now(),
		Nonce:          nonce,
		Type:           db.OrganizationType(req.Type),
		Size:           req.Size,
		Color:          req.Color,
		Country:        req.Country,
		Subdomain:      req.Subdomain,
		Timezone:       req.Timezone,
		DefaultLang:    cmp.Or(req.DefaultLang, apicommon.DefaultLang),
		Communications: req.Communications,
		Meta:           apicommon.BuildOrgMeta(nil, req.Name, req.Logo, req.Description, req.Meta),
		ManagedBy:      integratorAddr,
		Subscription: db.OrganizationSubscription{
			PlanID:    defaultPlan.ID,
			StartDate: time.Now(),
			Active:    true,
		},
	}
	// an integrator whose wallet kept a top-up has no cap; one that has not may create
	// maxUnfundedManagedOrgs, and the count and the creation it allows are one step per integrator,
	// so only that path is serialized (with a bounded wait: it holds through the faucet funding)
	netTopUp, err := a.db.WalletNetTopUpCents(integratorAddr)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	if netTopUp <= 0 {
		// ponytail: in-memory lock, like orgTxLocks — one API instance; a DB CAS if that changes
		managedOrgLock, err := a.managedOrgLocks.lockOrBusy(r.Context(), integratorAddr)
		if err != nil {
			writeSubscriptionError(w, err)
			return
		}
		defer managedOrgLock.Unlock()
		if err := a.unfundedManagedOrgError(integratorAddr); err != nil {
			writeSubscriptionError(w, err)
			return
		}
	}
	// forge the managed org's on-chain account (always eager) BEFORE persisting the DB
	// row. CreateOrgAccount is idempotent and the address derives from the signer, so a
	// failure here leaves nothing to clean up and the request can be retried safely.
	infoURI := fmt.Sprintf("%s/organizations/%s", a.serverURL, dbOrg.Address.String())
	if err := a.account.CreateOrgAccount(signer, apicommon.OrgDisplayName(dbOrg.Meta, dbOrg.Address.String()), infoURI); err != nil {
		errors.ErrGenericInternalServerError.Withf("could not provision managed organization account: %v", err).Write(w)
		return
	}
	if err := a.db.SetOrganization(dbOrg); err != nil {
		if err == db.ErrAlreadyExists {
			errors.ErrInvalidOrganizationData.WithErr(err).Write(w)
			return
		}
		if err == db.ErrNotFound {
			errors.ErrMalformedBody.Withf("owner email does not correspond to an existing user").Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// counted after the fact: while the organization exists the cap also counts it live, so a lost
	// increment only shows once it is deleted
	if err := a.db.IncrementManagedOrgsCreated(integratorAddr); err != nil {
		log.Errorw(err, fmt.Sprintf("could not count managed organization %s created by %s", dbOrg.Address, integratorAddr))
	}
	info := apicommon.OrganizationFromDB(dbOrg, nil)
	redactBillingEmails(info, user)
	apicommon.HTTPWriteJSON(w, info)
}

// maxUnfundedManagedOrgs is how many organizations an integrator may create before it has topped
// up its wallet: each one is an on-chain account the service funds, and a deleted one stays funded.
const maxUnfundedManagedOrgs = 1

// managedOrgCap is where an integrator stands against maxUnfundedManagedOrgs.
type managedOrgCap struct {
	live    int64 // managed organizations it has now
	created int64 // managed organizations it ever created, deleted ones included
	// netTopUpCents is what its wallet kept from Stripe top-ups (WalletNetTopUpCents)
	netTopUpCents pricing.Cents
}

// allowed reports whether the integrator may create one more managed organization: always once its
// wallet kept a top-up, whatever its balance; until then while it created fewer than the cap.
func (c managedOrgCap) allowed() bool {
	return c.netTopUpCents > 0 || c.created < maxUnfundedManagedOrgs
}

// managedOrgCap reads where integratorAddr stands against the unfunded cap. Creations are counted by
// Organization.ManagedOrgsCreated, never decremented, so deleting an organization frees nothing; an
// integrator migrated with organizations it created before the counter existed (it reads 0) counts
// at least the ones it manages now.
func (a *API) managedOrgCap(integratorAddr common.Address) (managedOrgCap, error) {
	integrator, err := a.db.Organization(integratorAddr)
	if err != nil {
		return managedOrgCap{}, fmt.Errorf("failed to get integrator: %w", err)
	}
	netTopUp, err := a.db.WalletNetTopUpCents(integratorAddr)
	if err != nil {
		return managedOrgCap{}, err
	}
	live, _, err := a.db.ListManagedOrganizations(integratorAddr, 1, 1)
	if err != nil {
		return managedOrgCap{}, fmt.Errorf("failed to count managed organizations: %w", err)
	}
	return managedOrgCap{
		live: live, created: max(int64(integrator.ManagedOrgsCreated), live), netTopUpCents: netTopUp,
	}, nil
}

// unfundedManagedOrgError refuses one more managed organization to an integrator whose wallet
// never kept a top-up (WalletNetTopUpCents ≤ 0) once it created maxUnfundedManagedOrgs. Once it
// has, there is no cap, whatever its balance. The caller holds the integrator's managedOrgLocks.
func (a *API) unfundedManagedOrgError(integratorAddr common.Address) error {
	standing, err := a.managedOrgCap(integratorAddr)
	if err != nil || standing.allowed() {
		return err
	}
	return errors.ErrManagedOrgTopUpRequired.Withf("an integrator creates %d organization until it tops up its "+
		"wallet, deleted ones included: top it up with POST /organizations/{orgAddress}/wallet/topup",
		maxUnfundedManagedOrgs).
		WithData(map[string]int64{
			"managedOrgs": standing.live, "managedOrgsCreated": standing.created,
			"netTopUpCents": int64(standing.netTopUpCents),
		})
}

// managedOrganizationsHandler godoc
//
//	@Summary		List organizations managed by an integrator
//	@Description	Returns a paginated list of organizations managed by the caller's integrator
//	@Description	organization (resolved from the API key's org or the user session — no address in the
//	@Description	URL). The caller must be an admin or manager of the integrator organization. Each item
//	@Description	carries its spend cap (`spendCapCents`, null when unlimited) and what its processes hold
//	@Description	debited from the integrator wallet (`spentCents`).
//	@Description
//	@Description	Also callable with a scoped API key (scope: `managed:read`).
//	@Tags			integrator
//	@Produce		json
//	@Security		BearerAuth
//	@Param			page	query		integer	false	"Page number (default: 1)"
//	@Param			limit	query		integer	false	"Number of items per page (default: 10)"
//	@Success		200		{object}	apicommon.ListManagedOrganizations
//	@Failure		400		{object}	errors.Error	"Invalid input data"
//	@Failure		401		{object}	errors.Error	"Unauthorized"
//	@Failure		500		{object}	errors.Error	"Internal server error"
//	@Router			/integrator/organizations [get]
func (a *API) managedOrganizationsHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	integratorAddr, ok := a.integratorAddress(w, r)
	if !ok {
		return
	}
	if !user.HasRoleFor(integratorAddr, db.AdminRole) && !user.HasRoleFor(integratorAddr, db.ManagerRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the integrator organization").Write(w)
		return
	}
	params, err := parsePaginationParams(r.URL.Query().Get(ParamPage), r.URL.Query().Get(ParamLimit))
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	totalItems, orgs, err := a.db.ListManagedOrganizations(integratorAddr, params.Page, params.Limit)
	if err != nil {
		errors.ErrGenericInternalServerError.Withf("could not list managed organizations: %v", err).Write(w)
		return
	}
	pagination, err := calculatePagination(params.Page, params.Limit, totalItems)
	if err != nil {
		errors.ErrMalformedURLParam.WithErr(err).Write(w)
		return
	}
	wallet, err := a.db.Wallet(integratorAddr)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	list := make([]*apicommon.ManagedOrganizationInfo, 0, len(orgs))
	for i := range orgs {
		info := apicommon.ManagedOrganizationFromDB(&orgs[i], wallet.ManagedOrgSpend(orgs[i].Address))
		redactBillingEmails(&info.OrganizationInfo, user)
		list = append(list, info)
	}
	apicommon.HTTPWriteJSON(w, &apicommon.ListManagedOrganizations{
		Pagination:    pagination,
		Organizations: list,
	})
}

// integratorInfoHandler godoc
//
//	@Summary		Get integrator status and usage
//	@Description	Returns whether the caller's organization (resolved from the API key's org or the user
//	@Description	session — no address in the URL) is enabled as an integrator, along with the usage summed
//	@Description	across its managed organizations. The caller must be an admin or manager of the
//	@Description	organization. Its spending is bounded by its wallet balance
//	@Description	(GET /organizations/{orgAddress}/wallet). Until its wallet keeps a top-up
//	@Description	(`netTopUpCents` > 0) it creates one managed organization, deleted ones included, and the
//	@Description	next creation answers 402 (40184); then there is no limit. `managedOrgsCreated` counts the
//	@Description	creations and `canCreateManagedOrg` says whether one more is allowed now. An integrator
//	@Description	migrated with managed organizations counts at least those, so it tops up before adding
//	@Description	another. When the organization is not an integrator, `enabled` is false.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `quota:read`).
//	@Tags			integrator
//	@Produce		json
//	@Security		BearerAuth
//	@Success		200	{object}	apicommon.IntegratorInfoResponse
//	@Failure		400	{object}	errors.Error	"Invalid input data"
//	@Failure		401	{object}	errors.Error	"Unauthorized"
//	@Failure		404	{object}	errors.Error	"Organization not found"
//	@Failure		500	{object}	errors.Error	"Internal server error"
//	@Router			/integrator [get]
func (a *API) integratorInfoHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	integratorAddr, ok := a.integratorAddress(w, r)
	if !ok {
		return
	}
	if !user.HasRoleFor(integratorAddr, db.AdminRole) && !user.HasRoleFor(integratorAddr, db.ManagerRole) {
		errors.ErrUnauthorized.Withf("user is not admin or manager of the integrator organization").Write(w)
		return
	}
	org, err := a.db.Organization(integratorAddr)
	if err != nil {
		if err == db.ErrNotFound {
			errors.ErrOrganizationNotFound.Write(w)
			return
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	resp := &apicommon.IntegratorInfoResponse{Enabled: a.subscriptions.IsIntegrator(org)}
	if resp.Enabled {
		// usage summed across the integrator's managed orgs, in one aggregation
		counters, err := a.db.SumManagedCounters(integratorAddr)
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		resp.Usage = apicommon.IntegratorUsage{
			ManagedOrgs: counters.ManagedOrgs,
			SentVotes:   counters.SentVotes,
			SentSMS:     counters.SentSMS,
			SentEmails:  counters.SentEmails,
		}
		standing, err := a.managedOrgCap(integratorAddr)
		if err != nil {
			errors.ErrGenericInternalServerError.WithErr(err).Write(w)
			return
		}
		resp.ManagedOrgsCreated = standing.created
		resp.NetTopUpCents = standing.netTopUpCents
		resp.CanCreateManagedOrg = standing.allowed()
	}
	apicommon.HTTPWriteJSON(w, resp)
}

// deleteManagedOrganizationHandler godoc
//
//	@Summary		Delete a managed organization and all its data
//	@Description	Delete the managed organization at {orgAddress} together with every piece of data
//	@Description	tied to it (memberbase, censuses + participants, processes and their questions,
//	@Description	bundles, CSP tokens, jobs, pending invites), and unlink it from its members. Drafts
//	@Description	are deleted first, exactly as DELETE /processes/{processId} does: a paid draft is
//	@Description	credited back to the payer's wallet before it goes, and its payment record goes with
//	@Description	it (the credit-back row in the payer's wallet history is the audit trail). Every
//	@Description	once-per-organization add-on price (branding, custom URL) an earlier credit-back
//	@Description	withheld that no published or mined process of the organization received is
//	@Description	credited back too, once. If any draft cannot be deleted (a publish holds it, or its
//	@Description	credit-back failed), the deletion stops with that draft's error (its message names the draft) and nothing else is
//	@Description	removed; retry once it settles. The caller must be an admin
//	@Description	of their integrator organization (resolved from the API key's org or the user session
//	@Description	— no address in the URL), and the target must be managed by it.
//	@Description
//	@Description	Deletion is blocked with 409 when any of the managed org's published elections is
//	@Description	still active on-chain (READY/PAUSED); end them (or wait for them to end) first.
//	@Description	The on-chain organization account and any published elections/censuses are immutable
//	@Description	on the Vochain and are not affected — only DB-side data is removed.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `managed:write`).
//	@Tags			integrator
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string	true	"Managed organization address to delete"
//	@Success		200			{object}	apicommon.DeleteManagedOrganizationResponse
//	@Failure		400			{object}	errors.Error	"Invalid address"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		403			{object}	errors.Error	"Forbidden"
//	@Failure		404			{object}	errors.Error	"Managed organization not found"
//	@Failure		409			{object}	errors.Error	"Managed organization has active elections, or a draft cannot be deleted yet"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Failure		503			{object}	errors.Error	"Busy (50302), retry after Retry-After"
//	@Router			/integrator/organizations/{orgAddress} [delete]
func (a *API) deleteManagedOrganizationHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	integratorAddr, ok := a.integratorAddress(w, r)
	if !ok {
		return
	}
	if !user.HasRoleFor(integratorAddr, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of the integrator organization").Write(w)
		return
	}
	managed, ok := a.managedOrgFromPath(w, r, integratorAddr)
	if !ok {
		return
	}
	managedAddr := managed.Address

	// Active-election guard: fetch the managed org's published processes once. The guard blocks
	// deletion while any published election is READY or PAUSED on-chain (a lookup error fails
	// closed so we never orphan a live election). The same list feeds the CSP cleanup below.
	// AllProcessesByOrg is unbounded so the guard inspects every published election, not just
	// the first page.
	published, err := a.db.AllProcessesByOrg(managedAddr, db.PublishedOnly)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	for _, p := range published {
		if p.Address.Equals(nil) {
			continue
		}
		election, err := a.account.Election(p.Address.Bytes())
		if err != nil {
			errors.ErrVochainRequestFailed.WithErr(err).Write(w)
			return
		}
		if election.Status == "READY" || election.Status == "PAUSED" {
			errors.ErrManagedOrgHasActiveElections.Write(w)
			return
		}
	}

	// new /processes questions: block deletion while any published question is active (READY|PAUSED).
	// Read the live chain status of each non-terminal question (bounded by the org's active question
	// count) rather than the stored value, so deletion — the one safety-critical moment — never runs
	// on a status the on-demand syncer hasn't refreshed yet. A lookup error fails closed.
	questionRefs, err := a.db.SyncableQuestionsByOrg(managedAddr)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	for _, ref := range questionRefs {
		election, err := a.account.Election(ref.UpstreamID)
		if err != nil {
			errors.ErrVochainRequestFailed.WithErr(err).Write(w)
			return
		}
		if election.Status == "READY" || election.Status == "PAUSED" {
			errors.ErrManagedOrgHasActiveElections.Write(w)
			return
		}
	}

	// from here on no draft may be created or published in the organization: one created and paid
	// after the draft sweep below would go in the bulk delete with no refund. A publish that claimed
	// a draft earlier holds it, and the sweep stops on it.
	if _, busy := a.teardowns.LoadOrStore(managedAddr, struct{}{}); busy {
		errors.ErrDuplicateConflict.Withf("the managed organization is already being deleted").Write(w)
		return
	}
	defer a.teardowns.Delete(managedAddr)
	// a creation that checked the flag before it was set holds the organization's draft lock
	// until its draft is stored: taking that lock once lets the sweep below find that draft
	draftLock, err := a.draftLocks.lockOrBusy(r.Context(), managedAddr)
	if err != nil {
		writeSubscriptionError(w, err)
		return
	}
	draftLock.Unlock()

	// drafts first, each the way DELETE /processes/{processId} deletes it, so a paid one is
	// credited back before it goes. Unlike the cascade below this aborts on the first failure, with
	// nothing else deleted: money must never be left behind in a deleted organization, and a
	// retry resumes where it stopped.
	if err := a.deleteOrgDrafts(managedAddr); err != nil {
		writeSubscriptionError(w, err)
		return
	}

	// cascade: each step is best-effort. Failures are logged but do not abort the teardown, so a
	// single stuck collection can't leave the org half-deleted. The org doc itself is deleted last.
	bundles, err := a.db.ProcessBundlesByOrg(managedAddr)
	if err != nil {
		log.Warnw("could not list bundles for managed org teardown",
			"org", managedAddr.Hex(), "error", err)
	}
	for _, b := range bundles {
		// match the encoding used by csp/handlers parseBundleID (hex-decoded ObjectID bytes).
		bundleID := new(internal.HexBytes)
		if err := bundleID.ParseString(b.ID.Hex()); err != nil {
			log.Warnw("could not encode bundle id for CSP cleanup",
				"org", managedAddr.Hex(), "bundle", b.ID.Hex(), "error", err)
			continue
		}
		if _, err := a.db.DeleteCSPAuthByAnchor(*bundleID); err != nil {
			log.Warnw("could not delete CSP auth tokens for bundle",
				"org", managedAddr.Hex(), "bundle", b.ID.Hex(), "error", err)
		}
	}
	for _, p := range published {
		if p.Address.Equals(nil) {
			continue
		}
		if _, err := a.db.DeleteCSPProcessByProcess(p.Address); err != nil {
			log.Warnw("could not delete CSP process status",
				"org", managedAddr.Hex(), "process", p.Address.String(), "error", err)
		}
	}
	if _, err := a.db.DeleteProcessBundlesByOrg(managedAddr); err != nil {
		log.Warnw("could not delete process bundles", "org", managedAddr.Hex(), "error", err)
	}
	censuses, err := a.db.CensusesByOrg(managedAddr)
	if err != nil {
		log.Warnw("could not list censuses for managed org teardown",
			"org", managedAddr.Hex(), "error", err)
	}
	for _, c := range censuses {
		if _, err := a.db.DeleteCensusParticipantsByCensus(c.ID.Hex()); err != nil {
			log.Warnw("could not delete census participants",
				"org", managedAddr.Hex(), "census", c.ID.Hex(), "error", err)
		}
		if err := a.db.DelCensus(c.ID.Hex()); err != nil {
			log.Warnw("could not delete census", "org", managedAddr.Hex(), "census", c.ID.Hex(), "error", err)
		}
	}
	if _, err := a.db.DeleteProcessesByOrg(managedAddr); err != nil {
		log.Warnw("could not delete processes", "org", managedAddr.Hex(), "error", err)
	}
	// the CSP rows of the voting processes go first: they are found through the processes' ids
	if err := a.db.DeleteVotingProcessCSPByOrg(managedAddr); err != nil {
		log.Warnw("could not delete voting process CSP data", "org", managedAddr.Hex(), "error", err)
	}
	// what is left are published voting processes; their payment rows stay as the audit trail
	if _, err := a.db.DeleteVotingProcessesByOrg(managedAddr); err != nil {
		log.Warnw("could not delete voting processes", "org", managedAddr.Hex(), "error", err)
	}
	if _, err := a.db.DeleteAllOrgMemberGroups(managedAddr); err != nil {
		log.Warnw("could not delete org member groups", "org", managedAddr.Hex(), "error", err)
	}
	// the emptied questions are ignored on purpose: this is an org teardown, so there is no
	// election left to resize.
	if _, _, err := a.db.DeleteAllOrgMembers(managedAddr); err != nil {
		log.Warnw("could not delete org members", "org", managedAddr.Hex(), "error", err)
	}
	if _, err := a.db.DeleteJobsByOrg(managedAddr); err != nil {
		log.Warnw("could not delete jobs", "org", managedAddr.Hex(), "error", err)
	}
	if _, err := a.db.DeleteMeteredVotesByOrg(managedAddr); err != nil {
		log.Warnw("could not delete metered votes", "org", managedAddr.Hex(), "error", err)
	}
	if _, err := a.db.DeleteAPIKeysByOrg(managedAddr); err != nil {
		log.Warnw("could not delete API keys", "org", managedAddr.Hex(), "error", err)
	}
	if _, err := a.db.DeleteInvitationsByOrg(managedAddr); err != nil {
		log.Warnw("could not delete invitations", "org", managedAddr.Hex(), "error", err)
	}
	if err := a.db.RemoveOrganizationFromAllUsers(managedAddr); err != nil {
		log.Warnw("could not unlink organization from users", "org", managedAddr.Hex(), "error", err)
	}
	if err := a.db.DelOrganization(managed); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	// its drafts were credited back above; what is left of its spend entry describes an
	// organization that no longer exists
	if err := a.db.DropManagedOrgSpend(integratorAddr, managedAddr); err != nil {
		log.Warnw("could not drop the spend entry of a deleted managed org", "org", managedAddr.Hex(), "error", err)
	}

	log.Infow("deleted managed organization", "integrator", integratorAddr.Hex(), "org", managedAddr.Hex())
	apicommon.HTTPWriteJSON(w, &apicommon.DeleteManagedOrganizationResponse{Address: managedAddr.Hex()})
}

// managedOrgFromPath resolves the {orgAddress} path parameter to an organization managed by
// integratorAddr, writing the error and reporting false otherwise. One managed by another
// integrator is a 404 (not 403), so a caller cannot probe which addresses other integrators manage.
func (a *API) managedOrgFromPath(
	w http.ResponseWriter, r *http.Request, integratorAddr common.Address,
) (*db.Organization, bool) {
	raw := chi.URLParam(r, "orgAddress")
	if !common.IsHexAddress(raw) {
		errors.ErrMalformedURLParam.With("invalid managed organization address").Write(w)
		return nil, false
	}
	managed, err := a.db.Organization(common.HexToAddress(raw))
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			errors.ErrOrganizationNotFound.Write(w)
			return nil, false
		}
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return nil, false
	}
	if managed.ManagedBy.Cmp(integratorAddr) != 0 {
		errors.ErrOrganizationNotFound.Write(w)
		return nil, false
	}
	return managed, true
}

// updateManagedOrganizationHandler godoc
//
//	@Summary		Set a managed organization's spend cap
//	@Description	Caps what the managed organization at {orgAddress} may spend from the caller's integrator
//	@Description	wallet: the most its processes may hold debited at once, in EUR cents (VAT excluded).
//	@Description	The body must hold exactly `spendCapCents`: `null` lifts the cap, and a missing key or
//	@Description	any other key is a 400, so a typo never lifts it. Organizations start unlimited.
//	@Description	The cap is checked in the same write as the wallet balance, so a publish or census
//	@Description	top-up that would pass it is refused with 402
//	@Description	(40183, data: capCents, spentCents, requiredCents) without charging anything. Refunds of
//	@Description	deleted drafts and credit-backs of failed publishes free their amount again. A cap below
//	@Description	what is already spent only refuses further debits. The caller must be an admin of their
//	@Description	integrator organization, and the target must be managed by it.
//	@Description
//	@Description	Also callable with a scoped API key (scope: `managed:write`).
//	@Tags			integrator
//	@Accept			json
//	@Produce		json
//	@Security		BearerAuth
//	@Param			orgAddress	path		string										true	"Managed organization address"
//	@Param			request		body		apicommon.UpdateManagedOrganizationRequest	true	"Spend cap"
//	@Success		200			{object}	apicommon.ManagedOrganizationInfo
//	@Failure		400			{object}	errors.Error	"Invalid address, negative cap, or a body without exactly spendCapCents"
//	@Failure		401			{object}	errors.Error	"Unauthorized"
//	@Failure		403			{object}	errors.Error	"Not an integrator"
//	@Failure		404			{object}	errors.Error	"Managed organization not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/integrator/organizations/{orgAddress} [put]
func (a *API) updateManagedOrganizationHandler(w http.ResponseWriter, r *http.Request) {
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}
	integratorAddr, ok := a.integratorAddress(w, r)
	if !ok {
		return
	}
	if !user.HasRoleFor(integratorAddr, db.AdminRole) {
		errors.ErrUnauthorized.Withf("user is not admin of the integrator organization").Write(w)
		return
	}
	managed, ok := a.managedOrgFromPath(w, r, integratorAddr)
	if !ok {
		return
	}
	// a money control: the key must be there (null is the only way to lift the cap), so neither
	// {} nor a misspelt key silently lifts it
	var fields map[string]json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&fields); err != nil {
		errors.ErrMalformedBody.Write(w)
		return
	}
	raw, ok := fields["spendCapCents"]
	if !ok || len(fields) != 1 {
		errors.ErrMalformedBody.Withf("the body must hold exactly spendCapCents (null lifts the cap)").Write(w)
		return
	}
	req := &apicommon.UpdateManagedOrganizationRequest{}
	if err := json.Unmarshal(raw, &req.SpendCapCents); err != nil {
		errors.ErrMalformedBody.Withf("spendCapCents must be a number of cents or null").Write(w)
		return
	}
	if req.SpendCapCents != nil && *req.SpendCapCents < 0 {
		errors.ErrMalformedBody.Withf("spendCapCents cannot be negative").Write(w)
		return
	}
	if err := a.db.SetManagedOrgSpendCap(integratorAddr, managed.Address, req.SpendCapCents); err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	wallet, err := a.db.Wallet(integratorAddr)
	if err != nil {
		errors.ErrGenericInternalServerError.WithErr(err).Write(w)
		return
	}
	info := apicommon.ManagedOrganizationFromDB(managed, wallet.ManagedOrgSpend(managed.Address))
	redactBillingEmails(&info.OrganizationInfo, user)
	apicommon.HTTPWriteJSON(w, info)
}

// refuseTearingDown refuses with 409 work in an organization whose teardown is running.
func (a *API) refuseTearingDown(org common.Address) error {
	if _, ok := a.teardowns.Load(org); ok {
		return errors.ErrDuplicateConflict.Withf("the organization is being deleted")
	}
	return nil
}
