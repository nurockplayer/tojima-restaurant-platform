// Package tax calculates tax-inclusive JPY amounts using an explicit, dated
// legal schedule. It does not choose an invoice boundary; callers pass one
// scope at a time.
package tax

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"reflect"
	"sort"
	"time"
)

const RuleVersion = "jp-invoice-floor-v1"

const (
	ClassDineIn           = "JP_DINE_IN_FOOD"
	ClassEligibleTakeaway = "JP_ELIGIBLE_TAKEAWAY_FOOD"
	LegalStatusEnacted    = "ENACTED"
	LegalStatusProposed   = "PROPOSED"
	PriceModeTaxInclusive = "TAX_INCLUSIVE"
	RoundingModeFloor     = "FLOOR"
)

var (
	ErrUnsupportedClass = errors.New("unsupported tax class")
	ErrNoEffectiveRule  = errors.New("no effective enacted tax rule")
	ErrOverlappingRules = errors.New("overlapping effective tax rules")
	ErrInvalidInput     = errors.New("invalid tax input")
	ErrOverflow         = errors.New("tax amount overflow")
)

// Rule is a versioned legal schedule entry. EffectiveTo is exclusive; nil
// means no known end date. Proposed entries are retained but never resolved.
type Rule struct {
	ID              string
	TaxClass        string
	RateNumerator   int64
	RateDenominator int64
	EffectiveFrom   time.Time
	EffectiveTo     *time.Time
	LegalSource     string
	LegalStatus     string
	Version         string
}

// Schedule is an explicit set of legal rows. It has no implicit/default rate.
type Schedule []Rule

// LineInput carries server-resolved, tax-inclusive price inputs. OptionGross
// is the sum of selected option deltas per unit; neither amount comes from a
// customer-submitted total.
type LineInput struct {
	Scope          Scope
	LineID         string
	TaxClass       string
	BaseGrossJPY   int64
	OptionGrossJPY int64
	Quantity       int64
}

// Scope identifies one invoice boundary without relying on delimiter-based
// tenant/store concatenation.
type Scope struct {
	TenantID string `json:"tenant_id"`
	StoreID  string `json:"store_id"`
}

// Snapshot stores a point-in-time calculation. Its fields are private and its
// accessors return copies, so later changes to input slices or schedule rows
// cannot mutate the recorded result.
type Snapshot struct {
	scope       Scope
	date        time.Time
	priceMode   string
	rounding    string
	ruleVersion string
	lines       []LineSnapshot
	groups      []GroupSnapshot
	total       Totals
}

type LineSnapshot struct {
	lineID         string
	taxClass       string
	rule           RuleSnapshot
	baseGrossJPY   int64
	optionGrossJPY int64
	quantity       int64
	grossJPY       int64
}

type GroupSnapshot struct {
	rateNumerator   int64
	rateDenominator int64
	rules           []RuleSnapshot
	totals          Totals
}

type RuleSnapshot struct {
	id              string
	taxClass        string
	rateNumerator   int64
	rateDenominator int64
	effectiveFrom   time.Time
	effectiveTo     *time.Time
	legalSource     string
	legalStatus     string
	version         string
}

type Totals struct {
	GrossJPY int64 `json:"gross_jpy"`
	NetJPY   int64 `json:"net_jpy"`
	TaxJPY   int64 `json:"tax_jpy"`
}

func (s Snapshot) Scope() Scope            { return s.scope }
func (s Snapshot) Date() time.Time         { return s.date }
func (s Snapshot) PriceMode() string       { return s.priceMode }
func (s Snapshot) RoundingMode() string    { return s.rounding }
func (s Snapshot) RuleVersion() string     { return s.ruleVersion }
func (s Snapshot) Total() Totals           { return s.total }
func (s Snapshot) Lines() []LineSnapshot   { return cloneLines(s.lines) }
func (s Snapshot) Groups() []GroupSnapshot { return cloneGroups(s.groups) }

func (l LineSnapshot) LineID() string        { return l.lineID }
func (l LineSnapshot) TaxClass() string      { return l.taxClass }
func (l LineSnapshot) Rule() RuleSnapshot    { return cloneRuleSnapshot(l.rule) }
func (l LineSnapshot) BaseGrossJPY() int64   { return l.baseGrossJPY }
func (l LineSnapshot) OptionGrossJPY() int64 { return l.optionGrossJPY }
func (l LineSnapshot) Quantity() int64       { return l.quantity }
func (l LineSnapshot) GrossJPY() int64       { return l.grossJPY }

func (g GroupSnapshot) Rate() (int64, int64) { return g.rateNumerator, g.rateDenominator }
func (g GroupSnapshot) Rules() []RuleSnapshot {
	out := append([]RuleSnapshot(nil), g.rules...)
	for i := range out {
		out[i] = cloneRuleSnapshot(out[i])
	}
	return out
}
func (g GroupSnapshot) Total() Totals           { return g.totals }
func (r RuleSnapshot) ID() string               { return r.id }
func (r RuleSnapshot) TaxClass() string         { return r.taxClass }
func (r RuleSnapshot) Rate() (int64, int64)     { return r.rateNumerator, r.rateDenominator }
func (r RuleSnapshot) EffectiveFrom() time.Time { return r.effectiveFrom }
func (r RuleSnapshot) EffectiveTo() *time.Time  { return cloneDate(r.effectiveTo) }
func (r RuleSnapshot) LegalSource() string      { return r.legalSource }
func (r RuleSnapshot) LegalStatus() string      { return r.legalStatus }
func (r RuleSnapshot) Version() string          { return r.version }

// Calculate resolves each distinct class against the supplied legal schedule,
// then floors inclusive tax once per normalized rate for this invoice scope.
func Calculate(schedule Schedule, invoiceDate time.Time, scope Scope, inputs []LineInput) (Snapshot, error) {
	date := dateOnly(invoiceDate)
	if scope.TenantID == "" || scope.StoreID == "" || invoiceDate.IsZero() || len(inputs) == 0 {
		return Snapshot{}, fmt.Errorf("%w: scope, invoice date, and at least one line are required", ErrInvalidInput)
	}
	classes := make(map[string]Rule)
	lineGross := make([]int64, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	var grossTotal int64
	for i, line := range inputs {
		if line.Scope != scope || line.LineID == "" || line.TaxClass == "" || line.Quantity <= 0 || line.BaseGrossJPY < 0 || line.OptionGrossJPY < 0 {
			return Snapshot{}, fmt.Errorf("%w: line %d has invalid scope, identity, quantity, or price", ErrInvalidInput, i)
		}
		if _, exists := seen[line.LineID]; exists {
			return Snapshot{}, fmt.Errorf("%w: duplicate line id %q", ErrInvalidInput, line.LineID)
		}
		seen[line.LineID] = struct{}{}
		unit, ok := add(line.BaseGrossJPY, line.OptionGrossJPY)
		if !ok {
			return Snapshot{}, ErrOverflow
		}
		gross, ok := multiply(unit, line.Quantity)
		if !ok {
			return Snapshot{}, ErrOverflow
		}
		grossTotal, ok = add(grossTotal, gross)
		if !ok {
			return Snapshot{}, ErrOverflow
		}
		lineGross[i] = gross
		if _, known := classes[line.TaxClass]; !known {
			rule, err := resolve(schedule, line.TaxClass, date)
			if err != nil {
				return Snapshot{}, err
			}
			classes[line.TaxClass] = rule
		}
	}

	type rateKey struct{ numerator, denominator int64 }
	classGross := make(map[string]int64, len(classes))
	lines := make([]LineSnapshot, len(inputs))
	for i, in := range inputs {
		value, ok := add(classGross[in.TaxClass], lineGross[i])
		if !ok {
			return Snapshot{}, ErrOverflow
		}
		classGross[in.TaxClass] = value
		lines[i] = LineSnapshot{lineID: in.LineID, taxClass: in.TaxClass, rule: snapshotRule(classes[in.TaxClass]), baseGrossJPY: in.BaseGrossJPY, optionGrossJPY: in.OptionGrossJPY, quantity: in.Quantity, grossJPY: lineGross[i]}
	}
	rateGross := make(map[rateKey]int64)
	rateRules := make(map[rateKey]map[string]RuleSnapshot)
	rateFormula := make(map[rateKey]Rule)
	for class, rule := range classes {
		divisor := gcd(rule.RateNumerator, rule.RateDenominator)
		key := rateKey{rule.RateNumerator / divisor, rule.RateDenominator / divisor}
		value, ok := add(rateGross[key], classGross[class])
		if !ok {
			return Snapshot{}, ErrOverflow
		}
		rateGross[key] = value
		if _, exists := rateFormula[key]; !exists {
			rateFormula[key] = rule
		}
		if rateRules[key] == nil {
			rateRules[key] = make(map[string]RuleSnapshot)
		}
		rateRules[key][rule.ID] = snapshotRule(rule)
	}
	groups := make([]GroupSnapshot, 0, len(rateGross))
	var taxTotal int64
	rateKeys := make([]rateKey, 0, len(rateGross))
	for key := range rateGross {
		rateKeys = append(rateKeys, key)
	}
	sort.Slice(rateKeys, func(i, j int) bool {
		if rateKeys[i].numerator != rateKeys[j].numerator {
			return rateKeys[i].numerator < rateKeys[j].numerator
		}
		return rateKeys[i].denominator < rateKeys[j].denominator
	})
	for _, key := range rateKeys {
		gross := rateGross[key]
		formula := rateFormula[key]
		tax, err := inclusiveTax(gross, formula.RateNumerator, formula.RateDenominator)
		if err != nil {
			return Snapshot{}, err
		}
		net := gross - tax
		taxTotal, _ = add(taxTotal, tax)
		rules := make([]RuleSnapshot, 0, len(rateRules[key]))
		for _, rule := range rateRules[key] {
			rules = append(rules, rule)
		}
		sort.Slice(rules, func(i, j int) bool {
			if rules[i].taxClass != rules[j].taxClass {
				return rules[i].taxClass < rules[j].taxClass
			}
			return rules[i].id < rules[j].id
		})
		groups = append(groups, GroupSnapshot{rateNumerator: key.numerator, rateDenominator: key.denominator, rules: rules, totals: Totals{GrossJPY: gross, NetJPY: net, TaxJPY: tax}})
	}
	return Snapshot{scope: scope, date: date, priceMode: PriceModeTaxInclusive, rounding: RoundingModeFloor, ruleVersion: RuleVersion, lines: lines, groups: groups, total: Totals{GrossJPY: grossTotal, NetJPY: grossTotal - taxTotal, TaxJPY: taxTotal}}, nil
}

func resolve(schedule Schedule, class string, date time.Time) (Rule, error) {
	if class != ClassDineIn && class != ClassEligibleTakeaway {
		return Rule{}, fmt.Errorf("%w: %s", ErrUnsupportedClass, class)
	}
	if date.IsZero() {
		return Rule{}, fmt.Errorf("%w: invoice date is required", ErrInvalidInput)
	}
	date = dateOnly(date)
	var match *Rule
	for i := range schedule {
		r := &schedule[i]
		if r.TaxClass != class || r.LegalStatus != LegalStatusEnacted {
			continue
		}
		if err := validateRule(*r); err != nil {
			return Rule{}, err
		}
		if date.Before(dateOnly(r.EffectiveFrom)) {
			continue
		}
		if r.EffectiveTo != nil && !date.Before(dateOnly(*r.EffectiveTo)) {
			continue
		}
		if match != nil {
			return Rule{}, fmt.Errorf("%w: %s on %s", ErrOverlappingRules, class, date.Format("2006-01-02"))
		}
		match = r
	}
	if match == nil {
		return Rule{}, fmt.Errorf("%w: %s on %s", ErrNoEffectiveRule, class, date.Format("2006-01-02"))
	}
	return cloneRule(*match), nil
}

func validateRule(rule Rule) error {
	if rule.ID == "" || rule.Version == "" || rule.LegalSource == "" || rule.LegalStatus != LegalStatusEnacted || rule.EffectiveFrom.IsZero() || rule.RateNumerator <= 0 || rule.RateDenominator <= 0 || rule.RateNumerator >= rule.RateDenominator || rule.RateNumerator > math.MaxInt64-rule.RateDenominator {
		return fmt.Errorf("%w: malformed rule %q", ErrInvalidInput, rule.ID)
	}
	if rule.EffectiveTo != nil && (rule.EffectiveTo.IsZero() || !dateOnly(*rule.EffectiveTo).After(dateOnly(rule.EffectiveFrom))) {
		return fmt.Errorf("%w: invalid effective range for rule %q", ErrInvalidInput, rule.ID)
	}
	return nil
}

func inclusiveTax(gross, numerator, denominator int64) (int64, error) {
	divisor := denominator + numerator
	n := new(big.Int).Mul(big.NewInt(gross), big.NewInt(numerator))
	n.Quo(n, big.NewInt(divisor))
	if !n.IsInt64() {
		return 0, ErrOverflow
	}
	return n.Int64(), nil
}

func add(a, b int64) (int64, bool) {
	if b > math.MaxInt64-a {
		return 0, false
	}
	return a + b, true
}
func multiply(a, b int64) (int64, bool) {
	if a == 0 || b == 0 {
		return 0, true
	}
	if a > math.MaxInt64/b {
		return 0, false
	}
	return a * b, true
}
func gcd(a, b int64) int64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}
func dateOnly(t time.Time) time.Time {
	if t.IsZero() {
		return time.Time{}
	}
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
func cloneDate(d *time.Time) *time.Time {
	if d == nil {
		return nil
	}
	v := dateOnly(*d)
	return &v
}
func cloneRule(r Rule) Rule {
	r.EffectiveFrom = dateOnly(r.EffectiveFrom)
	r.EffectiveTo = cloneDate(r.EffectiveTo)
	return r
}
func snapshotRule(r Rule) RuleSnapshot {
	r = cloneRule(r)
	return RuleSnapshot{id: r.ID, taxClass: r.TaxClass, rateNumerator: r.RateNumerator, rateDenominator: r.RateDenominator, effectiveFrom: r.EffectiveFrom, effectiveTo: r.EffectiveTo, legalSource: r.LegalSource, legalStatus: r.LegalStatus, version: r.Version}
}
func cloneRuleSnapshot(r RuleSnapshot) RuleSnapshot {
	r.effectiveTo = cloneDate(r.effectiveTo)
	return r
}
func cloneLines(lines []LineSnapshot) []LineSnapshot {
	out := append([]LineSnapshot(nil), lines...)
	for i := range out {
		out[i].rule = cloneRuleSnapshot(out[i].rule)
	}
	return out
}
func cloneGroups(groups []GroupSnapshot) []GroupSnapshot {
	out := append([]GroupSnapshot(nil), groups...)
	for i := range out {
		out[i].rules = append([]RuleSnapshot(nil), out[i].rules...)
		for j := range out[i].rules {
			out[i].rules[j] = cloneRuleSnapshot(out[i].rules[j])
		}
	}
	return out
}

// SnapshotRecord is the durable representation for an order. It contains
// the calculation inputs and rules used, and can be reloaded without looking
// at the current legal schedule.
type SnapshotRecord struct {
	Scope        Scope         `json:"scope"`
	Date         time.Time     `json:"date"`
	PriceMode    string        `json:"price_mode"`
	RoundingMode string        `json:"rounding_mode"`
	RuleVersion  string        `json:"rule_version"`
	Lines        []LineRecord  `json:"lines"`
	Groups       []GroupRecord `json:"groups"`
	Total        Totals        `json:"total"`
}
type LineRecord struct {
	LineID         string     `json:"line_id"`
	TaxClass       string     `json:"tax_class"`
	Rule           RuleRecord `json:"rule"`
	BaseGrossJPY   int64      `json:"base_gross_jpy"`
	OptionGrossJPY int64      `json:"option_gross_jpy"`
	Quantity       int64      `json:"quantity"`
	GrossJPY       int64      `json:"gross_jpy"`
}
type GroupRecord struct {
	RateNumerator   int64        `json:"rate_numerator"`
	RateDenominator int64        `json:"rate_denominator"`
	Rules           []RuleRecord `json:"rules"`
	Totals          Totals       `json:"totals"`
}
type RuleRecord struct {
	ID              string     `json:"id"`
	TaxClass        string     `json:"tax_class"`
	RateNumerator   int64      `json:"rate_numerator"`
	RateDenominator int64      `json:"rate_denominator"`
	EffectiveFrom   time.Time  `json:"effective_from"`
	EffectiveTo     *time.Time `json:"effective_to,omitempty"`
	LegalSource     string     `json:"legal_source"`
	LegalStatus     string     `json:"legal_status"`
	Version         string     `json:"version"`
}

func (s Snapshot) Record() SnapshotRecord {
	r := SnapshotRecord{Scope: s.scope, Date: s.date, PriceMode: s.priceMode, RoundingMode: s.rounding, RuleVersion: s.ruleVersion, Total: s.total, Lines: make([]LineRecord, len(s.lines)), Groups: make([]GroupRecord, len(s.groups))}
	for i, l := range s.lines {
		r.Lines[i] = LineRecord{LineID: l.lineID, TaxClass: l.taxClass, Rule: ruleRecord(l.rule), BaseGrossJPY: l.baseGrossJPY, OptionGrossJPY: l.optionGrossJPY, Quantity: l.quantity, GrossJPY: l.grossJPY}
	}
	for i, g := range s.groups {
		gr := GroupRecord{RateNumerator: g.rateNumerator, RateDenominator: g.rateDenominator, Totals: g.totals, Rules: make([]RuleRecord, len(g.rules))}
		for j, rule := range g.rules {
			gr.Rules[j] = ruleRecord(rule)
		}
		r.Groups[i] = gr
	}
	return cloneRecord(r)
}
func (s Snapshot) MarshalJSON() ([]byte, error) { return json.Marshal(s.Record()) }
func SnapshotFromRecord(r SnapshotRecord) (Snapshot, error) {
	r = cloneRecord(r)
	if r.Scope.TenantID == "" || r.Scope.StoreID == "" || r.Date.IsZero() || r.PriceMode != PriceModeTaxInclusive || r.RoundingMode != RoundingModeFloor || r.RuleVersion != RuleVersion || len(r.Lines) == 0 || len(r.Groups) == 0 {
		return Snapshot{}, ErrInvalidInput
	}
	inputs := make([]LineInput, len(r.Lines))
	scheduleByClass := make(map[string]Rule)
	rulesByID := make(map[string]Rule)
	for i, l := range r.Lines {
		rule := ruleRecordToRule(l.Rule)
		if l.LineID == "" || l.TaxClass != rule.TaxClass || l.Quantity <= 0 || l.BaseGrossJPY < 0 || l.OptionGrossJPY < 0 || l.GrossJPY < 0 {
			return Snapshot{}, ErrInvalidInput
		}
		if err := validateRule(rule); err != nil || !ruleApplies(fromRuleRecord(l.Rule), l.TaxClass, r.Date) {
			return Snapshot{}, ErrInvalidInput
		}
		if prior, ok := rulesByID[rule.ID]; ok && !reflect.DeepEqual(prior, rule) {
			return Snapshot{}, ErrInvalidInput
		}
		rulesByID[rule.ID] = rule
		if prior, ok := scheduleByClass[l.TaxClass]; ok && !reflect.DeepEqual(prior, ruleRecordToRule(l.Rule)) {
			return Snapshot{}, ErrInvalidInput
		}
		scheduleByClass[l.TaxClass] = rule
		inputs[i] = LineInput{Scope: r.Scope, LineID: l.LineID, TaxClass: l.TaxClass, BaseGrossJPY: l.BaseGrossJPY, OptionGrossJPY: l.OptionGrossJPY, Quantity: l.Quantity}
	}
	schedule := make(Schedule, 0, len(scheduleByClass))
	for _, rule := range scheduleByClass {
		schedule = append(schedule, rule)
	}
	calculated, err := Calculate(schedule, r.Date, r.Scope, inputs)
	if err != nil {
		return Snapshot{}, fmt.Errorf("%w: historical record inputs are invalid: %v", ErrInvalidInput, err)
	}
	if !reflect.DeepEqual(calculated.Record(), r) {
		return Snapshot{}, fmt.Errorf("%w: historical snapshot does not match embedded rules and inputs", ErrInvalidInput)
	}
	return calculated, nil
}

func ruleRecordToRule(r RuleRecord) Rule {
	return Rule{ID: r.ID, TaxClass: r.TaxClass, RateNumerator: r.RateNumerator, RateDenominator: r.RateDenominator, EffectiveFrom: r.EffectiveFrom, EffectiveTo: r.EffectiveTo, LegalSource: r.LegalSource, LegalStatus: r.LegalStatus, Version: r.Version}
}

func ruleApplies(rule RuleSnapshot, class string, date time.Time) bool {
	return rule.taxClass == class && !date.Before(rule.effectiveFrom) && (rule.effectiveTo == nil || date.Before(*rule.effectiveTo))
}
func (s *Snapshot) UnmarshalJSON(data []byte) error {
	var record SnapshotRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return err
	}
	v, err := SnapshotFromRecord(record)
	if err == nil {
		*s = v
	}
	return err
}
func ruleRecord(r RuleSnapshot) RuleRecord {
	r = cloneRuleSnapshot(r)
	return RuleRecord{ID: r.id, TaxClass: r.taxClass, RateNumerator: r.rateNumerator, RateDenominator: r.rateDenominator, EffectiveFrom: r.effectiveFrom, EffectiveTo: r.effectiveTo, LegalSource: r.legalSource, LegalStatus: r.legalStatus, Version: r.version}
}
func fromRuleRecord(r RuleRecord) RuleSnapshot {
	return RuleSnapshot{id: r.ID, taxClass: r.TaxClass, rateNumerator: r.RateNumerator, rateDenominator: r.RateDenominator, effectiveFrom: dateOnly(r.EffectiveFrom), effectiveTo: cloneDate(r.EffectiveTo), legalSource: r.LegalSource, legalStatus: r.LegalStatus, version: r.Version}
}
func cloneRecord(r SnapshotRecord) SnapshotRecord {
	r.Date = dateOnly(r.Date)
	r.Lines = append([]LineRecord(nil), r.Lines...)
	r.Groups = append([]GroupRecord(nil), r.Groups...)
	for i := range r.Lines {
		r.Lines[i].Rule = cloneRuleRecord(r.Lines[i].Rule)
	}
	for i := range r.Groups {
		r.Groups[i].Rules = append([]RuleRecord(nil), r.Groups[i].Rules...)
		for j := range r.Groups[i].Rules {
			r.Groups[i].Rules[j] = cloneRuleRecord(r.Groups[i].Rules[j])
		}
	}
	return r
}
func cloneRuleRecord(r RuleRecord) RuleRecord {
	r.EffectiveFrom = dateOnly(r.EffectiveFrom)
	r.EffectiveTo = cloneDate(r.EffectiveTo)
	return r
}
