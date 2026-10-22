package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/pricing"
)

// TestPublicPricing exercises the public formula calculator: no auth, spec examples,
// threshold flags, the quote-only shape, and input validation.
func TestPublicPricing(t *testing.T) {
	c := qt.New(t)

	price := func(query string) apicommon.ProcessPriceResponse {
		return requestAndParse[apicommon.ProcessPriceResponse](t, http.MethodGet, "", nil, "pricing"+query)
	}

	// spec examples, unauthenticated
	c.Assert(price("?voters=20").TotalCents, qt.Equals, pricing.Cents(0))
	c.Assert(price("?voters=20&signedCertificate=true").TotalCents, qt.Equals, pricing.Cents(3_900))
	c.Assert(price("?voters=40").TotalCents, qt.Equals, pricing.Cents(2_640))
	c.Assert(price("?voters=600").TotalCents, qt.Equals, pricing.Cents(26_200))
	c.Assert(price("?voters=1004").TotalCents, qt.Equals, pricing.Cents(35_850))
	c.Assert(price("?voters=1005").TotalCents, qt.Equals, pricing.Cents(35_863)) // €358.625, half up
	// the public calculator prices a standard organization: email 2FA is free, SMS is €0.05 per voter
	c.Assert(price("?voters=10000&emailTwoFA=true").TotalCents, qt.Equals, pricing.Cents(135_800))
	c.Assert(price("?voters=10000&smsTwoFA=true").TotalCents, qt.Equals, pricing.Cents(185_800))
	c.Assert(price("?voters=10000&smsTwoFA=true&signedCertificate=true").TotalCents, qt.Equals, pricing.Cents(189_700))
	c.Assert(price("?voters=900&smsTwoFA=true&signedCertificate=true").TotalCents, qt.Equals, pricing.Cents(41_800))
	// add-ons are charged on free processes too, and a priced amount below €1 is raised to €1
	minimum := price("?voters=10&smsTwoFA=true")
	c.Assert(minimum.TotalCents, qt.Equals, pricing.MinimumChargeCents)
	c.Assert(minimum.Lines, qt.HasLen, 3) // base 0, SMS 50, minimum charge 50
	c.Assert(minimum.Lines[2].Kind, qt.Equals, pricing.LineMinimumCharge)
	c.Assert(minimum.Lines[2].AmountCents, qt.Equals, pricing.Cents(50))
	c.Assert(price("?voters=1&smsTwoFA=true").TotalCents, qt.Equals, pricing.MinimumChargeCents)
	// branding and custom URL are charged as requested — there is no organization context here
	c.Assert(price("?voters=600&branding=true&customUrl=true").TotalCents,
		qt.Equals, pricing.Cents(26_200+pricing.BrandingCents+pricing.CustomURLCents))

	// the lines sum to the total
	full := price("?voters=900&emailTwoFA=true&smsTwoFA=true&signedCertificate=true")
	var sum pricing.Cents
	for _, line := range full.Lines {
		sum += line.AmountCents
	}
	c.Assert(sum, qt.Equals, full.TotalCents)
	c.Assert(full.Currency, qt.Equals, "eur")

	// threshold flags
	below := price("?voters=15000")
	c.Assert(below.QuoteRecommended, qt.IsFalse)
	c.Assert(below.QuoteRequired, qt.IsFalse)
	recommended := price("?voters=15001")
	c.Assert(recommended.QuoteRecommended, qt.IsTrue)
	c.Assert(recommended.QuoteRequired, qt.IsFalse)
	top := price("?voters=50000")
	c.Assert(top.TotalCents, qt.Equals, pricing.Cents(500_800))
	c.Assert(top.QuoteRequired, qt.IsFalse)
	// quote-only: no price
	over := price("?voters=50001&smsTwoFA=true&signedCertificate=true")
	c.Assert(over.QuoteRecommended, qt.IsTrue)
	c.Assert(over.QuoteRequired, qt.IsTrue)
	c.Assert(over.TotalCents, qt.Equals, pricing.Cents(0))
	c.Assert(over.Lines, qt.HasLen, 0)

	// input validation
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodGet, "", nil, "pricing")
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodGet, "", nil, "pricing?voters=0")
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodGet, "", nil, "pricing?voters=abc")
}
