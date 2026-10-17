package ordering

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
)

func TestMapCatalogErrorPreservesAuthorizationDenial(t *testing.T) {
	err := mapCatalogError(errors.Join(errors.New("catalog read failed"), catalog.ErrDenied))
	if !errors.Is(err, ErrDenied) {
		t.Fatalf("catalog denial mapped to %v, want ordering.ErrDenied", err)
	}
	if errors.Is(err, ErrUnavailable) {
		t.Fatalf("catalog denial was misreported as unavailable: %v", err)
	}
}

func TestCatalogSelectionOptionBounds(t *testing.T) {
	options := make([]catalog.OptionSelection, MaxOptionsPerLine)
	for i := range options {
		options[i] = catalog.OptionSelection{GroupKey: "group", OptionKey: fmt.Sprintf("option-%04d", i)}
	}
	if !validLine(1, options) {
		t.Fatalf("catalog-sized %d-option selection was rejected", MaxOptionsPerLine)
	}
	options = append(options, catalog.OptionSelection{GroupKey: "group", OptionKey: "option-extra"})
	if validLine(1, options) {
		t.Fatalf("selection larger than MaxOptionsPerLine (%d) was accepted", MaxOptionsPerLine)
	}
	cart := Cart{Lines: []CartLine{{Options: make([]CartOption, MaxCartOptions)}}}
	if got := cartOptionCount(cart); got != MaxCartOptions {
		t.Fatalf("aggregate option count=%d, want cap %d", got, MaxCartOptions)
	}
}

func TestMutateRejectsOversizedOptionsBeforeDatabase(t *testing.T) {
	options := make([]catalog.OptionSelection, MaxOptionsPerLine+1)
	service := &Service{}
	_, err := service.Mutate(context.Background(), Mutation{
		SessionID:          "session",
		GrantToken:         "grant",
		ExpectedGeneration: 1,
		ExpectedVersion:    1,
		IdempotencyKey:     "oversized-options",
		Kind:               AddLine,
		ProductKey:         "product",
		Quantity:           1,
		Options:            options,
	})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized mutation returned %v, want ErrInvalid before database access", err)
	}
}
