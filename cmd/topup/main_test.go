package main

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/pricing"
	"github.com/vocdoni/saas-backend/stripe/topup"
)

// TestValidate: the flags alone decide the amount bounds (€1 to €100 000 net), that exactly one
// mode is given, and that the organization, description and database are named; the request
// carries the trimmed description and the chosen mode.
func TestValidate(t *testing.T) {
	c := qt.New(t)
	valid := options{
		mongoURL: "mongodb://localhost", org: common.Address{0x01}.String(), amountCents: 50_000,
		description: " wallet top-up ", paidOutOfBand: true,
	}
	req, err := validate(&valid)
	c.Assert(err, qt.IsNil)
	c.Assert(req, qt.DeepEquals, topup.Request{
		Org: common.Address{0x01}, AmountCents: 50_000, Description: "wallet top-up", Mode: topup.PaidOutOfBand,
	})
	for name, mutate := range map[string]func(o *options){
		"no database":       func(o *options) { o.mongoURL = "" },
		"not an address":    func(o *options) { o.org = "0x1234" },
		"below the minimum": func(o *options) { o.amountCents = int64(pricing.MinimumChargeCents) - 1 },
		"above the maximum": func(o *options) { o.amountCents = int64(topup.MaxAmountCents) + 1 },
		"no description":    func(o *options) { o.description = " " },
		"long description":  func(o *options) { o.description = strings.Repeat("é", topup.MaxDescriptionChars+1) },
		"both modes":        func(o *options) { o.send = true },
		"neither mode":      func(o *options) { o.paidOutOfBand = false },
	} {
		c.Run(name, func(c *qt.C) {
			opts := valid
			mutate(&opts)
			_, err := validate(&opts)
			c.Assert(err, qt.IsNotNil)
		})
	}
	sent := valid
	sent.paidOutOfBand, sent.send, sent.customer, sent.allowDup = false, true, "cus_1", true
	req, err = validate(&sent)
	c.Assert(err, qt.IsNil)
	c.Assert(req.Mode, qt.Equals, topup.Send)
	c.Assert(req.CustomerID, qt.Equals, "cus_1")
	c.Assert(req.AllowDuplicate, qt.IsTrue)
}
