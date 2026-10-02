// Package pricing computes the pay-per-process price of a voting process from its census
// size and add-ons. Prices are EUR cents, VAT excluded (Stripe Tax adds it at checkout).
package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
)

// PayerType selects the pricing policy. Integrator pricing is identical today but may diverge.
type PayerType int

// Payer types.
const (
	PayerStandard PayerType = iota
	PayerIntegrator
)

// Quote line kinds, used as checkout line items.
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
	emailTwoFACentsPerVoter = 1    // €0.01 per eligible voter
	smsTwoFACentsPerVoter   = 3    // €0.03 per eligible voter
	signedCertCents         = 4900 // €49 per process
	customURLCents          = 8900 // €89 per process
	// BrandingCents is exported for refunds, which withhold it when the branding stays in use.
	BrandingCents = 14900 // €149 once per organization

	// FreeCensusSize is the largest census with a free base price (also db.TestMaxCensusSize).
	FreeCensusSize = 10

	// MaxCensusSize keeps the formula from overflowing int64 (reachable from the public
	// /pricing calculator); far above any real electorate.
	MaxCensusSize = 10_000_000

	quoteRecommendedAbove = 15_000 // recommend a custom quote above this census size
	quoteRequiredAbove    = 50_000 // self-service checkout unavailable above this
)

// QuoteInput is everything the price depends on. Branding must be false once the
// organization has paid it.
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

// Compute prices a voting process. A zero total means it publishes without checkout.
func Compute(in QuoteInput) (Quote, error) {
	if in.CensusSize < 1 || in.CensusSize > MaxCensusSize {
		return Quote{}, fmt.Errorf("census size must be between 1 and %d, got %d", MaxCensusSize, in.CensusSize)
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
	// per-voter add-ons are free in the free tier (below Stripe's minimum charge anyway);
	// flat add-ons are billed at any size
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
		addLine(LineBranding, "branding and white label", BrandingCents)
	}
	return quote, nil
}

// QuoteHash fingerprints the priced inputs and total; a changed hash marks an open
// checkout session obsolete.
func QuoteHash(in QuoteInput, totalCents int64) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d|%t|%t|%t|%t|%t|%d|%d",
		in.CensusSize, in.EmailTwoFA, in.SMSTwoFA, in.SignedCert, in.CustomURL, in.Branding, in.Payer, totalCents))
	return hex.EncodeToString(sum[:])
}

// basePriceFor selects the base-price formula for a payer type.
func basePriceFor(PayerType) func(censusSize int) int64 {
	return standardBasePriceCents
}

// standardBasePriceCents implements the base-price brackets, rounded to the nearest €5:
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
