// Package pricing computes the pay-per-process price of a voting process from its census
// size and add-ons. Prices are EUR cents, VAT excluded (Stripe Tax adds it at checkout).
package pricing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// Cents is an amount of money in euro cents: the unit Stripe, the database and the API use. It
// has no String method on purpose: QuoteHash formats it with %d, which a Stringer would change.
type Cents int64

// Units of Cents.
const (
	Cent Cents = 1
	Euro       = 100 * Cent
)

// PayerType selects the pricing policy. The base price is the same for both; integrators also
// pay email 2FA, which is free for standard organizations.
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
	// LineMinimumCharge raises a priced total below MinimumChargeCents to it.
	LineMinimumCharge = "minimumCharge"
)

// Add-on prices and the self-service thresholds, from the spec.
const (
	// email 2FA is free for standard organizations; integrators pay it per eligible voter
	integratorEmailTwoFAPerVoter = 1 * Cent
	smsTwoFAPerVoter             = 5 * Cent  // per eligible voter
	signedCertPrice              = 39 * Euro // per process
	// BrandingCents and CustomURLCents are the once-per-organization add-ons, exported for
	// refunds, which withhold them when another process relies on them.
	BrandingCents  = 149 * Euro
	CustomURLCents = 99 * Euro

	// MinimumChargeCents is the smallest amount ever charged: a priced total between zero and it
	// is raised to it (Stripe cannot charge less than €0.50).
	MinimumChargeCents = 1 * Euro

	// FreeCensusSize is the largest census with a free base price (also db.TestMaxCensusSize).
	FreeCensusSize = 25

	// MaxCensusSize bounds the census the public /pricing calculator accepts; far above any
	// real electorate.
	MaxCensusSize = 10_000_000

	quoteRecommendedAbove = 15_000 // recommend a custom quote above this census size
	quoteRequiredAbove    = 50_000 // quote-only above this: no price, no self-service payment
)

// QuoteInput is everything the price depends on. Branding and CustomURL must be false once the
// organization has paid them.
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
	Kind        string `json:"kind" bson:"kind"`
	Description string `json:"description" bson:"description"`
	AmountCents Cents  `json:"amountCents" bson:"amountCents"`
}

// Quote is the computed price breakdown of a voting process, VAT excluded.
type Quote struct {
	Lines            []QuoteLine `json:"lines" bson:"lines"`
	TotalCents       Cents       `json:"totalCents" bson:"totalCents"`
	QuoteRecommended bool        `json:"quoteRecommended" bson:"quoteRecommended"` // census above 15 000: suggest a custom quote
	// QuoteRequired: census above 50 000. The quote has no price (no lines, a zero total that
	// is never "free") and nothing may be paid or published through self-service.
	QuoteRequired bool `json:"quoteRequired" bson:"quoteRequired"`
}

// Shortfall is what a wallet lacks to pay a quote: the quote, the balance it was measured
// against, and the shortfall. PaidCents and CensusSize are set for a process that already paid part
// of its price (its census grew since): it buys the difference with the census checkout for
// CensusSize, not with a process checkout.
type Shortfall struct {
	Quote `bson:",inline"`
	// BalanceCents is the wallet balance the shortfall was measured against; it may be negative.
	BalanceCents   Cents `json:"balanceCents" bson:"balanceCents"`
	ShortfallCents Cents `json:"shortfallCents" bson:"shortfallCents"`
	// PaidCents and CensusSize are set for a process that already paid part of its price and buys
	// census headroom (POST /processes/{processId}/census/checkout for CensusSize). CensusSize (at
	// least 1 then) is what tells it apart: PaidCents is omitted when the process paid 0. The
	// headroom cost is TotalCents - PaidCents.
	PaidCents  Cents `json:"paidCents,omitempty" bson:"paidCents,omitempty"`
	CensusSize int64 `json:"censusSize,omitempty" bson:"censusSize,omitempty"`
}

// Breakdown explains the amount a wallet top-up checkout charged for a process: the shortfall
// (the quote and the wallet balance it was measured against) raised to the minimum charge. It is
// written to the checkout session's metadata and shown back on the checkout status and the
// wallet history. Display data only: no amount, size or eligibility decision is ever taken from it.
type Breakdown struct {
	Shortfall `bson:",inline"`
	Currency  string `json:"currency" bson:"currency"`
	// ChargeCents is what the session charges, net of VAT: max(ShortfallCents, MinimumChargeCents).
	ChargeCents Cents `json:"chargeCents" bson:"chargeCents"`
}

// Compute prices a voting process. A zero total means it publishes without checkout, unless
// QuoteRequired is set: then there is no price at all. Any other total is at least
// MinimumChargeCents.
func Compute(in QuoteInput) (Quote, error) {
	if in.CensusSize < 1 || in.CensusSize > MaxCensusSize {
		return Quote{}, fmt.Errorf("census size must be between 1 and %d, got %d", MaxCensusSize, in.CensusSize)
	}
	quote := Quote{QuoteRecommended: in.CensusSize > quoteRecommendedAbove}
	if in.CensusSize > quoteRequiredAbove {
		quote.Lines = []QuoteLine{}
		quote.QuoteRequired = true
		return quote, nil
	}
	addLine := func(kind, description string, cents Cents) {
		quote.Lines = append(quote.Lines, QuoteLine{Kind: kind, Description: description, AmountCents: cents})
		quote.TotalCents += cents
	}
	addLine(LineBase, fmt.Sprintf("voting process (%d eligible voters)", in.CensusSize), basePriceCents(in.CensusSize))
	// add-ons are billed at any census size, free base price included
	if in.EmailTwoFA && in.Payer == PayerIntegrator {
		addLine(LineEmailTwoFA, "email 2FA", Cents(in.CensusSize)*integratorEmailTwoFAPerVoter)
	}
	if in.SMSTwoFA {
		addLine(LineSMSTwoFA, "SMS 2FA", Cents(in.CensusSize)*smsTwoFAPerVoter)
	}
	if in.SignedCert {
		addLine(LineSignedCert, "signed results certificate", signedCertPrice)
	}
	if in.CustomURL {
		addLine(LineCustomURL, "custom URL", CustomURLCents)
	}
	if in.Branding {
		addLine(LineBranding, "branding and white label", BrandingCents)
	}
	if quote.TotalCents > 0 && quote.TotalCents < MinimumChargeCents {
		addLine(LineMinimumCharge, "minimum charge", MinimumChargeCents-quote.TotalCents)
	}
	return quote, nil
}

// QuoteHash fingerprints the priced inputs and total; a changed hash marks an open
// checkout session obsolete.
func QuoteHash(in QuoteInput, totalCents Cents) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%d|%t|%t|%t|%t|%t|%d|%d",
		in.CensusSize, in.EmailTwoFA, in.SMSTwoFA, in.SignedCert, in.CustomURL, in.Branding, in.Payer, totalCents))
	return hex.EncodeToString(sum[:])
}

// basePriceCents prices a census on graduated bands: each rate applies only to the voters inside
// its band. The rates are exact in hundredths of a cent (€0.125 = 1250), so the bands are summed
// exactly and rounded half-up to the cent once. Up to FreeCensusSize voters it is free; above it
// every voter is priced, from the first band. Compute never calls it above 50 000 (quote-only).
//
//	1–100          €0.660
//	101–500        €0.430
//	501–1 000      €0.240
//	1 001–5 000    €0.125
//	5 001–15 000   €0.100
//	15 001–50 000  €0.090
func basePriceCents(censusSize int) Cents {
	if censusSize <= FreeCensusSize {
		return 0
	}
	// each case prices the voters above its band's floor, then hands the floor to the band below
	voters, hundredths := int64(censusSize), int64(0)
	switch {
	case voters > 15_000:
		hundredths += (voters - 15_000) * 900
		voters = 15_000
		fallthrough
	case voters > 5_000:
		hundredths += (voters - 5_000) * 1_000
		voters = 5_000
		fallthrough
	case voters > 1_000:
		hundredths += (voters - 1_000) * 1_250
		voters = 1_000
		fallthrough
	case voters > 500:
		hundredths += (voters - 500) * 2_400
		voters = 500
		fallthrough
	case voters > 100:
		hundredths += (voters - 100) * 4_300
		voters = 100
		fallthrough
	default:
		hundredths += voters * 6_600
	}
	return Cents((hundredths + 50) / 100)
}
