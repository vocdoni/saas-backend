package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
	"github.com/vocdoni/saas-backend/db"
	"github.com/vocdoni/saas-backend/test"
)

// goldenInvoice is the invoice the fake Stripe answers for every invoice write: €500 net, €605
// with VAT, in the status the last call left it.
func goldenInvoice(status string) string {
	return `{"id":"in_golden","object":"invoice","number":"VOC-0042","status":"` + status + `",` +
		`"total_excluding_tax":50000,"total":60500,"hosted_invoice_url":"https://invoice.stripe.com/i/golden"}`
}

// TestRunGolden pins cmd/topup's printed output, byte for byte, for a dry run and both modes,
// against a fake Stripe and a real MongoDB: the refactor onto the topup package must not change it.
func TestRunGolden(t *testing.T) {
	c := qt.New(t)
	ctx := context.Background()
	container, err := test.StartMongoContainer(ctx)
	c.Assert(err, qt.IsNil)
	c.Cleanup(func() { c.Assert(container.Terminate(ctx), qt.IsNil) })
	uri, err := container.ConnectionString(ctx)
	c.Assert(err, qt.IsNil)
	database := test.RandomDatabaseName()
	ms, err := db.New(uri, database)
	c.Assert(err, qt.IsNil)
	c.Cleanup(ms.Close)
	org := common.HexToAddress("0x000000000000000000000000000000000000bEEF")
	c.Assert(ms.SetOrganization(&db.Organization{Address: org, Type: db.CompanyType}), qt.IsNil)

	var writes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path != "/v1/customers/search" {
			writes = append(writes, r.URL.Path)
		}
		switch {
		case r.URL.Path == "/v1/customers/search":
			_, _ = w.Write([]byte(`{"object":"search_result","has_more":false,"data":[{"id":"cus_golden",` +
				`"object":"customer","name":"Golden SL","email":"billing@golden.test","address":{"line1":"Carrer 1",` +
				`"postal_code":"08001","city":"Barcelona","country":"ES"},"metadata":{"address":"` + org.Hex() + `"}}]}`))
		case r.URL.Path == "/v1/invoices" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"object":"list","url":"/v1/invoices","has_more":false,"data":[]}`))
		case r.URL.Path == "/v1/invoices", r.URL.Path == "/v1/invoices/in_golden/add_lines":
			_, _ = w.Write([]byte(goldenInvoice("draft")))
		case r.URL.Path == "/v1/invoices/in_golden/finalize", r.URL.Path == "/v1/invoices/in_golden/send":
			_, _ = w.Write([]byte(goldenInvoice("open")))
		case r.URL.Path == "/v1/invoices/in_golden/pay":
			_, _ = w.Write([]byte(goldenInvoice("paid")))
		default:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"resource_missing"}}`))
		}
	}))
	c.Cleanup(srv.Close)
	previous := stripeapi.GetBackend(stripeapi.APIBackend)
	stripeapi.SetBackend(stripeapi.APIBackend, stripeapi.GetBackendWithConfig(stripeapi.APIBackend,
		&stripeapi.BackendConfig{URL: stripeapi.String(srv.URL), MaxNetworkRetries: stripeapi.Int64(0)}))
	c.Cleanup(func() { stripeapi.SetBackend(stripeapi.APIBackend, previous) })
	t.Setenv("VOCDONI_STRIPEAPISECRET", "sk_test_stub")

	header := "organization " + org.Hex() + ", customer cus_golden <billing@golden.test>\n" +
		"  Carrer 1, 08001, Barcelona, ES\n" +
		"invoice: 50000 eur cents net, \"wallet top-up\", send_invoice due in 30 days, automatic tax\n"
	issued := func(status string) string {
		return header + "invoice in_golden (VOC-0042) " + status + ": 50000 eur cents net, 60500 with tax\n" +
			"  https://invoice.stripe.com/i/golden\n" +
			"the wallet is credited by the invoice.paid webhook once the invoice is paid\n"
	}
	for _, tc := range []struct {
		name   string
		opts   options
		want   string
		writes []string
	}{
		{"dry run", options{paidOutOfBand: true, dryRun: true}, header + "dry run: nothing was created\n", nil},
		{"paid out of band", options{paidOutOfBand: true}, issued("paid"), []string{
			"/v1/invoices", "/v1/invoices/in_golden/add_lines", "/v1/invoices/in_golden/finalize", "/v1/invoices/in_golden/pay",
		}},
		{"send", options{send: true}, issued("open"), []string{
			"/v1/invoices", "/v1/invoices/in_golden/add_lines", "/v1/invoices/in_golden/finalize", "/v1/invoices/in_golden/send",
		}},
	} {
		c.Run(tc.name, func(c *qt.C) {
			writes = nil
			opts := tc.opts
			opts.mongoURL, opts.database, opts.org = uri, database, strings.ToLower(org.Hex())
			opts.amountCents, opts.description = 50_000, "  wallet top-up "
			var out bytes.Buffer
			c.Assert(run(&opts, &out), qt.IsNil)
			c.Assert(out.String(), qt.Equals, tc.want)
			c.Assert(writes, qt.DeepEquals, tc.writes)
		})
	}
}
