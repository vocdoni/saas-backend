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

// Cents is an amount of money in euro cents: the unit Stripe, the database and the API use. It
// has no String method on purpose: QuoteHash formats it with %d, which a Stringer would change.
type Cents int64

// Units of Cents.
const (
	Cent Cents = 1
	Euro       = 100 * Cent
)

// Round rounds c half-up to the nearest multiple of m.
func (c Cents) Round(m Cents) Cents { return (c + m/2) / m * m }

// floorEuros truncates a floating-point euro amount to whole cents. Truncating never crosses a
// whole-cent rounding threshold, so a later Round gives the exact result, where rounding to cents
// first would not: €477.499 (1673 voters) must round to €475, not to €477.50 and then €480.
func floorEuros(euros float64) Cents { return Cents(math.Floor(euros * float64(Euro))) }

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

// Add-on prices and the self-service thresholds, from the spec.
const (
	emailTwoFAPerVoter = 1 * Cent  // per eligible voter
	smsTwoFAPerVoter   = 3 * Cent  // per eligible voter
	signedCertPrice    = 49 * Euro // per process
	customURLPrice     = 89 * Euro // per process
	// BrandingCents is exported because a refund withholds it when the organization kept
	// using the branding the refunded process paid for.
	BrandingCents = 149 * Euro // once per organization

	// FreeCensusSize is the largest census that prices to a free base. db.TestMaxCensusSize,
	// which exempts such processes from counters, is defined as this constant.
	FreeCensusSize = 10

	// MaxCensusSize bounds what may be priced at all. The formula multiplies the census
	// size by per-voter cents and by 500 when rounding, so an unbounded size overflows
	// int64 into a negative total — reachable from the unauthenticated /pricing
	// calculator. Far above any real electorate, and above the 50 000 quote-only
	// threshold, so it only ever rejects nonsense.
	MaxCensusSize = 10_000_000

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
	AmountCents Cents  `json:"amountCents"`
}

// Quote is the computed price breakdown of a voting process, VAT excluded.
type Quote struct {
	Lines            []QuoteLine `json:"lines"`
	TotalCents       Cents       `json:"totalCents"`
	QuoteRecommended bool        `json:"quoteRecommended"` // census above 15 000: suggest a custom quote
	QuoteRequired    bool        `json:"quoteRequired"`    // census above 50 000: self-service checkout blocked
}

// Compute prices a voting process. A total of zero cents means the process is free and
// publishes without any checkout.
func Compute(in QuoteInput) (Quote, error) {
	if in.CensusSize < 1 || in.CensusSize > MaxCensusSize {
		return Quote{}, fmt.Errorf("census size must be between 1 and %d, got %d", MaxCensusSize, in.CensusSize)
	}
	quote := Quote{
		QuoteRecommended: in.CensusSize > quoteRecommendedAbove,
		QuoteRequired:    in.CensusSize > quoteRequiredAbove,
	}
	addLine := func(kind, description string, cents Cents) {
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
			addLine(LineEmailTwoFA, "email 2FA", Cents(in.CensusSize)*emailTwoFAPerVoter)
		}
		if in.SMSTwoFA {
			addLine(LineSMSTwoFA, "SMS 2FA", Cents(in.CensusSize)*smsTwoFAPerVoter)
		}
	}
	if in.SignedCert {
		addLine(LineSignedCert, "signed results certificate", signedCertPrice)
	}
	if in.CustomURL {
		addLine(LineCustomURL, "custom URL", customURLPrice)
	}
	if in.Branding {
		addLine(LineBranding, "branding and white label", BrandingCents)
	}
	return quote, nil
}

// QuoteHash fingerprints the priced inputs and total so an open checkout session can be
// matched against the draft's current state: a draft edit that changes any priced input
// changes the hash, marking the session obsolete.
func QuoteHash(in QuoteInput, totalCents Cents) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d|%t|%t|%t|%t|%t|%d|%d",
		in.CensusSize, in.EmailTwoFA, in.SMSTwoFA, in.SignedCert, in.CustomURL, in.Branding, in.Payer, totalCents))
	return hex.EncodeToString(sum[:])
}

// basePriceFor selects the base-price bracket function for a payer type. Both types
// share the standard formula today; integrator pricing can diverge here without
// touching the payment flow.
func basePriceFor(PayerType) func(censusSize int) Cents {
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
func standardBasePriceCents(censusSize int) Cents {
	v := float64(censusSize)
	var base Cents
	switch {
	case censusSize <= FreeCensusSize:
		return 0
	case censusSize <= 800:
		base = floorEuros(1.5 * math.Pow(v, 0.824))
	case censusSize <= 5000:
		base = floorEuros(33 * math.Pow(v, 0.36))
	case censusSize <= 15_000:
		// €0.142 per voter, in whole cents: the truncation stays below a cent, so it never
		// crosses a €2.50 midpoint (6250 voters is exactly €887.50, and rounds up)
		base = Cents(censusSize) * 142 / 10
	default:
		base = 2130*Euro + Cents(censusSize-15_000)*11*Cent
	}
	return base.Round(5 * Euro)
}
