package api

import (
	"net/http"

	"github.com/vocdoni/saas-backend/errors"
)

// certAuthCreateHandler godoc
//
//	@Summary		Create a certificate sign-in attempt
//	@Description	Starts a digital-certificate sign-in (FNMT natural person or idCAT) for a voter of a
//	@Description	process whose census has `authMethod: "certificate"`. Public, no Authorization header.
//	@Description	The body carries a PKCE S256 `challenge` (43 characters), `challengeMethod` "S256", the
//	@Description	`returnUrl` to come back to (an allowlisted app origin, path `/`, `/:lang`,
//	@Description	`/processes/{id}` or `/:lang/processes/{id}`, no query, fragment or userinfo) and the
//	@Description	`purpose` ("vote", or "check" for a pre-check of an UPCOMING process).
//	@Description
//	@Description	The answer's `authUrl` is on the separate auth listener, on a fresh host per attempt
//	@Description	(`https://<nonce>.<certAuthHost>/v1/authenticate?state=...`, TLS 1.2, client certificate
//	@Description	requested): the browser navigates there, presents its certificate, and is sent back with
//	@Description	`303 <returnUrl>#code=<code>&state=<state>` (or `#error=attempt_expired`), never
//	@Description	verifying anything there. `probeUrl` (`https://probe.<certAuthHost>/ok`, no client
//	@Description	certificate requested) lets the app detect networks that block the auth listener.
//	@Description	The state is single use and valid 15 minutes.
//	@Description
//	@Description	Validation failures are plain errors (400; 404 unknown process; 409 a process that is not
//	@Description	published or no longer takes sign-ins; 400 a census that is not a certificate census).
//	@Description	Over the per-IP and process limit (60 per minute by default) it answers 429, code 40950,
//	@Description	with `data.reason` "too_many_attempts" and `data.retryAfter`. Without certificate sign-in
//	@Description	configured on the server it answers 400, code 40185.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string							true	"Process ID"
//	@Param			request		body		apicommon.CertAuthCreateRequest	true	"PKCE challenge, return URL and purpose"
//	@Success		200			{object}	apicommon.CertAuthCreateResponse
//	@Failure		400			{object}	errors.Error	"Invalid request, not a certificate census, or certificate sign-in not enabled (40185)"
//	@Failure		404			{object}	errors.Error	"Process not found"
//	@Failure		409			{object}	errors.Error	"Process not published, or no longer taking sign-ins"
//	@Failure		429			{object}	errors.Error	"Too many attempts (40950, data.reason too_many_attempts)"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/processes/{processId}/auth/cert [post]
func (a *API) certAuthCreateHandler(w http.ResponseWriter, r *http.Request) {
	if a.certAuth == nil {
		errors.ErrCertificateAuthDisabled.Write(w)
		return
	}
	a.certAuth.Create(w, r)
}

// certAuthRedeemHandler godoc
//
//	@Summary		Redeem a certificate sign-in code
//	@Description	Trades the `code` the auth listener returned in the URL fragment, with the PKCE
//	@Description	`verifier`, for the voter's CSP token. Public, no Authorization header. The code is single
//	@Description	use and valid 5 minutes, and is consumed by the first redeem whatever its outcome. The
//	@Description	server verifies the presented certificate (chain, profile, revocation: up to ~40 s),
//	@Description	matches its DNI/NIE against the census `nationalId` and answers the `holder` (full name,
//	@Description	document type, last 4 characters), an opaque `voterRef`, the diagnostic `reference`, and
//	@Description	`authToken`: the same pre-verified token an auth/0 auth-only census issues, usable with
//	@Description	check, sign-batch, blind-point, blind-sign and sign-info. `authToken` is null (and
//	@Description	`weight` absent) for a "check" attempt and for a "vote" while the process does not
//	@Description	accept votes.
//	@Description
//	@Description	Every failure is code 40950 with `data.reason`: the cert-check codes verbatim
//	@Description	(no_certificate, provider_mismatch, untrusted_issuer, invalid_chain, expired,
//	@Description	not_yet_valid, unsupported_profile, unsupported_purpose, weak_algorithm, test_certificate,
//	@Description	identity_invalid, identity_unsupported_document, revoked, revocation_unknown: 422;
//	@Description	revocation_unavailable: 503; internal: 500) and dni_not_in_census (403),
//	@Description	attempt_expired (410), code_used (409), verifier_mismatch (403), too_many_attempts (429).
//	@Description	`data` also carries `retryable` (true only for revocation_unavailable and internal), and
//	@Description	when they apply `reference`, `holder`, `issuer`, `notAfter` and `retryAfter`. Failed
//	@Description	attempts are capped per hour per presented certificate and per verified document;
//	@Description	revocation outages are never charged. Without certificate sign-in configured on the
//	@Description	server it answers 400, code 40185.
//	@Tags			processes
//	@Accept			json
//	@Produce		json
//	@Param			processId	path		string							true	"Process ID"
//	@Param			request		body		apicommon.CertAuthRedeemRequest	true	"Code and PKCE verifier"
//	@Success		200			{object}	apicommon.CertAuthRedeemResponse
//	@Failure		400			{object}	errors.Error	"Certificate sign-in not enabled (40185)"
//	@Failure		403			{object}	errors.Error	"dni_not_in_census or verifier_mismatch (40950)"
//	@Failure		409			{object}	errors.Error	"code_used (40950)"
//	@Failure		410			{object}	errors.Error	"attempt_expired (40950)"
//	@Failure		422			{object}	errors.Error	"Certificate rejected, data.reason is the cert-check code (40950)"
//	@Failure		429			{object}	errors.Error	"too_many_attempts (40950)"
//	@Failure		500			{object}	errors.Error	"internal, retryable (40950)"
//	@Failure		503			{object}	errors.Error	"revocation_unavailable, retryable (40950)"
//	@Router			/processes/{processId}/auth/cert/redeem [post]
func (a *API) certAuthRedeemHandler(w http.ResponseWriter, r *http.Request) {
	if a.certAuth == nil {
		errors.ErrCertificateAuthDisabled.Write(w)
		return
	}
	a.certAuth.Redeem(w, r)
}
