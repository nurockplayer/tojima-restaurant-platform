package tax

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestReduceHistoricalMixedRatesAndSingleFloor(t *testing.T) {
	original, err := Calculate(schedule(), day("2026-10-18"), scope(), []LineInput{
		{Scope: scope(), LineID: "a", TaxClass: ClassDineIn, BaseGrossJPY: 6, Quantity: 2},
		{Scope: scope(), LineID: "b", TaxClass: ClassDineIn, BaseGrossJPY: 6, Quantity: 1},
		{Scope: scope(), LineID: "c", TaxClass: ClassEligibleTakeaway, BaseGrossJPY: 108, Quantity: 2},
	})
	if err != nil {
		t.Fatal(err)
	}
	saved := original.Record()
	before, _ := json.Marshal(saved)
	got, err := ReduceHistorical(saved, []int64{1, 1, 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != (Totals{GrossJPY: 120, NetJPY: 111, TaxJPY: 9}) {
		t.Fatalf("effective mixed-rate totals=%+v", got.Total)
	}
	if len(got.Groups) != 2 {
		t.Fatal(got.Groups)
	}
	for i, l := range got.Lines {
		if l.Rule.ID != saved.Lines[i].Rule.ID || l.BaseGrossJPY != saved.Lines[i].BaseGrossJPY {
			t.Fatal("historical inputs changed")
		}
	}
	after, _ := json.Marshal(saved)
	if string(before) != string(after) {
		t.Fatal("source mutated")
	}
	for _, qty := range [][]int64{{-1, 1, 1}, {3, 1, 1}, {1, 1}} {
		if _, err = ReduceHistorical(saved, qty); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("invalid reduction %v: %v", qty, err)
		}
	}
	if empty, e := ReduceHistorical(saved, []int64{0, 0, 0}); e != nil || empty != nil {
		t.Fatalf("empty=%v %v", empty, e)
	}
}
