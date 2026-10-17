package tablesession

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

type allowClose struct{}

func (allowClose) CheckClose(context.Context, pgx.Tx, Session) error { return nil }

func TestPostgresSessionAcceptance(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	random := make([]byte, 8)
	if _, err = rand.Read(random); err != nil {
		t.Fatal(err)
	}
	schema := "p76_test_" + hex.EncodeToString(random)
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, dropErr := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); dropErr != nil {
			t.Errorf("drop owned test schema: %v", dropErr)
		}
	}()
	poolConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = postgres.Up(ctx, conn.Conn()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	if err = postgres.Down(ctx, conn.Conn()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	var gone bool
	if err = conn.QueryRow(ctx, "SELECT to_regclass('table_sessions') IS NULL").Scan(&gone); err != nil || !gone {
		conn.Release()
		t.Fatalf("down migration did not remove table_sessions: gone=%v err=%v", gone, err)
	}
	if err = postgres.Reapply(ctx, conn.Conn()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()
	if _, err = pool.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES ('tenant-a','A'),('tenant-b','B');
		INSERT INTO stores(tenant_id,store_id,name,qr_entry_mode) VALUES ('tenant-a','store-a','A','STAFF_OPEN'),('tenant-b','store-b','B','DIRECT_SCAN');
		INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES
		('tenant-a','store-a','table-1','1','public-a'),('tenant-a','store-a','table-2','2','public-b'),('tenant-a','store-a','table-3','3','public-rate'),('tenant-a','store-a','table-4','4','public-fail'),('tenant-b','store-b','table-1','1','public-c');`); err != nil {
		t.Fatal(err)
	}
	options := testOptions()
	service, err := New(pool, options, allowClose{})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("concurrent staff opens have one session and three clients join it", func(t *testing.T) {
		scope := Scope{"tenant-a", "store-a", "table-1"}
		start := make(chan struct{})
		results := make(chan error, 2)
		ids := make(chan string, 2)
		for range 2 {
			go func() {
				<-start
				opened, openErr := service.Open(ctx, scope, "staff-1")
				results <- openErr
				if openErr == nil {
					ids <- opened.Session.ID
				}
			}()
		}
		close(start)
		var successes int
		var sessionID string
		for range 2 {
			if e := <-results; e == nil {
				successes++
				sessionID = <-ids
			} else if !errors.Is(e, ErrConflict) {
				t.Fatalf("unexpected concurrent open: %v", e)
			}
		}
		if successes != 1 {
			t.Fatalf("successful opens=%d", successes)
		}
		var wg sync.WaitGroup
		grants := make(chan Grant, 3)
		errs := make(chan error, 3)
		for range 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				g, e := service.Join(ctx, sessionID, sessionID, "")
				if e != nil {
					errs <- e
					return
				}
				grants <- g
			}()
		}
		wg.Wait()
		close(grants)
		close(errs)
		for e := range errs {
			t.Fatal(e)
		}
		tokens := make([]string, 0, 3)
		for g := range grants {
			if g.Session.ID != sessionID {
				t.Fatalf("join created another session: %s", g.Session.ID)
			}
			tokens = append(tokens, g.Token)
		}
		if len(tokens) != 3 {
			t.Fatalf("join grants=%d", len(tokens))
		}
		if _, e := service.Join(ctx, sessionID, "old-stale-session", ""); !errors.Is(e, ErrDenied) {
			t.Fatalf("expected stale expected_session_id deny, got %v", e)
		}
		if e := service.Close(ctx, Scope{"tenant-b", "store-b", "table-1"}, sessionID, "staff-x"); !errors.Is(e, ErrNotFound) {
			t.Fatalf("cross-scope close should be hidden, got %v", e)
		}
		if e := service.Close(ctx, scope, sessionID, "staff-1"); e != nil {
			t.Fatal(e)
		}
		if _, e := pool.Exec(ctx, `UPDATE table_sessions SET status='OPEN',closed_at=NULL WHERE session_id=$1`, sessionID); e == nil {
			t.Fatal("database allowed a closed session to reopen")
		}
		if _, e := pool.Exec(ctx, `UPDATE table_sessions SET qr_mode='DIRECT_SCAN' WHERE session_id=$1`, sessionID); e == nil {
			t.Fatal("database allowed a session mode snapshot to change")
		}
		if _, e := service.Read(ctx, sessionID, tokens[0]); !errors.Is(e, ErrDenied) {
			t.Fatalf("closed session grant remained valid: %v", e)
		}
		opened, e := service.Open(ctx, scope, "staff-1")
		if e != nil {
			t.Fatal(e)
		}
		if opened.Session.ID == sessionID {
			t.Fatal("next seating reused session ID")
		}
		if _, e = service.Read(ctx, opened.Session.ID, tokens[0]); !errors.Is(e, ErrDenied) {
			t.Fatalf("old guest read next seating: %v", e)
		}
		if e = service.Close(ctx, scope, opened.Session.ID, "staff-1"); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("direct scan creates one staff acceptance session atomically", func(t *testing.T) {
		var wg sync.WaitGroup
		grants := make(chan Grant, 3)
		errs := make(chan error, 3)
		for range 3 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				g, e := service.DirectJoin(ctx, "public-c", "")
				if e != nil {
					errs <- e
					return
				}
				grants <- g
			}()
		}
		wg.Wait()
		close(grants)
		close(errs)
		for e := range errs {
			t.Fatal(e)
		}
		sessionIDs := map[string]int{}
		for g := range grants {
			sessionIDs[g.Session.ID]++
			if !g.Session.RequiresStaffAcceptance {
				t.Fatal("DIRECT_SCAN missing RequiresStaffAcceptance")
			}
		}
		if len(sessionIDs) != 1 {
			t.Fatalf("parallel direct scans created %d sessions", len(sessionIDs))
		}
		if _, e := service.Join(ctx, sessionIDsKey(sessionIDs), "", ""); e != nil {
			t.Fatalf("second direct client could not join active session: %v", e)
		}
	})
	t.Run("PIN lockout persists across service instances and reset revokes grants", func(t *testing.T) {
		if _, e := pool.Exec(ctx, `UPDATE stores SET qr_entry_mode='SESSION_PIN' WHERE tenant_id='tenant-a' AND store_id='store-a'`); e != nil {
			t.Fatal(e)
		}
		scope := Scope{"tenant-a", "store-a", "table-2"}
		opened, e := service.Open(ctx, scope, "staff-1")
		if e != nil {
			t.Fatal(e)
		}
		if opened.PIN == "" {
			t.Fatal("session PIN not generated")
		}
		oldGrant, e := service.Join(ctx, opened.Session.ID, "", opened.PIN)
		if e != nil {
			t.Fatal(e)
		}
		service2, e := New(pool, options, allowClose{})
		if e != nil {
			t.Fatal(e)
		}
		wrong := wrongPIN(opened.PIN)
		if _, e = service2.Join(ctx, opened.Session.ID, "", wrong); !errors.Is(e, ErrInvalidPIN) {
			t.Fatalf("first wrong PIN: %v", e)
		}
		if _, e = service.Join(ctx, opened.Session.ID, "", wrong); !errors.Is(e, ErrPINLocked) {
			t.Fatalf("lockout did not persist across instances: %v", e)
		}
		if _, e = service2.Join(ctx, opened.Session.ID, "", opened.PIN); !errors.Is(e, ErrPINLocked) {
			t.Fatalf("correct PIN bypassed lock: %v", e)
		}
		pinNumber, e := strconv.Atoi(opened.PIN)
		if e != nil {
			t.Fatal(e)
		}
		collisionOptions := options
		collisionOptions.Random = bytes.NewReader(bytes.Repeat([]byte{byte(pinNumber >> 8), byte(pinNumber)}, 8))
		collisionService, e := New(pool, collisionOptions, allowClose{})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = collisionService.ResetPIN(ctx, scope, opened.Session.ID, "staff-1"); e == nil {
			t.Fatal("reset accepted repeated generation of the existing PIN")
		}
		if _, e = service.Read(ctx, opened.Session.ID, oldGrant.Token); e != nil {
			t.Fatalf("failed reset revoked the prior grant: %v", e)
		}
		if _, e = service.Join(ctx, opened.Session.ID, "", opened.PIN); !errors.Is(e, ErrPINLocked) {
			t.Fatalf("failed reset cleared persisted lockout: %v", e)
		}
		reset, e := service.ResetPIN(ctx, scope, opened.Session.ID, "staff-1")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = service.Read(ctx, opened.Session.ID, oldGrant.Token); !errors.Is(e, ErrDenied) {
			t.Fatalf("PIN reset did not revoke prior grant: %v", e)
		}
		if _, e = service.Join(ctx, opened.Session.ID, "", opened.PIN); !errors.Is(e, ErrInvalidPIN) {
			t.Fatalf("old PIN remained valid after reset: %v", e)
		}
		if _, e = service.Join(ctx, opened.Session.ID, "", reset.PIN); e != nil {
			t.Fatalf("new PIN failed: %v", e)
		}
		if e = service.Close(ctx, scope, opened.Session.ID, "staff-1"); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("expired grants and PINs reject access", func(t *testing.T) {
		shortSvc, e := New(pool, options, allowClose{})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, `UPDATE stores SET qr_entry_mode='SESSION_PIN' WHERE tenant_id='tenant-a' AND store_id='store-a'`); e != nil {
			t.Fatal(e)
		}
		scope := Scope{"tenant-a", "store-a", "table-1"}
		opened, e := shortSvc.Open(ctx, scope, "staff-1")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, `UPDATE table_sessions SET pin_expires_at=clock_timestamp()-interval '1 second' WHERE session_id=$1`, opened.Session.ID); e != nil {
			t.Fatal(e)
		}
		if _, e = shortSvc.Join(ctx, opened.Session.ID, "", opened.PIN); !errors.Is(e, ErrInvalidPIN) {
			t.Fatalf("expired PIN accepted: %v", e)
		}
		if e = shortSvc.Close(ctx, scope, opened.Session.ID, "staff-1"); e != nil {
			t.Fatal(e)
		}
		grantSvc, e := New(pool, options, allowClose{})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, `UPDATE stores SET qr_entry_mode='STAFF_OPEN' WHERE tenant_id='tenant-a' AND store_id='store-a'`); e != nil {
			t.Fatal(e)
		}
		opened, e = grantSvc.Open(ctx, scope, "staff-1")
		if e != nil {
			t.Fatal(e)
		}
		grant, e := grantSvc.Join(ctx, opened.Session.ID, "", "")
		if e != nil {
			t.Fatal(e)
		}
		grantHash := sha256.Sum256([]byte(grant.Token))
		if _, e = pool.Exec(ctx, `UPDATE table_join_grants SET expires_at=clock_timestamp()-interval '1 second' WHERE token_hash=$1`, grantHash[:]); e != nil {
			t.Fatal(e)
		}
		if _, e = grantSvc.Read(ctx, opened.Session.ID, grant.Token); !errors.Is(e, ErrDenied) {
			t.Fatalf("expired grant accepted: %v", e)
		}
		if e = grantSvc.Close(ctx, scope, opened.Session.ID, "staff-1"); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("expired OPEN session denies every guest access and blocks replacement until gated close", func(t *testing.T) {
		if _, e := pool.Exec(ctx, `UPDATE stores SET qr_entry_mode='STAFF_OPEN' WHERE tenant_id='tenant-a' AND store_id='store-a'`); e != nil {
			t.Fatal(e)
		}
		expiring, e := New(pool, options, allowClose{})
		if e != nil {
			t.Fatal(e)
		}
		scope := Scope{"tenant-a", "store-a", "table-1"}
		opened, e := expiring.Open(ctx, scope, "staff-1")
		if e != nil {
			t.Fatal(e)
		}
		grant, e := expiring.Join(ctx, opened.Session.ID, opened.Session.ID, "")
		if e != nil {
			t.Fatal(e)
		}
		// The test schema deliberately bypasses the immutable snapshot trigger to
		// model expiry without relying on wall-clock sleeps or tiny TTLs.
		if _, e = pool.Exec(ctx, `ALTER TABLE table_sessions DISABLE TRIGGER table_session_snapshot_immutable`); e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, `UPDATE table_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE session_id=$1`, opened.Session.ID); e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, `ALTER TABLE table_sessions ENABLE TRIGGER table_session_snapshot_immutable`); e != nil {
			t.Fatal(e)
		}
		if _, e = expiring.Join(ctx, opened.Session.ID, "", ""); !errors.Is(e, ErrExpired) {
			t.Fatalf("expired session allowed join: %v", e)
		}
		if _, e = expiring.Read(ctx, opened.Session.ID, grant.Token); !errors.Is(e, ErrDenied) {
			t.Fatalf("expired session allowed capability read: %v", e)
		}
		public, e := expiring.PublicStatus(ctx, "public-a")
		if e != nil || !public.Expired || public.Open || public.CurrentSessionID != "" {
			t.Fatalf("expired locator state: %+v err=%v", public, e)
		}
		if _, e = expiring.Open(ctx, scope, "staff-1"); !errors.Is(e, ErrExpired) {
			t.Fatalf("expired OPEN row was replaced without close: %v", e)
		}
		defaultDeny, e := New(pool, options, nil)
		if e != nil {
			t.Fatal(e)
		}
		if e = defaultDeny.Close(ctx, scope, opened.Session.ID, "staff-1"); !errors.Is(e, ErrCloseBlocked) {
			t.Fatalf("default close gate failed open on expired session: %v", e)
		}
		if e = expiring.Close(ctx, scope, opened.Session.ID, "staff-1"); e != nil {
			t.Fatal(e)
		}
		next, e := expiring.Open(ctx, scope, "staff-1")
		if e != nil {
			t.Fatalf("new seating failed after policy-approved close: %v", e)
		}
		if next.Session.ID == opened.Session.ID {
			t.Fatal("expired seating ID reused")
		}
		if e = expiring.Close(ctx, scope, next.Session.ID, "staff-1"); e != nil {
			t.Fatal(e)
		}
		if e = expiring.SetStoreMode(ctx, scope.TenantID, scope.StoreID, SessionPIN); e != nil {
			t.Fatal(e)
		}
		pinSession, e := expiring.Open(ctx, scope, "staff-1")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, `ALTER TABLE table_sessions DISABLE TRIGGER table_session_snapshot_immutable`); e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, `UPDATE table_sessions SET expires_at=clock_timestamp()-interval '1 second' WHERE session_id=$1`, pinSession.Session.ID); e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, `ALTER TABLE table_sessions ENABLE TRIGGER table_session_snapshot_immutable`); e != nil {
			t.Fatal(e)
		}
		if _, e = expiring.ResetPIN(ctx, scope, pinSession.Session.ID, "staff-1"); !errors.Is(e, ErrExpired) {
			t.Fatalf("expired session PIN was reset: %v", e)
		}
		if e = expiring.Close(ctx, scope, pinSession.Session.ID, "staff-1"); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("join grant cap is persistent per table and shared by direct and session joins", func(t *testing.T) {
		limited := options
		limited.GrantWindowMax = 2
		firstService, e := New(pool, limited, allowClose{})
		if e != nil {
			t.Fatal(e)
		}
		secondService, e := New(pool, limited, allowClose{})
		if e != nil {
			t.Fatal(e)
		}
		if e = firstService.SetStoreMode(ctx, "tenant-a", "store-a", DirectScan); e != nil {
			t.Fatal(e)
		}
		staffOpened, e := firstService.Open(ctx, Scope{"tenant-a", "store-a", "table-3"}, "staff-1")
		if e != nil {
			t.Fatal(e)
		}
		if !staffOpened.Session.RequiresStaffAcceptance {
			t.Fatal("staff-opened DIRECT_SCAN session omitted acceptance snapshot")
		}
		if e = firstService.Close(ctx, staffOpened.Session.Scope, staffOpened.Session.ID, "staff-1"); e != nil {
			t.Fatal(e)
		}
		first, e := firstService.DirectJoin(ctx, "public-rate", "")
		if e != nil {
			t.Fatal(e)
		}
		if !first.Session.RequiresStaffAcceptance {
			t.Fatal("direct session omitted acceptance snapshot")
		}
		start := make(chan struct{})
		results := make(chan error, 6)
		var wg sync.WaitGroup
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				var joinErr error
				if i%2 == 0 {
					_, joinErr = firstService.DirectJoin(ctx, "public-rate", first.Session.ID)
				} else {
					_, joinErr = secondService.Join(ctx, first.Session.ID, first.Session.ID, "")
				}
				results <- joinErr
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)
		successes, limitedCount := 0, 0
		for e := range results {
			if e == nil {
				successes++
			} else if errors.Is(e, ErrRateLimited) {
				limitedCount++
			} else {
				t.Fatalf("unexpected concurrent grant result: %v", e)
			}
		}
		if successes != 1 || limitedCount != 5 {
			t.Fatalf("grant cap results success=%d limited=%d", successes, limitedCount)
		}
		if _, e = firstService.Join(ctx, first.Session.ID, first.Session.ID, ""); !errors.Is(e, ErrRateLimited) {
			t.Fatalf("rate cap did not persist across service instances: %v", e)
		}
		if e = firstService.Close(ctx, first.Session.Scope, first.Session.ID, "staff-1"); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("direct session and grant creation roll back together on entropy failure", func(t *testing.T) {
		if e := service.SetStoreMode(ctx, "tenant-a", "store-a", DirectScan); e != nil {
			t.Fatal(e)
		}
		failingOptions := options
		failingOptions.Random = bytes.NewReader(make([]byte, 16))
		failing, e := New(pool, failingOptions, allowClose{})
		if e != nil {
			t.Fatal(e)
		}
		if _, e = failing.DirectJoin(ctx, "public-fail", ""); e == nil {
			t.Fatal("direct join unexpectedly succeeded without token entropy")
		}
		var sessions, limits, grants int
		if e = pool.QueryRow(ctx, `SELECT count(*) FROM table_sessions WHERE tenant_id='tenant-a' AND store_id='store-a' AND table_id='table-4' AND status='OPEN'`).Scan(&sessions); e != nil {
			t.Fatal(e)
		}
		if e = pool.QueryRow(ctx, `SELECT count(*) FROM table_join_grant_limits WHERE tenant_id='tenant-a' AND store_id='store-a' AND table_id='table-4'`).Scan(&limits); e != nil {
			t.Fatal(e)
		}
		if e = pool.QueryRow(ctx, `SELECT count(*) FROM table_join_grants g JOIN table_sessions s USING(session_id) WHERE s.tenant_id='tenant-a' AND s.store_id='store-a' AND s.table_id='table-4'`).Scan(&grants); e != nil {
			t.Fatal(e)
		}
		if sessions != 0 || limits != 0 || grants != 0 {
			t.Fatalf("failed direct join left durable state: sessions=%d limits=%d grants=%d", sessions, limits, grants)
		}
		retry, e := service.DirectJoin(ctx, "public-fail", "")
		if e != nil {
			t.Fatalf("healthy retry after rollback failed: %v", e)
		}
		if e = service.Close(ctx, retry.Session.Scope, retry.Session.ID, "staff"); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("disabled tenant blocks guest and staff access", func(t *testing.T) {
		if e := service.SetStoreMode(ctx, "tenant-a", "store-a", DirectScan); e != nil {
			t.Fatal(e)
		}
		grant, e := service.DirectJoin(ctx, "public-b", "")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, `UPDATE tenants SET enabled=false WHERE tenant_id='tenant-a'`); e != nil {
			t.Fatal(e)
		}
		defer pool.Exec(ctx, `UPDATE tenants SET enabled=true WHERE tenant_id='tenant-a'`)
		if _, e = service.DirectJoin(ctx, "public-b", grant.Session.ID); !errors.Is(e, ErrDenied) {
			t.Fatalf("disabled tenant allowed direct join: %v", e)
		}
		if _, e = service.Join(ctx, grant.Session.ID, grant.Session.ID, ""); !errors.Is(e, ErrDenied) {
			t.Fatalf("disabled tenant allowed session join: %v", e)
		}
		if _, e = service.Read(ctx, grant.Session.ID, grant.Token); !errors.Is(e, ErrDenied) {
			t.Fatalf("disabled tenant allowed guest read: %v", e)
		}
		if _, e = service.ReadStaff(ctx, grant.Session.Scope, grant.Session.ID); !errors.Is(e, ErrNotFound) {
			t.Fatalf("disabled tenant allowed staff read: %v", e)
		}
		if _, e = service.Open(ctx, Scope{"tenant-a", "store-a", "table-1"}, "staff-1"); !errors.Is(e, ErrDenied) {
			t.Fatalf("disabled tenant allowed staff open: %v", e)
		}
		if e = service.Close(ctx, grant.Session.Scope, grant.Session.ID, "staff-1"); !errors.Is(e, ErrNotFound) {
			t.Fatalf("disabled tenant allowed close mutation: %v", e)
		}
		if _, e = pool.Exec(ctx, `UPDATE tenants SET enabled=true WHERE tenant_id='tenant-a'`); e != nil {
			t.Fatal(e)
		}
		if e = service.Close(ctx, grant.Session.Scope, grant.Session.ID, "staff-1"); e != nil {
			t.Fatal(e)
		}
	})
	t.Run("mode changes apply only to new sessions and close defaults deny", func(t *testing.T) {
		if _, e := pool.Exec(ctx, `UPDATE stores SET qr_entry_mode='STAFF_OPEN' WHERE tenant_id='tenant-a' AND store_id='store-a'`); e != nil {
			t.Fatal(e)
		}
		scope := Scope{"tenant-a", "store-a", "table-1"}
		opened, e := service.Open(ctx, scope, "staff-1")
		if e != nil {
			t.Fatal(e)
		}
		if e = service.SetStoreMode(ctx, "tenant-a", "store-a", DirectScan); e != nil {
			t.Fatal(e)
		}
		var mode Mode
		if e = pool.QueryRow(ctx, `SELECT qr_mode FROM table_sessions WHERE session_id=$1`, opened.Session.ID).Scan(&mode); e != nil {
			t.Fatal(e)
		}
		if mode != StaffOpen {
			t.Fatalf("existing mode changed to %s", mode)
		}
		if e = service.Close(ctx, scope, opened.Session.ID, "staff-1"); e != nil {
			t.Fatal(e)
		}
		if _, e = service.DirectJoin(ctx, "public-a", ""); e != nil {
			t.Fatalf("new mode was not applied to next session: %v", e)
		}
		current, e := service.PublicStatus(ctx, "public-a")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = service.DirectJoin(ctx, "public-a", "stale-session-id"); !errors.Is(e, ErrDenied) {
			t.Fatalf("stale direct scan rebound to active session: %v", e)
		}
		if e = service.SetStoreMode(ctx, "tenant-a", "store-a", StaffOpen); e != nil {
			t.Fatal(e)
		}
		if _, e = service.DirectJoin(ctx, "public-a", current.CurrentSessionID); e != nil {
			t.Fatalf("mode change cut off existing DIRECT_SCAN session: %v", e)
		}
		public, e := service.PublicStatus(ctx, "public-a")
		if e != nil || public.Mode != DirectScan {
			t.Fatalf("public status lost existing session mode: %+v err=%v", public, e)
		}
		denyService, e := New(pool, options, nil)
		if e != nil {
			t.Fatal(e)
		}
		if e = denyService.Close(ctx, Scope{"tenant-a", "store-a", "table-2"}, opened.Session.ID, "staff-1"); !errors.Is(e, ErrNotFound) {
			t.Fatalf("cross-scope close: %v", e)
		}
		second, e := service.Open(ctx, Scope{"tenant-a", "store-a", "table-2"}, "staff-1")
		if e != nil {
			t.Fatal(e)
		}
		if e = denyService.Close(ctx, Scope{"tenant-a", "store-a", "table-2"}, second.Session.ID, "staff-1"); !errors.Is(e, ErrCloseBlocked) {
			t.Fatalf("default close policy did not deny: %v", e)
		}
	})
}

func testOptions() Options {
	return Options{SessionTTL: time.Hour, GrantTTL: time.Minute, GrantWindow: time.Minute, GrantWindowMax: 1000, PINTTL: time.Minute, PINLockout: time.Minute, PINMaxAttempts: 2, PINLength: 4, PINPepper: []byte("pppppppppppppppppppppppppppppppp")}
}
func sessionIDsKey(sessions map[string]int) string {
	for id := range sessions {
		return id
	}
	return ""
}
func wrongPIN(pin string) string {
	if pin[0] == '0' {
		return "1" + pin[1:]
	}
	return "0" + pin[1:]
}
