package pricing

import (
	"fmt"
	"testing"

	qt "github.com/frankban/quicktest"
)

// TestBasePriceBands pins the graduated bands at the spec's examples, every band boundary and
// the half-up cent rounding of the €0.125 band (EUR cents).
func TestBasePriceBands(t *testing.T) {
	c := qt.New(t)
	cases := []struct {
		censusSize int
		wantCents  Cents
	}{
		{1, 0},
		{20, 0},           // spec example: free
		{25, 0},           // the free tier ends at 25 voters
		{26, 1_716},       // above it every voter is priced from the first band: 26 × €0.66
		{40, 2_640},       // spec example: €26.40
		{100, 6_600},      // top of band 1
		{101, 6_643},      // + €0.43
		{500, 23_800},     // top of band 2
		{501, 23_824},     // + €0.24
		{600, 26_200},     // spec example: €262
		{900, 33_400},     // 66 + 172 + 400 × 0.24
		{1_000, 35_800},   // top of band 3
		{1_001, 35_813},   // €358.125 rounds half up to €358.13
		{1_003, 35_838},   // €358.375 -> €358.38
		{1_004, 35_850},   // €358.50 exactly
		{1_005, 35_863},   // €358.625 -> €358.63
		{1_200, 38_300},   // spec example: €383
		{3_000, 60_800},   // spec legacy-credit example: €608
		{5_000, 85_800},   // top of band 4
		{5_001, 85_810},   // + €0.10
		{10_000, 135_800}, // spec example: €1,358
		{15_000, 185_800}, // top of band 5
		{15_001, 185_809}, // + €0.09
		{50_000, 500_800}, // top of band 6: €5,008
	}
	for _, tc := range cases {
		c.Run(fmt.Sprintf("%d voters", tc.censusSize), func(c *qt.C) {
			c.Assert(basePriceCents(tc.censusSize), qt.Equals, tc.wantCents)
		})
	}
}

// TestComputeAddOns pins the add-on billing rules and the spec's worked examples.
func TestComputeAddOns(t *testing.T) {
	c := qt.New(t)
	cases := []struct {
		name      string
		in        QuoteInput
		wantCents Cents
	}{
		{"20 voters, no add-ons", QuoteInput{CensusSize: 20}, 0},
		// add-ons are charged on free processes too
		{"20 voters + certificate", QuoteInput{CensusSize: 20, SignedCert: true}, 3_900},
		// a priced amount below €1 is raised to the €1 minimum charge
		{"10 voters + SMS 2FA", QuoteInput{CensusSize: 10, SMSTwoFA: true}, 100},
		{"1 voter + SMS 2FA", QuoteInput{CensusSize: 1, SMSTwoFA: true}, 100},
		// email 2FA is free for standard organizations and billed per voter to integrators
		{"10 voters + email 2FA", QuoteInput{CensusSize: 10, EmailTwoFA: true}, 0},
		{"10 voters + email 2FA, integrator", QuoteInput{CensusSize: 10, EmailTwoFA: true, Payer: PayerIntegrator}, 100},
		{"40 voters", QuoteInput{CensusSize: 40}, 2_640},
		{"600 voters", QuoteInput{CensusSize: 600}, 26_200},
		{"1 200 voters", QuoteInput{CensusSize: 1_200}, 38_300},
		{"3 000 voters", QuoteInput{CensusSize: 3_000}, 60_800},
		{"10k voters", QuoteInput{CensusSize: 10_000}, 135_800},
		{"10k + SMS 2FA", QuoteInput{CensusSize: 10_000, SMSTwoFA: true}, 185_800},
		{"10k + SMS 2FA + certificate", QuoteInput{CensusSize: 10_000, SMSTwoFA: true, SignedCert: true}, 189_700},
		{"10k + email 2FA", QuoteInput{CensusSize: 10_000, EmailTwoFA: true}, 135_800},
		{"10k + email 2FA, integrator", QuoteInput{CensusSize: 10_000, EmailTwoFA: true, Payer: PayerIntegrator}, 145_800},
		{"900 + SMS 2FA + certificate", QuoteInput{CensusSize: 900, SMSTwoFA: true, SignedCert: true}, 41_800},
		{"custom URL", QuoteInput{CensusSize: 10, CustomURL: true}, 9_900},
		{"branding", QuoteInput{CensusSize: 10, Branding: true}, 14_900},
		{
			"everything at once",
			QuoteInput{CensusSize: 600, EmailTwoFA: true, SMSTwoFA: true, SignedCert: true, CustomURL: true, Branding: true},
			26_200 + 3_000 + 3_900 + 9_900 + 14_900, // email 2FA free, SMS €0.05 × 600
		},
	}
	for _, tc := range cases {
		c.Run(tc.name, func(c *qt.C) {
			quote, err := Compute(tc.in)
			c.Assert(err, qt.IsNil)
			c.Assert(quote.TotalCents, qt.Equals, tc.wantCents)
			c.Assert(quote.QuoteRequired, qt.IsFalse)
			// The lines always sum to the total.
			var sum Cents
			for _, line := range quote.Lines {
				sum += line.AmountCents
			}
			c.Assert(sum, qt.Equals, quote.TotalCents)
		})
	}
}

// TestComputeMinimumCharge: a priced total between €0 and €1 is raised to €1 by a
// minimumCharge line, for both payers; a zero total and totals from €1 up are left alone.
func TestComputeMinimumCharge(t *testing.T) {
	c := qt.New(t)
	minimumLine := func(quote Quote) (QuoteLine, bool) {
		for _, line := range quote.Lines {
			if line.Kind == LineMinimumCharge {
				return line, true
			}
		}
		return QuoteLine{}, false
	}
	cases := []struct {
		name        string
		in          QuoteInput
		wantTotal   Cents
		wantMinimum Cents // 0: no minimumCharge line
	}{
		{"10 voters + SMS 2FA", QuoteInput{CensusSize: 10, SMSTwoFA: true}, 100, 50},
		{"1 voter + SMS 2FA", QuoteInput{CensusSize: 1, SMSTwoFA: true}, 100, 95},
		{
			"10 voters + email 2FA, integrator",
			QuoteInput{CensusSize: 10, EmailTwoFA: true, Payer: PayerIntegrator},
			100, 90,
		},
		{
			"10 voters + SMS 2FA, integrator",
			QuoteInput{CensusSize: 10, SMSTwoFA: true, Payer: PayerIntegrator},
			100, 50,
		},
		{"20 voters + SMS 2FA: exactly €1", QuoteInput{CensusSize: 20, SMSTwoFA: true}, 100, 0},
		{"20 voters + certificate", QuoteInput{CensusSize: 20, SignedCert: true}, 3_900, 0},
		{"20 voters, free", QuoteInput{CensusSize: 20}, 0, 0},
		{"10 voters + email 2FA, standard: free", QuoteInput{CensusSize: 10, EmailTwoFA: true}, 0, 0},
	}
	for _, tc := range cases {
		c.Run(tc.name, func(c *qt.C) {
			quote, err := Compute(tc.in)
			c.Assert(err, qt.IsNil)
			c.Assert(quote.TotalCents, qt.Equals, tc.wantTotal)
			line, ok := minimumLine(quote)
			c.Assert(ok, qt.Equals, tc.wantMinimum > 0)
			c.Assert(line.AmountCents, qt.Equals, tc.wantMinimum)
			if ok {
				c.Assert(line.Description, qt.Equals, "minimum charge")
				c.Assert(quote.TotalCents, qt.Equals, MinimumChargeCents)
			}
		})
	}
	// quote-only stays priceless: no lines, total 0
	quote, err := Compute(QuoteInput{CensusSize: 50_001, SMSTwoFA: true})
	c.Assert(err, qt.IsNil)
	c.Assert(quote.TotalCents, qt.Equals, Cents(0))
	c.Assert(quote.Lines, qt.HasLen, 0)
}

// TestComputeQuoteOnly: above 50 000 voters there is no price at all, whatever the add-ons.
func TestComputeQuoteOnly(t *testing.T) {
	c := qt.New(t)
	for _, payer := range []PayerType{PayerStandard, PayerIntegrator} {
		quote, err := Compute(QuoteInput{
			CensusSize: 50_001, EmailTwoFA: true, SMSTwoFA: true, SignedCert: true,
			CustomURL: true, Branding: true, Payer: payer,
		})
		c.Assert(err, qt.IsNil)
		c.Assert(quote.QuoteRequired, qt.IsTrue)
		c.Assert(quote.QuoteRecommended, qt.IsTrue)
		c.Assert(quote.TotalCents, qt.Equals, Cents(0))
		c.Assert(quote.Lines, qt.Not(qt.IsNil)) // encodes as [], not null
		c.Assert(quote.Lines, qt.HasLen, 0)
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
		c.Run(fmt.Sprintf("%d voters", tc.censusSize), func(c *qt.C) {
			quote, err := Compute(QuoteInput{CensusSize: tc.censusSize})
			c.Assert(err, qt.IsNil)
			c.Assert(quote.QuoteRecommended, qt.Equals, tc.recommended)
			c.Assert(quote.QuoteRequired, qt.Equals, tc.quoteRequired)
		})
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

// TestPayerPolicyParity asserts the integrator base price is identical to the
// standard one (only email 2FA differs, see TestComputeAddOns).
func TestPayerPolicyParity(t *testing.T) {
	c := qt.New(t)
	for _, censusSize := range []int{1, 26, 600, 801, 5001, 15_001, 60_000} {
		c.Run(fmt.Sprintf("%d voters", censusSize), func(c *qt.C) {
			standard, err := Compute(QuoteInput{CensusSize: censusSize, Payer: PayerStandard})
			c.Assert(err, qt.IsNil)
			integrator, err := Compute(QuoteInput{CensusSize: censusSize, Payer: PayerIntegrator})
			c.Assert(err, qt.IsNil)
			c.Assert(integrator.TotalCents, qt.Equals, standard.TotalCents)
		})
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

// TestComputeRejectsOutOfRangeCensus: the calculator only accepts 1..MaxCensusSize voters.
func TestComputeRejectsOutOfRangeCensus(t *testing.T) {
	c := qt.New(t)

	_, err := Compute(QuoteInput{CensusSize: 0})
	c.Assert(err, qt.Not(qt.IsNil))

	_, err = Compute(QuoteInput{CensusSize: MaxCensusSize + 1})
	c.Assert(err, qt.Not(qt.IsNil))

	_, err = Compute(QuoteInput{CensusSize: 9_000_000_000_000_000_000, EmailTwoFA: true})
	c.Assert(err, qt.Not(qt.IsNil))

	// the bound itself is accepted, and quote-only
	quote, err := Compute(QuoteInput{CensusSize: MaxCensusSize, EmailTwoFA: true, SMSTwoFA: true})
	c.Assert(err, qt.IsNil)
	c.Assert(quote.QuoteRequired, qt.IsTrue)
	c.Assert(quote.TotalCents, qt.Equals, Cents(0))
}
