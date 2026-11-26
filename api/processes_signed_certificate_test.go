package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// newPublishedPaidProcess creates a process of growthBaseVoters fresh members without 2FA in
// orgAddress, pays it from the wallet and marks it published with a €0 base, the state its publish
// leaves, without the chain. It returns the process.
func newPublishedPaidProcess(t *testing.T, token string, orgAddress common.Address) bson.ObjectID {
	t.Helper()
	c := qt.New(t)
	req := newVotingProcessRequest(orgAddress, postNewOrgMembers(t, token, orgAddress, growthBaseVoters))
	req.Census.TwoFaFields = nil
	req.Census.AuthFields = db.OrgMemberAuthFields{db.OrgMemberAuthFieldsMemberNumber}
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
	c.Assert(markProcessPaid(t, pid), qt.Equals, growthCents(t, growthBaseVoters))
	oid := objectID(c, pid)
	c.Assert(testDB.SetVotingProcessPublished(oid, "", time.Now()), qt.IsNil)
	_, err := testDB.SetProcessPaymentBase(oid, orgAddress, 0)
	c.Assert(err, qt.IsNil)
	return oid
}

// buyCert calls POST /processes/{processId}/signed-certificate and expects a 200.
func buyCert(t *testing.T, token string, oid bson.ObjectID) pricing.Cents {
	t.Helper()
	return requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token, nil,
		"processes", oid.Hex(), "signed-certificate").AmountCents
}

// growthRefusal is the 402 data the growth guard answers for growing oid's census to size.
func growthRefusal(c *qt.C, oid bson.ObjectID, size int64) apicommon.ProcessCensusGrowthQuote {
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	census, err := testDB.Census(vp.CensusID.Hex())
	c.Assert(err, qt.IsNil)
	err = testAPI.censusGrowthPaymentError(censusGrowth{vp: vp, census: census, size: size})
	var apiErr errors.Error
	c.Assert(errors.As(err, &apiErr), qt.IsTrue, qt.Commentf("error: %v", err))
	quote, ok := apiErr.Data.(apicommon.ProcessCensusGrowthQuote)
	c.Assert(ok, qt.IsTrue)
	return quote
}

// setCensusSize overwrites the stored size of oid's census, as a path the growth guard does not
// see would leave it.
func setCensusSize(c *qt.C, oid bson.ObjectID, size int64) {
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	_, err = testDB.DBClient.Database(testDBName).Collection("census").UpdateOne(context.Background(),
		bson.M{"_id": vp.CensusID}, bson.M{"$set": bson.M{"size": size}})
	c.Assert(err, qt.IsNil)
}

// assertBalance asserts wallet's balance.
func assertBalance(c *qt.C, wallet common.Address, cents pricing.Cents) {
	got, err := testDB.Wallet(wallet)
	c.Assert(err, qt.IsNil)
	c.Assert(got.BalanceCents, qt.Equals, cents)
}

// TestBuySignedCertificate: a published process buys the certificate once, from its wallet; a
// short wallet gets the shortfall to top up and nothing moves; the 3900 never reads as headroom.
func TestBuySignedCertificate(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "signedcert12345")
	orgAddress := testCreateProvisionedOrganization(t, token)
	oid := newPublishedPaidProcess(t, token, orgAddress)
	paidCents := growthCents(t, growthBaseVoters)

	// a draft selects it with PUT; another organization's user and an unknown process are refused
	draft := newPricedVotingProcess(t, token, orgAddress)
	requestAndAssertError(errors.ErrMalformedBody, t, http.MethodPost, token, nil, "processes", draft, "signed-certificate")
	other := testCreateUser(t, "signedcertother1")
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, other, nil,
		"processes", oid.Hex(), "signed-certificate")
	// a manager reads the price but cannot spend the wallet
	managerToken := testCreateUser(t, "signedcertmanager1")
	managerInfo := requestAndParse[apicommon.UserInfo](t, http.MethodGet, managerToken, nil, usersMeEndpoint)
	manager, err := testDB.UserByEmail(managerInfo.Email)
	c.Assert(err, qt.IsNil)
	manager.Organizations = append(manager.Organizations, db.OrganizationUser{Address: orgAddress, Role: db.ManagerRole})
	_, err = testDB.SetUser(manager)
	c.Assert(err, qt.IsNil)
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodPost, managerToken, nil,
		"processes", oid.Hex(), "signed-certificate")
	requestAndAssertError(errors.ErrProcessNotFound, t, http.MethodPost, token, nil,
		"processes", bson.NewObjectID().Hex(), "signed-certificate")

	// an empty wallet: 402 with what to top up, nothing debited
	short := requestAndParseWithAssertCode[struct {
		Code    int                              `json:"code"`
		Message string                           `json:"error"`
		Data    apicommon.ProcessPaymentRequired `json:"data"`
	}](http.StatusPaymentRequired, t, http.MethodPost, token, nil, "processes", oid.Hex(), "signed-certificate")
	c.Assert(short.Code, qt.Equals, errors.ErrPaymentRequired.Code)
	c.Assert(short.Message, qt.Contains, "signed results certificate")
	c.Assert(short.Data.Lines, qt.DeepEquals, []pricing.QuoteLine{{
		Kind: pricing.LineSignedCert, Description: "signed results certificate", AmountCents: pricing.SignedCertCents,
	}})
	c.Assert(short.Data.TotalCents, qt.Equals, pricing.SignedCertCents)
	c.Assert(short.Data.BalanceCents, qt.Equals, pricing.Cents(0))
	c.Assert(short.Data.ShortfallCents, qt.Equals, pricing.SignedCertCents)
	_, err = testDB.WalletEntry(orgAddress, db.SignedCertificateDebitKey(oid))
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)

	// topped up: debited once, recorded on the ledger and echoed on the process
	fundWallet(t, orgAddress, pricing.SignedCertCents)
	c.Assert(buyCert(t, token, oid), qt.Equals, pricing.SignedCertCents)
	assertBalance(c, orgAddress, 0)
	row, err := testDB.WalletEntry(orgAddress, db.SignedCertificateDebitKey(oid))
	c.Assert(err, qt.IsNil)
	c.Assert(row.SignedCertificate, qt.IsTrue)
	c.Assert(row.AmountCents, qt.Equals, -pricing.SignedCertCents)
	vp, err := testDB.VotingProcess(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(vp.AddOns.SignedCertificate, qt.IsTrue)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.AmountCents, qt.Equals, paidCents+pricing.SignedCertCents)

	// a second call debits nothing
	c.Assert(buyCert(t, token, oid), qt.Equals, pricing.Cents(0))
	assertBalance(c, orgAddress, 0)

	// growth is charged the growth only
	refusal := growthRefusal(c, oid, growthTierEnd)
	c.Assert(refusal.PaidCents, qt.Equals, paidCents+pricing.SignedCertCents)
	c.Assert(refusal.DueCents, qt.Equals, growthCents(t, growthTierEnd)-paidCents)

	// and buying that growth debits exactly it
	growthDue := growthCents(t, growthTierEnd) - paidCents
	fundWallet(t, orgAddress, growthDue)
	bought := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.ProcessCensusCheckoutRequest{CensusSize: growthTierEnd}, "processes", oid.Hex(), "census", "checkout")
	c.Assert(bought.AmountCents, qt.Equals, growthDue)
	assertBalance(c, orgAddress, 0)
}

// TestSignedCertificateFlatPrice: the certificate costs 3900 whatever the census did after the
// publish: unused headroom, a census that shrank, or one that grew past what was paid.
func TestSignedCertificateFlatPrice(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "signedcertflat12")
	orgAddress := testCreateProvisionedOrganization(t, token)
	paidCents := growthCents(t, growthBaseVoters)

	headroom := newPublishedPaidProcess(t, token, orgAddress)
	buyHeadroom(t, headroom, paidCents)
	shrunk := newPublishedPaidProcess(t, token, orgAddress)
	setCensusSize(c, shrunk, 1)
	grown := newPublishedPaidProcess(t, token, orgAddress)
	setCensusSize(c, grown, growthTotalVoters)

	for _, oid := range []bson.ObjectID{headroom, shrunk, grown} {
		fundWallet(t, orgAddress, pricing.SignedCertCents)
		c.Assert(buyCert(t, token, oid), qt.Equals, pricing.SignedCertCents)
		assertBalance(c, orgAddress, 0)
	}
}

// TestGrandfatheredSignedCertificate: a published process with no base gets it from its census
// without the certificate, then pays the 3900; later growth is charged the growth only.
func TestGrandfatheredSignedCertificate(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "signedcertgrand1")
	orgAddress := testCreateProvisionedOrganization(t, token)
	oid := newPublishedPaidProcess(t, token, orgAddress)
	// what a process published before pay-per-process looks like: no payment row, no movements
	database := testDB.DBClient.Database(testDBName)
	_, err := database.Collection("processPayments").DeleteOne(context.Background(), bson.M{"_id": oid})
	c.Assert(err, qt.IsNil)
	_, err = database.Collection("walletLedger").DeleteMany(context.Background(), bson.M{"processId": oid})
	c.Assert(err, qt.IsNil)

	fundWallet(t, orgAddress, pricing.SignedCertCents)
	c.Assert(buyCert(t, token, oid), qt.Equals, pricing.SignedCertCents)
	baseCents := growthCents(t, growthBaseVoters)
	payment, err := testDB.ProcessPayment(oid)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.BaseCents, qt.DeepEquals, &baseCents)

	refusal := growthRefusal(c, oid, growthTierEnd)
	c.Assert(refusal.PaidCents, qt.Equals, baseCents+pricing.SignedCertCents)
	c.Assert(refusal.DueCents, qt.Equals, growthCents(t, growthTierEnd)-baseCents)

	// a base lost after the purchase is recorded again without the certificate the ledger holds
	_, err = database.Collection("processPayments").DeleteOne(context.Background(), bson.M{"_id": oid})
	c.Assert(err, qt.IsNil)
	refusal = growthRefusal(c, oid, growthTierEnd)
	c.Assert(refusal.PaidCents, qt.Equals, baseCents+pricing.SignedCertCents)
	c.Assert(refusal.DueCents, qt.Equals, growthCents(t, growthTierEnd)-baseCents)
}

// TestConcurrentSignedCertificate: two purchases racing debit the certificate once; both succeed.
func TestConcurrentSignedCertificate(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "signedcertrace12")
	orgAddress := testCreateProvisionedOrganization(t, token)
	oid := newPublishedPaidProcess(t, token, orgAddress)
	fundWallet(t, orgAddress, 2*pricing.SignedCertCents)

	debited := make(chan pricing.Cents, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() { debited <- buyCert(t, token, oid) })
	}
	wg.Wait()
	close(debited)
	var total pricing.Cents
	for cents := range debited {
		total += cents
	}
	c.Assert(total, qt.Equals, pricing.SignedCertCents)
	assertBalance(c, orgAddress, pricing.SignedCertCents)
}

// TestManagedSignedCertificate: a managed organization's certificate is debited from its
// integrator's wallet: 40175 when short, 40183 past its spend cap, which it counts towards. An API
// key buys it with voting:write only.
func TestManagedSignedCertificate(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "signedcertmanage")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token)
	oid := newPublishedPaidProcess(t, token, managedAddr)

	short := requestAndParseWithAssertCode[struct {
		Code int                      `json:"code"`
		Data map[string]pricing.Cents `json:"data"`
	}](http.StatusPaymentRequired, t, http.MethodPost, token, nil, "processes", oid.Hex(), "signed-certificate")
	c.Assert(short.Code, qt.Equals, errors.ErrInsufficientWalletBalance.Code)
	c.Assert(short.Data, qt.DeepEquals, map[string]pricing.Cents{
		"requiredCents": pricing.SignedCertCents, "availableCents": 0,
	})

	fundWallet(t, integratorAddr, pricing.SignedCertCents)
	wallet, err := testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	spent := wallet.ManagedOrgSpend(managedAddr).SpentCents
	capCents := spent + pricing.SignedCertCents - 1
	c.Assert(testDB.SetManagedOrgSpendCap(integratorAddr, managedAddr, &capCents), qt.IsNil)
	requestAndAssertError(errors.ErrManagedOrgSpendCapReached, t, http.MethodPost, token, nil,
		"processes", oid.Hex(), "signed-certificate")
	c.Assert(testDB.SetManagedOrgSpendCap(integratorAddr, managedAddr, nil), qt.IsNil)

	mintKey := func(scopes ...string) string {
		data, code := testRequest(t, http.MethodPost, token,
			&apicommon.CreateAPIKeyRequest{Label: "cert", Scopes: scopes},
			"integrator", "organizations", integratorAddr.String(), "apikeys")
		c.Assert(code, qt.Equals, http.StatusOK, qt.Commentf("resp: %s", data))
		var created apicommon.CreateAPIKeyResponse
		c.Assert(json.Unmarshal(data, &created), qt.IsNil)
		return created.Secret
	}
	requestAndAssertCode(http.StatusForbidden, t, http.MethodPost, mintKey(ScopeQuotaRead), nil,
		"processes", oid.Hex(), "signed-certificate")
	c.Assert(buyCert(t, mintKey(ScopeVotingWrite), oid), qt.Equals, pricing.SignedCertCents)
	wallet, err = testDB.Wallet(integratorAddr)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, pricing.Cents(0))
	c.Assert(wallet.ManagedOrgSpend(managedAddr).SpentCents, qt.Equals, spent+pricing.SignedCertCents)
}

// TestSignedCertificateEndedAndQuoteOnly: an ended process and a quote-only one (published
// outside the wallet, so grandfathered at a €0 base) buy the certificate like any other.
func TestSignedCertificateEndedAndQuoteOnly(t *testing.T) {
	c := qt.New(t)
	token := testCreateUser(t, "signedcertended1")
	orgAddress := testCreateProvisionedOrganization(t, token)

	ended := newPublishedPaidProcess(t, token, orgAddress)
	c.Assert(testDB.SetVotingProcessEndDate(ended, time.Now().Add(-time.Hour)), qt.IsNil)
	fundWallet(t, orgAddress, pricing.SignedCertCents)
	c.Assert(buyCert(t, token, ended), qt.Equals, pricing.SignedCertCents)

	quoteOnly := objectID(c, newPricedVotingProcess(t, token, orgAddress))
	c.Assert(testDB.SetVotingProcessPublished(quoteOnly, "", time.Now()), qt.IsNil)
	setCensusSize(c, quoteOnly, 50_001)
	fundWallet(t, orgAddress, pricing.SignedCertCents)
	c.Assert(buyCert(t, token, quoteOnly), qt.Equals, pricing.SignedCertCents)
	payment, err := testDB.ProcessPayment(quoteOnly)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.BaseCents, qt.DeepEquals, new(pricing.Cents))
	assertBalance(c, orgAddress, 0)
}
