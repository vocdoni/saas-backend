package api

import (
	"testing"
	"time"

	qt "github.com/frankban/quicktest"
)

// TestBuildLoginResponseExpiry guards the JWT expiry regression: the exp claim must be encoded
// as seconds (a sane ~15-day expiry), not nanoseconds. jwx v2 reads an int64 exp as seconds, so
// a nanosecond value would push expiration ~55 billion years out and jwt.Validate would never
// reject an expired token.
func TestBuildLoginResponseExpiry(t *testing.T) {
	c := qt.New(t)

	res, err := testAPI.buildLoginResponse("expiry@test.com")
	c.Assert(err, qt.IsNil)

	token, err := testAPI.auth.Decode(res.Token)
	c.Assert(err, qt.IsNil)

	exp := token.Expiration()
	now := time.Now()
	// expiry is in the future and close to now+jwtExpiration — not decades/eons away
	c.Assert(exp.After(now), qt.IsTrue)
	c.Assert(exp.Before(now.Add(jwtExpiration+time.Hour)), qt.IsTrue,
		qt.Commentf("exp %s is too far out — likely encoded as nanoseconds", exp))
	// the response-level expiry mirrors the token claim
	c.Assert(res.Expirity.Before(now.Add(jwtExpiration+time.Hour)), qt.IsTrue)
}
