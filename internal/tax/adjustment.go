package tax

// ReduceHistorical recomputes retained quantities using only the validated
// transaction snapshot. Zero lines are omitted; nil denotes an empty invoice.
// The source snapshot (and its unit prices and rule identities) is never edited.
func ReduceHistorical(original SnapshotRecord, quantities []int64) (*SnapshotRecord, error) {
	if _, err := SnapshotFromRecord(original); err != nil {
		return nil, err
	}
	if len(quantities) != len(original.Lines) {
		return nil, ErrInvalidInput
	}
	inputs := []LineInput{}
	rules := map[string]Rule{}
	for i, l := range original.Lines {
		q := quantities[i]
		if q < 0 || q > l.Quantity {
			return nil, ErrInvalidInput
		}
		if q == 0 {
			continue
		}
		inputs = append(inputs, LineInput{Scope: original.Scope, LineID: l.LineID, TaxClass: l.TaxClass, BaseGrossJPY: l.BaseGrossJPY, OptionGrossJPY: l.OptionGrossJPY, Quantity: q})
		rules[l.Rule.ID] = ruleRecordToRule(l.Rule)
	}
	if len(inputs) == 0 {
		return nil, nil
	}
	schedule := make(Schedule, 0, len(rules))
	for _, r := range rules {
		schedule = append(schedule, r)
	}
	calculated, err := Calculate(schedule, original.Date, original.Scope, inputs)
	if err != nil {
		return nil, err
	}
	r := calculated.Record()
	return &r, nil
}
