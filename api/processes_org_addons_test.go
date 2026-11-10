package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.vocdoni.io/dvote/util"
)

// newAddOnsVotingProcess creates a priced draft (pricedProcessVoters fresh voters) selecting addOns.
func newAddOnsVotingProcess(t *testing.T, token string, orgAddress common.Address, addOns db.ProcessAddOns) string {
	t.Helper()
	req := newVotingProcessRequest(orgAddress, postNewOrgMembers(t, token, orgAddress, pricedProcessVoters))
	req.AddOns = addOns
	return requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, req, processesCreateEndpoint).ProcessID
}

// TestStripeCheckoutWebhookStampsCustomURL: the custom URL is paid once per organization, like
// branding: fulfillment stamps it, and the organization's next process is no longer charged it.
// The two add-ons are stamped independently.
func TestStripeCheckoutWebhookStampsCustomURL(t *testing.T) {
	c := qt.New(t)
	installStripeWebhookService(t)
	fake := installFakePaymentGW(t)
	token := testCreateUser(t, "customurlpaid123")
	orgAddress := testCreateProvisionedOrganization(t, token)
	members := postOrgMembers(t, token, orgAddress, newOrgMembers(2)...)

	// 2 voters: free base, the custom URL is charged anyway
	reqA := newVotingProcessRequest(orgAddress, memberIDs(members))
	reqA.AddOns = db.ProcessAddOns{CustomURL: true}
	createdA := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, reqA, processesCreateEndpoint)
	_, paid := openProcessTopUp(t, fake, token, createdA.ProcessID)
	c.Assert(paid["amount_subtotal"], qt.Equals, pricing.CustomURLCents)
	code := postSignedStripeEvent(t, testWebhookSecret, "evt_"+util.RandomHex(8), "checkout.session.completed", paid)
	c.Assert(code, qt.Equals, http.StatusOK)
	pollProcessPublished(t, token, createdA.ProcessID)
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.CustomURLPaidAt.IsZero(), qt.IsFalse)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsTrue)

	// process B selects both: only branding is still charged
	reqB := newVotingProcessRequest(orgAddress, memberIDs(members))
	reqB.AddOns = db.ProcessAddOns{CustomURL: true, Branding: true}
	createdB := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, reqB, processesCreateEndpoint)
	priceB := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, token, nil, "processes", createdB.ProcessID, "price")
	c.Assert(priceB.TotalCents, qt.Equals, pricing.BrandingCents)
	for _, line := range priceB.Lines {
		c.Assert(line.Kind, qt.Not(qt.Equals), pricing.LineCustomURL)
	}
}

// TestOrgAddOnClaimsAreIndependent: a checkout holding the custom URL claim holds only the custom
// URL: a sibling selecting both is told the custom URL is held, not branding, which nobody holds,
// and once the claim is released it pays for both.
func TestOrgAddOnClaimsAreIndependent(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "addonclaims12345")
	orgAddress := testCreateOrganization(t, token)
	checkoutReq := &apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}
	const bothCents = pricedProcessCents + pricing.BrandingCents + pricing.CustomURLCents

	first := newAddOnsVotingProcess(t, token, orgAddress, db.ProcessAddOns{CustomURL: true})
	second := newAddOnsVotingProcess(t, token, orgAddress, db.ProcessAddOns{CustomURL: true, Branding: true})
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, token, checkoutReq, "processes", first, "checkout")
	c.Assert(checkout.AmountCents, qt.Equals, pricedProcessCents+pricing.CustomURLCents)

	price := requestAndParse[apicommon.ProcessPriceResponse](
		t, http.MethodGet, token, nil, "processes", second, "price")
	c.Assert(price.TotalCents, qt.Equals, bothCents)
	c.Assert(price.AddOnsHeldBy, qt.DeepEquals, map[string]string{string(db.OrgAddOnCustomURL): first})
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPost, token, checkoutReq,
		"processes", second, "checkout")

	// cancelling the first checkout releases its custom URL claim at once
	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", first, "checkout")
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.CustomURLClaimedBy, qt.Equals, bson.NilObjectID)

	secondCheckout := requestAndParse[apicommon.ProcessCheckoutResponse](
		t, http.MethodPost, token, checkoutReq, "processes", second, "checkout")
	c.Assert(secondCheckout.AmountCents, qt.Equals, bothCents)
	secondOID, err := bson.ObjectIDFromHex(second)
	c.Assert(err, qt.IsNil)
	payment, err := testDB.ProcessPayment(secondOID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.Branding, qt.IsTrue)
	c.Assert(payment.CustomURL, qt.IsTrue)
	org, err = testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.BrandingClaimedBy, qt.Equals, secondOID)
	c.Assert(org.CustomURLClaimedBy, qt.Equals, secondOID)
}

// TestPaidDraftCannotAddCustomURL: like branding, nothing sells the custom URL to a paid draft,
// so an edit turning it on is refused — unless the organization already paid it.
func TestPaidDraftCannotAddCustomURL(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "customurlfixed12")
	orgAddress := testCreateProvisionedOrganization(t, token)

	ids := postNewOrgMembers(t, token, orgAddress, pricedProcessVoters)
	pid := requestAndParse[apicommon.CreateVotingProcessResponse](
		t, http.MethodPost, token, newVotingProcessRequest(orgAddress, ids), processesCreateEndpoint).ProcessID
	c.Assert(markProcessPaid(t, pid), qt.Equals, pricedProcessCents)
	withURL := newVotingProcessRequest(orgAddress, ids)
	withURL.AddOns = db.ProcessAddOns{CustomURL: true}
	requestAndAssertError(errors.ErrPaymentSessionConflict, t, http.MethodPut, token, withURL, "processes", pid)

	stamped, err := testDB.SetOrganizationAddOnPaid(orgAddress, db.OrgAddOnCustomURL, time.Now())
	c.Assert(err, qt.IsNil)
	c.Assert(stamped, qt.IsTrue)
	requestAndAssertCode(http.StatusOK, t, http.MethodPut, token, withURL, "processes", pid)
}

// TestDeletePaidDraftWithholdsEveryReliedOnAddOn: a refund withholds each once-per-organization
// add-on another process relies on, and only those: the relied-on custom URL stays the
// organization's, the branding nobody relies on is released with its money.
func TestDeletePaidDraftWithholdsEveryReliedOnAddOn(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	token := testCreateUser(t, "withholdaddons12")
	orgAddress := testCreateOrganization(t, token)

	paid := newAddOnsVotingProcess(t, token, orgAddress, db.ProcessAddOns{CustomURL: true, Branding: true})
	net := pricedProcessCents + pricing.BrandingCents + pricing.CustomURLCents
	c.Assert(markProcessPaid(t, paid), qt.Equals, net) // stamps both add-ons
	oid, err := bson.ObjectIDFromHex(paid)
	c.Assert(err, qt.IsNil)

	// a sibling that paid without the custom URL because of the stamp, yet carries it
	sibling, err := bson.ObjectIDFromHex(newAddOnsVotingProcess(t, token, orgAddress, db.ProcessAddOns{CustomURL: true}))
	c.Assert(err, qt.IsNil)
	fundWallet(t, orgAddress, pricedProcessCents)
	c.Assert(testDB.DebitWalletForProcess(db.WalletDebit{
		OrgAddress: orgAddress, ProcessID: sibling, ProcessOrgAddress: orgAddress,
		AmountCents: pricedProcessCents, PriceCents: pricedProcessCents,
	}), qt.IsNil)

	requestAndAssertCode(http.StatusOK, t, http.MethodDelete, token, nil, "processes", paid)
	c.Assert(fake.created, qt.HasLen, 0)
	wallet, err := testDB.Wallet(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(wallet.BalanceCents, qt.Equals, net-pricing.CustomURLCents) // credited back, less the URL
	_, err = testDB.ProcessPayment(oid)
	c.Assert(err, qt.ErrorIs, db.ErrNotFound)
	_, entries, err := testDB.WalletLedger(orgAddress, 1, 1)
	c.Assert(err, qt.IsNil)
	c.Assert(entries[0].IdempotencyKey, qt.Equals, db.RefundCreditKey(oid))
	c.Assert(entries[0].WithheldAddOns, qt.DeepEquals, []db.OrgAddOn{db.OrgAddOnCustomURL})
	c.Assert(entries[0].WithheldCents, qt.Equals, pricing.CustomURLCents)
	org, err := testDB.Organization(orgAddress)
	c.Assert(err, qt.IsNil)
	c.Assert(org.CustomURLPaidAt.IsZero(), qt.IsFalse)
	c.Assert(org.BrandingPaidAt.IsZero(), qt.IsTrue)
}

// TestQuoteOnlyRefusedOnEveryPaymentPath: above 50 000 voters a quote has no price and a zero
// total, which no payment path may read as free: publish, the dry-run, paid fulfillment, the
// census growth guard and the wallet charge all refuse it.
func TestQuoteOnlyRefusedOnEveryPaymentPath(t *testing.T) {
	c := qt.New(t)
	installFakePaymentGW(t)
	token := testCreateUser(t, "quoteonlypaths12")
	orgAddress := testCreateProvisionedOrganization(t, token)
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
	// the publish gate (the census size cap answers first over HTTP on this branch)
	draftVP, err := testDB.VotingProcess(draftOID(draft))
	c.Assert(err, qt.IsNil)
	err = testAPI.paymentDueForPublish(draftVP)
	c.Assert(err, qt.ErrorIs, errors.ErrQuoteRequired)
	validation := requestAndParse[apicommon.VotingProcessValidateResponse](
		t, http.MethodGet, token, nil, "processes", draft, "validation")
	c.Assert(validation.Errors, qt.Contains, errors.ErrQuoteRequired.Error())

	// a draft topped up and paid whose census then outgrew the self-service limit: the top-up
	// follow-up leaves the credit for a manual publish instead of failing the webhook forever, and
	// growth checks refuse it. Paid through a top-up, so the payment names the requesting user.
	paid := newPricedVotingProcess(t, token, orgAddress)
	checkout := requestAndParse[apicommon.ProcessCheckoutResponse](t, http.MethodPost, token,
		&apicommon.ProcessCheckoutRequest{ReturnURL: "https://app.example.com/payment"}, "processes", paid, "checkout")
	won, err := testDB.ConsumeProcessPaymentTopUp(draftOID(paid), checkout.SessionID)
	c.Assert(err, qt.IsNil)
	c.Assert(won, qt.IsTrue)
	paidCents := markProcessPaid(t, paid)
	vp, census := resize(paid, 50_000)
	c.Assert(testAPI.censusGrowthPaymentError(censusGrowth{vp: vp, census: census, by: 1}),
		qt.ErrorIs, errors.ErrQuoteRequired)
	c.Assert(testAPI.censusGrowthPaymentError(censusGrowth{vp: vp, census: census}),
		qt.ErrorIs, errors.ErrPaymentRequired) // 50 000 still has a price, above what was paid
	resize(paid, 50_001)
	c.Assert(testAPI.publishPaidProcess(stripe.ProcessTopUp{ProcessID: vp.ID, SessionID: checkout.SessionID}), qt.IsNil)
	got, err := testDB.VotingProcess(vp.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(got.Published, qt.IsFalse)
	c.Assert(got.Publishing.IsZero(), qt.IsTrue) // never reached startProcessPublish: re-pricing refused it

	// a managed organization's publish debit does not read the quote-only zero total as free
	managedOrg := &db.Organization{Address: orgAddress, ManagedBy: common.Address{1}}
	c.Assert(testAPI.debitProcessWallet(vp, managedOrg, nil), qt.ErrorIs, errors.ErrQuoteRequired)
	// a wallet charge of a quote-only price is refused even when "already covered"
	_, err = testAPI.chargeProcessWallet(processCharge{
		vp: vp, org: managedOrg, quote: pricing.Quote{QuoteRequired: true},
	})
	c.Assert(err, qt.ErrorIs, errors.ErrQuoteRequired)
	payment, err := testDB.ProcessPayment(vp.ID)
	c.Assert(err, qt.IsNil)
	c.Assert(payment.AmountCents, qt.Equals, paidCents)
}
