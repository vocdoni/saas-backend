package api

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/certauth"
	"github.com/vocdoni/saas-backend/certauth/testprovider"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/internal"
	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	// testCertAuthHost is the auth domain of the certificate-enabled test API.
	testCertAuthHost = "auth.test"
	// testCertAppOrigin is its only allowed app origin.
	testCertAppOrigin = "https://app.test"
)

var (
	// testCertPort is the port of the certificate-enabled API, testCertListenerPort its auth listener.
	testCertPort, testCertListenerPort int
	// testCertAPI is the API with certificate sign-in on; testAPI stays without it.
	testCertAPI *API
	// testCertPKI is the synthetic CA the certificate-enabled API trusts: tests mint voters' certificates
	// with it, and it signs the auth listener's server certificate.
	testCertPKI *testprovider.PKI
)

// startCertAuthAPI starts a second API sharing conf's database, CSP and chain, with certificate
// sign-in on: origin mode on a loopback auth listener, the synthetic provider next to FNMT and idCAT.
func startCertAuthAPI(ctx context.Context, conf Config, rootKey internal.HexBytes) error {
	testCertPort = 30000 + internal.RandomInt(5000)
	testCertListenerPort = 35000 + internal.RandomInt(5000)
	var err error
	if testCertPKI, err = testprovider.NewPKI(); err != nil {
		return err
	}
	certPEM, keyPEM, err := testCertPKI.ServerCertificate("*." + testCertAuthHost)
	if err != nil {
		return err
	}
	service, err := certauth.New(certauth.Config{
		Mode: certauth.ModeOrigin, Host: testCertAuthHost,
		ListenAddr: "127.0.0.1:" + strconv.Itoa(testCertListenerPort), PublicPort: testCertListenerPort,
		TLSCertPEM: certPEM, TLSKeyPEM: keyPEM, Providers: []string{"fnmt", "idcat"},
		AllowedOrigins: []string{testCertAppOrigin}, DefaultReturn: testCertAppOrigin + "/",
		CreatesPerMinute: certauth.DefaultCreatesPerMinute,
		TestProvider:     true, TestProviderCAPEM: testCertPKI.CertPEM(), VocdoniAPI: testAPIEndpoint,
		RootKey: rootKey,
	}, certauth.Deps{Storage: conf.DB, Tokens: conf.CSP})
	if err != nil {
		return err
	}
	conf.Port, conf.CertAuth = testCertPort, service
	testCertAPI = New(ctx, &conf)
	testCertAPI.Start()
	return pingAPI(fmt.Sprintf("http://%s:%d%s", testHost, testCertPort, pingEndpoint), 5)
}

// useCertAPI points every request helper at the certificate-enabled API until the test ends. The
// package's tests never run in parallel, which is what makes swapping testPort safe.
func useCertAPI(t *testing.T) {
	t.Helper()
	saved := testPort
	testPort = testCertPort
	t.Cleanup(func() { testPort = saved })
}

// TestCertAuthDisabled pins feature off (AC1): the API without certificate sign-in configured runs
// no auth listener, and both certificate endpoints answer the plain ErrCertificateAuthDisabled.
func TestCertAuthDisabled(t *testing.T) {
	c := qt.New(t)
	c.Assert(testAPI.certAuth, qt.IsNil)
	pid := bson.NewObjectID().Hex()
	requestAndAssertError(errors.ErrCertificateAuthDisabled, t, http.MethodPost, "",
		&apicommon.CertAuthCreateRequest{}, "processes", pid, "auth", "cert")
	requestAndAssertError(errors.ErrCertificateAuthDisabled, t, http.MethodPost, "",
		&apicommon.CertAuthRedeemRequest{}, "processes", pid, "auth", "cert", "redeem")
	// the typed auth steps still route as before
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodPost, "", nil, "processes", pid, "auth", "7")
	// and the CORS stays the permissive one, for the certificate paths too
	req, err := http.NewRequest(http.MethodOptions, testURL("/processes/"+pid+"/auth/cert"), nil)
	c.Assert(err, qt.IsNil)
	req.Header.Set("Origin", "https://anywhere.test")
	req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	resp, err := http.DefaultClient.Do(req)
	c.Assert(err, qt.IsNil)
	c.Assert(resp.Body.Close(), qt.IsNil)
	c.Assert(resp.Header.Get("Access-Control-Allow-Origin"), qt.Equals, "*")
}
