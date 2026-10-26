package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/staffauth"
)

type fakeStore struct {
	calls  []string
	args   []any
	err    error
	status staffauth.SubjectStatus
}

func (f *fakeStore) record(name string, args ...any) error {
	f.calls = append(f.calls, name)
	f.args = args
	return f.err
}

func (f *fakeStore) Grant(_ context.Context, subject, tenant, store string, role authz.Role, actor string) error {
	return f.record("grant", subject, tenant, store, role, actor)
}
func (f *fakeStore) RevokeMembership(_ context.Context, subject, tenant, store string, role authz.Role, actor string) (bool, error) {
	return true, f.record("revoke", subject, tenant, store, role, actor)
}
func (f *fakeStore) RevokeSubjectSessions(_ context.Context, subject, reason string, actor ...string) (int64, error) {
	return 3, f.record("revoke-sessions", subject, reason, actor[0])
}
func (f *fakeStore) Hold(_ context.Context, subject, reason, actor string) error {
	return f.record("hold", subject, reason, actor)
}
func (f *fakeStore) Restore(_ context.Context, subject, actor string) error {
	return f.record("restore", subject, actor)
}
func (f *fakeStore) Status(_ context.Context, subject string) (staffauth.SubjectStatus, error) {
	if err := f.record("status", subject); err != nil {
		return staffauth.SubjectStatus{}, err
	}
	return f.status, nil
}

func runTest(args ...string) (int, string, string, *fakeStore) {
	store := &fakeStore{}
	var stdout, stderr strings.Builder
	code := run(context.Background(), args, &stdout, &stderr, store)
	return code, stdout.String(), stderr.String(), store
}

func TestRunSubcommandsPassArguments(t *testing.T) {
	tests := []struct {
		name string
		args []string
		call string
		want []any
	}{
		{"grant", []string{"grant", "--actor", "op", "--subject", "sub", "--tenant", "ten", "--store", "st", "--role", "STORE_MANAGER"}, "grant", []any{"sub", "ten", "st", authz.StoreManager, "op"}},
		{"revoke", []string{"revoke", "--actor", "op", "--subject", "sub", "--tenant", "ten", "--store", "st", "--role", "KITCHEN"}, "revoke", []any{"sub", "ten", "st", authz.Kitchen, "op"}},
		{"revoke-sessions reason", []string{"revoke-sessions", "--actor", "op", "--subject", "sub", "--reason", "incident"}, "revoke-sessions", []any{"sub", "incident", "op"}},
		{"hold", []string{"hold", "--actor", "op", "--subject", "sub", "--reason", "reset"}, "hold", []any{"sub", "reset", "op"}},
		{"restore", []string{"restore", "--actor", "op", "--subject", "sub"}, "restore", []any{"sub", "op"}},
		{"status", []string{"status", "--subject", "sub"}, "status", []any{"sub"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, stderr, store := runTest(tt.args...)
			if code != 0 {
				t.Fatalf("code=%d stderr=%s", code, stderr)
			}
			if len(store.calls) != 1 || store.calls[0] != tt.call {
				t.Fatalf("calls=%v", store.calls)
			}
			if len(store.args) != len(tt.want) {
				t.Fatalf("args=%v want=%v", store.args, tt.want)
			}
			for i := range tt.want {
				if store.args[i] != tt.want[i] {
					t.Fatalf("args=%v want=%v", store.args, tt.want)
				}
			}
		})
	}
}

func TestRunUsageErrorsDoNotCallStore(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"missing actor", []string{"grant", "--subject", "sub", "--tenant", "ten", "--store", "st", "--role", "STAFF_CASHIER"}},
		{"bad role", []string{"grant", "--actor", "op", "--subject", "sub", "--tenant", "ten", "--store", "st", "--role", "ADMIN"}},
		{"empty store for store manager", []string{"grant", "--actor", "op", "--subject", "sub", "--tenant", "ten", "--role", "STORE_MANAGER"}},
		{"whitespace store for staff", []string{"grant", "--actor", "op", "--subject", "sub", "--tenant", "ten", "--store", "   ", "--role", "STAFF_CASHIER"}},
		{"whitespace store for tenant admin", []string{"grant", "--actor", "op", "--subject", "sub", "--tenant", "ten", "--store", "   ", "--role", "TENANT_ADMIN"}},
		{"hold without reason", []string{"hold", "--actor", "op", "--subject", "sub"}},
		{"revoke-sessions without reason", []string{"revoke-sessions", "--actor", "op", "--subject", "sub"}},
		{"revoke-sessions whitespace reason", []string{"revoke-sessions", "--actor", "op", "--subject", "sub", "--reason", " \t "}},
		{"unknown subcommand", []string{"list"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, _, stderr, store := runTest(tt.args...)
			if code != 2 || len(store.calls) != 0 || stderr == "" {
				t.Fatalf("code=%d calls=%v stderr=%q", code, store.calls, stderr)
			}
		})
	}
}

func TestRunHoldReminderShellQuotesArguments(t *testing.T) {
	var stdout, stderr strings.Builder
	store := &fakeStore{}
	code := run(context.Background(), []string{"hold", "--actor", "Jane Doe O'Neil;$HOME", "--subject", "sub with spaces", "--reason", "reset"}, &stdout, &stderr, store)
	want := "Next: change the credential in Keycloak, then run: staffadmin restore --actor 'Jane Doe O'\"'\"'Neil;$HOME' --subject 'sub with spaces'\n"
	if code != 0 || stderr.String() != "" || stdout.String() != want {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunTenantWideTenantAdminAllowed(t *testing.T) {
	code, _, stderr, store := runTest("grant", "--actor", "op", "--subject", "sub", "--tenant", "ten", "--role", "TENANT_ADMIN")
	if code != 0 || stderr != "" || len(store.calls) != 1 || store.args[2] != "" || store.args[3] != authz.TenantAdmin {
		t.Fatalf("code=%d args=%v stderr=%q", code, store.args, stderr)
	}
}

func TestRunStoreErrorIsRuntimeFailure(t *testing.T) {
	store := &fakeStore{err: errors.New("connection failed for postgres://user:secret@host/db")}
	var stdout, stderr strings.Builder
	code := run(context.Background(), []string{"restore", "--actor", "op", "--subject", "sub"}, &stdout, &stderr, store)
	if code != 1 || stderr.String() != "staffadmin: database operation failed\n" {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if strings.Contains(stdout.String()+stderr.String(), "secret") {
		t.Fatalf("credential leaked to output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestRunStoreErrorsUseFixedMessages(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"held", staffauth.ErrSubjectHeld, "staffadmin: subject is on hold\n"},
		{"not held", staffauth.ErrSubjectNotHeld, "staffadmin: subject is not on hold\n"},
		{"constraint", &pgconn.PgError{Code: "23514", Message: "constraint failed: secret"}, "staffadmin: invalid membership (check role/store)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{err: tt.err}
			var stdout, stderr strings.Builder
			code := run(context.Background(), []string{"restore", "--actor", "op", "--subject", "sub"}, &stdout, &stderr, store)
			if code != 1 || stderr.String() != tt.want || strings.Contains(stdout.String()+stderr.String(), "secret") {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestRunStatusOutput(t *testing.T) {
	heldAt := time.Date(2026, 10, 26, 18, 4, 5, 0, time.UTC)
	store := &fakeStore{status: staffauth.SubjectStatus{
		SubjectID: "sub-1", Exists: true, HoldAt: &heldAt, HoldReason: "credential reset", AuthEpoch: 4, LiveSessions: 0,
		Memberships: []authz.Membership{
			{TenantID: "tenant-a", Role: authz.TenantAdmin},
			{TenantID: "tenant-a", StoreID: "store-b", Role: authz.StoreManager},
		},
	}}
	var stdout, stderr strings.Builder
	code := run(context.Background(), []string{"status", "--subject", "sub-1"}, &stdout, &stderr, store)
	want := "Subject: sub-1\nExists: true\nHold: 2026-10-27T03:04:05+09:00 reason=\"credential reset\"\nAuth epoch: 4\nLive sessions: 0\nMembership: tenant-a/* TENANT_ADMIN\nMembership: tenant-a/store-b STORE_MANAGER\n"
	if code != 0 || stderr.String() != "" || stdout.String() != want {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
