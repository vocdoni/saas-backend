package pricing

import (
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestBasePriceBrackets pins the base-price formula to the spec's examples and to the
// values at every bracket boundary. Expected values are EUR cents, hand-derived from
// the spec formulas (R5 = round to the nearest €5).
func TestBasePriceBrackets(t *testing.T) {
	c := qt.New(t)
	cases := []struct {
		censusSize int
		wantCents  int64
	}{
		{1, 0},
		{10, 0},           // spec example: 10 voters are free
		{11, 1_000},       // R5(1.5·11^0.824) = R5(10.82) = €10
		{600, 29_000},     // spec example: €290
		{800, 37_000},     // R5(1.5·800^0.824) = R5(370.0) = €370
		{801, 36_500},     // R5(33·801^0.36) = R5(366.3) = €365
		{900, 38_000},     // spec legacy-credit example: €380 base
		{3000, 59_000},    // spec legacy-credit example: €590
		{5000, 71_000},    // R5(33·5000^0.36) = R5(708.2) = €710
		{5001, 71_000},    // R5(0.142·5001) = R5(710.1) = €710
		{10_000, 142_000}, // spec example: €1420
		{15_000, 213_000}, // R5(0.142·15000) = €2130
		{15_001, 213_000}, // R5(2130 + 0.11·1) = €2130
		{50_000, 598_000}, // R5(2130 + 0.11·35000) = €5980
	}
	for _, tc := range cases {
		c.Assert(standardBasePriceCents(tc.censusSize), qt.Equals, tc.wantCents,
			qt.Commentf("census size %d", tc.censusSize))
	}
}

// TestComputeAddOns pins the add-on billing rules and the spec's worked examples.
func TestComputeAddOns(t *testing.T) {
	c := qt.New(t)
	cases := []struct {
		name      string
		in        QuoteInput
		wantCents int64
	}{
		{"free process, no add-ons", QuoteInput{CensusSize: 10}, 0},
		{"free base still pays add-ons", QuoteInput{CensusSize: 10, SignedCert: true}, 4_900},
		{"10k voters", QuoteInput{CensusSize: 10_000}, 142_000},
		// spec examples for 10 000 voters
		{"10k + email 2FA", QuoteInput{CensusSize: 10_000, EmailTwoFA: true}, 152_000},
		{"10k + email and SMS 2FA", QuoteInput{CensusSize: 10_000, EmailTwoFA: true, SMSTwoFA: true}, 182_000},
		{"10k + email 2FA + certificate", QuoteInput{CensusSize: 10_000, EmailTwoFA: true, SignedCert: true}, 156_900},
		// spec legacy-plans example: 900 voters + SMS 2FA + certificate = €456
		{"900 + SMS 2FA + certificate", QuoteInput{CensusSize: 900, SMSTwoFA: true, SignedCert: true}, 45_600},
		{"custom URL", QuoteInput{CensusSize: 10, CustomURL: true}, 8_900},
		{"branding", QuoteInput{CensusSize: 10, Branding: true}, 14_900},
		{
			"everything at once",
			QuoteInput{CensusSize: 600, EmailTwoFA: true, SMSTwoFA: true, SignedCert: true, CustomURL: true, Branding: true},
			29_000 + 600 + 1_800 + 4_900 + 8_900 + 14_900,
		},
	}
	for _, tc := range cases {
		quote, err := Compute(tc.in)
		c.Assert(err, qt.IsNil, qt.Commentf("%s", tc.name))
		c.Assert(quote.TotalCents, qt.Equals, tc.wantCents, qt.Commentf("%s", tc.name))
		// The lines always sum to the total.
		var sum int64
		for _, line := range quote.Lines {
			sum += line.AmountCents
		}
		c.Assert(sum, qt.Equals, quote.TotalCents, qt.Commentf("%s", tc.name))
	}
}

// TestComputeThresholds checks the self-service quote flags at their boundaries.
func TestComputeThresholds(t *testing.T) {
	c := qt.New(t)
	cases := []struct {
		censusSize    int
		recommended   bool
		quoteRequired bool
	}{
		{15_000, false, false},
		{15_001, true, false},
		{50_000, true, false},
		{50_001, true, true},
	}
	for _, tc := range cases {
		quote, err := Compute(QuoteInput{CensusSize: tc.censusSize})
		c.Assert(err, qt.IsNil)
		c.Assert(quote.QuoteRecommended, qt.Equals, tc.recommended, qt.Commentf("census size %d", tc.censusSize))
		c.Assert(quote.QuoteRequired, qt.Equals, tc.quoteRequired, qt.Commentf("census size %d", tc.censusSize))
	}
}

// TestComputeRejectsInvalidCensus ensures an invalid census never prices as free.
func TestComputeRejectsInvalidCensus(t *testing.T) {
	c := qt.New(t)
	_, err := Compute(QuoteInput{CensusSize: 0})
	c.Assert(err, qt.Not(qt.IsNil))
	_, err = Compute(QuoteInput{CensusSize: -5})
	c.Assert(err, qt.Not(qt.IsNil))
}

// TestPayerPolicyParity asserts the integrator policy is currently identical to the
// standard one — the seam exists for future divergence, not present behavior.
func TestPayerPolicyParity(t *testing.T) {
	c := qt.New(t)
	for _, censusSize := range []int{1, 11, 600, 801, 5001, 15_001, 60_000} {
		standard, err := Compute(QuoteInput{CensusSize: censusSize, Payer: PayerStandard})
		c.Assert(err, qt.IsNil)
		integrator, err := Compute(QuoteInput{CensusSize: censusSize, Payer: PayerIntegrator})
		c.Assert(err, qt.IsNil)
		c.Assert(integrator.TotalCents, qt.Equals, standard.TotalCents, qt.Commentf("census size %d", censusSize))
	}
}

// TestQuoteHash pins that the hash is stable for identical inputs and changes when any
// priced input or the total changes — the staleness signal for open checkout sessions.
func TestQuoteHash(t *testing.T) {
	c := qt.New(t)
	in := QuoteInput{CensusSize: 600, EmailTwoFA: true}
	quote, err := Compute(in)
	c.Assert(err, qt.IsNil)

	base := QuoteHash(in, quote.TotalCents)
	c.Assert(QuoteHash(in, quote.TotalCents), qt.Equals, base)

	changedCensus := in
	changedCensus.CensusSize = 601
	c.Assert(QuoteHash(changedCensus, quote.TotalCents), qt.Not(qt.Equals), base)

	changedAddOn := in
	changedAddOn.SMSTwoFA = true
	c.Assert(QuoteHash(changedAddOn, quote.TotalCents), qt.Not(qt.Equals), base)

	c.Assert(QuoteHash(in, quote.TotalCents+1), qt.Not(qt.Equals), base)
}
