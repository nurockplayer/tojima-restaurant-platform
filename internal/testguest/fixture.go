// Package testguest creates disposable, isolated data for local guest UI tests.
// It is never imported by the production server or used as an identity adapter.
package testguest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/docs/demo"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

type Fixture struct {
	Pool          *pgxpool.Pool
	Sessions      *tablesession.Service
	Menus         *catalog.Service
	Orders        *ordering.Service
	Operations    *operations.Service
	Locator       string
	TenantID      string
	StoreID       string
	TableID       string
	Schema        string
	admin         *pgx.Conn
	schemaCreated bool
}

// Options are explicit test-fixture settings and never select production
// restaurant defaults.
type Options struct {
	GuestDisplayName      *string
	CashEnabled           bool
	CashMode              operations.CashMode
	DemoShironagasukujira bool
	StripeSandboxTenant   string
}

// New requires an explicit loopback test database and creates a random schema.
// TTLs are named local test values; they do not select production guest policy.
func New(ctx context.Context, dsn string) (f *Fixture, err error) {
	// Existing guest API tests exercise CASH checkout. Keep that compatibility
	// visible as a fixture choice; production cash settings remain fail-closed.
	return NewWithOptions(ctx, dsn, Options{CashEnabled: true, CashMode: operations.PayLater})
}

func NewWithOptions(ctx context.Context, dsn string, options Options) (f *Fixture, err error) {
	if options.CashMode != operations.Prepaid && options.CashMode != operations.PayLater {
		return nil, errors.New("invalid test cash mode")
	}
	if options.DemoShironagasukujira && (!options.CashEnabled || options.CashMode != operations.PayLater) {
		return nil, errors.New("Shironagasukujira demo requires the explicit synthetic CASH/PAY_LATER test setting")
	}
	if options.StripeSandboxTenant != "" && (options.DemoShironagasukujira || (options.StripeSandboxTenant != "A" && options.StripeSandboxTenant != "B" && options.StripeSandboxTenant != "C")) {
		return nil, errors.New("invalid synthetic Stripe Sandbox tenant")
	}
	if dsn == "" {
		return nil, errors.New("TEST_DATABASE_URL is required")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid test database URL")
	}
	if !loopbackTestDatabaseHosts(cfg) {
		return nil, errors.New("guest fixture requires a loopback test database")
	}
	var raw [12]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return nil, err
	}
	f = &Fixture{Locator: "test-table", TenantID: "test-tenant", StoreID: "test-store", TableID: "table-01", Schema: "p78_guest_" + hex.EncodeToString(raw[:])}
	tenantName, storeName, tableName := "P78 test tenant", "P78 test store", "テーブル 01"
	if options.DemoShironagasukujira {
		f.Schema = "p85_demo_" + hex.EncodeToString(raw[:])
		f.TenantID, f.StoreID, f.TableID = "demo-synthetic", "demo-shironagasukujira", "table-05"
		f.Locator = "demo-shironagasukujira-table-05"
		tenantName, storeName, tableName = "Synthetic demo tenant", "DEMO — 博多餃子酒場 シロナガスクジラ（historical reference）", "テーブル 05"
	}
	if options.StripeSandboxTenant != "" {
		suffix := strings.ToLower(options.StripeSandboxTenant)
		f.Schema = "p80_sandbox_" + hex.EncodeToString(raw[:])
		f.TenantID, f.StoreID, f.TableID = "stripe-test-tenant-"+suffix, "stripe-test-store-"+suffix, "stripe-test-table-"+suffix
		f.Locator = "stripe-test-table-" + suffix
		tenantName, storeName, tableName = "Synthetic Stripe Sandbox tenant "+options.StripeSandboxTenant, "Synthetic JP JPY test store "+options.StripeSandboxTenant, "Synthetic test table"
	}
	f.admin, err = pgx.ConnectConfig(ctx, cfg.ConnConfig)
	if err != nil {
		return nil, err
	}
	created := f
	defer func() {
		if err != nil {
			created.Close(context.Background())
		}
	}()
	if _, err = f.admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{f.Schema}.Sanitize()); err != nil {
		return nil, err
	}
	f.schemaCreated = true
	cfg.ConnConfig.RuntimeParams["search_path"] = f.Schema
	f.Pool, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	conn, e := f.Pool.Acquire(ctx)
	if e != nil {
		return nil, e
	}
	err = postgres.Up(ctx, conn.Conn())
	conn.Release()
	if err != nil {
		return nil, err
	}
	_, err = f.Pool.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES ($1,$2)`, f.TenantID, tenantName)
	if err == nil {
		_, err = f.Pool.Exec(ctx, `INSERT INTO stores(tenant_id,store_id,name,qr_entry_mode,guest_display_name) VALUES ($1,$2,$3,'DIRECT_SCAN',$4)`, f.TenantID, f.StoreID, storeName, fixtureGuestDisplayName(options))
	}
	if err == nil {
		_, err = f.Pool.Exec(ctx, `INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES ($1,$2,$3,$4,$5)`, f.TenantID, f.StoreID, f.TableID, tableName, f.Locator)
	}
	if err != nil {
		return nil, err
	}
	pepper := make([]byte, 32)
	if _, err = rand.Read(pepper); err != nil {
		return nil, err
	}
	f.Operations, err = operations.New(f.Pool)
	if err != nil {
		return nil, err
	}
	f.Sessions, err = tablesession.New(f.Pool, tablesession.Options{SessionTTL: 4 * time.Hour, GrantTTL: time.Hour, GrantWindow: time.Minute, GrantWindowMax: 1000, PINTTL: 15 * time.Minute, PINLockout: time.Minute, PINMaxAttempts: 3, PINLength: 6, PINPepper: pepper}, f.Operations)
	if err != nil {
		return nil, err
	}
	f.Menus, err = catalog.New(f.Pool)
	if err != nil {
		return nil, err
	}
	data := catalog.CatalogData{Categories: []catalog.Category{{Key: "meals", Name: "お食事", Products: []catalog.Product{
		{Key: "teishoku", Name: "東島の定食", Description: "焼き魚・小鉢・ごはん・お味噌汁", BasePriceMinor: 1100, Active: true, Groups: []catalog.OptionGroup{{Key: "rice", Name: "ごはんの量", MinSelect: 1, MaxSelect: 1, Options: []catalog.Option{{Key: "regular", Name: "普通", Active: true}, {Key: "large", Name: "大盛り", PriceDeltaMinor: 110, Active: true}}}}},
		{Key: "tea", Name: "ほうじ茶", Description: "香ばしい温かなお茶", BasePriceMinor: 330, Active: true, Groups: []catalog.OptionGroup{{Key: "aroma", Name: "香り（任意）", MinSelect: 0, MaxSelect: 1, Options: []catalog.Option{{Key: "yuzu", Name: "ゆず", Active: true}, {Key: "ginger", Name: "生姜", Active: true}}}}},
	}}}}
	if options.DemoShironagasukujira {
		evidence, loadErr := demo.LoadShironagasukujira()
		if loadErr != nil {
			return nil, fmt.Errorf("load Shironagasukujira demo evidence: %w", loadErr)
		}
		data, err = shironagasukujiraCatalogData(evidence)
		if err != nil {
			return nil, err
		}
	}
	draft, e := f.Menus.SaveCompanyDraft(ctx, f.TenantID, "fixture-bootstrap", 0, data)
	if e != nil {
		return nil, e
	}
	if _, err = f.Menus.PublishCompanyDraft(ctx, f.TenantID, "fixture-bootstrap", draft.Version); err != nil {
		return nil, err
	}
	scope := catalog.StoreScope{TenantID: f.TenantID, StoreID: f.StoreID}
	m, e := f.Menus.ReadStoreMenu(ctx, scope)
	if e != nil {
		return nil, e
	}
	for _, c := range m.Categories {
		for _, p := range c.Products {
			_, err = f.Menus.SetStoreAvailability(ctx, scope, "fixture-bootstrap", catalog.AvailabilityChange{Target: catalog.AvailabilityTarget{Kind: catalog.ProductAvailability, ProductKey: p.Key}, Status: catalog.Available, ExpectedVersion: p.AvailabilityVersion})
			if err != nil {
				return nil, err
			}
			for _, g := range p.Groups {
				for _, o := range g.Options {
					_, err = f.Menus.SetStoreAvailability(ctx, scope, "fixture-bootstrap", catalog.AvailabilityChange{Target: catalog.AvailabilityTarget{Kind: catalog.OptionAvailability, ProductKey: p.Key, GroupKey: g.Key, OptionKey: o.Key}, Status: catalog.Available, ExpectedVersion: o.AvailabilityVersion})
					if err != nil {
						return nil, err
					}
				}
			}
		}
	}
	f.Orders, err = ordering.NewWithOrderPolicy(f.Pool, f.Sessions, f.Menus, f.Operations)
	if err != nil {
		return nil, err
	}
	if _, err = f.Operations.SetCashSettings(ctx, operations.StoreScope{TenantID: f.TenantID, StoreID: f.StoreID}, "fixture-bootstrap", options.CashEnabled, options.CashMode, 0); err != nil {
		return nil, err
	}
	return f, nil
}

func shironagasukujiraCatalogData(evidence demo.Evidence) (catalog.CatalogData, error) {
	if err := demo.ValidateShironagasukujira(evidence); err != nil {
		return catalog.CatalogData{}, err
	}
	categoryPositions := make(map[string]int, len(evidence.Categories))
	categories := make([]catalog.Category, len(evidence.Categories))
	for i, category := range evidence.Categories {
		categoryPositions[category.Key] = i
		categories[i] = catalog.Category{Key: category.Key, Name: category.LabelJA, Position: i, Products: []catalog.Product{}}
	}
	for _, item := range evidence.Items {
		if !item.OrderableInDemo || item.ReferenceChannel != "DINE_IN_HISTORICAL" || item.ReferencePriceJPY == nil {
			continue
		}
		position, found := categoryPositions[item.CategoryKey]
		if !found {
			return catalog.CatalogData{}, demo.ErrInvalidEvidence
		}
		sourceLabel, ok := shironagasukujiraSourceLabel(item.SourceID)
		if !ok {
			return catalog.CatalogData{}, demo.ErrInvalidEvidence
		}
		basePrice := *item.ReferencePriceJPY
		product := catalog.Product{
			Key:            strings.ToLower(strings.ReplaceAll(item.DemoKey, "-", "_")),
			Name:           item.NameJA,
			Description:    fmt.Sprintf("DEMO / 歴史資料の参考価格・現行店内価格未確認 — %s", sourceLabel),
			BasePriceMinor: basePrice, Active: true, Position: len(categories[position].Products),
			Groups: []catalog.OptionGroup{},
		}
		if item.DemoKey == "LUNCH-SHIRO" {
			group := evidence.DemoOptionGroups[0]
			regularPrice := *item.ReferencePriceJPY + group.Options[0].PriceDeltaJPY
			smallPrice := *item.ReferencePriceJPY + group.Options[1].PriceDeltaJPY
			regularAdjustment := regularPrice - smallPrice
			product.BasePriceMinor = smallPrice
			product.Description += fmt.Sprintf("；ごはん普通%d円・小（100g）%d円", regularPrice, smallPrice)
			product.Groups = []catalog.OptionGroup{{
				Key: "rice", Name: group.NameJA, MinSelect: 1, MaxSelect: 1, Position: 0,
				Options: []catalog.Option{
					{Key: group.Options[0].Key, Name: group.Options[0].NameJA, PriceDeltaMinor: regularAdjustment, Position: 0, Active: true},
					{Key: group.Options[1].Key, Name: group.Options[1].NameJA, PriceDeltaMinor: 0, Position: 1, Active: true},
				},
			}}
		}
		categories[position].Products = append(categories[position].Products, product)
	}
	orderableCategories := make([]catalog.Category, 0, len(categories))
	for _, category := range categories {
		if len(category.Products) != 0 {
			orderableCategories = append(orderableCategories, category)
		}
	}
	return catalog.CatalogData{Categories: orderableCategories}, nil
}

func shironagasukujiraSourceLabel(sourceID string) (string, bool) {
	labels := map[string]string{
		"tabelog_menu_photo":  "食べログの過去の投稿メニュー写真",
		"tabelog_2026_review": "2026年6月の口コミ",
		"dinner_2025_blog":    "2025年2月の食記",
	}
	label, ok := labels[sourceID]
	return label, ok
}

func (f *Fixture) Close(ctx context.Context) {
	if f.Pool != nil {
		f.Pool.Close()
	}
	if f.admin != nil {
		if f.schemaCreated {
			_, _ = f.admin.Exec(ctx, "DROP SCHEMA IF EXISTS "+pgx.Identifier{f.Schema}.Sanitize()+" CASCADE")
		}
		_ = f.admin.Close(ctx)
	}
}

// pgx can connect to a later host when the primary is unreachable. Validate the
// entire parsed candidate set before opening any connection or creating schemas.
func loopbackTestDatabaseHosts(cfg *pgxpool.Config) bool {
	if cfg == nil || cfg.ConnConfig == nil || !loopbackTestDatabaseHost(cfg.ConnConfig.Host) {
		return false
	}
	for _, fallback := range cfg.ConnConfig.Fallbacks {
		if fallback == nil || !loopbackTestDatabaseHost(fallback.Host) {
			return false
		}
	}
	return true
}

func loopbackTestDatabaseHost(host string) bool {
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}

// The demo constant remains the only display-name source for demo fixtures.
func fixtureGuestDisplayName(options Options) *string {
	if options.DemoShironagasukujira {
		return nil
	}
	return options.GuestDisplayName
}
