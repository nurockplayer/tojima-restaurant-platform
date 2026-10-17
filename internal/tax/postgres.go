package tax

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Queryer is the small pgx surface needed by Reader; *pgxpool.Pool and
// pgx.Tx both satisfy it.
type Queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type Reader struct{ db Queryer }

func NewReader(db Queryer) (*Reader, error) {
	if db == nil {
		return nil, fmt.Errorf("%w: nil tax database", ErrInvalidInput)
	}
	return &Reader{db: db}, nil
}

// EffectiveRule returns the sole enacted row applicable on the requested
// date. Multiple matches fail closed so schedule mistakes cannot silently
// select an arbitrary rate. Proposed rows are deliberately excluded.
func (r *Reader) EffectiveRule(ctx context.Context, class string, date time.Time) (Rule, error) {
	if r == nil || r.db == nil || date.IsZero() {
		return Rule{}, ErrInvalidInput
	}
	rows, err := r.db.Query(ctx, `SELECT tax_rule_id, tax_class, rate_numerator, rate_denominator,
       effective_from, effective_to, legal_source, legal_status, rule_version
FROM tax_rate_schedule
WHERE tax_class = $1 AND legal_status = 'ENACTED'
  AND effective_from <= $2::date
  AND (effective_to IS NULL OR $2::date < effective_to)
ORDER BY effective_from, tax_rule_id
LIMIT 2`, class, dateOnly(date))
	if err != nil {
		return Rule{}, err
	}
	defer rows.Close()
	var found *Rule
	for rows.Next() {
		var row Rule
		if err := rows.Scan(&row.ID, &row.TaxClass, &row.RateNumerator, &row.RateDenominator, &row.EffectiveFrom, &row.EffectiveTo, &row.LegalSource, &row.LegalStatus, &row.Version); err != nil {
			return Rule{}, err
		}
		if found != nil {
			return Rule{}, fmt.Errorf("%w: %s on %s", ErrOverlappingRules, class, dateOnly(date).Format("2006-01-02"))
		}
		row = cloneRule(row)
		found = &row
	}
	if err := rows.Err(); err != nil {
		return Rule{}, err
	}
	if found == nil {
		return Rule{}, fmt.Errorf("%w: %s on %s", ErrNoEffectiveRule, class, dateOnly(date).Format("2006-01-02"))
	}
	// Run the same class and rule validation as the pure calculator.
	validated, err := resolve(Schedule{*found}, class, date)
	if err != nil {
		return Rule{}, err
	}
	return validated, nil
}
