// Package pricing computes the pay-per-process price of a voting process from its
// eligible voters and selected add-ons, per the "VocdoniApp new pricing model" spec.
// Prices are EUR cents, VAT excluded (Stripe Tax adds VAT at checkout). The package is
// pure: callers derive the input from the draft (census size, census 2FA fields,
// process add-ons) and act on the returned quote.
package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
)

// PayerType selects the pricing policy. Managed-organization processes are paid by
// their integrator and may eventually be priced differently; today both policies are
// identical, but the seam keeps the payment flow unaware of any future divergence.
type PayerType int

// Payer types.
const (
	PayerStandard PayerType = iota
	PayerIntegrator
)

// Quote line kinds, used to build checkout line items and to identify which lines a
// legacy plan credit zeroes out.
const (
	LineBase       = "base"
	LineEmailTwoFA = "emailTwoFA"
	LineSMSTwoFA   = "smsTwoFA"
	LineSignedCert = "signedCertificate"
	LineCustomURL  = "customUrl"
	LineBranding   = "branding"
)

// Add-on prices in EUR cents and the self-service thresholds, from the spec.
const (
	emailTwoFACentsPerVoter = 1     // €0.01 per eligible voter
	smsTwoFACentsPerVoter   = 3     // €0.03 per eligible voter
	signedCertCents         = 4900  // €49 per process
	customURLCents          = 8900  // €89 per process
	brandingCents           = 14900 // €149 once per organization

	// FreeCensusSize is the largest census that prices to a free base. It matches
	// db.TestMaxCensusSize, which already exempts such processes from counters.
	FreeCensusSize = 10

	quoteRecommendedAbove = 15_000 // recommend a custom quote above this census size
	quoteRequiredAbove    = 50_000 // self-service checkout unavailable above this
)

// QuoteInput is everything the price depends on. EmailTwoFA/SMSTwoFA derive from the
// census twoFaFields; SignedCert/CustomURL/Branding are process add-on selections.
// Branding must be passed false when the organization has already paid it once.
type QuoteInput struct {
	CensusSize int
	EmailTwoFA bool
	SMSTwoFA   bool
	SignedCert bool
	CustomURL  bool
	Branding   bool
	Payer      PayerType
}

// QuoteLine is one priced component of a quote.
type QuoteLine struct {
	Kind        string `json:"kind"`
	Description string `json:"description"`
	AmountCents int64  `json:"amountCents"`
}

// Quote is the computed price breakdown of a voting process, VAT excluded.
type Quote struct {
	Lines            []QuoteLine `json:"lines"`
	TotalCents       int64       `json:"totalCents"`
	QuoteRecommended bool        `json:"quoteRecommended"` // census above 15 000: suggest a custom quote
	QuoteRequired    bool        `json:"quoteRequired"`    // census above 50 000: self-service checkout blocked
}

// Compute prices a voting process. A total of zero cents means the process is free and
// publishes without any checkout.
func Compute(in QuoteInput) (Quote, error) {
	if in.CensusSize < 1 {
		return Quote{}, fmt.Errorf("census size must be at least 1, got %d", in.CensusSize)
	}
	quote := Quote{
		QuoteRecommended: in.CensusSize > quoteRecommendedAbove,
		QuoteRequired:    in.CensusSize > quoteRequiredAbove,
	}
	addLine := func(kind, description string, cents int64) {
		quote.Lines = append(quote.Lines, QuoteLine{Kind: kind, Description: description, AmountCents: cents})
		quote.TotalCents += cents
	}
	basePrice := basePriceFor(in.Payer)
	addLine(LineBase, fmt.Sprintf("voting process (%d eligible voters)", in.CensusSize), basePrice(in.CensusSize))
	// Per-voter add-ons are free inside the free tier: at most 10 voters they price
	// below any chargeable amount (Stripe's minimum charge), and such test-sized
	// processes are meant to be entirely free. Flat add-ons stay billable at any size.
	if in.CensusSize > FreeCensusSize {
		if in.EmailTwoFA {
			addLine(LineEmailTwoFA, "email 2FA", int64(in.CensusSize)*emailTwoFACentsPerVoter)
		}
		if in.SMSTwoFA {
			addLine(LineSMSTwoFA, "SMS 2FA", int64(in.CensusSize)*smsTwoFACentsPerVoter)
		}
	}
	if in.SignedCert {
		addLine(LineSignedCert, "signed results certificate", signedCertCents)
	}
	if in.CustomURL {
		addLine(LineCustomURL, "custom URL", customURLCents)
	}
	if in.Branding {
		addLine(LineBranding, "branding and white label", brandingCents)
	}
	return quote, nil
}

// QuoteHash fingerprints the priced inputs and total so an open checkout session can be
// matched against the draft's current state: a draft edit that changes any priced input
// changes the hash, marking the session obsolete.
func QuoteHash(in QuoteInput, totalCents int64) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d|%t|%t|%t|%t|%t|%d|%d",
		in.CensusSize, in.EmailTwoFA, in.SMSTwoFA, in.SignedCert, in.CustomURL, in.Branding, in.Payer, totalCents))
	return hex.EncodeToString(sum[:])
}

// basePriceFor selects the base-price bracket function for a payer type. Both types
// share the standard formula today; integrator pricing can diverge here without
// touching the payment flow.
func basePriceFor(PayerType) func(censusSize int) int64 {
	return standardBasePriceCents
}

// standardBasePriceCents implements the spec's base-price brackets, rounding to the
// nearest €5 before add-ons:
//
//	1–10          free
//	11–800        R5(1.5·v^0.824)
//	801–5000      R5(33·v^0.36)
//	5001–15000    R5(0.142·v)
//	>15000        R5(2130 + 0.11·(v−15000))
func standardBasePriceCents(censusSize int) int64 {
	v := float64(censusSize)
	switch {
	case censusSize <= FreeCensusSize:
		return 0
	case censusSize <= 800:
		return round5Cents(1.5 * math.Pow(v, 0.824))
	case censusSize <= 5000:
		return round5Cents(33 * math.Pow(v, 0.36))
	case censusSize <= 15_000:
		return round5Cents(0.142 * v)
	default:
		return round5Cents(2130 + 0.11*(v-15_000))
	}
}

// round5Cents rounds a euro amount to the nearest €5 and returns it in cents.
func round5Cents(euros float64) int64 {
	return int64(math.Round(euros/5)) * 500
}
