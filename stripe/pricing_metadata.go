package stripe

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	stripeapi "github.com/stripe/stripe-go/v87"
	"github.com/vocdoni/saas-backend/errors"
	"github.com/vocdoni/saas-backend/pricing"
)

// The pricing breakdown of a top-up opened for a process travels as JSON in the session metadata,
// cut over the keys pricing_0, pricing_1, ... because Stripe caps a metadata value at 500
// characters. It is display data only: nothing may take an amount, a size or an eligibility
// decision from it. The credited amount is netPaidCents, and the census follow-up is driven by
// MetadataKeyProcessTopUpCents and MetadataKeyProcessTopUpFromCents.
const (
	// pricingKeyPrefix starts the metadata keys of the breakdown's chunks; the chunk index follows.
	pricingKeyPrefix = "pricing_"
	// pricingChunkBytes is the most bytes one chunk holds, so its value fits Stripe's 500 characters.
	pricingChunkBytes = 500
	// maxPricingChunks is the most chunks a breakdown may take: a worst case one takes 2.
	maxPricingChunks = 4
)

// pricingKey is the metadata key of chunk i of the breakdown.
func pricingKey(i int) string { return pricingKeyPrefix + strconv.Itoa(i) }

// encodePricing serializes a breakdown into its metadata chunks, in order: at most
// pricingChunkBytes each, cut on a UTF-8 rune boundary so every value is valid UTF-8. A breakdown
// that needs more than maxPricingChunks is an error.
func encodePricing(breakdown *pricing.Breakdown) ([]string, error) {
	raw, err := json.Marshal(breakdown)
	if err != nil {
		return nil, fmt.Errorf("encoding the pricing breakdown: %w", err)
	}
	var chunks []string
	for len(raw) > 0 {
		n := min(len(raw), pricingChunkBytes)
		// never cut inside a rune: step back to the start of the one the cut would split
		for n < len(raw) && !utf8.RuneStart(raw[n]) {
			n--
		}
		chunks = append(chunks, string(raw[:n]))
		raw = raw[n:]
	}
	if len(chunks) > maxPricingChunks {
		return nil, fmt.Errorf("the pricing breakdown needs %d metadata keys, at most %d fit", len(chunks), maxPricingChunks)
	}
	return chunks, nil
}

// sessionPricing reads the pricing breakdown a top-up session was opened with back from the
// session's own metadata (never the payment intent's). A session without pricing_0 has none: nil
// and no error. A malformed one is an error: a chunk at index maxPricingChunks or above, a chunk
// past a gap, JSON that is not exactly a pricing.Breakdown, a currency other than eur, a negative
// amount other than the balance, or no charge. The charge is not checked against today's minimum:
// a later release may raise it, and a session opened before must still read back.
//
// What it returns is display data only, shown as stored: session metadata can be edited by
// whoever has access to the Stripe account, so no caller may act on it.
func sessionPricing(session *stripeapi.CheckoutSession) (*pricing.Breakdown, error) {
	if _, ok := session.Metadata[pricingKey(0)]; !ok {
		return nil, nil
	}
	chunks := 0
	for key := range session.Metadata {
		if strings.HasPrefix(key, pricingKeyPrefix) {
			chunks++
		}
	}
	if chunks > maxPricingChunks {
		return nil, fmt.Errorf("%d pricing chunks, at most %d are written", chunks, maxPricingChunks)
	}
	// the chunks must be exactly pricing_0 .. pricing_<chunks-1>: any other key is past a gap,
	// or at an index that is never written
	var raw strings.Builder
	for i := range chunks {
		chunk, ok := session.Metadata[pricingKey(i)]
		if !ok {
			return nil, fmt.Errorf("pricing chunk %d is missing while %d chunks exist", i, chunks)
		}
		raw.WriteString(chunk)
	}
	decoder := json.NewDecoder(strings.NewReader(raw.String()))
	decoder.DisallowUnknownFields()
	breakdown := &pricing.Breakdown{}
	if err := decoder.Decode(breakdown); err != nil {
		return nil, fmt.Errorf("decoding the pricing breakdown: %w", err)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("data after the pricing breakdown")
	}
	if breakdown.Currency != "eur" {
		return nil, fmt.Errorf("pricing breakdown in currency %q", breakdown.Currency)
	}
	cents := []pricing.Cents{breakdown.TotalCents, breakdown.ShortfallCents, breakdown.ChargeCents, breakdown.PaidCents}
	for _, line := range breakdown.Lines {
		cents = append(cents, line.AmountCents)
	}
	for _, amount := range cents {
		if amount < 0 {
			return nil, fmt.Errorf("negative amount %d in the pricing breakdown", amount)
		}
	}
	if breakdown.ChargeCents <= 0 {
		return nil, fmt.Errorf("pricing breakdown charges nothing")
	}
	return breakdown, nil
}
