// Package testguest creates disposable, isolated data for local guest UI tests.
// It is never imported by the production server or used as an identity adapter.
package testguest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

type Fixture struct {
	Pool          *pgxpool.Pool
	Sessions      *tablesession.Service
	Menus         *catalog.Service
	Orders        *ordering.Service
	Locator       string
	Schema        string
	admin         *pgx.Conn
	schemaCreated bool
}

// New requires an explicit loopback test database and creates a random schema.
// TTLs are named local test values; they do not select production guest policy.
func New(ctx context.Context, dsn string) (f *Fixture, err error) {
	if dsn == "" {
		return nil, errors.New("TEST_DATABASE_URL is required")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid test database URL")
	}
	if h := cfg.ConnConfig.Host; h != "127.0.0.1" && h != "localhost" && h != "::1" {
		return nil, errors.New("guest fixture requires a loopback test database")
	}
	var raw [12]byte
	if _, err = rand.Read(raw[:]); err != nil {
		return nil, err
	}
	f = &Fixture{Locator: "test-table", Schema: "p78_guest_" + hex.EncodeToString(raw[:])}
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
	_, err = f.Pool.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES ('test-tenant','P78 test tenant');
	 INSERT INTO stores(tenant_id,store_id,name,qr_entry_mode) VALUES ('test-tenant','test-store','P78 test store','DIRECT_SCAN');
	 INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES ('test-tenant','test-store','table-01','テーブル 01','test-table')`)
	if err != nil {
		return nil, err
	}
	pepper := make([]byte, 32)
	if _, err = rand.Read(pepper); err != nil {
		return nil, err
	}
	f.Sessions, err = tablesession.New(f.Pool, tablesession.Options{SessionTTL: 4 * time.Hour, GrantTTL: time.Hour, GrantWindow: time.Minute, GrantWindowMax: 1000, PINTTL: 15 * time.Minute, PINLockout: time.Minute, PINMaxAttempts: 3, PINLength: 6, PINPepper: pepper}, nil)
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
	draft, e := f.Menus.SaveCompanyDraft(ctx, "test-tenant", "fixture-bootstrap", 0, data)
	if e != nil {
		return nil, e
	}
	if _, err = f.Menus.PublishCompanyDraft(ctx, "test-tenant", "fixture-bootstrap", draft.Version); err != nil {
		return nil, err
	}
	scope := catalog.StoreScope{TenantID: "test-tenant", StoreID: "test-store"}
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
	f.Orders, err = ordering.New(f.Pool, f.Sessions, f.Menus)
	if err != nil {
		return nil, err
	}
	return f, nil
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
