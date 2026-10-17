package catalog

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tax"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

func TestPostgresDineInTaxQuoteAndHistoricalScheduleSnapshot(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	random := make([]byte, 8)
	if _, err = rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "p77_tax_quote_" + hex.EncodeToString(random)
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, dropErr := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); dropErr != nil {
			t.Errorf("drop owned tax quote test schema: %v", dropErr)
		}
	}()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = postgres.Up(ctx, conn.Conn())
	conn.Release()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES ('t','Tenant'),('other','Other');
		INSERT INTO stores(tenant_id,store_id,name) VALUES ('t','s','Store'),('other','s','Other store')`); err != nil {
		t.Fatal(err)
	}
	service, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	scope := StoreScope{TenantID: "t", StoreID: "s"}
	otherScope := StoreScope{TenantID: "other", StoreID: "s"}
	if _, err = service.SaveCompanyDraft(ctx, "t", "owner", 0, taxQuoteMenu(1100)); err != nil {
		t.Fatal(err)
	}
	if _, err = service.PublishCompanyDraft(ctx, "t", "owner", 1); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SaveCompanyDraft(ctx, "other", "owner", 0, taxQuoteForeignMenu()); err != nil {
		t.Fatal(err)
	}
	if _, err = service.PublishCompanyDraft(ctx, "other", "owner", 1); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"meal", "six-a", "six-b"} {
		if _, err = service.SetStoreAvailability(ctx, scope, "manager", AvailabilityChange{Target: AvailabilityTarget{Kind: ProductAvailability, ProductKey: key}, Status: Available, ExpectedVersion: 1}); err != nil {
			t.Fatalf("enable product %s: %v", key, err)
		}
	}
	if _, err = service.SetStoreAvailability(ctx, scope, "manager", AvailabilityChange{Target: AvailabilityTarget{Kind: OptionAvailability, ProductKey: "meal", GroupKey: "extras", OptionKey: "egg"}, Status: Available, ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SetStoreAvailability(ctx, otherScope, "manager", AvailabilityChange{Target: AvailabilityTarget{Kind: ProductAvailability, ProductKey: "foreign"}, Status: Available, ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.SetStorePriceOverride(ctx, scope, "manager", "meal", 1200, 0); err != nil {
		t.Fatal(err)
	}

	t.Run("seeded legal rates and date selection", func(t *testing.T) {
		reader, err := tax.NewReader(pool)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			class string
			gross int64
			want  int64
		}{
			{tax.ClassDineIn, 1100, 100},
			{tax.ClassEligibleTakeaway, 1080, 80},
		} {
			rule, readErr := reader.EffectiveRule(ctx, tc.class, time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
			if readErr != nil {
				t.Fatalf("read seeded %s rule: %v", tc.class, readErr)
			}
			got, calcErr := tax.Calculate(tax.Schedule{rule}, time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC), tax.Scope{TenantID: "t", StoreID: "s"}, []tax.LineInput{{Scope: tax.Scope{TenantID: "t", StoreID: "s"}, LineID: "sample", TaxClass: tc.class, BaseGrossJPY: tc.gross, Quantity: 1}})
			if calcErr != nil || got.Total().TaxJPY != tc.want {
				t.Fatalf("%s gross %d: tax=%d err=%v, want %d", tc.class, tc.gross, got.Total().TaxJPY, calcErr, tc.want)
			}
		}
		if _, err = reader.EffectiveRule(ctx, tax.ClassDineIn, time.Date(2019, 9, 30, 12, 0, 0, 0, time.UTC)); !errors.Is(err, tax.ErrNoEffectiveRule) {
			t.Fatalf("pre-enactment date returned rule: %v", err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO tax_rate_schedule(tax_rule_id,tax_class,rate_numerator,rate_denominator,effective_from,legal_source,legal_status,rule_version)
			VALUES ('tax-quote-proposed','JP_DINE_IN_FOOD',1,100,DATE '2019-10-01','test proposal','PROPOSED','proposal-only')`); err != nil {
			t.Fatal(err)
		}
		rule, err := reader.EffectiveRule(ctx, tax.ClassDineIn, time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC))
		if err != nil || rule.ID != "jp-standard-10-2019-10-01" {
			t.Fatalf("proposal displaced enacted rule: id=%q err=%v", rule.ID, err)
		}
		if _, err = pool.Exec(ctx, `INSERT INTO tax_rate_schedule(tax_rule_id,tax_class,rate_numerator,rate_denominator,effective_from,legal_source,legal_status,rule_version)
			VALUES ('tax-quote-overlap','JP_DINE_IN_FOOD',9,100,DATE '2020-01-01','test overlap','ENACTED','test-only')`); err != nil {
			t.Fatal(err)
		}
		if _, err = reader.EffectiveRule(ctx, tax.ClassDineIn, time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)); !errors.Is(err, tax.ErrOverlappingRules) {
			t.Fatalf("overlapping enacted rows did not fail closed: %v", err)
		}
		if _, err = pool.Exec(ctx, `DELETE FROM tax_rate_schedule WHERE tax_rule_id='tax-quote-overlap'`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("published prices options quantities and scope are server resolved", func(t *testing.T) {
		at := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
		quote, err := service.QuoteDineIn(ctx, scope, at, []MenuLine{{LineID: "meal-line", ProductKey: "meal", Options: []OptionSelection{{GroupKey: "extras", OptionKey: "egg"}}, Quantity: 2}})
		if err != nil {
			t.Fatal(err)
		}
		if len(quote.Lines) != 1 || quote.Lines[0].BaseGrossJPY != 1200 || quote.Lines[0].Options[0].PriceGrossJPY != 25 || quote.Lines[0].Quantity != 2 {
			t.Fatalf("quote did not use published option and store override: %+v", quote.Lines)
		}
		if quote.Tax.Total().GrossJPY != 2450 || quote.Tax.Total().TaxJPY != 222 || quote.Tax.Total().NetJPY != 2228 {
			t.Fatalf("server calculated incorrect inclusive quote: %+v", quote.Tax.Total())
		}
		rounded, err := service.QuoteDineIn(ctx, scope, at, []MenuLine{
			{LineID: "six-a", ProductKey: "six-a", Quantity: 1},
			{LineID: "six-b", ProductKey: "six-b", Quantity: 1},
		})
		if err != nil || rounded.Tax.Total().GrossJPY != 12 || rounded.Tax.Total().TaxJPY != 1 {
			t.Fatalf("per-rate invoice aggregation quote=%+v err=%v", rounded.Tax.Total(), err)
		}
		if _, err = service.QuoteDineIn(ctx, scope, at, []MenuLine{{LineID: "missing", ProductKey: "missing", Quantity: 1}}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("nonexistent product accepted: %v", err)
		}
		if _, err = service.QuoteDineIn(ctx, scope, at, []MenuLine{{LineID: "foreign", ProductKey: "foreign", Quantity: 1}}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-tenant product accepted: %v", err)
		}
		if _, err = service.QuoteDineIn(ctx, scope, at, []MenuLine{{LineID: "unknown-option", ProductKey: "meal", Options: []OptionSelection{{GroupKey: "extras", OptionKey: "unknown"}}, Quantity: 1}}); !errors.Is(err, ErrDenied) {
			t.Fatalf("unknown option accepted: %v", err)
		}
		if _, err = service.QuoteDineIn(ctx, scope, at, []MenuLine{{LineID: "negative", ProductKey: "six-a", Quantity: -1}}); !errors.Is(err, tax.ErrInvalidInput) {
			t.Fatalf("negative quantity accepted: %v", err)
		}
		if _, err = service.QuoteDineIn(ctx, scope, at, []MenuLine{{LineID: "overflow", ProductKey: "six-a", Quantity: math.MaxInt64}}); !errors.Is(err, tax.ErrOverflow) {
			t.Fatalf("overflowing quantity accepted: %v", err)
		}
	})

	t.Run("JST effective boundary and historical snapshot survive schedule change", func(t *testing.T) {
		beforeBoundary := time.Date(2027, 3, 31, 14, 59, 59, 0, time.UTC)
		oldQuote, err := service.QuoteDineIn(ctx, scope, beforeBoundary, []MenuLine{{LineID: "historical", ProductKey: "meal", Options: []OptionSelection{{GroupKey: "extras", OptionKey: "egg"}}, Quantity: 2}})
		if err != nil {
			t.Fatal(err)
		}
		oldJSON, err := json.Marshal(oldQuote.Tax)
		if err != nil {
			t.Fatal(err)
		}
		if oldQuote.Tax.Total().TaxJPY != 222 || oldQuote.Tax.Lines()[0].Rule().ID() != "jp-standard-10-2019-10-01" {
			t.Fatalf("pre-boundary quote did not use old rule: total=%+v rule=%q", oldQuote.Tax.Total(), oldQuote.Tax.Lines()[0].Rule().ID())
		}
		if _, err = pool.Exec(ctx, `UPDATE tax_rate_schedule SET effective_to=DATE '2027-04-01' WHERE tax_rule_id='jp-standard-10-2019-10-01';
			INSERT INTO tax_rate_schedule(tax_rule_id,tax_class,rate_numerator,rate_denominator,effective_from,legal_source,legal_status,rule_version)
			VALUES ('tax-quote-future-approved','JP_DINE_IN_FOOD',12,100,DATE '2027-04-01','synthetic integration-test approved schedule row','ENACTED','test-synthetic-v1')`); err != nil {
			t.Fatal(err)
		}
		boundary := time.Date(2027, 3, 31, 15, 0, 0, 0, time.UTC) // 2027-04-01 00:00 JST
		newQuote, err := service.QuoteDineIn(ctx, scope, boundary, []MenuLine{{LineID: "new-date", ProductKey: "meal", Options: []OptionSelection{{GroupKey: "extras", OptionKey: "egg"}}, Quantity: 2}})
		if err != nil {
			t.Fatal(err)
		}
		if newQuote.Tax.Total().GrossJPY != oldQuote.Tax.Total().GrossJPY || newQuote.Tax.Total().TaxJPY != 262 || newQuote.Tax.Lines()[0].Rule().ID() != "tax-quote-future-approved" {
			t.Fatalf("new effective date did not apply future test rule to same catalog prices: %+v rule=%q", newQuote.Tax.Total(), newQuote.Tax.Lines()[0].Rule().ID())
		}
		var restored tax.Snapshot
		if err = json.Unmarshal(oldJSON, &restored); err != nil {
			t.Fatalf("restore historical tax snapshot after schedule change: %v", err)
		}
		if restored.Total() != oldQuote.Tax.Total() || restored.Lines()[0].Rule().ID() != "jp-standard-10-2019-10-01" {
			t.Fatalf("historical snapshot changed after legal schedule update: %+v rule=%q", restored.Total(), restored.Lines()[0].Rule().ID())
		}
	})

	if _, err = pool.Exec(ctx, `UPDATE stores SET enabled=false WHERE tenant_id='t' AND store_id='s'`); err != nil {
		t.Fatal(err)
	}
	if _, err = service.QuoteDineIn(ctx, scope, time.Now(), []MenuLine{{LineID: "disabled", ProductKey: "meal", Quantity: 1}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled store accepted a quote: %v", err)
	}
}

func taxQuoteMenu(mealPrice int64) CatalogData {
	return CatalogData{Categories: []Category{{Key: "food", Name: "Food", Products: []Product{
		{Key: "meal", Name: "Meal", BasePriceMinor: mealPrice, Active: true, Groups: []OptionGroup{{Key: "extras", Name: "Extras", MinSelect: 0, MaxSelect: 1, Options: []Option{{Key: "egg", Name: "Egg", PriceDeltaMinor: 25, Active: true}}}}},
		{Key: "six-a", Name: "Six A", BasePriceMinor: 6, Active: true},
		{Key: "six-b", Name: "Six B", BasePriceMinor: 6, Active: true},
	}}}}
}

func taxQuoteForeignMenu() CatalogData {
	return CatalogData{Categories: []Category{{Key: "foreign-food", Name: "Food", Products: []Product{{Key: "foreign", Name: "Foreign product", BasePriceMinor: 500, Active: true}}}}}
}
