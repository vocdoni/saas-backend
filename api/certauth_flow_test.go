package api

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/csp/handlers"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/crypto/ethereum"
)

// certPKCE returns a fresh verifier and its S256 challenge.
func certPKCE() (verifier, challenge string) {
	verifier = internal.RandomHex(32) // 64 characters of the verifier grammar
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// certCreate starts an attempt on pid and returns the answer and its verifier.
func certCreate(t *testing.T, pid, purpose string) (apicommon.CertAuthCreateResponse, string) {
	t.Helper()
	verifier, challenge := certPKCE()
	created := requestAndParse[apicommon.CertAuthCreateResponse](t, http.MethodPost, "",
		&apicommon.CertAuthCreateRequest{
			Challenge: challenge, ChallengeMethod: "S256", Purpose: purpose,
			ReturnURL: testCertAppOrigin + "/ca/processes/" + pid,
		}, "processes", pid, "auth", "cert")
	return created, verifier
}

// certAuthenticate navigates to authURL on the auth listener presenting cert (nil: none) and returns
// the 303's target and fragment.
func certAuthenticate(t *testing.T, authURL string, cert *tls.Certificate) (string, url.Values) {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(testCertPKI.CA)
	client := &http.Client{
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, "127.0.0.1:"+strconv.Itoa(testCertListenerPort))
			},
			TLSClientConfig: &tls.Config{
				RootCAs: roots, MinVersion: tls.VersionTLS12,
				GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
					if cert == nil {
						return &tls.Certificate{}, nil
					}
					return cert, nil
				},
			},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       10 * time.Second,
	}
	resp, err := client.Get(authURL)
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, resp.Body.Close(), qt.IsNil)
	qt.Assert(t, resp.StatusCode, qt.Equals, http.StatusSeeOther)
	loc, err := url.Parse(resp.Header.Get("Location"))
	qt.Assert(t, err, qt.IsNil)
	frag, err := url.ParseQuery(loc.Fragment)
	qt.Assert(t, err, qt.IsNil)
	loc.Fragment = ""
	return loc.String(), frag
}

// certSignIn runs create and authenticate with cert, returning the code and verifier to redeem.
func certSignIn(t *testing.T, pid, purpose string, cert *tls.Certificate) (code, verifier string) {
	t.Helper()
	created, verifier := certCreate(t, pid, purpose)
	_, frag := certAuthenticate(t, created.AuthURL, cert)
	qt.Assert(t, frag.Get("state"), qt.Equals, created.State)
	return frag.Get("code"), verifier
}

// certRedeem redeems a code, returning the status and either the 200 answer or the error envelope.
func certRedeem(t *testing.T, pid, code, verifier string,
) (int, *apicommon.CertAuthRedeemResponse, *certErrorBody) {
	t.Helper()
	body, status := testRequest(t, http.MethodPost, "",
		&apicommon.CertAuthRedeemRequest{Code: code, Verifier: verifier}, "processes", pid, "auth", "cert", "redeem")
	if status == http.StatusOK {
		resp := &apicommon.CertAuthRedeemResponse{}
		qt.Assert(t, json.Unmarshal(body, resp), qt.IsNil)
		return status, resp, nil
	}
	failure := &certErrorBody{}
	qt.Assert(t, json.Unmarshal(body, failure), qt.IsNil, qt.Commentf("body: %s", body))
	return status, nil, failure
}

// certErrorBody is a decoded certificate error envelope.
type certErrorBody struct {
	Error string                      `json:"error"`
	Code  int                         `json:"code"`
	Data  apicommon.CertAuthErrorData `json:"data"`
}

// issueCert mints a synthetic certificate for document.
func issueCert(t *testing.T, document string) *tls.Certificate {
	t.Helper()
	cert, err := testCertPKI.Issue(testprovider.Holder{GivenName: "Maria", Surname: "García López", Document: document})
	qt.Assert(t, err, qt.IsNil)
	return cert
}

// TestCertAuthCreate pins the create endpoint against the real stack (AC6): the 200 shape, the
// returnUrl allowlist, 409 on an ended process, 400 on a typed census, and the per-IP limit.
func TestCertAuthCreate(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")
	pid, _ := publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)

	before := time.Now()
	created, _ := certCreate(t, pid, "vote")
	c.Assert(created.State, qt.Matches, `[A-Za-z0-9_-]{43}`)
	authURL, err := url.Parse(created.AuthURL)
	c.Assert(err, qt.IsNil)
	c.Assert(authURL.Scheme, qt.Equals, "https")
	c.Assert(authURL.Hostname(), qt.Matches, `[a-z2-7]{26}\.`+strings.ReplaceAll(testCertAuthHost, ".", `\.`))
	c.Assert(authURL.Path, qt.Equals, "/v1/authenticate")
	c.Assert(authURL.Query().Get("state"), qt.Equals, created.State)
	c.Assert(strings.HasPrefix(authURL.Hostname(), created.State[:26]), qt.IsFalse) // independent of the state
	expires, err := time.Parse(time.RFC3339, created.ExpiresAt)
	c.Assert(err, qt.IsNil)
	c.Assert(expires.Sub(before) > 14*time.Minute && expires.Sub(before) < 16*time.Minute, qt.IsTrue)
	c.Assert(created.ProbeURL, qt.Equals, "https://probe."+testCertAuthHost+":"+strconv.Itoa(testCertListenerPort)+"/ok")
	again, _ := certCreate(t, pid, "vote")
	c.Assert(again.AuthURL, qt.Not(qt.Equals), created.AuthURL) // a fresh host per attempt

	_, challenge := certPKCE()
	for _, tc := range []struct {
		returnURL string
		ok        bool
	}{
		{testCertAppOrigin + "/", true},
		{testCertAppOrigin + "/es", true},
		{testCertAppOrigin + "/processes/" + pid, true},
		{"https://evil.test/processes/" + pid, false},
		{testCertAppOrigin + "/processes/" + pid + "?x=1", false},
		{testCertAppOrigin + "/processes/" + pid + "#x", false},
		{"https://user@app.test/", false},
		{testCertAppOrigin + "/processes/" + pid + "/results", false},
		{"http://app.test/", false},
	} {
		_, status := testRequest(t, http.MethodPost, "", &apicommon.CertAuthCreateRequest{
			Challenge: challenge, ChallengeMethod: "S256", ReturnURL: tc.returnURL, Purpose: "vote",
		}, "processes", pid, "auth", "cert")
		want := http.StatusBadRequest
		if tc.ok {
			want = http.StatusOK
		}
		c.Assert(status, qt.Equals, want, qt.Commentf("%s", tc.returnURL))
	}

	// a typed census is a 400, an ended process a 409
	typedPid, _ := publishCensusProcess(t, token, orgAddress,
		apicommon.CensusSpec{AuthFields: nationalIDCensus, MemberIDs: ids}, 1)
	_, status := testRequest(t, http.MethodPost, "", &apicommon.CertAuthCreateRequest{
		Challenge: challenge, ChallengeMethod: "S256", ReturnURL: testCertAppOrigin + "/", Purpose: "vote",
	}, "processes", typedPid, "auth", "cert")
	c.Assert(status, qt.Equals, http.StatusBadRequest)
	endedPid, _ := publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)
	endCertProcess(t, endedPid)
	_, status = testRequest(t, http.MethodPost, "", &apicommon.CertAuthCreateRequest{
		Challenge: challenge, ChallengeMethod: "S256", ReturnURL: testCertAppOrigin + "/", Purpose: "vote",
	}, "processes", endedPid, "auth", "cert")
	c.Assert(status, qt.Equals, http.StatusConflict)

	// the 61st create in one UTC minute from one IP answers 429 (stay clear of a minute boundary)
	limitPid, _ := publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)
	if s := time.Now().UTC().Second(); s > 40 {
		time.Sleep(time.Duration(61-s) * time.Second)
	}
	for range 60 {
		certCreate(t, limitPid, "check")
	}
	body, status := testRequest(t, http.MethodPost, "", &apicommon.CertAuthCreateRequest{
		Challenge: challenge, ChallengeMethod: "S256", ReturnURL: testCertAppOrigin + "/", Purpose: "check",
	}, "processes", limitPid, "auth", "cert")
	c.Assert(status, qt.Equals, http.StatusTooManyRequests, qt.Commentf("%s", body))
	c.Assert(string(body), qt.Contains, `"reason":"too_many_attempts"`)
	c.Assert(string(body), qt.Contains, `"retryAfter":`)
}

// TestCertAuthRedeem pins the happy path against the real stack (AC8): a member stored as
// "12.345.678-z" redeems a certificate for 12345678Z into an auth-only token that works with check,
// sign-batch and sign-info like an auth/0 one; holder, weight, voterRef and reference are right for
// a DNI and an NIE, and the transaction's chain is gone.
func TestCertAuthRedeem(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12.345.678-z", "x1234567l")
	census := certCensus(ids...)
	census.Weighted = true
	pid, elections := publishCensusProcess(t, token, orgAddress, census, 2)

	code, verifier := certSignIn(t, pid, "vote", issueCert(t, "12345678Z"))
	status, ok, failure := certRedeem(t, pid, code, verifier)
	c.Assert(status, qt.Equals, http.StatusOK, qt.Commentf("%+v", failure))
	c.Assert(ok.Purpose, qt.Equals, "vote")
	c.Assert(ok.AuthToken, qt.Not(qt.IsNil))
	c.Assert(ok.Weight, qt.DeepEquals, internal.HexBytes{1})
	c.Assert(ok.Holder, qt.DeepEquals, apicommon.CertAuthHolder{
		FullName: "Maria García López", DocumentType: "DNI", DocumentEnding: "678Z",
	})
	c.Assert(ok.Reference, qt.Matches, `[0-9a-f]{32}`)
	tx, err := testDB.CertAuthTransactionByCode(code)
	c.Assert(err, qt.IsNil)
	c.Assert(tx.Status, qt.Equals, db.CertAuthStatusConsumed)
	c.Assert(tx.Chain, qt.HasLen, 0)

	// the token is an auth-only token like auth/0's: check, sign-batch and sign-info take it
	tok := *ok.AuthToken
	auth, err := testDB.CSPAuth(tok)
	c.Assert(err, qt.IsNil)
	c.Assert(auth.Verified, qt.IsTrue)
	c.Assert(auth.UserID.String(), qt.Equals, ids[0])
	check := requestAndParse[handlers.ProcessCheckResponse](t, http.MethodPost, "",
		&handlers.CheckMembershipRequest{AuthToken: tok}, "processes", pid, "check")
	c.Assert(check.BelongsToProcess, qt.IsTrue)
	c.Assert(check.Weight, qt.DeepEquals, ok.Weight)
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	batch := requestAndParse[handlers.SignBatchResponse](t, http.MethodPost, "", &handlers.SignBatchRequest{
		AuthToken: tok, Ballots: []handlers.SignBatchBallot{
			{UpstreamID: elections[0], Address: voter.Address().Bytes()},
			{UpstreamID: elections[1], Address: voter.Address().Bytes()},
		},
	}, "processes", pid, "sign-batch")
	c.Assert(batch.Signatures, qt.HasLen, 2)
	for _, s := range batch.Signatures {
		c.Assert(s.Code, qt.Equals, "", qt.Commentf("%s", s.Error))
		c.Assert(hex.EncodeToString(s.Weight), qt.Equals, "01")
	}
	info := requestAndParse[handlers.ProcessSignInfoResponse](t, http.MethodPost, "",
		&handlers.ConsumedAddressRequest{AuthToken: tok}, "processes", pid, "sign-info")
	c.Assert(info.Consumed, qt.HasLen, 2)

	// voterRef is stable for one member, and different for another member and another process
	code, verifier = certSignIn(t, pid, "vote", issueCert(t, "12345678Z"))
	_, again, _ := certRedeem(t, pid, code, verifier)
	c.Assert(again.VoterRef, qt.Equals, ok.VoterRef)
	nieCert, err := testCertPKI.Issue(testprovider.Holder{GivenName: "Jordi", Surname: "Puig Serra", Document: "X1234567L"})
	c.Assert(err, qt.IsNil)
	code, verifier = certSignIn(t, pid, "vote", nieCert)
	_, nie, _ := certRedeem(t, pid, code, verifier)
	c.Assert(nie.Holder, qt.DeepEquals, apicommon.CertAuthHolder{
		FullName: "Jordi Puig Serra", DocumentType: "NIE", DocumentEnding: "567L",
	})
	c.Assert(nie.Weight, qt.DeepEquals, internal.HexBytes{2})
	c.Assert(nie.VoterRef, qt.Not(qt.Equals), ok.VoterRef)
	otherPid, _ := publishCensusProcess(t, token, orgAddress, census, 1)
	code, verifier = certSignIn(t, otherPid, "vote", issueCert(t, "12345678Z"))
	_, other, _ := certRedeem(t, otherPid, code, verifier)
	c.Assert(other.VoterRef, qt.Not(qt.Equals), ok.VoterRef)
}

// TestCertAuthRedeemSingleUse pins single use and PKCE against the real stack (AC9).
func TestCertAuthRedeemSingleUse(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")
	pid, _ := publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)
	otherPid, _ := publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)
	cert := issueCert(t, "12345678Z")

	code, verifier := certSignIn(t, pid, "vote", cert)
	status, _, _ := certRedeem(t, pid, code, verifier)
	c.Assert(status, qt.Equals, http.StatusOK)
	status, _, failure := certRedeem(t, pid, code, verifier)
	c.Assert(status, qt.Equals, http.StatusConflict)
	c.Assert(failure.Data.Reason, qt.Equals, "code_used")

	code, verifier = certSignIn(t, pid, "vote", cert)
	wrong, _ := certPKCE()
	status, _, failure = certRedeem(t, pid, code, wrong)
	c.Assert(status, qt.Equals, http.StatusForbidden)
	c.Assert(failure.Data.Reason, qt.Equals, "verifier_mismatch")
	status, _, failure = certRedeem(t, pid, code, verifier) // consumed by the mismatch
	c.Assert(status, qt.Equals, http.StatusConflict)
	c.Assert(failure.Data.Reason, qt.Equals, "code_used")

	// past the code TTL (the transaction aged by hand, as the TTL monitor may lag)
	code, verifier = certSignIn(t, pid, "vote", cert)
	ageCertAuthCode(t, code, 6*time.Minute)
	status, _, failure = certRedeem(t, pid, code, verifier)
	c.Assert(status, qt.Equals, http.StatusGone)
	c.Assert(failure.Data.Reason, qt.Equals, "attempt_expired")

	// a code is bound to its process
	code, verifier = certSignIn(t, pid, "vote", cert)
	status, _, failure = certRedeem(t, otherPid, code, verifier)
	c.Assert(status, qt.Equals, http.StatusGone)
	c.Assert(failure.Data.Reason, qt.Equals, "attempt_expired")
}

// TestCertAuthRedeemBudget pins the two-stage failed-attempt budget against the real stack (AC11).
func TestCertAuthRedeemBudget(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")
	pid, _ := publishCensusProcess(t, token, orgAddress, certCensus(ids...), 1)
	redeemWith := func(cert any) (int, *certErrorBody) {
		var code, verifier string
		if document, ok := cert.(string); ok {
			code, verifier = certSignIn(t, pid, "vote", issueCert(t, document))
		} else {
			forged, err := testprovider.SelfSigned(cert.(testprovider.Holder))
			c.Assert(err, qt.IsNil)
			code, verifier = certSignIn(t, pid, "vote", forged)
		}
		status, _, failure := certRedeem(t, pid, code, verifier)
		return status, failure
	}

	// ten self-signed certificates naming the member's document do not lock the member out
	for range 10 {
		status, failure := redeemWith(testprovider.Holder{GivenName: "X", Surname: "X", Document: "12345678Z"})
		c.Assert(status, qt.Equals, http.StatusUnprocessableEntity)
		c.Assert(failure.Data.Reason, qt.Equals, "untrusted_issuer")
	}
	status, _ := redeemWith("12345678Z")
	c.Assert(status, qt.Equals, http.StatusOK)

	// a verified document outside the census is capped at 10 per hour, another one is not
	for range 10 {
		status, failure := redeemWith("87654321X")
		c.Assert(status, qt.Equals, http.StatusForbidden)
		c.Assert(failure.Data.Reason, qt.Equals, "dni_not_in_census")
	}
	status, failure := redeemWith("87654321X")
	c.Assert(status, qt.Equals, http.StatusTooManyRequests)
	c.Assert(failure.Data.Reason, qt.Equals, "too_many_attempts")
	c.Assert(failure.Data.RetryAfter > 0, qt.IsTrue)
	status, _ = redeemWith("11111111H")
	c.Assert(status, qt.Equals, http.StatusForbidden)

	// revocation outages are never charged: the same certificate keeps answering 503, not 429
	outage, err := testCertPKI.Issue(testprovider.Holder{
		GivenName: "O", Surname: "O", Document: "12345678Z",
		OU: testprovider.OUOutage,
	})
	c.Assert(err, qt.IsNil)
	for range 11 {
		code, verifier := certSignIn(t, pid, "vote", outage)
		status, _, failure := certRedeem(t, pid, code, verifier)
		c.Assert(status, qt.Equals, http.StatusServiceUnavailable)
		c.Assert(failure.Data.Reason, qt.Equals, "revocation_unavailable")
	}
	status, _ = redeemWith("12345678Z")
	c.Assert(status, qt.Equals, http.StatusOK)
}

// TestCertAuthCheckOnly pins the check-only redeems against the real stack (AC12): an UPCOMING
// process answers a null token, no weight and no CSP token, for both purposes, and spends budget.
func TestCertAuthCheckOnly(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")
	req := minimalVotingProcessRequest(orgAddress) // starts in an hour
	req.Census = certCensus(ids...)
	pid, _ := publishProcessRequest(t, token, req)

	for _, purpose := range []string{"check", "vote"} {
		code, verifier := certSignIn(t, pid, purpose, issueCert(t, "12345678Z"))
		status, ok, failure := certRedeem(t, pid, code, verifier)
		c.Assert(status, qt.Equals, http.StatusOK, qt.Commentf("%+v", failure))
		c.Assert(ok.Purpose, qt.Equals, purpose)
		c.Assert(ok.AuthToken, qt.IsNil)
		c.Assert(ok.Weight, qt.IsNil)
		c.Assert(ok.Holder.DocumentEnding, qt.Equals, "678Z")
		c.Assert(ok.VoterRef, qt.Not(qt.Equals), "")
		c.Assert(ok.Reference, qt.Matches, `[0-9a-f]{32}`)
	}
	oid := objectID(c, pid)
	_, err := testDB.LastCSPAuth(internal.HexBytesFromString(ids[0]), internal.HexBytes(oid[:]))
	c.Assert(err, qt.ErrorIs, db.ErrTokenNotFound)

	// check redeems spend the budget like votes do: ten unknown documents, then 429
	for range 10 {
		code, verifier := certSignIn(t, pid, "check", issueCert(t, "87654321X"))
		status, _, _ := certRedeem(t, pid, code, verifier)
		c.Assert(status, qt.Equals, http.StatusForbidden)
	}
	code, verifier := certSignIn(t, pid, "check", issueCert(t, "87654321X"))
	status, _, _ := certRedeem(t, pid, code, verifier)
	c.Assert(status, qt.Equals, http.StatusTooManyRequests)
}

// TestCertAuthBlindCSP pins the anonymous certificate census (AC13): it publishes with the blind key
// and the redeemed token runs blind-point and blind-sign into a vote cast on chain.
func TestCertAuthBlindCSP(t *testing.T) {
	c := qt.New(t)
	useCertAPI(t)
	token, orgAddress, ids := certVoters(t, "12345678Z")
	census := certCensus(ids...)
	census.Anonymous = true
	pid, elections := publishCensusProcess(t, token, orgAddress, census, 1)
	got := requestAndParse[apicommon.VotingProcessResponse](t, http.MethodGet, "", nil, "processes", pid)
	c.Assert(got.Census.Anonymous, qt.IsTrue)
	c.Assert(got.Census.AuthMethod, qt.Equals, db.CensusAuthMethodCertificate)

	code, verifier := certSignIn(t, pid, "vote", issueCert(t, "12345678Z"))
	status, ok, failure := certRedeem(t, pid, code, verifier)
	c.Assert(status, qt.Equals, http.StatusOK, qt.Commentf("%+v", failure))
	voter := ethereum.SignKeys{}
	c.Assert(voter.Generate(), qt.IsNil)
	proof := testBlindSignBallot(t, pid, *ok.AuthToken, elections[0], voter.Address().Bytes())
	nullifier := testCastVote(t, testNewVocdoniClient(t), &voter, elections[0], proof, []byte(`{"votes":[0]}`))
	c.Assert(nullifier, qt.Not(qt.HasLen), 0)
}

// endCertProcess marks every question of a published process ENDED in storage.
func endCertProcess(t *testing.T, pid string) {
	t.Helper()
	questions, err := testDB.QuestionsByProcess(objectID(qt.New(t), pid))
	qt.Assert(t, err, qt.IsNil)
	for _, q := range questions {
		qt.Assert(t, testDB.SetQuestionStatus(q.ID, db.QuestionStatusEnded), qt.IsNil)
	}
}

// ageCertAuthCode moves a code's authentication back by age, as if it had waited that long.
func ageCertAuthCode(t *testing.T, code string, age time.Duration) {
	t.Helper()
	_, err := testDB.DBClient.Database(testDBName).Collection("certAuthTransactions").UpdateOne(context.Background(),
		bson.M{"code": code}, bson.M{"$set": bson.M{"authenticatedAt": time.Now().Add(-age)}})
	qt.Assert(t, err, qt.IsNil)
}
