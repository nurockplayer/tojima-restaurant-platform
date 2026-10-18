package testguest

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
)

func TestStripeSandboxIdentityRejectsUnboundedInputs(t *testing.T) {
	for _, tenant := range []string{"D", "a", "real-merchant"} {
		_, err := NewWithOptions(context.Background(), "", Options{CashEnabled: true, CashMode: operations.PayLater, StripeSandboxTenant: tenant})
		if err == nil || err.Error() != "invalid synthetic Stripe Sandbox tenant" {
			t.Fatal("unbounded synthetic identity accepted")
		}
	}
}

func TestStripeSandboxSyntheticTenantsPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	for _, tenant := range []string{"A", "B", "C"} {
		t.Run(tenant, func(t *testing.T) {
			f, err := NewWithOptions(context.Background(), dsn, Options{CashEnabled: true, CashMode: operations.PayLater, StripeSandboxTenant: tenant})
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close(context.Background())
			suffix := strings.ToLower(tenant)
			if f.TenantID != "stripe-test-tenant-"+suffix || f.StoreID != "stripe-test-store-"+suffix || f.Locator != "stripe-test-table-"+suffix || !strings.HasPrefix(f.Schema, "p80_sandbox_") {
				t.Fatal("synthetic scope differs")
			}
		})
	}
}

func TestStripeSandboxDatabaseRejectsRemoteFallbacks(t *testing.T) {
	for _, tc := range []struct {
		dsn     string
		allowed bool
	}{
		{"host=127.0.0.1 port=5432 dbname=tojima_test user=postgres sslmode=disable", true},
		{"host=127.0.0.1,::1 port=5432,5432 dbname=tojima_test user=postgres sslmode=disable", true},
		{"host=127.0.0.1,remote.example port=1,5432 dbname=tojima_test user=postgres sslmode=disable", false},
		{"host=remote.example,127.0.0.1 port=5432,5432 dbname=tojima_test user=postgres sslmode=disable", false},
	} {
		cfg, err := pgxpool.ParseConfig(tc.dsn)
		if err != nil {
			t.Fatal("test database parsing failed")
		}
		if loopbackTestDatabaseHosts(cfg) != tc.allowed {
			t.Fatal("database fallback admission mismatch")
		}
	}
	if loopbackTestDatabaseHosts(nil) {
		t.Fatal("nil database config accepted")
	}
}
