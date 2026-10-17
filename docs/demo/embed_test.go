package demo

import (
	"errors"
	"testing"
)

func TestShironagasukujiraEvidenceStrictGuards(t *testing.T) {
	evidence, err := LoadShironagasukujira()
	if err != nil {
		t.Fatal(err)
	}
	if len(evidence.Items) != 32 || len(evidence.Sources) != 6 {
		t.Fatalf("unexpected embedded evidence counts: items=%d sources=%d", len(evidence.Items), len(evidence.Sources))
	}

	mutated := evidence
	mutated.Metadata.LivePaymentEnabled = true
	mutated.Guardrails.NoRealStripeOrCash = false
	if err = ValidateShironagasukujira(mutated); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("disabled test-only guardrail accepted: %v", err)
	}

	mutated = evidence
	mutated.Items = append([]Item(nil), evidence.Items...)
	mutated.Items[0].ReferencePriceJPY = new(int64)
	*mutated.Items[0].ReferencePriceJPY = 1
	if err = ValidateShironagasukujira(mutated); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("changed historical price/provenance accepted: %v", err)
	}

	mutated = evidence
	mutated.Items = append([]Item(nil), evidence.Items...)
	for i := range mutated.Items {
		if mutated.Items[i].ReferenceChannel == "DELIVERY_ONLY" {
			mutated.Items[i].OrderableInDemo = true
			break
		}
	}
	if err = ValidateShironagasukujira(mutated); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("delivery item enabled for dine-in: %v", err)
	}

	mutated = evidence
	mutated.Items = append([]Item(nil), evidence.Items...)
	for i := range mutated.Items {
		if mutated.Items[i].ReferenceChannel == "DINE_IN_UNPRICED" {
			mutated.Items[i].ReferencePriceJPY = new(int64)
			*mutated.Items[i].ReferencePriceJPY = 900
			break
		}
	}
	if err = ValidateShironagasukujira(mutated); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("invented price for unpriced item accepted: %v", err)
	}

	mutated = evidence
	mutated.Items = append([]Item(nil), evidence.Items...)
	mutated.Items[0].SourceID = "missing-source"
	if err = ValidateShironagasukujira(mutated); !errors.Is(err, ErrInvalidEvidence) {
		t.Fatalf("missing source reference accepted: %v", err)
	}
}
