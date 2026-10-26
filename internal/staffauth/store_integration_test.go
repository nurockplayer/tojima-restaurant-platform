package staffauth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

func staffAuthTestStore(t *testing.T) (context.Context, *pgxpool.Pool, *Store) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 8)
	if _, err = rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	schema := "p91_staffauth_" + hex.EncodeToString(raw)
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		_ = admin.Close(ctx)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, e := admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); e != nil {
			t.Errorf("drop owned test schema: %v", e)
		}
		_ = admin.Close(context.Background())
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = postgres.Up(ctx, conn.Conn()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()
	store, err := NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	return ctx, pool, store
}

func dbNow(t *testing.T, ctx context.Context, pool *pgxpool.Pool) time.Time {
	t.Helper()
	var now time.Time
	if err := pool.QueryRow(ctx, "SELECT now()").Scan(&now); err != nil {
		t.Fatal(err)
	}
	return now
}

func requireKind(t *testing.T, ctx context.Context, pool *pgxpool.Pool, kind string) {
	t.Helper()
	var found bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM staff_auth_events WHERE kind=$1)", kind).Scan(&found); err != nil || !found {
		t.Fatalf("event %q found=%v err=%v", kind, found, err)
	}
}

func TestStoreSessionLifecycleHoldAndRestore(t *testing.T) {
	ctx, pool, store := staffAuthTestStore(t)
	if _, err := NewStore(nil); err == nil {
		t.Fatal("nil pool accepted")
	}
	firstLoginStarted := dbNow(t, ctx, pool)
	if _, _, err := store.CreateSession(ctx, "first-time-subject", "idp-first", false, firstLoginStarted); err != nil {
		t.Fatalf("first login for an unknown subject: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO staff_subjects(subject_id) VALUES('subject-a'),('subject-b')"); err != nil {
		t.Fatal(err)
	}
	started := dbNow(t, ctx, pool)
	token, session, err := store.CreateSession(ctx, "subject-a", "idp-a", false, started)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveSession(ctx, token)
	if err != nil || resolved.ID != session.ID {
		t.Fatalf("resolve=%+v err=%v", resolved, err)
	}
	hash := sha256.Sum256([]byte(token))
	var stored []byte
	var containsRaw bool
	if err = pool.QueryRow(ctx, "SELECT token_sha256,position($1 in encode(token_sha256,'hex'))>0 FROM staff_sessions WHERE id=$2", token, session.ID).Scan(&stored, &containsRaw); err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(hash[:]) || containsRaw {
		t.Fatal("session table does not contain only the token hash")
	}
	other, _, err := store.CreateSession(ctx, "subject-a", "idp-a-2", false, dbNow(t, ctx, pool))
	if err != nil {
		t.Fatal(err)
	}
	subjectBToken, _, err := store.CreateSession(ctx, "subject-b", "idp-b", false, dbNow(t, ctx, pool))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.RevokeSession(ctx, session.ID, "logout"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ResolveSession(ctx, token); !errors.Is(err, ErrNoSession) {
		t.Fatalf("revoked resolve err=%v", err)
	}
	n, err := store.RevokeSubjectSessions(ctx, "subject-a", "operator")
	if err != nil || n != 1 {
		t.Fatalf("subject revoke count=%d err=%v", n, err)
	}
	if _, err = store.ResolveSession(ctx, other); !errors.Is(err, ErrNoSession) {
		t.Fatalf("subject session remained live: %v", err)
	}
	if _, err = store.ResolveSession(ctx, subjectBToken); err != nil {
		t.Fatalf("revoking subject-a affected subject-b: %v", err)
	}
	// A direct expiry update models the passage of absolute session time.
	expiredToken, expired, err := store.CreateSession(ctx, "subject-b", "idp-exp", false, dbNow(t, ctx, pool))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE staff_sessions SET created_at=now()-interval '31 days',absolute_expires_at=now()-interval '1 day' WHERE id=$1", expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ResolveSession(ctx, expiredToken); !errors.Is(err, ErrNoSession) {
		t.Fatalf("expired resolve err=%v", err)
	}

	liveToken, _, err := store.CreateSession(ctx, "subject-a", "idp-hold", false, dbNow(t, ctx, pool))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Grant(ctx, "subject-a", "tenant-a", "store-a", authz.Staff, "admin"); err != nil {
		t.Fatal(err)
	}
	if err = store.Hold(ctx, "subject-a", "incident", "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ResolveSession(ctx, liveToken); !errors.Is(err, ErrNoSession) {
		t.Fatalf("held session resolve err=%v", err)
	}
	members, err := store.Current(ctx, "subject-a", "tenant-a")
	if err != nil || len(members) != 0 {
		t.Fatalf("held memberships=%v err=%v", members, err)
	}
	if _, _, err = store.CreateSession(ctx, "subject-a", "idp-held", false, dbNow(t, ctx, pool)); !errors.Is(err, ErrSubjectHeld) {
		t.Fatalf("held create err=%v", err)
	}
	beforeRestore := dbNow(t, ctx, pool)
	if err = store.Restore(ctx, "subject-a", "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ResolveSession(ctx, liveToken); !errors.Is(err, ErrNoSession) {
		t.Fatalf("restore resurrected old session: %v", err)
	}
	if _, _, err = store.CreateSession(ctx, "subject-a", "idp-stale", false, beforeRestore); !errors.Is(err, ErrStaleAuth) {
		t.Fatalf("stale create err=%v", err)
	}
	if _, _, err = store.CreateSession(ctx, "subject-a", "idp-new", false, dbNow(t, ctx, pool)); err != nil {
		t.Fatalf("fresh create: %v", err)
	}
	requireKind(t, ctx, pool, "login")
	requireKind(t, ctx, pool, "login_rejected")
	requireKind(t, ctx, pool, "logout")
	requireKind(t, ctx, pool, "session_revoked")
	requireKind(t, ctx, pool, "hold")
	requireKind(t, ctx, pool, "restore")
	if _, err = pool.Exec(ctx, "UPDATE staff_auth_events SET detail='{}' WHERE kind='login'"); err == nil {
		t.Fatal("event update accepted")
	}
	if _, err = pool.Exec(ctx, "DELETE FROM staff_auth_events WHERE kind='login'"); err == nil {
		t.Fatal("fresh event deletion accepted")
	}
	if _, err = pool.Exec(ctx, "TRUNCATE staff_auth_events"); err == nil {
		t.Fatal("event truncation accepted")
	}
	requireKind(t, ctx, pool, "login")
}

func TestStoreAuthTransactionsAndReturnPathConstraint(t *testing.T) {
	ctx, pool, store := staffAuthTestStore(t)
	state := sha256.Sum256([]byte("state value"))
	binding := sha256.Sum256([]byte("binding value"))
	in := AuthTxInput{StateHash: state, BindingHash: binding, Nonce: "nonce", PKCEVerifier: "verifier", Purpose: "login", ReturnPath: "/staff"}
	if err := store.CreateAuthTx(ctx, in); err != nil {
		t.Fatal(err)
	}
	out, err := store.ConsumeAuthTx(ctx, state, binding)
	if err != nil || out.Nonce != "nonce" || out.Purpose != "login" || out.CreatedAt.IsZero() {
		t.Fatalf("consume=%+v err=%v", out, err)
	}
	if _, err = store.ConsumeAuthTx(ctx, state, binding); !errors.Is(err, ErrTxInvalid) {
		t.Fatalf("second consume err=%v", err)
	}
	state2 := sha256.Sum256([]byte("state wrong bind"))
	in.StateHash = state2
	if err = store.CreateAuthTx(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ConsumeAuthTx(ctx, state2, sha256.Sum256([]byte("wrong"))); !errors.Is(err, ErrTxInvalid) {
		t.Fatalf("wrong binding err=%v", err)
	}
	if _, err = store.ConsumeAuthTx(ctx, state2, binding); !errors.Is(err, ErrTxInvalid) {
		t.Fatalf("wrong-binding transaction was reusable: %v", err)
	}
	state3 := sha256.Sum256([]byte("state expired"))
	in.StateHash = state3
	if err = store.CreateAuthTx(ctx, in); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, "UPDATE staff_auth_tx SET created_at=now()-interval '11 minutes',expires_at=now()-interval '1 minute' WHERE state_sha256=$1", state3[:]); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ConsumeAuthTx(ctx, state3, binding); !errors.Is(err, ErrTxInvalid) {
		t.Fatalf("expired consume err=%v", err)
	}
	bad := in
	bad.StateHash = sha256.Sum256([]byte("bad path"))
	bad.ReturnPath = "//evil"
	if err = store.CreateAuthTx(ctx, bad); err == nil {
		t.Fatal("unsafe return path accepted")
	}
}

func TestStorePromoteStrongAndBackchannel(t *testing.T) {
	ctx, pool, store := staffAuthTestStore(t)
	if _, err := pool.Exec(ctx, "INSERT INTO staff_subjects(subject_id) VALUES('promote-a'),('promote-b')"); err != nil {
		t.Fatal(err)
	}
	token, basic, err := store.CreateSession(ctx, "promote-a", "sid-a", false, dbNow(t, ctx, pool))
	if err != nil {
		t.Fatal(err)
	}
	if err = store.PromoteStrong(ctx, basic.ID, "wrong", dbNow(t, ctx, pool)); !errors.Is(err, ErrNoSession) {
		t.Fatalf("wrong subject promote err=%v", err)
	}
	if err = store.PromoteStrong(ctx, basic.ID, "promote-a", dbNow(t, ctx, pool)); err != nil {
		t.Fatal(err)
	}
	strong, err := store.ResolveSession(ctx, token)
	if err != nil || !strong.StrongLogin || strong.LastStrongAuth == nil || !strong.AbsoluteExpires.Equal(basic.AbsoluteExpires) {
		t.Fatalf("promoted session=%+v err=%v", strong, err)
	}
	if err = store.Hold(ctx, "promote-a", "test", "operator"); err != nil {
		t.Fatal(err)
	}
	oldAuth := dbNow(t, ctx, pool)
	if err = store.Restore(ctx, "promote-a", "operator"); err != nil {
		t.Fatal(err)
	}
	if err = store.PromoteStrong(ctx, basic.ID, "promote-a", oldAuth); !errors.Is(err, ErrStaleAuth) {
		t.Fatalf("stale promote err=%v", err)
	}
	if _, _, err = store.CreateSession(ctx, "promote-b", "sid-b", false, dbNow(t, ctx, pool)); err != nil {
		t.Fatal(err)
	}
	count, err := store.RevokeByIDP(ctx, "sid-a", "promote-a")
	if err != nil || count != 0 {
		t.Fatalf("sid of held session count=%d err=%v", count, err)
	}
	sidB2Token, _, err := store.CreateSession(ctx, "promote-b", "sid-b2", false, dbNow(t, ctx, pool))
	if err != nil {
		t.Fatal(err)
	}
	count, err = store.RevokeByIDP(ctx, "sid-b", "wrong-sub")
	if err != nil || count != 0 {
		t.Fatalf("mismatched sid/sub count=%d err=%v", count, err)
	}
	count, err = store.RevokeByIDP(ctx, "sid-b", "")
	if err != nil || count != 1 {
		t.Fatalf("sid revoke count=%d err=%v", count, err)
	}
	sidB2RetryToken, _, err := store.CreateSession(ctx, "promote-b", "sid-b2", false, dbNow(t, ctx, pool))
	if err != nil {
		t.Fatal(err)
	}
	count, err = store.RevokeByIDP(ctx, "", "promote-b")
	if err != nil || count != 2 {
		t.Fatalf("sub revoke count=%d err=%v", count, err)
	}
	for _, raw := range []string{sidB2Token, sidB2RetryToken} {
		if _, err = store.ResolveSession(ctx, raw); !errors.Is(err, ErrNoSession) {
			t.Fatalf("sub-only revocation left a sid-b2 session live: %v", err)
		}
	}
	if _, err = store.RevokeByIDP(ctx, "", ""); err == nil {
		t.Fatal("empty backchannel identifiers accepted")
	}
	fresh, err := store.SeeLogoutJTI(ctx, "logout-jti")
	if err != nil || !fresh {
		t.Fatalf("first JTI fresh=%v err=%v", fresh, err)
	}
	fresh, err = store.SeeLogoutJTI(ctx, "logout-jti")
	if err != nil || fresh {
		t.Fatalf("second JTI fresh=%v err=%v", fresh, err)
	}
	requireKind(t, ctx, pool, "step_up")
	requireKind(t, ctx, pool, "backchannel_revoke")
}

func TestStoreMemberships(t *testing.T) {
	ctx, pool, store := staffAuthTestStore(t)
	if err := store.Grant(ctx, "member-a", "tenant-a", "store-a", authz.Staff, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := store.Grant(ctx, "member-a", "tenant-a", "store-a", authz.Staff, "admin"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM staff_memberships WHERE subject_id='member-a'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate grant rows=%d err=%v", count, err)
	}
	if err := store.Grant(ctx, "member-a", "tenant-a", "", authz.Staff, "admin"); err == nil {
		t.Fatal("storeless staff grant accepted")
	}
	if err := store.Grant(ctx, "member-a", "tenant-b", "store-b", authz.Staff, "admin"); err != nil {
		t.Fatal(err)
	}
	current, err := store.Current(ctx, "member-a", "tenant-a")
	if err != nil || len(current) != 1 || current[0].StoreID != "store-a" || current[0].Role != authz.Staff {
		t.Fatalf("current memberships=%v err=%v", current, err)
	}
	policy := authz.PolicyAuthorizer{Directory: store}
	request := authz.Request{Actor: authz.Actor{SubjectID: "member-a"}, Resource: authz.Scope{TenantID: "tenant-a", StoreID: "store-a"}, AllowedRoles: []authz.Role{authz.Staff}}
	if err = policy.Authorize(ctx, request); err != nil {
		t.Fatalf("granted store denied: %v", err)
	}
	request.Resource.StoreID = "other-store"
	if err = policy.Authorize(ctx, request); !errors.Is(err, authz.ErrDenied) {
		t.Fatalf("other store allowed: %v", err)
	}
	request.Resource = authz.Scope{TenantID: "tenant-b", StoreID: "store-b"}
	if err = policy.Authorize(ctx, request); err != nil {
		t.Fatalf("tenant-b row missing: %v", err)
	}
	changed, err := store.RevokeMembership(ctx, "member-a", "tenant-a", "store-a", authz.Staff, "admin")
	if err != nil || !changed {
		t.Fatalf("revoke changed=%v err=%v", changed, err)
	}
	changed, err = store.RevokeMembership(ctx, "member-a", "tenant-a", "store-a", authz.Staff, "admin")
	if err != nil || changed {
		t.Fatalf("repeat revoke changed=%v err=%v", changed, err)
	}
	current, err = store.Current(ctx, "member-a", "tenant-a")
	if err != nil || len(current) != 0 {
		t.Fatalf("revoked memberships=%v err=%v", current, err)
	}
	requireKind(t, ctx, pool, "membership_grant")
	requireKind(t, ctx, pool, "membership_revoke")
}
