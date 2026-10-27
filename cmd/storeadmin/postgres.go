package main

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

// postgresStore is the production storeAPI. Mode changes and cash settings go
// through their owning services so storeadmin cannot diverge from the server.
type postgresStore struct {
	pool     *pgxpool.Pool
	sessions *tablesession.Service
	cash     *operations.Service
}

func newPostgresStore(pool *pgxpool.Pool) (*postgresStore, error) {
	if pool == nil {
		return nil, errors.New("PostgreSQL pool is required")
	}
	cash, err := operations.New(pool)
	if err != nil {
		return nil, err
	}
	// storeadmin never opens a session or issues a PIN; these options only
	// satisfy tablesession.New's validation, and the pepper is never reused.
	pepper := make([]byte, 32)
	if _, err := rand.Read(pepper); err != nil {
		return nil, err
	}
	sessions, err := tablesession.New(pool, tablesession.Options{
		SessionTTL:     time.Hour,
		GrantTTL:       time.Minute,
		GrantWindow:    time.Minute,
		GrantWindowMax: 1,
		PINTTL:         time.Minute,
		PINLockout:     time.Minute,
		PINMaxAttempts: 3,
		PINLength:      6,
		PINPepper:      pepper,
	}, tablesession.DenyClose{})
	if err != nil {
		return nil, err
	}
	return &postgresStore{pool: pool, sessions: sessions, cash: cash}, nil
}

func (p *postgresStore) Tenant(ctx context.Context, tenantID string) (named, bool, error) {
	var tenant named
	err := p.pool.QueryRow(ctx, `SELECT tenant_id,name FROM tenants WHERE tenant_id=$1`, tenantID).Scan(&tenant.ID, &tenant.Name)
	if errors.Is(err, pgx.ErrNoRows) {
		return named{}, false, nil
	}
	if err != nil {
		return named{}, false, err
	}
	return tenant, true, nil
}

func (p *postgresStore) Store(ctx context.Context, tenantID, storeID string) (storeRow, bool, error) {
	var store storeRow
	var mode string
	err := p.pool.QueryRow(ctx, `SELECT name,guest_display_name,qr_entry_mode FROM stores WHERE tenant_id=$1 AND store_id=$2`, tenantID, storeID).
		Scan(&store.Name, &store.GuestDisplayName, &mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return storeRow{}, false, nil
	}
	if err != nil {
		return storeRow{}, false, err
	}
	store.Mode = tablesession.Mode(mode)
	return store, true, nil
}

func (p *postgresStore) Tables(ctx context.Context, tenantID, storeID string) ([]tableRow, error) {
	rows, err := p.pool.Query(ctx, `SELECT table_id,name,public_locator FROM dining_tables WHERE tenant_id=$1 AND store_id=$2 ORDER BY table_id`, tenantID, storeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var tables []tableRow
	for rows.Next() {
		var table tableRow
		if err := rows.Scan(&table.ID, &table.Name, &table.Locator); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	return tables, rows.Err()
}

func (p *postgresStore) Cash(ctx context.Context, tenantID, storeID string) (operations.CashSettings, error) {
	return p.cash.ReadCashSettings(ctx, operations.StoreScope{TenantID: tenantID, StoreID: storeID})
}

// Apply writes tenant, store and tables in one transaction. The mode and cash
// services own their own transactions, so they run after the commit; a failure
// between the steps leaves a state the next apply converges from.
func (p *postgresStore) Apply(ctx context.Context, tenantID string, write plan) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES($1,$2)
		ON CONFLICT (tenant_id) DO UPDATE SET name=EXCLUDED.name`, tenantID, write.Tenant.Name); err != nil {
		return err
	}
	if write.GuestDisplayName != nil {
		_, err = tx.Exec(ctx, `INSERT INTO stores(tenant_id,store_id,name,qr_entry_mode,guest_display_name) VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (tenant_id,store_id) DO UPDATE SET name=EXCLUDED.name,guest_display_name=EXCLUDED.guest_display_name`,
			tenantID, write.StoreID, write.StoreName, string(write.Mode), *write.GuestDisplayName)
	} else {
		_, err = tx.Exec(ctx, `INSERT INTO stores(tenant_id,store_id,name,qr_entry_mode) VALUES($1,$2,$3,$4)
			ON CONFLICT (tenant_id,store_id) DO UPDATE SET name=EXCLUDED.name`,
			tenantID, write.StoreID, write.StoreName, string(write.Mode))
	}
	if err != nil {
		return err
	}
	for _, table := range write.NewTables {
		if _, err := tx.Exec(ctx, `INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES($1,$2,$3,$4,$5)
			ON CONFLICT (tenant_id,store_id,table_id) DO UPDATE SET name=EXCLUDED.name`,
			tenantID, write.StoreID, table.ID, table.Name, table.Locator); err != nil {
			return err
		}
	}
	for _, table := range write.Renamed {
		if _, err := tx.Exec(ctx, `UPDATE dining_tables SET name=$4 WHERE tenant_id=$1 AND store_id=$2 AND table_id=$3`,
			tenantID, write.StoreID, table.ID, table.Name); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// Re-read instead of trusting the pre-read plan: a concurrent first apply
	// may have created the store with another mode between our read and insert.
	var current string
	if err := p.pool.QueryRow(ctx, `SELECT qr_entry_mode FROM stores WHERE tenant_id=$1 AND store_id=$2`, tenantID, write.StoreID).Scan(&current); err != nil {
		return err
	}
	if tablesession.Mode(current) != write.Mode {
		if err := p.sessions.SetStoreMode(ctx, tenantID, write.StoreID, write.Mode); err != nil {
			return err
		}
	}
	if write.Cash != nil {
		scope := operations.StoreScope{TenantID: tenantID, StoreID: write.StoreID}
		if _, err := p.cash.SetCashSettings(ctx, scope, write.Cash.Actor, write.Cash.Enabled, write.Cash.Mode, write.Cash.Expected); err != nil {
			return err
		}
	}
	return nil
}

func (p *postgresStore) Rotate(ctx context.Context, tenantID, storeID, tableID, locator string) error {
	result, err := p.pool.Exec(ctx, `UPDATE dining_tables SET public_locator=$4 WHERE tenant_id=$1 AND store_id=$2 AND table_id=$3`,
		tenantID, storeID, tableID, locator)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errNotFound
	}
	return nil
}
