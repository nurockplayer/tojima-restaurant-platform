package tax

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"
)

// InvoiceOrder binds an immutable historical order tax snapshot to its stable
// order identity. Snapshot dates and rules are retained even when a later
// settlement invoice combines orders priced under different legal schedules.
type InvoiceOrder struct {
	OrderID  string         `json:"order_id"`
	Snapshot SnapshotRecord `json:"snapshot"`
}

// InvoiceRateGroup contains one floor-rounded tax result for a normalized
// rate. Rules lists every distinct historical rule used by the included lines
// at that rate; per-order line-to-rule provenance remains in Orders.
type InvoiceRateGroup struct {
	RateNumerator   int64        `json:"rate_numerator"`
	RateDenominator int64        `json:"rate_denominator"`
	Rules           []RuleRecord `json:"rules"`
	Totals          Totals       `json:"totals"`
}

// InvoiceRecord is the durable tax calculation for one settlement invoice.
// Its JSON decoder and encoder validate that stored totals still follow from
// the embedded, independently validated historical order snapshots.
type InvoiceRecord struct {
	Scope        Scope              `json:"scope"`
	Date         time.Time          `json:"date"`
	Orders       []InvoiceOrder     `json:"orders"`
	Groups       []InvoiceRateGroup `json:"groups"`
	Total        Totals             `json:"total"`
	PriceMode    string             `json:"price_mode"`
	RoundingMode string             `json:"rounding_mode"`
	RuleVersion  string             `json:"rule_version"`
}

// AggregateInvoice calculates inclusive tax across the order snapshots using
// one floor per normalized rate for the whole invoice. It never consults a
// current schedule: each order's validated embedded rule is authoritative.
func AggregateInvoice(scope Scope, invoiceDate time.Time, orders []InvoiceOrder) (InvoiceRecord, error) {
	if strings.TrimSpace(scope.TenantID) == "" || strings.TrimSpace(scope.StoreID) == "" || invoiceDate.IsZero() || len(orders) == 0 {
		return InvoiceRecord{}, fmt.Errorf("%w: invoice scope, date, and orders are required", ErrInvalidInput)
	}
	date := dateOnly(invoiceDate)
	canonicalOrders := make([]InvoiceOrder, 0, len(orders))
	seenOrders := make(map[string]struct{}, len(orders))
	type rateKey struct{ numerator, denominator int64 }
	type aggregate struct {
		gross int64
		rules map[string]RuleRecord
	}
	byRate := make(map[rateKey]*aggregate)
	allRules := make(map[string]RuleRecord)

	for i, order := range orders {
		if strings.TrimSpace(order.OrderID) == "" {
			return InvoiceRecord{}, fmt.Errorf("%w: order %d has empty identity", ErrInvalidInput, i)
		}
		if _, exists := seenOrders[order.OrderID]; exists {
			return InvoiceRecord{}, fmt.Errorf("%w: duplicate order id %q", ErrInvalidInput, order.OrderID)
		}
		seenOrders[order.OrderID] = struct{}{}

		validated, err := SnapshotFromRecord(order.Snapshot)
		if err != nil {
			return InvoiceRecord{}, fmt.Errorf("%w: invalid snapshot for order %q", ErrInvalidInput, order.OrderID)
		}
		if validated.Scope() != scope {
			return InvoiceRecord{}, fmt.Errorf("%w: order %q has a different invoice scope", ErrInvalidInput, order.OrderID)
		}
		if dateOnly(validated.Date()).After(date) {
			return InvoiceRecord{}, fmt.Errorf("%w: order %q is after invoice date", ErrInvalidInput, order.OrderID)
		}
		record := validated.Record()
		canonicalOrders = append(canonicalOrders, InvoiceOrder{OrderID: order.OrderID, Snapshot: record})
		for _, line := range record.Lines {
			rule := cloneRuleRecord(line.Rule)
			if prior, ok := allRules[rule.ID]; ok && !reflect.DeepEqual(prior, rule) {
				return InvoiceRecord{}, fmt.Errorf("%w: conflicting historical rule identity %q", ErrInvalidInput, rule.ID)
			}
			allRules[rule.ID] = rule
			divisor := gcd(rule.RateNumerator, rule.RateDenominator)
			key := rateKey{rule.RateNumerator / divisor, rule.RateDenominator / divisor}
			group := byRate[key]
			if group == nil {
				group = &aggregate{rules: make(map[string]RuleRecord)}
				byRate[key] = group
			}
			gross, ok := add(group.gross, line.GrossJPY)
			if !ok {
				return InvoiceRecord{}, ErrOverflow
			}
			group.gross = gross
			group.rules[rule.ID] = rule
		}
	}

	sort.Slice(canonicalOrders, func(i, j int) bool { return canonicalOrders[i].OrderID < canonicalOrders[j].OrderID })
	keys := make([]rateKey, 0, len(byRate))
	for key := range byRate {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].numerator != keys[j].numerator {
			return keys[i].numerator < keys[j].numerator
		}
		return keys[i].denominator < keys[j].denominator
	})

	groups := make([]InvoiceRateGroup, 0, len(keys))
	var grossTotal, taxTotal int64
	for _, key := range keys {
		entry := byRate[key]
		taxJPY, err := inclusiveTax(entry.gross, key.numerator, key.denominator)
		if err != nil {
			return InvoiceRecord{}, err
		}
		var ok bool
		grossTotal, ok = add(grossTotal, entry.gross)
		if !ok {
			return InvoiceRecord{}, ErrOverflow
		}
		taxTotal, ok = add(taxTotal, taxJPY)
		if !ok {
			return InvoiceRecord{}, ErrOverflow
		}
		rules := make([]RuleRecord, 0, len(entry.rules))
		for _, rule := range entry.rules {
			rules = append(rules, cloneRuleRecord(rule))
		}
		sort.Slice(rules, func(i, j int) bool {
			if rules[i].TaxClass != rules[j].TaxClass {
				return rules[i].TaxClass < rules[j].TaxClass
			}
			return rules[i].ID < rules[j].ID
		})
		groups = append(groups, InvoiceRateGroup{
			RateNumerator: key.numerator, RateDenominator: key.denominator,
			Rules: rules, Totals: Totals{GrossJPY: entry.gross, NetJPY: entry.gross - taxJPY, TaxJPY: taxJPY},
		})
	}
	if grossTotal < 0 || taxTotal < 0 || taxTotal > grossTotal {
		return InvoiceRecord{}, ErrOverflow
	}
	return InvoiceRecord{
		Scope: scope, Date: date, Orders: canonicalOrders, Groups: groups,
		Total:     Totals{GrossJPY: grossTotal, NetJPY: grossTotal - taxTotal, TaxJPY: taxTotal},
		PriceMode: PriceModeTaxInclusive, RoundingMode: RoundingModeFloor, RuleVersion: RuleVersion,
	}, nil
}

// Validate recomputes the record from its embedded historical snapshots.
func (r InvoiceRecord) Validate() error {
	calculated, err := AggregateInvoice(r.Scope, r.Date, r.Orders)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(calculated, cloneInvoiceRecord(r)) {
		return fmt.Errorf("%w: invoice record does not match historical orders", ErrInvalidInput)
	}
	return nil
}

func (r InvoiceRecord) MarshalJSON() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	type wire InvoiceRecord
	return json.Marshal(wire(cloneInvoiceRecord(r)))
}

func (r *InvoiceRecord) UnmarshalJSON(data []byte) error {
	if r == nil {
		return ErrInvalidInput
	}
	type wire InvoiceRecord
	var decoded wire
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	value := cloneInvoiceRecord(InvoiceRecord(decoded))
	if err := value.Validate(); err != nil {
		return err
	}
	*r = value
	return nil
}

func cloneInvoiceRecord(r InvoiceRecord) InvoiceRecord {
	r.Date = dateOnly(r.Date)
	r.Orders = append([]InvoiceOrder(nil), r.Orders...)
	for i := range r.Orders {
		r.Orders[i].Snapshot = cloneRecord(r.Orders[i].Snapshot)
	}
	r.Groups = append([]InvoiceRateGroup(nil), r.Groups...)
	for i := range r.Groups {
		r.Groups[i].Rules = append([]RuleRecord(nil), r.Groups[i].Rules...)
		for j := range r.Groups[i].Rules {
			r.Groups[i].Rules[j] = cloneRuleRecord(r.Groups[i].Rules[j])
		}
	}
	return r
}
