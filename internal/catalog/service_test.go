package catalog

import (
	"encoding/json"
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

func TestCatalogBudgetReservesStorePriceExpansion(t *testing.T) {
	data, actualSize := priceExpansionBoundaryCatalog(t)
	reservedSize, err := catalogBudgetSize(data)
	if err != nil {
		t.Fatal(err)
	}
	priceExpansion := 18 * len(flattenProducts(data))
	if reservedSize != actualSize+priceExpansion || reservedSize > MaxCatalogCanonicalJSONBytes || reservedSize < MaxCatalogCanonicalJSONBytes-2000 {
		t.Fatalf("price reserve size=%d actual=%d expansion=%d", reservedSize, actualSize, priceExpansion)
	}
	for ci := range data.Categories {
		for pi := range data.Categories[ci].Products {
			data.Categories[ci].Products[pi].BasePriceMinor = math.MaxInt64
		}
	}
	transformedSize, err := catalogBudgetSize(data)
	if err != nil || transformedSize != reservedSize {
		t.Fatalf("detached prices changed reserved size: got=%d want=%d err=%v", transformedSize, reservedSize, err)
	}
	if err = validateData(data); err != nil {
		t.Fatalf("detached catalog at reserved boundary became invalid: %v", err)
	}
}

func priceExpansionBoundaryCatalog(t *testing.T) (CatalogData, int) {
	t.Helper()
	makeData := func(descriptionBytes, optionNameRunes int) CatalogData {
		products := make([]Product, 500)
		for pi := range products {
			groups := make([]OptionGroup, 2)
			for gi := range groups {
				groups[gi] = OptionGroup{
					Key: fmt.Sprintf("g%02d", gi) + strings.Repeat("g", 97), Name: strings.Repeat("G", 160), MaxSelect: 1,
					Options: []Option{{Key: "o" + strings.Repeat("o", 99), Name: strings.Repeat("O", optionNameRunes), Active: true}},
				}
			}
			products[pi] = Product{
				Key: fmt.Sprintf("p%03d", pi) + strings.Repeat("p", 96), Name: strings.Repeat("P", 200),
				Description: strings.Repeat("d", descriptionBytes), Active: true, Groups: groups,
			}
		}
		return CatalogData{Categories: []Category{{Key: strings.Repeat("c", 100), Name: strings.Repeat("C", 160), Products: products}}}
	}

	baseSize := len(mustCatalogJSON(t, makeData(0, 1)))
	const priceReserve = 18 * 500
	target := MaxCatalogCanonicalJSONBytes - priceReserve
	remaining := target - baseSize
	if remaining < 0 {
		t.Fatalf("boundary fixture baseline already exceeds target: %d", baseSize)
	}
	descriptionCost := len(mustCatalogJSON(t, makeData(1, 1))) - baseSize
	descriptionOverhead := descriptionCost - 500
	descriptionBytes := 0
	if remaining > descriptionOverhead {
		descriptionBytes = (remaining - descriptionOverhead) / 500
	}
	if descriptionBytes > 512 {
		descriptionBytes = 512
	}
	withDescriptionsSize := len(mustCatalogJSON(t, makeData(descriptionBytes, 1)))
	remaining = target - withDescriptionsSize
	if remaining < 0 {
		t.Fatalf("description fill exceeded target: %d > %d", withDescriptionsSize, target)
	}
	optionNameRunes := 1 + remaining/1000
	if optionNameRunes > 160 {
		optionNameRunes = 160
	}
	data := makeData(descriptionBytes, optionNameRunes)
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	budget, err := catalogBudgetSize(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > MaxCatalogCanonicalJSONBytes || budget > MaxCatalogCanonicalJSONBytes || budget < MaxCatalogCanonicalJSONBytes-2000 {
		t.Fatalf("could not construct price-expansion boundary: actual=%d budget=%d target=%d description=%d option-name=%d", len(encoded), budget, target, descriptionBytes, optionNameRunes)
	}
	if err = validateData(data); err != nil {
		t.Fatalf("boundary source catalog is not accepted: %v", err)
	}
	return data, len(encoded)
}

func mustCatalogJSON(t *testing.T, data CatalogData) []byte {
	t.Helper()
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
