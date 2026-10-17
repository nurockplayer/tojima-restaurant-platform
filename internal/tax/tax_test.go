package tax

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"
)

func day(s string) time.Time { d, _ := time.Parse("2006-01-02", s); return d }
func schedule() Schedule {
	return Schedule{
		{ID: "dine-v1", TaxClass: ClassDineIn, RateNumerator: 10, RateDenominator: 100, EffectiveFrom: day("2019-10-01"), LegalSource: "NTA 6303", LegalStatus: LegalStatusEnacted, Version: "jp-v1"},
		{ID: "takeaway-v1", TaxClass: ClassEligibleTakeaway, RateNumerator: 8, RateDenominator: 100, EffectiveFrom: day("2019-10-01"), LegalSource: "NTA 6303/6102", LegalStatus: LegalStatusEnacted, Version: "jp-v1"},
	}
}
func scope() Scope { return Scope{TenantID: "tenant-a", StoreID: "store-a"} }

func TestInclusiveTaxExamplesAndAggregateFloor(t *testing.T) {
	got, err := Calculate(schedule(), day("2026-10-18"), scope(), []LineInput{{Scope: scope(), LineID: "a", TaxClass: ClassDineIn, BaseGrossJPY: 1100, Quantity: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total() != (Totals{GrossJPY: 1100, NetJPY: 1000, TaxJPY: 100}) {
		t.Fatalf("10%% result: %+v", got.Total())
	}
	got, err = Calculate(schedule(), day("2026-10-18"), scope(), []LineInput{{Scope: scope(), LineID: "b", TaxClass: ClassEligibleTakeaway, BaseGrossJPY: 1080, Quantity: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total() != (Totals{GrossJPY: 1080, NetJPY: 1000, TaxJPY: 80}) {
		t.Fatalf("8%% result: %+v", got.Total())
	}
	got, err = Calculate(schedule(), day("2026-10-18"), scope(), []LineInput{
		{Scope: scope(), LineID: "c", TaxClass: ClassDineIn, BaseGrossJPY: 6, Quantity: 1},
		{Scope: scope(), LineID: "d", TaxClass: ClassDineIn, BaseGrossJPY: 6, Quantity: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total().TaxJPY != 1 {
		t.Fatalf("aggregate tax=%d, want floor(12*10/110)=1", got.Total().TaxJPY)
	}
}

func TestMixedRatesAndOptionGross(t *testing.T) {
	got, err := Calculate(schedule(), day("2026-10-18"), scope(), []LineInput{
		{Scope: scope(), LineID: "dine", TaxClass: ClassDineIn, BaseGrossJPY: 1000, OptionGrossJPY: 100, Quantity: 1},
		{Scope: scope(), LineID: "takeaway", TaxClass: ClassEligibleTakeaway, BaseGrossJPY: 1000, OptionGrossJPY: 80, Quantity: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Total() != (Totals{GrossJPY: 2180, NetJPY: 2000, TaxJPY: 180}) {
		t.Fatalf("mixed result: %+v", got.Total())
	}
	lines := got.Lines()
	if len(lines) != 2 || lines[0].GrossJPY() != 1100 || lines[1].GrossJPY() != 1080 {
		t.Fatalf("line inputs not retained: %+v", lines)
	}
	groups := got.Groups()
	if len(groups) != 2 || groups[0].Total().TaxJPY != 100 || groups[1].Total().TaxJPY != 80 {
		t.Fatalf("rate groups: %+v", groups)
	}
}

func TestScheduleEffectiveBoundariesAndPendingIgnored(t *testing.T) {
	s := schedule()
	end := day("2027-04-01")
	s[0].EffectiveTo = &end
	s = append(s, Rule{ID: "future-proposal", TaxClass: ClassDineIn, RateNumerator: 1, RateDenominator: 100, EffectiveFrom: end, LegalSource: "proposal", LegalStatus: LegalStatusProposed, Version: "proposal-v1"})
	if _, err := Calculate(s, day("2019-09-30"), scope(), []LineInput{{Scope: scope(), LineID: "l", TaxClass: ClassDineIn, BaseGrossJPY: 110, Quantity: 1}}); !errors.Is(err, ErrNoEffectiveRule) {
		t.Fatalf("before start error=%v", err)
	}
	old, err := Calculate(s, day("2027-03-31"), scope(), []LineInput{{Scope: scope(), LineID: "l", TaxClass: ClassDineIn, BaseGrossJPY: 110, Quantity: 1}})
	if err != nil || old.Lines()[0].Rule().ID() != "dine-v1" {
		t.Fatalf("pre-boundary rule=%v err=%v", old.Lines(), err)
	}
	if _, err := Calculate(s, end, scope(), []LineInput{{Scope: scope(), LineID: "l", TaxClass: ClassDineIn, BaseGrossJPY: 110, Quantity: 1}}); !errors.Is(err, ErrNoEffectiveRule) {
		t.Fatalf("pending row activated or gap accepted: %v", err)
	}
}

func TestResolveNormalizesJSTCalendarDateBeforeComparingUTCDateRules(t *testing.T) {
	starts := day("2027-04-01")
	rule := Rule{ID: "future", TaxClass: ClassDineIn, RateNumerator: 12, RateDenominator: 100, EffectiveFrom: starts, LegalSource: "test", LegalStatus: LegalStatusEnacted, Version: "test-v1"}
	jst := time.FixedZone("JST", 9*60*60)
	resolved, err := resolve(Schedule{rule}, ClassDineIn, time.Date(2027, 4, 1, 0, 0, 0, 0, jst))
	if err != nil || resolved.ID != rule.ID {
		t.Fatalf("JST midnight should match UTC DATE effective_from: rule=%+v err=%v", resolved, err)
	}
	if _, err := resolve(Schedule{rule}, ClassDineIn, time.Date(2027, 3, 31, 23, 59, 59, 0, jst)); !errors.Is(err, ErrNoEffectiveRule) {
		t.Fatalf("last pre-boundary JST instant matched future rule: %v", err)
	}
}

func TestRejectsUnknownOverlapScopeAndInvalidQuantity(t *testing.T) {
	base := []LineInput{{Scope: scope(), LineID: "l", TaxClass: ClassDineIn, BaseGrossJPY: 1, Quantity: 1}}
	unknown := append([]LineInput(nil), base...)
	unknown[0].TaxClass = "EXEMPT"
	if _, err := Calculate(schedule(), day("2026-10-18"), scope(), unknown); !errors.Is(err, ErrUnsupportedClass) {
		t.Fatalf("unknown class error=%v", err)
	}
	if _, err := Calculate(schedule(), day("2026-10-18"), scope(), []LineInput{{Scope: Scope{TenantID: "tenant-b", StoreID: "store-a"}, LineID: "l", TaxClass: ClassDineIn, BaseGrossJPY: 1, Quantity: 1}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("scope error=%v", err)
	}
	if _, err := Calculate(schedule(), day("2026-10-18"), scope(), []LineInput{{Scope: scope(), LineID: "l", TaxClass: ClassDineIn, BaseGrossJPY: 1, Quantity: 0}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("quantity error=%v", err)
	}
	overlap := schedule()
	overlap = append(overlap, overlap[0])
	overlap[2].ID = "duplicate-effective-rule"
	if _, err := Calculate(overlap, day("2026-10-18"), scope(), base); !errors.Is(err, ErrOverlappingRules) {
		t.Fatalf("overlap error=%v", err)
	}
}

func TestRejectsOverflow(t *testing.T) {
	for _, line := range []LineInput{
		{Scope: scope(), LineID: "l", TaxClass: ClassDineIn, BaseGrossJPY: math.MaxInt64, OptionGrossJPY: 1, Quantity: 1},
		{Scope: scope(), LineID: "l", TaxClass: ClassDineIn, BaseGrossJPY: math.MaxInt64, Quantity: 2},
	} {
		if _, err := Calculate(schedule(), day("2026-10-18"), scope(), []LineInput{line}); !errors.Is(err, ErrOverflow) {
			t.Fatalf("line %+v error=%v", line, err)
		}
	}
}

func TestSnapshotDoesNotAliasInputsScheduleOrAccessors(t *testing.T) {
	s := schedule()
	end := day("2030-01-01")
	s[0].EffectiveTo = &end
	inputs := []LineInput{{Scope: scope(), LineID: "before", TaxClass: ClassDineIn, BaseGrossJPY: 1100, OptionGrossJPY: 20, Quantity: 1}}
	got, err := Calculate(s, day("2026-10-18"), scope(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	inputs[0].BaseGrossJPY = 1
	s[0].ID = "mutated"
	*s[0].EffectiveTo = day("2020-01-01")
	lines := got.Lines()
	lines[0].lineID = "altered"
	groups := got.Groups()
	groups[0].rules[0].id = "altered"
	*groups[0].rules[0].effectiveTo = day("2020-01-01")
	if got.Lines()[0].LineID() != "before" || got.Lines()[0].BaseGrossJPY() != 1100 || got.Lines()[0].Rule().ID() != "dine-v1" || !got.Lines()[0].Rule().EffectiveTo().Equal(day("2030-01-01")) {
		t.Fatal("snapshot changed through aliased source or accessor")
	}
}

func TestSnapshotJSONRoundTripKeepsHistoricalRulesAndCopiesRecord(t *testing.T) {
	got, err := Calculate(schedule(), day("2026-10-18"), scope(), []LineInput{{Scope: scope(), LineID: "persisted", TaxClass: ClassDineIn, BaseGrossJPY: 1100, OptionGrossJPY: 40, Quantity: 2}})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var restored Snapshot
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Total() != got.Total() || restored.Lines()[0].Rule().ID() != "dine-v1" || restored.Lines()[0].GrossJPY() != 2280 {
		t.Fatalf("round trip lost snapshot: %+v", restored.Record())
	}
	r := got.Record()
	r.Lines[0].Rule.ID = "mutated"
	if got.Lines()[0].Rule().ID() != "dine-v1" {
		t.Fatal("record aliases snapshot")
	}
}

func TestSnapshotFromRecordRejectsCorruption(t *testing.T) {
	valid, err := Calculate(schedule(), day("2026-10-18"), scope(), []LineInput{{Scope: scope(), LineID: "persisted", TaxClass: ClassDineIn, BaseGrossJPY: 1100, OptionGrossJPY: 40, Quantity: 2}})
	if err != nil {
		t.Fatal(err)
	}
	mutations := map[string]func(*SnapshotRecord){
		"negative total":            func(r *SnapshotRecord) { r.Total.TaxJPY = -1 },
		"line gross":                func(r *SnapshotRecord) { r.Lines[0].GrossJPY++ },
		"group tax":                 func(r *SnapshotRecord) { r.Groups[0].Totals.TaxJPY++ },
		"class does not match rule": func(r *SnapshotRecord) { r.Lines[0].TaxClass = ClassEligibleTakeaway },
		"unknown rule version":      func(r *SnapshotRecord) { r.RuleVersion = "future-unreviewed-rule" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			record := valid.Record()
			mutate(&record)
			if _, err := SnapshotFromRecord(record); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("corrupt record error=%v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestRejectsMalformedOrExpiredEffectiveRules(t *testing.T) {
	base := schedule()
	bad := []struct {
		name string
		edit func(*Rule)
	}{
		{"zero start", func(r *Rule) { r.EffectiveFrom = time.Time{} }},
		{"end before start", func(r *Rule) { end := day("2019-09-30"); r.EffectiveTo = &end }},
		{"end equals start", func(r *Rule) { end := day("2019-10-01"); r.EffectiveTo = &end }},
		{"rate denominator overflows formula", func(r *Rule) { r.RateNumerator = 2; r.RateDenominator = math.MaxInt64 }},
	}
	for _, test := range bad {
		t.Run(test.name, func(t *testing.T) {
			s := append(Schedule(nil), base...)
			test.edit(&s[0])
			if _, err := Calculate(s, day("2026-10-18"), scope(), []LineInput{{Scope: scope(), LineID: "l", TaxClass: ClassDineIn, BaseGrossJPY: 100, Quantity: 1}}); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("malformed schedule error=%v, want ErrInvalidInput", err)
			}
		})
	}
	ended := day("2020-01-01")
	base[0].EffectiveTo = &ended
	if _, err := Calculate(base, day("2026-10-18"), scope(), []LineInput{{Scope: scope(), LineID: "l", TaxClass: ClassDineIn, BaseGrossJPY: 100, Quantity: 1}}); !errors.Is(err, ErrNoEffectiveRule) {
		t.Fatalf("expired legal rule error=%v, want ErrNoEffectiveRule", err)
	}
}

func TestSameNormalizedRateRoundsOnceAcrossClasses(t *testing.T) {
	s := schedule()
	s[1].RateNumerator = 20
	s[1].RateDenominator = 200
	got, err := Calculate(s, day("2026-10-18"), scope(), []LineInput{
		{Scope: scope(), LineID: "d", TaxClass: ClassDineIn, BaseGrossJPY: 6, Quantity: 1},
		{Scope: scope(), LineID: "t", TaxClass: ClassEligibleTakeaway, BaseGrossJPY: 6, Quantity: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	groups := got.Groups()
	if len(groups) != 1 || groups[0].Total().TaxJPY != 1 || len(groups[0].Rules()) != 2 {
		t.Fatalf("normalized rate aggregation failed: %+v", groups)
	}
}
