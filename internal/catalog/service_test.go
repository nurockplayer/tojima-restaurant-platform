package catalog

import (
	"errors"
	"fmt"
	"math"
	"strings"
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

func TestCatalogKeyMatchesDatabaseCharacterLimit(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want bool
	}{
		{name: "100 ASCII characters", key: strings.Repeat("a", 100), want: true},
		{name: "101 ASCII characters", key: strings.Repeat("a", 101)},
		{name: "100 multibyte characters", key: strings.Repeat("界", 100), want: true},
		{name: "101 multibyte characters", key: strings.Repeat("界", 101)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := catalogKey(test.key); got != test.want {
				t.Fatalf("catalogKey(%q)=%v, want %v", test.key, got, test.want)
			}
		})
	}
}

func TestCleanKeyRejectsControlsWithoutLimitingIdentityLength(t *testing.T) {
	for _, key := range []string{"a\x00b", "a\nb", string([]byte{0xff})} {
		if cleanKey(key) {
			t.Errorf("cleanKey(%q) accepted control or invalid UTF-8", key)
		}
	}
	longIdentity := strings.Repeat("x", 200)
	if !validActorScope(longIdentity, longIdentity) {
		t.Fatal("cleanKey's catalog-key length limit leaked into actor or tenant scopes")
	}
}

func TestValidateDataBoundsWholeCatalogBudget(t *testing.T) {
	products := make([]Product, 6)
	for i := range products {
		products[i] = Product{Key: fmt.Sprintf("meal-%d", i), Name: "Meal", Description: strings.Repeat("x", 4000), Active: true}
	}
	if err := validateData(CatalogData{Categories: []Category{{Key: "food", Name: "Food", Products: products}}}); err != nil {
		t.Fatalf("valid catalog over 16 KiB was rejected: %v", err)
	}

	options := make([]Option, 1000)
	for i := range options {
		options[i] = Option{Key: fmt.Sprintf("%04d%s", i, strings.Repeat("🙂", 96)), Name: strings.Repeat("🙂", 160)}
	}
	tooLarge := CatalogData{Categories: []Category{{Key: "food", Name: "Food", Products: []Product{{
		Key: "meal", Name: "Meal", Active: true,
		Groups: []OptionGroup{{Key: "choices", Name: "Choices", MaxSelect: 1, Options: options}},
	}}}}}
	if err := validateData(tooLarge); !errors.Is(err, ErrInvalid) {
		t.Fatalf("catalog exceeding canonical byte budget returned %v", err)
	}
}
