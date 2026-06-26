package stripe

import (
	"testing"

	qt "github.com/frankban/quicktest"
	stripeapi "github.com/stripe/stripe-go/v82"
)

func testPrices() []stripeapi.Price {
	return []stripeapi.Price{
		{ID: "price_m", UnitAmount: 1000, Recurring: &stripeapi.PriceRecurring{Interval: stripeapi.PriceRecurringIntervalMonth}},
		{ID: "price_y", UnitAmount: 10000, Recurring: &stripeapi.PriceRecurring{Interval: stripeapi.PriceRecurringIntervalYear}},
	}
}

func TestProcessProductToPlanIntegratorLimits(t *testing.T) {
	c := qt.New(t)

	baseMetadata := func() map[string]string {
		return map[string]string{
			"organization": `{}`,
			"votingTypes":  `{}`,
			"features":     `{}`,
		}
	}

	t.Run("parses integrator limits when present", func(_ *testing.T) {
		md := baseMetadata()
		md["integratorLimits"] = `{"maxManagedOrgs":3}`
		product := &stripeapi.Product{
			ID:           "prod_integrator",
			Name:         "Integrator",
			Metadata:     md,
			DefaultPrice: &stripeapi.Price{Metadata: map[string]string{"Default": "false"}},
		}

		plan, err := processProductToPlan(product, testPrices())
		c.Assert(err, qt.IsNil)
		c.Assert(plan.IntegratorLimits.MaxManagedOrgs, qt.Equals, 3)
	})

	t.Run("leaves zero limits when metadata is absent", func(_ *testing.T) {
		product := &stripeapi.Product{
			ID:           "prod_regular",
			Name:         "Regular",
			Metadata:     baseMetadata(),
			DefaultPrice: &stripeapi.Price{Metadata: map[string]string{"Default": "false"}},
		}

		plan, err := processProductToPlan(product, testPrices())
		c.Assert(err, qt.IsNil)
		c.Assert(plan.IntegratorLimits.MaxManagedOrgs, qt.Equals, 0)
	})

	t.Run("errors on malformed integrator limits", func(_ *testing.T) {
		md := baseMetadata()
		md["integratorLimits"] = `{not json}`
		product := &stripeapi.Product{
			ID:           "prod_bad",
			Name:         "Bad",
			Metadata:     md,
			DefaultPrice: &stripeapi.Price{Metadata: map[string]string{"Default": "false"}},
		}

		_, err := processProductToPlan(product, testPrices())
		c.Assert(err, qt.IsNotNil)
	})
}
