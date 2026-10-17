package tax

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
)

func invoiceOrder(t *testing.T, id, date, class, ruleID string, rateNumerator, rateDenominator, gross int64) InvoiceOrder {
	t.Helper()
	from := day("2019-10-01")
	if date == "2027-04-01" {
		from = day(date)
	}
	toDate := day("2027-04-01")
	var to *time.Time
	if date != "2027-04-01" {
		to = &toDate
	}
	legalSource := "test legal source"
	snapshot, err := Calculate(Schedule{{
		ID: ruleID, TaxClass: class, RateNumerator: rateNumerator, RateDenominator: rateDenominator,
		EffectiveFrom: from, EffectiveTo: to, LegalSource: legalSource, LegalStatus: LegalStatusEnacted, Version: "fixture-v1",
	}}, day(date), scope(), []LineInput{{
		Scope: scope(), LineID: "line-" + id, TaxClass: class, BaseGrossJPY: gross, Quantity: 1,
	}})
	if err != nil {
		t.Fatalf("create order %s snapshot: %v", id, err)
	}
	return InvoiceOrder{OrderID: id, Snapshot: snapshot.Record()}
}

func TestAggregateInvoiceFloorsOnceAcrossOrders(t *testing.T) {
	first := invoiceOrder(t, "order-a", "2026-10-18", ClassDineIn, "dine-v1", 10, 100, 6)
	second := invoiceOrder(t, "order-b", "2026-10-18", ClassDineIn, "dine-v1", 10, 100, 6)
	if first.Snapshot.Total.TaxJPY != 0 || second.Snapshot.Total.TaxJPY != 0 {
		t.Fatal("fixture orders should each floor to zero tax")
	}
	got, err := AggregateInvoice(scope(), day("2026-10-18"), []InvoiceOrder{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != (Totals{GrossJPY: 12, NetJPY: 11, TaxJPY: 1}) || len(got.Groups) != 1 || got.Groups[0].Totals.TaxJPY != 1 {
		t.Fatalf("invoice aggregate=%+v groups=%+v", got.Total, got.Groups)
	}
}

func TestAggregateInvoiceGroupsMultipleNormalizedRates(t *testing.T) {
	orders := []InvoiceOrder{
		invoiceOrder(t, "dine", "2026-10-18", ClassDineIn, "dine-v1", 10, 100, 1100),
		invoiceOrder(t, "takeaway", "2026-10-18", ClassEligibleTakeaway, "takeaway-v1", 8, 100, 1080),
	}
	got, err := AggregateInvoice(scope(), day("2026-10-18"), orders)
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != (Totals{GrossJPY: 2180, NetJPY: 2000, TaxJPY: 180}) || len(got.Groups) != 2 {
		t.Fatalf("aggregate=%+v groups=%+v", got.Total, got.Groups)
	}
	if got.Groups[0].RateNumerator != 1 || got.Groups[0].RateDenominator != 10 || got.Groups[0].Totals.TaxJPY != 100 || got.Groups[1].RateNumerator != 2 || got.Groups[1].RateDenominator != 25 || got.Groups[1].Totals.TaxJPY != 80 {
		t.Fatalf("rate groups not normalized/sorted: %+v", got.Groups)
	}
}

func TestAggregateInvoiceCombinesEquivalentHistoricalRateFractions(t *testing.T) {
	orders := []InvoiceOrder{
		invoiceOrder(t, "dine", "2026-10-18", ClassDineIn, "dine-v1", 10, 100, 6),
		invoiceOrder(t, "takeaway", "2026-10-18", ClassEligibleTakeaway, "takeaway-equal-rate", 20, 200, 6),
	}
	got, err := AggregateInvoice(scope(), day("2026-10-18"), orders)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Groups) != 1 || got.Groups[0].RateNumerator != 1 || got.Groups[0].RateDenominator != 10 || got.Groups[0].Totals.TaxJPY != 1 || len(got.Groups[0].Rules) != 2 {
		t.Fatalf("equivalent rates were not aggregated once: %+v", got.Groups)
	}
}

func TestAggregateInvoiceUsesHistoricalRulesAcrossEffectiveChange(t *testing.T) {
	oldOrder := invoiceOrder(t, "old", "2026-10-18", ClassDineIn, "dine-v1", 10, 100, 110)
	newOrder := invoiceOrder(t, "new", "2027-04-01", ClassDineIn, "dine-v2", 12, 100, 112)
	got, err := AggregateInvoice(scope(), day("2027-04-01"), []InvoiceOrder{newOrder, oldOrder})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total != (Totals{GrossJPY: 222, NetJPY: 200, TaxJPY: 22}) || len(got.Groups) != 2 {
		t.Fatalf("historical aggregate=%+v groups=%+v", got.Total, got.Groups)
	}
	if got.Orders[0].OrderID != "new" || got.Orders[1].OrderID != "old" {
		t.Fatalf("order records are not canonical: %+v", got.Orders)
	}
}

func TestAggregateInvoiceRejectsInvalidIdentityScopeAndDates(t *testing.T) {
	valid := invoiceOrder(t, "order", "2026-10-18", ClassDineIn, "dine-v1", 10, 100, 110)
	for name, input := range map[string][]InvoiceOrder{
		"empty order id":     {{Snapshot: valid.Snapshot}},
		"duplicate order id": {valid, valid},
		"cross scope": {func() InvoiceOrder {
			otherScope := Scope{TenantID: "tenant-b", StoreID: "store-a"}
			snapshot, err := Calculate(schedule(), day("2026-10-18"), otherScope, []LineInput{{Scope: otherScope, LineID: "foreign", TaxClass: ClassDineIn, BaseGrossJPY: 110, Quantity: 1}})
			if err != nil {
				t.Fatal(err)
			}
			return InvoiceOrder{OrderID: "foreign", Snapshot: snapshot.Record()}
		}()},
		"future order": {invoiceOrder(t, "future", "2027-04-01", ClassDineIn, "dine-v2", 12, 100, 112)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := AggregateInvoice(scope(), day("2026-10-18"), input); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error=%v, want ErrInvalidInput", err)
			}
		})
	}
	if _, err := AggregateInvoice(scope(), day("2026-10-18"), nil); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("empty invoice error=%v", err)
	}
}

func TestAggregateInvoiceRejectsConflictingHistoricalRuleIdentity(t *testing.T) {
	first := invoiceOrder(t, "old", "2026-10-18", ClassDineIn, "same-id", 10, 100, 110)
	second := invoiceOrder(t, "new", "2027-04-01", ClassDineIn, "same-id", 12, 100, 112)
	if _, err := AggregateInvoice(scope(), day("2027-04-01"), []InvoiceOrder{first, second}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("conflicting rule identity error=%v", err)
	}
}

func TestAggregateInvoiceRejectsTamperedSnapshotAndRecord(t *testing.T) {
	order := invoiceOrder(t, "order", "2026-10-18", ClassDineIn, "dine-v1", 10, 100, 110)
	order.Snapshot.Lines[0].GrossJPY++
	if _, err := AggregateInvoice(scope(), day("2026-10-18"), []InvoiceOrder{order}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("tampered order snapshot error=%v", err)
	}

	valid := invoiceOrder(t, "order", "2026-10-18", ClassDineIn, "dine-v1", 10, 100, 110)
	record, err := AggregateInvoice(scope(), day("2026-10-18"), []InvoiceOrder{valid})
	if err != nil {
		t.Fatal(err)
	}
	record.Total.TaxJPY++
	if err := record.Validate(); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("tampered invoice record error=%v", err)
	}
}

func TestAggregateInvoiceChecksOverflow(t *testing.T) {
	orders := []InvoiceOrder{
		invoiceOrder(t, "large-a", "2026-10-18", ClassDineIn, "dine-v1", 10, 100, math.MaxInt64),
		invoiceOrder(t, "large-b", "2026-10-18", ClassDineIn, "dine-v1", 10, 100, 1),
	}
	if _, err := AggregateInvoice(scope(), day("2026-10-18"), orders); !errors.Is(err, ErrOverflow) {
		t.Fatalf("overflow error=%v", err)
	}
}

func TestInvoiceRecordJSONRoundTripValidatesAndCopies(t *testing.T) {
	orders := []InvoiceOrder{
		invoiceOrder(t, "b", "2026-10-18", ClassDineIn, "dine-v1", 10, 100, 6),
		invoiceOrder(t, "a", "2026-10-18", ClassDineIn, "dine-v1", 10, 100, 6),
	}
	record, err := AggregateInvoice(scope(), day("2026-10-18"), orders)
	if err != nil {
		t.Fatal(err)
	}
	orders[0].Snapshot.Lines[0].Rule.ID = "mutated"
	if record.Orders[0].Snapshot.Lines[0].Rule.ID != "dine-v1" {
		t.Fatal("invoice record aliases input snapshots")
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	var restored InvoiceRecord
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Total != record.Total || restored.Orders[0].Snapshot.Lines[0].Rule.ID != "dine-v1" {
		t.Fatalf("round trip changed invoice: %+v", restored)
	}

	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	total := wire["total"].(map[string]any)
	total["tax_jpy"] = float64(record.Total.TaxJPY + 1)
	tampered, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(tampered, &restored); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("tampered invoice JSON error=%v", err)
	}
}
