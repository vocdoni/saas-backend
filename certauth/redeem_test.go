package certauth

import (
	"bytes"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/go-chi/chi/v5"
	certcheck "github.com/vocdoni/cert-check"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// redeem posts a redeem, decoding a 200 into ok and anything else into failure.
func (e *testEnv) redeem(c *qt.C, code, verifier string) (int, *apicommon.CertAuthRedeemResponse, *errorBody) {
	data, err := json.Marshal(apicommon.CertAuthRedeemRequest{Code: code, Verifier: verifier})
	c.Assert(err, qt.IsNil)
	r := chi.NewRouter()
	r.Post("/processes/{processId}/auth/cert/redeem", e.s.Redeem)
	req := httptest.NewRequest(http.MethodPost, "/processes/"+e.store.vp.ID.Hex()+"/auth/cert/redeem",
		bytes.NewReader(data))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		ok := &apicommon.CertAuthRedeemResponse{}
		c.Assert(json.Unmarshal(rec.Body.Bytes(), ok), qt.IsNil)
		return rec.Code, ok, nil
	}
	failure := &errorBody{}
	c.Assert(json.Unmarshal(rec.Body.Bytes(), failure), qt.IsNil, qt.Commentf("body: %s", rec.Body))
	return rec.Code, nil, failure
}

// signIn runs create, authenticate with cert and returns the code and verifier to redeem.
func (e *testEnv) signIn(c *qt.C, purpose string, cert *tls.Certificate) (code, verifier string) {
	created, verifier := e.create(c, purpose)
	_, frag := fragment(c, e.authenticate(c, created.AuthURL, cert))
	c.Assert(frag.Get("state"), qt.Equals, created.State)
	return frag.Get("code"), verifier
}

var (
	maria = testprovider.Holder{GivenName: "Maria", Surname: "García López", Document: "12345678Z"}
	jordi = testprovider.Holder{GivenName: "Jordi", Surname: "Puig Serra", Document: "X1234567L"}
)

// TestCertAuthRedeemFlow runs create, authenticate and redeem against the in-memory store: the
// happy path for a DNI and an NIE, single use and PKCE, the census match, the two-stage budget and
// the check-only answers. The Docker suites in ./api repeat the essentials against Mongo and the CSP.
func TestCertAuthRedeemFlow(t *testing.T) {
	c := qt.New(t)

	c.Run("vote", func(c *qt.C) {
		e := newTestEnv(c, nil)
		member := e.store.addMember("12.345.678-z", 1)
		other := e.store.addMember("x1234567l", 1)
		code, verifier := e.signIn(c, "vote", e.issue(c, maria))
		status, ok, _ := e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusOK)
		c.Assert(ok.Purpose, qt.Equals, "vote")
		c.Assert(ok.AuthToken, qt.Not(qt.IsNil))
		c.Assert(ok.Weight, qt.DeepEquals, internal.HexBytes{1})
		c.Assert(ok.Holder, qt.DeepEquals, apicommon.CertAuthHolder{
			FullName: "Maria García López", DocumentType: "DNI", DocumentEnding: "678Z",
		})
		c.Assert(ok.Reference, qt.Matches, `[0-9a-f]{32}`)
		c.Assert(ok.VoterRef, qt.Matches, `[A-Za-z0-9_-]{22}`)
		c.Assert(e.tokens.issued, qt.DeepEquals, []string{member.ID.Hex()})

		// the chain left storage with the code
		for _, tx := range e.store.txs {
			if tx.Code == code {
				c.Assert(tx.Status, qt.Equals, db.CertAuthStatusConsumed)
				c.Assert(tx.Chain, qt.HasLen, 0)
			}
		}

		// voterRef is stable for the member, and differs for another one
		code, verifier = e.signIn(c, "vote", e.issue(c, maria))
		_, again, _ := e.redeem(c, code, verifier)
		c.Assert(again.VoterRef, qt.Equals, ok.VoterRef)
		code, verifier = e.signIn(c, "vote", e.issue(c, jordi))
		_, nie, _ := e.redeem(c, code, verifier)
		c.Assert(nie.Holder, qt.DeepEquals, apicommon.CertAuthHolder{
			FullName: "Jordi Puig Serra", DocumentType: "NIE", DocumentEnding: "567L",
		})
		c.Assert(nie.VoterRef, qt.Not(qt.Equals), ok.VoterRef)
		c.Assert(e.tokens.issued[2], qt.Equals, other.ID.Hex())
	})

	c.Run("single use and pkce", func(c *qt.C) {
		e := newTestEnv(c, nil)
		e.store.addMember("12345678Z", 1)
		cert := e.issue(c, maria)
		code, verifier := e.signIn(c, "vote", cert)
		status, _, _ := e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusOK)
		status, _, failure := e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusConflict)
		c.Assert(failure.Data.Reason, qt.Equals, "code_used")

		// a wrong verifier burns the code
		code, verifier = e.signIn(c, "vote", cert)
		_, wrong := pkce()
		status, _, failure = e.redeem(c, code, wrong)
		c.Assert(status, qt.Equals, http.StatusForbidden)
		c.Assert(failure.Data.Reason, qt.Equals, "verifier_mismatch")
		status, _, failure = e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusConflict)
		c.Assert(failure.Data.Reason, qt.Equals, "code_used")

		// an expired code, an unknown one and a malformed one are attempt_expired
		code, verifier = e.signIn(c, "vote", cert)
		for _, tx := range e.store.txs {
			if tx.Code == code {
				tx.AuthenticatedAt = time.Now().Add(-6 * time.Minute)
			}
		}
		for _, candidate := range []string{code, randomToken(), "short"} {
			status, _, failure = e.redeem(c, candidate, verifier)
			c.Assert(status, qt.Equals, http.StatusGone)
			c.Assert(failure.Data.Reason, qt.Equals, "attempt_expired")
		}

		// a code is bound to its process
		code, verifier = e.signIn(c, "vote", cert)
		e.store.txs[len(e.store.txs)-1].ProcessID = bson.NewObjectID()
		status, _, failure = e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusGone)
		c.Assert(failure.Data.Reason, qt.Equals, "attempt_expired")
	})

	c.Run("census", func(c *qt.C) {
		e := newTestEnv(c, nil)
		e.store.addMember("X1234567L", 0)
		code, verifier := e.signIn(c, "vote", e.issue(c, maria))
		status, _, failure := e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusForbidden)
		c.Assert(failure.Error, qt.Equals, "certificate is not in the census")
		c.Assert(failure.Data.Reason, qt.Equals, "dni_not_in_census")
		c.Assert(failure.Data.Reference, qt.Matches, `[0-9a-f]{32}`)
		c.Assert(failure.Data.Holder.DocumentEnding, qt.Equals, "678Z")

		// a zero weight in a weighted census cannot vote either
		e.store.census.Weighted = true
		code, verifier = e.signIn(c, "vote", e.issue(c, jordi))
		status, _, failure = e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusForbidden)
		c.Assert(failure.Data.Reason, qt.Equals, "dni_not_in_census")
		c.Assert(e.tokens.count(), qt.Equals, 0)

		// presented, but too large to keep: invalid_chain, never no_certificate
		code, verifier = e.signIn(c, "vote", e.issue(c, jordi))
		for _, tx := range e.store.txs {
			if tx.Code == code {
				tx.Chain = nil
			}
		}
		status, _, failure = e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusUnprocessableEntity)
		c.Assert(failure.Data.Reason, qt.Equals, "invalid_chain")
	})

	c.Run("not a certificate census", func(c *qt.C) {
		// an attempt authenticated while the census took certificates, redeemed after it switched to
		// typed fields with an email second factor: a pre-verified token would skip that factor
		e := newTestEnv(c, nil)
		e.store.addMember("12345678Z", 1)
		code, verifier := e.signIn(c, "vote", e.issue(c, maria))
		e.store.census.AuthMethod = ""
		e.store.census.TwoFaFields = db.OrgMemberTwoFaFields{db.OrgMemberTwoFaFieldEmail}
		status, _, failure := e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusBadRequest)
		c.Assert(failure.Code, qt.Equals, 40037)
		c.Assert(failure.Data.Holder, qt.IsNil)
		c.Assert(e.tokens.count(), qt.Equals, 0)
		// the code is burnt all the same
		e.store.census.AuthMethod = db.CensusAuthMethodCertificate
		status, _, failure = e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusConflict)
		c.Assert(failure.Data.Reason, qt.Equals, "code_used")
	})

	c.Run("budget", func(c *qt.C) {
		e := newTestEnv(c, nil)
		e.store.addMember("12345678Z", 1)
		redeemWith := func(cert any) (int, *errorBody) {
			var code, verifier string
			if holder, ok := cert.(testprovider.Holder); ok {
				code, verifier = e.signIn(c, "vote", e.issue(c, holder))
			} else {
				code, verifier = e.signIn(c, "vote", cert.(*tls.Certificate))
			}
			status, _, failure := e.redeem(c, code, verifier)
			return status, failure
		}

		// ten forged certificates naming Maria's document spend their own budgets, not hers
		for range 10 {
			forged, err := testprovider.SelfSigned(maria)
			c.Assert(err, qt.IsNil)
			status, failure := redeemWith(forged)
			c.Assert(status, qt.Equals, http.StatusUnprocessableEntity)
			c.Assert(failure.Data.Reason, qt.Equals, "untrusted_issuer")
		}
		status, _ := redeemWith(maria)
		c.Assert(status, qt.Equals, http.StatusOK)

		// a verified document not in the census is capped per document, whatever the certificate
		outsider := testprovider.Holder{GivenName: "Nobody", Surname: "Else", Document: "87654321X"}
		for range 10 {
			status, failure := redeemWith(outsider)
			c.Assert(status, qt.Equals, http.StatusForbidden)
			c.Assert(failure.Data.Reason, qt.Equals, "dni_not_in_census")
		}
		status, failure := redeemWith(outsider)
		c.Assert(status, qt.Equals, http.StatusTooManyRequests)
		c.Assert(failure.Data.Reason, qt.Equals, "too_many_attempts")
		c.Assert(failure.Data.RetryAfter > 0, qt.IsTrue)
		// another document is unaffected
		status, _ = redeemWith(testprovider.Holder{GivenName: "Other", Surname: "One", Document: "11111111H"})
		c.Assert(status, qt.Equals, http.StatusForbidden)

		// one certificate failing verification is capped before Verify runs
		revoked := e.issue(c, testprovider.Holder{GivenName: "R", Surname: "R", Document: "22222222J", OU: testprovider.OURevoked})
		for range 10 {
			status, failure := redeemWith(revoked)
			c.Assert(status, qt.Equals, http.StatusUnprocessableEntity)
			c.Assert(failure.Data.Reason, qt.Equals, "revoked")
		}
		status, failure = redeemWith(revoked)
		c.Assert(status, qt.Equals, http.StatusTooManyRequests)
		c.Assert(failure.Data.Reason, qt.Equals, "too_many_attempts")

		// revocation outages are never charged
		outage := e.issue(c, testprovider.Holder{GivenName: "O", Surname: "O", Document: "33333333P", OU: testprovider.OUOutage})
		for range 11 {
			status, failure := redeemWith(outage)
			c.Assert(status, qt.Equals, http.StatusServiceUnavailable)
			c.Assert(failure.Data.Reason, qt.Equals, "revocation_unavailable")
			c.Assert(failure.Data.Retryable, qt.IsTrue)
		}
	})

	c.Run("check only", func(c *qt.C) {
		e := newTestEnv(c, nil)
		e.store.addMember("12345678Z", 3)
		e.store.census.Weighted = true
		e.store.vp.StartDate = time.Now().Add(time.Hour)
		for _, purpose := range []string{"check", "vote"} {
			code, verifier := e.signIn(c, purpose, e.issue(c, maria))
			status, ok, _ := e.redeem(c, code, verifier)
			c.Assert(status, qt.Equals, http.StatusOK)
			c.Assert(ok.Purpose, qt.Equals, purpose)
			c.Assert(ok.AuthToken, qt.IsNil)
			c.Assert(ok.Weight, qt.IsNil)
			c.Assert(ok.Holder.DocumentEnding, qt.Equals, "678Z")
			c.Assert(ok.VoterRef, qt.Not(qt.Equals), "")
			c.Assert(ok.Reference, qt.Matches, `[0-9a-f]{32}`)
		}
		// open: a check still opens no session, a vote does, with the member weight
		e.store.vp.StartDate = time.Now().Add(-time.Hour)
		code, verifier := e.signIn(c, "check", e.issue(c, maria))
		_, ok, _ := e.redeem(c, code, verifier)
		c.Assert(ok.AuthToken, qt.IsNil)
		c.Assert(e.tokens.count(), qt.Equals, 0)
		code, verifier = e.signIn(c, "vote", e.issue(c, maria))
		_, ok, _ = e.redeem(c, code, verifier)
		c.Assert(ok.AuthToken, qt.Not(qt.IsNil))
		c.Assert(ok.Weight, qt.DeepEquals, internal.HexBytes{3})
		// a vote created before the end and redeemed after it gets no token
		code, verifier = e.signIn(c, "vote", e.issue(c, maria))
		e.store.vp.EndDate = time.Now().Add(-2 * time.Minute)
		status, ok, _ := e.redeem(c, code, verifier)
		c.Assert(status, qt.Equals, http.StatusOK)
		c.Assert(ok.AuthToken, qt.IsNil)
		c.Assert(e.tokens.count(), qt.Equals, 1)
	})
}

// TestCertAuthErrorMappingLogs pins the logging discipline of the whole flow (AC10): a sign-in with
// no certificate answers a fresh reference although no Verify ran, and a successful and a failed
// sign-in log only cert-check's LogAttrs keys and the attempt ids, never the document, the holder's
// name, the certificate, the code or the verifier.
func TestCertAuthErrorMappingLogs(t *testing.T) {
	c := qt.New(t)
	e := newTestEnv(c, nil)
	e.store.addMember("12.345.678-z", 1)

	code, verifier := e.signIn(c, "vote", nil)
	status, _, failure := e.redeem(c, code, verifier)
	c.Assert(status, qt.Equals, http.StatusUnprocessableEntity)
	c.Assert(failure.Data.Reason, qt.Equals, "no_certificate")
	c.Assert(failure.Data.Reference, qt.Matches, `[0-9a-f]{32}`)
	c.Assert(failure.Data.Retryable, qt.IsFalse)

	cert := e.issue(c, maria)
	code, verifier = e.signIn(c, "vote", cert)
	status, _, _ = e.redeem(c, code, verifier)
	c.Assert(status, qt.Equals, http.StatusOK)
	revokedHolder := maria
	revokedHolder.OU = testprovider.OURevoked
	code2, verifier2 := e.signIn(c, "vote", e.issue(c, revokedHolder))
	status, _, _ = e.redeem(c, code2, verifier2)
	c.Assert(status, qt.Equals, http.StatusUnprocessableEntity)

	full := certcheck.Diagnostics{TLS: &certcheck.TLSInfo{}, Certificate: &certcheck.CertificateDetails{}}
	allowed := slices.Concat(attrKeys(full),
		[]string{"transaction", "processId", "certificatePresented", "chainLength", "clientIP", "reference", "error"})
	secrets := []string{
		"12345678Z", "678Z", "Maria", "García", code, verifier, code2, verifier2, "IDCES",
		base64.StdEncoding.EncodeToString(cert.Certificate[0])[:40],
	}
	for _, entry := range e.logs.all() {
		for k, v := range entry.kv {
			c.Assert(slices.Contains(allowed, k), qt.IsTrue, qt.Commentf("log %q key %q", entry.msg, k))
			for _, secret := range secrets {
				c.Assert(strings.Contains(stringify(v), secret), qt.IsFalse, qt.Commentf("log %q key %q", entry.msg, k))
			}
		}
	}
}
