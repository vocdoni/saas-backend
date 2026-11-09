package api

import (
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/errors"
	"go.mongodb.org/mongo-driver/v2/bson"
)

// getWalletInvoice downloads the invoice of a wallet history row as token and returns the response.
func getWalletInvoice(t *testing.T, token string, org common.Address, entryID string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://%s:%d/organizations/%s/wallet/history/%s/invoice",
		testHost, testPort, org.String(), entryID), nil)
	qt.Assert(t, err, qt.IsNil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	qt.Assert(t, err, qt.IsNil)
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// topUpEntryID credits a top-up to the wallet, as a verified checkout would, and returns its row id.
func topUpEntryID(t *testing.T, org common.Address, key string) string {
	t.Helper()
	qt.Assert(t, testDB.CreditWallet(db.WalletCredit{
		OrgAddress: org, AmountCents: 10_000, IdempotencyKey: key, PaymentIntentID: "pi_" + key, GrossCents: 12_100,
	}), qt.IsNil)
	entry, err := testDB.WalletEntry(org, key)
	qt.Assert(t, err, qt.IsNil)
	return entry.ID.Hex()
}

// TestWalletInvoiceDownload: a wallet reader downloads the invoice PDF of a top-up row by its id,
// named after the invoice number; the row is resolved by the key it was credited under. A row
// that is not a top-up, another wallet's row, an unknown id and a top-up without an invoice are
// all 404, a malformed id is 400, and a stranger reads nothing.
func TestWalletInvoiceDownload(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	fake.invoicePDF = []byte("%PDF-1.4 fake invoice")
	token := testCreateUser(t, "walletinvoice123")
	orgAddress := testCreateOrganization(t, token)
	otherOrg := testCreateOrganization(t, token) // another wallet of the same admin
	entryID := topUpEntryID(t, orgAddress, "cs_invoice_1")
	otherEntryID := topUpEntryID(t, otherOrg, "cs_invoice_other")

	// the row id is what GET /wallet lists
	wallet := requestAndParse[apicommon.WalletResponse](t, http.MethodGet, token, nil,
		"organizations", orgAddress.String(), "wallet")
	c.Assert(wallet.History, qt.HasLen, 1)
	c.Assert(wallet.History[0].ID.Hex(), qt.Equals, entryID)

	resp := getWalletInvoice(t, token, orgAddress, entryID)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(resp.Header.Get("Content-Type"), qt.Equals, "application/pdf")
	c.Assert(resp.Header.Get("Content-Disposition"), qt.Equals, `attachment; filename="INV-0001.pdf"`)
	body, err := io.ReadAll(resp.Body)
	c.Assert(err, qt.IsNil)
	c.Assert(string(body), qt.Equals, "%PDF-1.4 fake invoice")
	c.Assert(fake.invoiceKeys, qt.DeepEquals, []string{"cs_invoice_1"})

	// a manager reads it too
	managerToken := testAddOrgManager(t, orgAddress)
	c.Assert(getWalletInvoice(t, managerToken, orgAddress, entryID).StatusCode, qt.Equals, http.StatusOK)

	// not a top-up: a payment row has no invoice
	c.Assert(testDB.DebitWalletForProcess(db.WalletDebit{
		OrgAddress: orgAddress, ProcessID: bson.NewObjectID(), AmountCents: 1_000, PriceCents: 1_000,
	}), qt.IsNil)
	wallet = requestAndParse[apicommon.WalletResponse](t, http.MethodGet, token, nil,
		"organizations", orgAddress.String(), "wallet")
	c.Assert(wallet.History[0].Kind, qt.Equals, db.WalletEntryPayment)
	for _, id := range []string{wallet.History[0].ID.Hex(), otherEntryID, bson.NewObjectID().Hex(), bson.NilObjectID.Hex()} {
		requestAndAssertError(errors.ErrWalletInvoiceNotFound, t, http.MethodGet, token, nil,
			"organizations", orgAddress.String(), "wallet", "history", id, "invoice")
	}
	requestAndAssertError(errors.ErrMalformedURLParam, t, http.MethodGet, token, nil,
		"organizations", orgAddress.String(), "wallet", "history", "not-an-id", "invoice")
	// a top-up whose invoice Stripe has not attached yet: 404 too, told to retry in a moment
	fake.invoicePDF = nil
	notYet := requestAndExpectError(t, http.MethodGet, token, nil,
		"organizations", orgAddress.String(), "wallet", "history", entryID, "invoice")
	c.Assert(notYet.Code, qt.Equals, errors.ErrWalletInvoiceNotFound.Code)
	c.Assert(notYet.Error(), qt.Contains, "not available yet")
	// nothing reached Stripe for the rows that are not invoiced top-ups of this wallet
	c.Assert(fake.invoiceKeys, qt.DeepEquals, []string{"cs_invoice_1", "cs_invoice_1", "cs_invoice_1"})

	stranger := testCreateUser(t, "walletinvoicestr")
	requestAndAssertCode(http.StatusUnauthorized, t, http.MethodGet, stranger, nil,
		"organizations", orgAddress.String(), "wallet", "history", entryID, "invoice")
}

// TestWalletInvoiceAuthorization: the download is authorized exactly like the wallet read — a
// managed organization has none (403, 40181), and a quota:read API key downloads from its own
// organization's wallet only (403, 40157, on any other).
func TestWalletInvoiceAuthorization(t *testing.T) {
	c := qt.New(t)
	fake := installFakePaymentGW(t)
	fake.invoicePDF = []byte("%PDF-1.4 fake invoice")
	token := testCreateUser(t, "walletinvoiceauth")
	integratorAddr, managedAddr := newIntegratorWithManagedOrg(t, token)
	otherOrg := testCreateOrganization(t, token)
	entryID := topUpEntryID(t, integratorAddr, "cs_invoice_integrator")
	otherEntryID := topUpEntryID(t, otherOrg, "cs_invoice_other_org")

	requestAndAssertError(errors.ErrManagedOrgHasNoWallet, t, http.MethodGet, token, nil,
		"organizations", managedAddr.String(), "wallet", "history", entryID, "invoice")

	expires := time.Now().Add(time.Hour)
	key := requestAndParse[apicommon.CreateAPIKeyResponse](t, http.MethodPost, token,
		&apicommon.CreateAPIKeyRequest{Label: "invoice", Scopes: []string{ScopeQuotaRead}, ExpiresAt: &expires},
		"integrator", "organizations", integratorAddr.String(), "apikeys").Secret
	resp := getWalletInvoice(t, key, integratorAddr, entryID)
	c.Assert(resp.StatusCode, qt.Equals, http.StatusOK)
	c.Assert(resp.Header.Get("Content-Type"), qt.Equals, "application/pdf")
	requestAndAssertError(errors.ErrAPIKeyNotAllowed, t, http.MethodGet, key, nil,
		"organizations", otherOrg.String(), "wallet", "history", otherEntryID, "invoice")
	c.Assert(fake.invoiceKeys, qt.DeepEquals, []string{"cs_invoice_integrator"})
}
