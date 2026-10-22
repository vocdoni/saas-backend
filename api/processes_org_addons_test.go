package api

import (
	"net/http"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// TestQuoteOnlyRefusedOnEveryPaymentPath: above 50 000 voters a quote has no price and a zero
// total, which no payment path may read as free: publish, the dry-run, paid fulfillment, the
// census growth guard and the wallet charge all refuse it.
func TestQuoteOnlyRefusedOnEveryPaymentPath(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "quoteonlypaths12")
	orgAddress := testCreateProvisionedOrganization(t, token)
	setOrganizationSubscription(t, orgAddress, mockEssentialPlan.ID)
	draftOID := func(pid string) bson.ObjectID {
		oid, err := bson.ObjectIDFromHex(pid)
		c.Assert(err, qt.IsNil)
		return oid
	}
	resize := func(pid string, size int64) (*db.VotingProcess, *db.Census) {
		vp, err := testDB.VotingProcess(draftOID(pid))
		c.Assert(err, qt.IsNil)
		census, err := testDB.Census(vp.CensusID.Hex())
		c.Assert(err, qt.IsNil)
		census.Size = size
		_, err = testDB.SetCensus(census)
		c.Assert(err, qt.IsNil)
		return vp, census
	}

	// an unpaid draft: the publish gate refuses it with 422, not as free, and the dry-run says so
	draft := newPricedVotingProcess(t, token, orgAddress)
	resize(draft, 50_001)
	price := requestAndParse[apicommon.ProcessPriceResponse](t, http.MethodGet, token, nil, "processes", draft, "price")
	c.Assert(price.QuoteRequired, qt.IsTrue)
	c.Assert(price.TotalCents, qt.Equals, pricing.Cents(0))
	c.Assert(price.Lines, qt.HasLen, 0)
	// the publish gate (the plan's own census cap answers first over HTTP on this branch)
	draftVP, err := testDB.VotingProcess(draftOID(draft))
	c.Assert(err, qt.IsNil)
	_, err = testAPI.paymentDueForPublish(draftVP)
	c.Assert(err, qt.ErrorIs, errors.ErrQuoteRequired)
	validation := requestAndParse[apicommon.VotingProcessValidateResponse](
		t, http.MethodGet, token, nil, "processes", draft, "validation")
	c.Assert(validation.Errors, qt.Contains, errors.ErrQuoteRequired.Error())

	// a paid draft whose census then outgrew the self-service limit: fulfillment leaves it paid for
	// a manual publish instead of failing the webhook forever, and growth checks refuse it. Paid
	// through checkout, so the payment names the requesting user fulfillment publishes as.
	paid := newPricedVotingProcess(t, token, orgAddress)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}, "processes", paid, "checkout")
	paidCents := checkout.AmountCents
	won, err := testDB.MarkProcessPaymentPaid(draftOID(paid), checkout.SessionID,
		db.ProcessCharge{PaymentIntentID: "pi_test_quote_only"})
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	vp, census := resize(paid, 50_000)
	c.Assert(testAPI.censusGrowthPaymentError(censusGrowth{vp: vp, census: census, by: 1}),
		qt.ErrorIs, errors.ErrQuoteRequired)
	c.Assert(testAPI.censusGrowthPaymentError(censusGrowth{vp: vp, census: census}),
		qt.ErrorIs, errors.ErrPaymentRequired) // 50 000 still has a price, above what was paid
	resize(paid, 50_001)
	c.Assert(testAPI.publishPaidProcess(vp.ID), qt.IsNil)
	got, err := testDB.VotingProcess(vp.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(got.Published, qt.IsFalse)
	c.Assert(got.Publishing.IsZero(), qt.IsTrue) // never reached startProcessPublish: the gate refused it

	// a managed organization's publish debit does not read the quote-only zero total as free
	managedOrg := &db.Organization{Address: orgAddress, ManagedBy: common.Address{1}}
	c.Assert(testAPI.debitManagedProcessWallet(vp, managedOrg), qt.ErrorIs, errors.ErrQuoteRequired)
	// a wallet charge of a quote-only price is refused even when "already covered"
	_, err = testAPI.chargeManagedProcessWallet(managedProcessCharge{
		vp: vp, org: managedOrg, quote: pricing.Quote{QuoteRequired: true},
	})
	c.Assert(err, qt.ErrorIs, errors.ErrQuoteRequired)
	payment, err := testDB.ProcessPayment(vp.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.AmountCents, qt.Equals, paidCents)
}
