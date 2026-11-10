package stripe

import (
	"maps"
	"strings"
	"testing"
	"unicode/utf8"

	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v87"
	"github.com/vocdoni/saas-backend/pricing"
)

// worstCaseBreakdown is the longest breakdown the service can produce: every add-on line, the
// largest self-service census, the census headroom fields and a negative balance.
func worstCaseBreakdown(t *testing.T) *pricing.Breakdown {
	t.Helper()
	quote, err := pricing.Compute(pricing.QuoteInput{
		CensusSize: 50_000, EmailTwoFA: true, SMSTwoFA: true, SignedCert: true, CustomURL: true, Branding: true,
		Payer: pricing.PayerIntegrator,
	})
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, quote.Lines, qt.HasLen, 6)
	const paid, balance = 12_345_67, -99_999_99
	return &pricing.Breakdown{
		Shortfall: pricing.Shortfall{
			Quote:          quote,
			BalanceCents:   balance,
			ShortfallCents: quote.TotalCents - paid - balance,
			PaidCents:      paid,
			CensusSize:     50_000,
		},
		Currency:    "eur",
		ChargeCents: quote.TotalCents - paid - balance,
	}
}

// testPricingJSON is the breakdown of a process checkout as it travels in pricing_0, and
// testPricing what it reads back as.
const testPricingJSON = `{"lines":[{"kind":"base","description":"voting process (26 eligible voters)","amountCents":1716}],` +
	`"totalCents":1716,"quoteRecommended":false,"quoteRequired":false,"balanceCents":-200,` +
	`"shortfallCents":1916,"currency":"eur","chargeCents":1916}`

func testPricing() *pricing.Breakdown {
	return &pricing.Breakdown{
		Shortfall: pricing.Shortfall{
			Quote: pricing.Quote{
				Lines: []pricing.QuoteLine{
					{Kind: pricing.LineBase, Description: "voting process (26 eligible voters)", AmountCents: 1_716},
				},
				TotalCents: 1_716,
			},
			BalanceCents:   -200,
			ShortfallCents: 1_916,
		},
		Currency:    "eur",
		ChargeCents: 1_916,
	}
}

// pricingKeys returns the pricing_N entries of a metadata map.
func pricingKeys(metadata map[string]string) map[string]string {
	keys := map[string]string{}
	for key, value := range metadata {
		if strings.HasPrefix(key, pricingKeyPrefix) {
			keys[key] = value
		}
	}
	return keys
}

// TestBuildPaymentSessionParamsPricing: the breakdown of a top-up opened for a process is written
// as pricing_N chunks, the same on the session and on its payment intent, next to the routing
// metadata and without touching the caller's map; the worst case fits two keys of at most 500
// bytes and reads back as it was written. A plain top-up carries none, and a breakdown that does
// not fit the four keys opens no session.
func TestBuildPaymentSessionParamsPricing(t *testing.T) {
	c := qt.New(t)
	routing := map[string]string{
		MetadataKeyWalletTopUpOrg: "0x000000000000000000000000000000000000bEEF",
		MetadataKeyProcessID:      "656e6f7567682062797465732100",
	}
	breakdown := worstCaseBreakdown(t)

	params, err := buildPaymentSessionParams(&PaymentSessionParams{Metadata: routing, Pricing: breakdown})
	c.Assert(err, qt.IsNil)
	chunks := pricingKeys(params.Metadata)
	c.Assert(chunks, qt.HasLen, 2)
	for key, chunk := range chunks {
		c.Assert(len(chunk) <= pricingChunkBytes, qt.IsTrue, qt.Commentf("%s is %d bytes", key, len(chunk)))
	}
	c.Assert(params.PaymentIntentData.Metadata, qt.DeepEquals, params.Metadata)
	c.Assert(params.Metadata[MetadataKeyProcessID], qt.Equals, routing[MetadataKeyProcessID])
	c.Assert(params.Metadata, qt.HasLen, len(routing)+2)
	c.Assert(routing, qt.HasLen, 2) // the caller's map is left alone
	// the invoice names its organization and its source, nothing else
	c.Assert(params.InvoiceCreation.InvoiceData.Metadata, qt.HasLen, 2)

	read, err := sessionPricing(&stripeapi.CheckoutSession{Metadata: params.Metadata})
	c.Assert(err, qt.IsNil)
	c.Assert(read, qt.DeepEquals, breakdown)

	// a plain top-up carries no breakdown, and its metadata is passed as it came
	plain, err := buildPaymentSessionParams(&PaymentSessionParams{Metadata: routing})
	c.Assert(err, qt.IsNil)
	c.Assert(pricingKeys(plain.Metadata), qt.HasLen, 0)
	c.Assert(plain.Metadata, qt.DeepEquals, routing)
	c.Assert(plain.PaymentIntentData.Metadata, qt.DeepEquals, routing)

	// more than four chunks: an error, and no parameters to open a session with
	oversized := *breakdown
	oversized.Lines = []pricing.QuoteLine{{Kind: pricing.LineBase, Description: strings.Repeat("x", 4*pricingChunkBytes)}}
	params, err = buildPaymentSessionParams(&PaymentSessionParams{Metadata: routing, Pricing: &oversized})
	c.Assert(err, qt.ErrorMatches, "the pricing breakdown needs 5 metadata keys, at most 4 fit")
	c.Assert(params, qt.IsNil)
}

// TestPricingChunksCutOnRuneBoundaries: multibyte text is never cut inside a rune, so every chunk
// is valid UTF-8 of at most 500 bytes, and the chunks still read back as the breakdown.
func TestPricingChunksCutOnRuneBoundaries(t *testing.T) {
	c := qt.New(t)
	breakdown := &pricing.Breakdown{
		// 3-byte runes: a cut every 500 bytes lands inside one
		Shortfall: pricing.Shortfall{
			Quote: pricing.Quote{
				Lines:      []pricing.QuoteLine{{Kind: pricing.LineBase, Description: strings.Repeat("€", 400), AmountCents: 1_716}},
				TotalCents: 1_716,
			},
			ShortfallCents: 1_716,
		},
		Currency: "eur", ChargeCents: 1_716,
	}
	chunks, err := encodePricing(breakdown)
	c.Assert(err, qt.IsNil)
	c.Assert(chunks, qt.HasLen, 3)
	shortened := 0
	metadata := map[string]string{}
	for i, chunk := range chunks {
		c.Assert(utf8.ValidString(chunk), qt.IsTrue)
		c.Assert(len(chunk) <= pricingChunkBytes, qt.IsTrue)
		if i < len(chunks)-1 && len(chunk) < pricingChunkBytes {
			shortened++
		}
		metadata[pricingKey(i)] = chunk
	}
	c.Assert(shortened > 0, qt.IsTrue) // at least one cut was moved back to a rune boundary
	read, err := sessionPricing(&stripeapi.CheckoutSession{Metadata: metadata})
	c.Assert(err, qt.IsNil)
	c.Assert(read, qt.DeepEquals, breakdown)
}

// TestSessionPricing: a session without pricing_0 has no breakdown, which is not an error; one
// with chunks reads back only when they are exactly pricing_0..pricing_N (N below 4) and hold
// exactly a breakdown in eur whose amounts, the balance aside, are not negative and whose charge
// is positive, even below today's minimum.
func TestSessionPricing(t *testing.T) {
	const valid = testPricingJSON
	whole, err := sessionPricing(&stripeapi.CheckoutSession{Metadata: map[string]string{"pricing_0": valid}})
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, whole, qt.DeepEquals, testPricing())
	// what the service writes is what it reads
	written, err := encodePricing(testPricing())
	qt.Assert(t, err, qt.IsNil)
	qt.Assert(t, written, qt.DeepEquals, []string{valid})
	edited := func(old, replacement string) map[string]string {
		if !strings.Contains(valid, old) {
			t.Fatalf("%q is not in the valid breakdown", old)
		}
		return map[string]string{"pricing_0": strings.Replace(valid, old, replacement, 1)}
	}
	half := len(valid) / 2
	fifth := len(valid) / 5
	for _, tc := range []struct {
		name     string
		metadata map[string]string
		wantErr  string // "" for no error
		wantNone bool   // no breakdown and no error
	}{
		{name: "absent", metadata: map[string]string{MetadataKeyWalletTopUpOrg: "0x01"}, wantNone: true},
		{name: "no metadata", wantNone: true},
		{name: "no first chunk", metadata: map[string]string{"pricing_1": valid}, wantNone: true},
		{name: "one chunk", metadata: map[string]string{"pricing_0": valid, MetadataKeyProcessID: "aa"}},
		{name: "two chunks", metadata: map[string]string{"pricing_0": valid[:half], "pricing_1": valid[half:]}},
		{name: "census headroom", metadata: edited(`"chargeCents":1916`, `"chargeCents":1916,"paidCents":500,"censusSize":27`)},
		{name: "charge below today's minimum", metadata: edited(`"chargeCents":1916`, `"chargeCents":50`)},
		{
			name:     "gap",
			metadata: map[string]string{"pricing_0": valid[:half], "pricing_2": valid[half:]},
			wantErr:  "pricing chunk 1 is missing while 2 chunks exist",
		},
		{
			name:     "index 4 after a complete breakdown",
			metadata: map[string]string{"pricing_0": valid, "pricing_4": "x"},
			wantErr:  "pricing chunk 1 is missing while 2 chunks exist",
		},
		{
			name:     "stray key",
			metadata: map[string]string{"pricing_0": valid, "pricing_x": "1"},
			wantErr:  "pricing chunk 1 is missing while 2 chunks exist",
		},
		{
			name: "five chunks",
			metadata: map[string]string{
				"pricing_0": valid[:fifth], "pricing_1": valid[fifth : 2*fifth], "pricing_2": valid[2*fifth : 3*fifth],
				"pricing_3": valid[3*fifth : 4*fifth], "pricing_4": valid[4*fifth:],
			},
			wantErr: "5 pricing chunks, at most 4 are written",
		},
		{name: "bad json", metadata: map[string]string{"pricing_0": valid[:half]}, wantErr: "decoding the pricing breakdown: .*"},
		{name: "trailing data", metadata: map[string]string{"pricing_0": valid + "{}"}, wantErr: "data after the pricing breakdown"},
		{
			name:     "unknown field",
			metadata: edited(`"currency":"eur"`, `"currency":"eur","creditCents":999999`),
			wantErr:  `decoding the pricing breakdown: json: unknown field "creditCents"`,
		},
		{
			name:     "wrong currency",
			metadata: edited(`"currency":"eur"`, `"currency":"usd"`),
			wantErr:  `pricing breakdown in currency "usd"`,
		},
		{name: "no currency", metadata: map[string]string{"pricing_0": "{}"}, wantErr: `pricing breakdown in currency ""`},
		{name: "negative total", metadata: edited(`"totalCents":1716`, `"totalCents":-1`), wantErr: "negative amount -1 .*"},
		{
			name:     "negative shortfall",
			metadata: edited(`"shortfallCents":1916`, `"shortfallCents":-2`),
			wantErr:  "negative amount -2 .*",
		},
		{name: "negative charge", metadata: edited(`"chargeCents":1916`, `"chargeCents":-3`), wantErr: "negative amount -3 .*"},
		{
			name:     "negative paid",
			metadata: edited(`"chargeCents":1916`, `"chargeCents":1916,"paidCents":-4,"censusSize":27`),
			wantErr:  "negative amount -4 .*",
		},
		{name: "negative line", metadata: edited(`"amountCents":1716`, `"amountCents":-5`), wantErr: "negative amount -5 .*"},
		{
			name:     "no charge",
			metadata: edited(`"chargeCents":1916`, `"chargeCents":0`),
			wantErr:  "pricing breakdown charges nothing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := qt.New(t)
			// the payment intent's copy is never read: only the session's own metadata counts
			session := &stripeapi.CheckoutSession{
				ID:            "cs_pricing",
				Metadata:      tc.metadata,
				PaymentIntent: &stripeapi.PaymentIntent{Metadata: map[string]string{"pricing_0": valid}},
			}
			before := maps.Clone(tc.metadata)
			breakdown, err := sessionPricing(session)
			c.Assert(tc.metadata, qt.DeepEquals, before)
			switch {
			case tc.wantErr != "":
				c.Assert(err, qt.ErrorMatches, tc.wantErr)
				c.Assert(breakdown, qt.IsNil)
			case tc.wantNone:
				c.Assert(err, qt.IsNil)
				c.Assert(breakdown, qt.IsNil)
			default:
				c.Assert(err, qt.IsNil)
				c.Assert(breakdown.Currency, qt.Equals, "eur")
				c.Assert(breakdown.BalanceCents, qt.Equals, pricing.Cents(-200))
				c.Assert(breakdown.Lines, qt.HasLen, 1)
				c.Assert(breakdown.TotalCents, qt.Equals, pricing.Cents(1_716))
			}
		})
	}
}
