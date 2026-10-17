package catalog

import (
	"context"
	"errors"
	"math"
	"testing"
)

func TestQuoteValidatesSelectionsAndIntegerOverflow(t *testing.T) {
	menu := PublishedMenu{CatalogData: CatalogData{Categories: []Category{{Products: []Product{{
		Key: "soup", Active: true, Availability: Available, Selectable: true,
		EffectivePriceMinor: 500,
		Groups:              []OptionGroup{{Key: "size", MinSelect: 1, MaxSelect: 1, Options: []Option{{Key: "large", Active: true, Availability: Available, PriceDeltaMinor: 50}}}},
	}}}}}}
	quote, err := Quote(menu, "soup", []OptionSelection{{GroupKey: "size", OptionKey: "large"}})
	if err != nil || quote.UnitMinor != 550 {
		t.Fatalf("valid selection quote = %+v, %v", quote, err)
	}
	for name, selected := range map[string][]OptionSelection{
		"duplicate":        {{GroupKey: "size", OptionKey: "large"}, {GroupKey: "size", OptionKey: "large"}},
		"unknown group":    {{GroupKey: "unknown", OptionKey: "large"}},
		"missing required": nil,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Quote(menu, "soup", selected); !errors.Is(err, ErrInvalid) {
				t.Fatalf("invalid selection returned %v", err)
			}
		})
	}
	menu.Categories[0].Products[0].EffectivePriceMinor = math.MaxInt64
	if _, err := Quote(menu, "soup", []OptionSelection{{GroupKey: "size", OptionKey: "large"}}); !errors.Is(err, ErrPriceOverflow) {
		t.Fatalf("overflowing integer quote returned %v", err)
	}
}

func TestTaxQuoteFailsClosedWithoutOwnerPolicy(t *testing.T) {
	if _, err := TaxQuote(context.Background(), nil, TaxInput{TenantID: "t", StoreID: "s", AmountMinor: 100}); !errors.Is(err, ErrTaxUnavailable) {
		t.Fatalf("tax quote without confirmed policy returned %v", err)
	}
}
