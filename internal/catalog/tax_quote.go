package catalog

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tax"
)

// MenuLine contains selections only. Prices, tax classes and rates cannot be
// supplied by a guest; they are resolved from the current published menu and
// central legal schedule. This MVP accepts dine-in orders only.
type MenuLine struct {
	LineID     string
	ProductKey string
	Options    []OptionSelection
	Quantity   int64
}

type QuotedOption struct {
	GroupKey      string `json:"group_key"`
	OptionKey     string `json:"option_key"`
	Name          string `json:"name"`
	PriceGrossJPY int64  `json:"price_gross_jpy"`
}

type QuotedLine struct {
	LineID       string         `json:"line_id"`
	ProductKey   string         `json:"product_key"`
	Name         string         `json:"name"`
	BaseGrossJPY int64          `json:"base_gross_jpy"`
	Options      []QuotedOption `json:"options"`
	Quantity     int64          `json:"quantity"`
}

// MenuQuote is a server calculation, not an issued invoice or order admission.
// P-78 must revalidate within order commit and persist the accepted snapshot;
// P-79/P-80 define which committed lines share a receipt/invoice boundary.
type MenuQuote struct {
	CatalogMode string       `json:"catalog_mode"`
	Revision    int64        `json:"revision"`
	Lines       []QuotedLine `json:"lines"`
	Tax         tax.Snapshot `json:"tax"`
}

// QuoteDineIn reads one consistent database snapshot for all menu prices,
// availability and effective legal rules. at is a trusted server instant,
// converted to the Japanese civil date; it is not a client-selected rate date.
// Authorization/session checks are supplied by the caller's owning stage.
func (s *Service) QuoteDineIn(ctx context.Context, scope StoreScope, at time.Time, selections []MenuLine) (MenuQuote, error) {
	if at.IsZero() || len(selections) == 0 {
		return MenuQuote{}, ErrInvalid
	}
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return MenuQuote{}, err
	}
	defer tx.Rollback(ctx)
	var enabled bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM stores s JOIN tenants n USING(tenant_id)
		WHERE s.tenant_id=$1 AND s.store_id=$2 AND s.enabled AND s.operating AND n.enabled)`, scope.TenantID, scope.StoreID).Scan(&enabled)
	if err != nil {
		return MenuQuote{}, err
	}
	if !enabled {
		return MenuQuote{}, ErrNotFound
	}
	menu, err := readStoreMenu(ctx, tx, scope)
	if err != nil {
		return MenuQuote{}, err
	}
	date := at.In(time.FixedZone("JST", 9*60*60))
	reader, err := tax.NewReader(tx)
	if err != nil {
		return MenuQuote{}, err
	}
	rule, err := reader.EffectiveRule(ctx, tax.ClassDineIn, date)
	if err != nil {
		return MenuQuote{}, err
	}
	products := flattenProducts(menu.CatalogData)
	result := MenuQuote{CatalogMode: menu.Mode, Revision: menu.Revision, Lines: make([]QuotedLine, 0, len(selections))}
	inputs := make([]tax.LineInput, 0, len(selections))
	taxScope := tax.Scope{TenantID: scope.TenantID, StoreID: scope.StoreID}
	for _, selection := range selections {
		priced, err := Quote(menu, selection.ProductKey, selection.Options)
		if err != nil {
			return MenuQuote{}, err
		}
		product := products[selection.ProductKey]
		line := QuotedLine{LineID: selection.LineID, ProductKey: product.Key, Name: product.Name,
			BaseGrossJPY: product.EffectivePriceMinor, Quantity: selection.Quantity, Options: make([]QuotedOption, 0, len(selection.Options))}
		for _, pick := range selection.Options {
			for _, group := range product.Groups {
				if group.Key != pick.GroupKey {
					continue
				}
				for _, option := range group.Options {
					if option.Key == pick.OptionKey {
						line.Options = append(line.Options, QuotedOption{GroupKey: group.Key, OptionKey: option.Key, Name: option.Name, PriceGrossJPY: option.PriceDeltaMinor})
					}
				}
			}
		}
		inputs = append(inputs, tax.LineInput{Scope: taxScope, LineID: selection.LineID, TaxClass: tax.ClassDineIn,
			BaseGrossJPY: product.EffectivePriceMinor, OptionGrossJPY: priced.UnitMinor - product.EffectivePriceMinor, Quantity: selection.Quantity})
		result.Lines = append(result.Lines, line)
	}
	result.Tax, err = tax.Calculate(tax.Schedule{rule}, date, taxScope, inputs)
	if err != nil {
		return MenuQuote{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return MenuQuote{}, err
	}
	return result, nil
}
