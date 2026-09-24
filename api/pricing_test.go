package api

import (
	"net/http"
	"testing"

	qt "github.com/frankban/quicktest"
	"github.com/vocdoni/saas-backend/api/apicommon"
)

// TestPublicPricing exercises the public formula calculator: no auth, spec examples,
// threshold flags, and input validation.
func TestPublicPricing(t *testing.T) {
	c := qt.New(t)

	price := func(query string) apicommon.ProcessPriceResponse {
		return requestAndParse[apicommon.ProcessPriceResponse](t, http.MethodGet, "", nil, "pricing"+query)
	}

	// spec examples, unauthenticated
	c.Assert(price("?voters=600").TotalCents, qt.Equals, int64(29_000))
	c.Assert(price("?voters=10000&emailTwoFA=true").TotalCents, qt.Equals, int64(152_000))
	c.Assert(price("?voters=10").TotalCents, qt.Equals, int64(0))
	// flat add-ons bill even in the free tier; per-voter ones do not
	c.Assert(price("?voters=10&signedCertificate=true&smsTwoFA=true").TotalCents, qt.Equals, int64(4_900))
	// branding is charged as requested — there is no organization context here
	c.Assert(price("?voters=600&branding=true&customUrl=true").TotalCents,
		qt.Equals, int64(29_000+14_900+8_900))

	// the lines sum to the total
	full := price("?voters=900&emailTwoFA=true&smsTwoFA=true&signedCertificate=true")
	var sum int64
	for _, line := range full.Lines {
		sum += line.AmountCents
	}
	c.Assert(sum, qt.Equals, full.TotalCents)
	c.Assert(full.Currency, qt.Equals, "eur")

	// threshold flags
	c.Assert(price("?voters=15000").QuoteRecommended, qt.IsFalse)
	c.Assert(price("?voters=15001").QuoteRecommended, qt.IsTrue)
	c.Assert(price("?voters=15001").QuoteRequired, qt.IsFalse)
	over := price("?voters=50001")
	c.Assert(over.QuoteRequired, qt.IsTrue)
	c.Assert(over.TotalCents > 0, qt.IsTrue) // informational: the price still computes

	// input validation
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodGet, "", nil, "pricing")
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodGet, "", nil, "pricing?voters=0")
	requestAndAssertCode(http.StatusBadRequest, t, http.MethodGet, "", nil, "pricing?voters=abc")
}
