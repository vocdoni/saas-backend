package handlers

import (
	"net/http"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/csp/attestation"
	"github.com/vocdoni/saas-backend/csp/notifications"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// attestedFirstStep is auth/0 for an attested census: the voter presents the attestation the
// integrator signed for them instead of member login data. It spends no failed-attempt budget: a bad
// signature or a wrong contact reveals nothing about who belongs to the census. With 2FA the OTP goes
// to the attested contact; without it the voter gets a pre-verified token.
func (c *CSPHandlers) attestedFirstStep(
	r *http.Request, req *AuthRequest, anchorID internal.HexBytes, census *db.Census,
) (internal.HexBytes, error) {
	if census.Anonymous {
		return nil, errors.ErrInvalidData.With("an attested census cannot be anonymous")
	}
	var processID bson.ObjectID
	if len(anchorID) != len(processID) {
		return nil, errors.ErrProcessNotFound
	}
	copy(processID[:], anchorID)
	vp, err := c.mainDB.VotingProcess(processID)
	if err != nil {
		if err == db.ErrNotFound {
			return nil, errors.ErrProcessNotFound
		}
		return nil, errors.ErrGenericInternalServerError.WithErr(err)
	}
	if processEnded(vp) {
		return nil, errors.ErrUnauthorized.Withf("process has ended")
	}
	verified, uid, err := c.verifyAttestation(req, processID, census, time.Now())
	if err != nil {
		return nil, err
	}
	org, err := c.mainDB.Organization(census.OrgAddress)
	if err != nil {
		if err == db.ErrNotFound {
			return nil, errors.ErrOrganizationNotFound
		}
		return nil, errors.ErrGenericInternalServerError.WithErr(err)
	}
	if len(census.TwoFaFields) == 0 {
		return c.csp.AttestedAuthOnlyToken(anchorID, uid, verified)
	}
	name, logo := orgNameAndLogo(org)
	return c.csp.AttestedAuthToken(anchorID, uid, verified, req.Contact,
		challengeTypeFor(attestation.ContactKind(verified.ContactKind)),
		apicommon.NotificationLang(r.Context(), org),
		notifications.OrganizationInfo{Address: org.Address, Name: name, Logo: logo})
}

// verifyAttestation checks the attestation of an auth/0 request against the census and returns it in
// the form a token stores, with the attested user id. The checks run in a fixed order: shape, signer
// and signature, process, time window, then the contact. No error names the contact or the salt.
func (c *CSPHandlers) verifyAttestation(
	req *AuthRequest, processID bson.ObjectID, census *db.Census, now time.Time,
) (*db.CSPAuthAttestation, internal.HexBytes, error) {
	p := req.Attestation
	if p == nil || len(p.ProcessID) != attestation.ProcessIDLen || len(p.VoterID) != common.HashLength ||
		(len(p.ContactHash) != 0 && len(p.ContactHash) != common.HashLength) {
		return nil, nil, errors.ErrAttestationInvalid.With("malformed attestation")
	}
	twoFa := len(census.TwoFaFields) > 0
	if twoFa != (req.Contact != "") || twoFa != (len(req.Salt) > 0) {
		return nil, nil, errors.ErrInvalidData.With("contact and salt are required exactly when the census uses 2FA")
	}
	att := attestation.Attestation{
		VoterID:     common.BytesToHash(p.VoterID),
		ContactHash: common.BytesToHash(p.ContactHash),
		ExpiresAt:   p.ExpiresAt,
	}
	copy(att.ProcessID[:], p.ProcessID)
	domain, err := c.attestationDomain()
	if err != nil {
		return nil, nil, err
	}
	signature, err := domain.Verify(&att, req.Signature, census.Attested.Signer)
	if err != nil {
		return nil, nil, errors.ErrAttestationInvalid.WithErr(err)
	}
	if att.ProcessID != processID {
		return nil, nil, errors.ErrAttestationInvalid.With("attestation is for another process")
	}
	if err := attestation.CheckExpiry(att.ExpiresAt, now); err != nil {
		return nil, nil, errors.ErrAttestationExpired
	}
	stored := &db.CSPAuthAttestation{
		VoterID: internal.HexBytes(att.VoterID.Bytes()),
		// bounded by CheckExpiry to at most an hour from now
		ExpiresAt: int64(att.ExpiresAt), //nolint:gosec
		Signature: signature,
		Signer:    census.Attested.Signer,
	}
	if twoFa {
		if att.ContactHash == (common.Hash{}) {
			return nil, nil, errors.ErrAttestationInvalid.With("attestation has no contactHash")
		}
		kind := attestation.ContactKind(census.TwoFaFields[0])
		if err := attestation.CheckCanonical(kind, req.Contact); err != nil {
			return nil, nil, errors.ErrInvalidData.With(err.Error())
		}
		if !attestation.MatchContact(att.ContactHash, req.Salt, kind, req.Contact) {
			return nil, nil, errors.ErrContactHashMismatch
		}
		stored.ContactHash = internal.HexBytes(att.ContactHash.Bytes())
		stored.ContactKind = string(kind)
	}
	if !twoFa && att.ContactHash != (common.Hash{}) {
		return nil, nil, errors.ErrAttestationInvalid.With("attestation carries a contactHash but the census has no 2FA")
	}
	return stored, attestation.UserID(att.ProcessID, att.VoterID), nil
}

// attestedResend is auth/resend for an attested census: the voter proves the contact with the salt
// of their attestation, and the challenge goes to that contact again.
func (c *CSPHandlers) attestedResend(
	w http.ResponseWriter, r *http.Request, req *AuthResendRequest, auth *db.CSPAuth, org *db.Organization,
) {
	if auth.Attestation == nil || auth.Attestation.ContactKind == "" {
		errors.ErrUnauthorized.Withf("token has no attested contact").Write(w)
		return
	}
	kind := attestation.ContactKind(auth.Attestation.ContactKind)
	if err := attestation.CheckCanonical(kind, req.Contact); err != nil {
		errors.ErrInvalidData.With(err.Error()).Write(w)
		return
	}
	if !attestation.MatchContact(common.BytesToHash(auth.Attestation.ContactHash), req.Salt, kind, req.Contact) {
		errors.ErrContactHashMismatch.Write(w)
		return
	}
	if err := c.csp.ClaimChallengeResend(req.AuthToken); err != nil {
		writeResendError(w, err)
		return
	}
	name, logo := orgNameAndLogo(org)
	if err := c.csp.ResendChallenge(req.AuthToken, req.Contact, challengeTypeFor(kind),
		apicommon.NotificationLang(r.Context(), org), name, logo, org.Address); err != nil {
		writeResendError(w, err)
		return
	}
	apicommon.HTTPWriteJSON(w, &AuthResponse{AuthToken: req.AuthToken})
}

// attestationDomain returns the EIP-712 domain of this CSP, or a 500 when its key is unavailable.
func (c *CSPHandlers) attestationDomain() (attestation.Domain, error) {
	domain, err := c.csp.AttestationDomain()
	if err != nil {
		return attestation.Domain{}, errors.ErrGenericInternalServerError.WithErr(err)
	}
	return domain, nil
}

// challengeTypeFor maps an attested contact kind to the challenge channel that reaches it.
func challengeTypeFor(kind attestation.ContactKind) notifications.ChallengeType {
	if kind == attestation.ContactPhone {
		return notifications.SMSChallenge
	}
	return notifications.EmailChallenge
}

// attestedSignContext is resolveSignContext for an attested census: there is no participant row nor
// member to re-check, so the token must instead have been issued under the active signer, which
// makes a signer rotation revoke every earlier token. Every voter weighs 1.
func attestedSignContext(
	w http.ResponseWriter, vp *db.VotingProcess, census *db.Census, auth *db.CSPAuth,
) (*signContext, bool) {
	if census.Anonymous {
		errors.ErrInvalidData.With("an attested census cannot be anonymous").Write(w)
		return nil, false
	}
	if auth.Attestation == nil {
		errors.ErrUnauthorized.Withf("token was not issued for an attested voter").Write(w)
		return nil, false
	}
	if !attestedBySigner(auth, census) {
		errors.ErrAttestationSignerRevoked.Write(w)
		return nil, false
	}
	return &signContext{
		process:     vp,
		memberID:    auth.UserID.String(),
		weight:      weightBytes(1),
		attestation: auth.Attestation,
		userID:      auth.UserID,
	}, true
}

// attestedBySigner reports whether the token was issued against an attestation of the census's
// active signer.
func attestedBySigner(auth *db.CSPAuth, census *db.Census) bool {
	return auth.Attestation != nil && census.Attested != nil &&
		auth.Attestation.Signer == census.Attested.Signer
}

// recordAttested stores, before the CSP signs question upstreamID for an attested voter, the
// attestation and the question in the voter's attestation record, so no signature exists without
// one. A record may list a question whose signature then failed; it never misses a signed one. It
// does nothing for a memberbase census.
func (c *CSPHandlers) recordAttested(sc *signContext, upstreamID internal.HexBytes) error {
	if sc.attestation == nil {
		return nil
	}
	return c.mainDB.RecordAttestedSignature(sc.process.ID, sc.userID, sc.attestation, upstreamID)
}
