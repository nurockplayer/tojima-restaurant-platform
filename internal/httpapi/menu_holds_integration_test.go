package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

func TestCompanyProductEmergencyHoldHTTPIntegration(t *testing.T) {
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
	schema := "p81_hold_http_" + randomSchemaSuffix(t)
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, dropErr := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); dropErr != nil {
			t.Errorf("drop owned emergency hold schema: %v", dropErr)
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
	if _, err = pool.Exec(ctx, `
		INSERT INTO tenants(tenant_id,name) VALUES ('hold-tenant','Hold tenant'),('other-tenant','Other tenant');
		INSERT INTO stores(tenant_id,store_id,name) VALUES
			('hold-tenant','hold-store','Hold store'),('hold-tenant','other-store','Other store'),('other-tenant','hold-store','Foreign store');
		INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES
			('hold-tenant','hold-store','table','Table','hold-public'),
			('hold-tenant','other-store','table','Other table','other-store-public'),
			('other-tenant','hold-store','table','Foreign table','foreign-public')`); err != nil {
		t.Fatal(err)
	}
	service, err := catalog.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.SaveCompanyDraft(ctx, "hold-tenant", "owner", 0, catalog.CatalogData{Categories: []catalog.Category{{
		Key: "food", Name: "Food", Products: []catalog.Product{{Key: "meal", Name: "Meal", BasePriceMinor: 1000, Active: true}},
	}}}); err != nil {
		t.Fatalf("save company draft: %v", err)
	}
	if _, err = service.PublishCompanyDraft(ctx, "hold-tenant", "owner", 1); err != nil {
		t.Fatalf("publish company catalog: %v", err)
	}

	holdPath := "/v1/admin/tenants/hold-tenant/menu/products/meal/emergency-hold"
	ownerMemberships := &httpMemberships{values: []authz.Membership{{TenantID: "hold-tenant", Role: authz.TenantAdmin}}}
	owner := NewMenuHandler(service, httpTestIdentity{authz.Actor{SubjectID: "owner", MFA: true}}, authz.PolicyAuthorizer{Directory: ownerMemberships})
	ownerNoMFA := NewMenuHandler(service, httpTestIdentity{authz.Actor{SubjectID: "owner", MFA: false}}, authz.PolicyAuthorizer{Directory: ownerMemberships})
	secondAdmin := NewMenuHandler(service, httpTestIdentity{authz.Actor{SubjectID: "second-admin", MFA: true}}, authz.PolicyAuthorizer{Directory: &httpMemberships{values: []authz.Membership{{TenantID: "hold-tenant", Role: authz.TenantAdmin}}}})
	manager := NewMenuHandler(service, httpTestIdentity{authz.Actor{SubjectID: "manager", MFA: true}}, authz.PolicyAuthorizer{Directory: &httpMemberships{values: []authz.Membership{{TenantID: "hold-tenant", StoreID: "hold-store", Role: authz.StoreManager}}}})
	staff := NewMenuHandler(service, httpTestIdentity{authz.Actor{SubjectID: "staff"}}, authz.PolicyAuthorizer{Directory: &httpMemberships{values: []authz.Membership{{TenantID: "hold-tenant", StoreID: "hold-store", Role: authz.Staff}}}})
	otherStoreManager := NewMenuHandler(service, httpTestIdentity{authz.Actor{SubjectID: "other-store-manager", MFA: true}}, authz.PolicyAuthorizer{Directory: &httpMemberships{values: []authz.Membership{{TenantID: "hold-tenant", StoreID: "other-store", Role: authz.StoreManager}}}})
	otherTenantAdmin := NewMenuHandler(service, httpTestIdentity{authz.Actor{SubjectID: "other-admin", MFA: true}}, authz.PolicyAuthorizer{Directory: &httpMemberships{values: []authz.Membership{{TenantID: "other-tenant", Role: authz.TenantAdmin}}}})
	defaultDeny := NewMenuHandler(service, nil, nil)

	if response := requestJSON(t, defaultDeny, http.MethodGet, holdPath, "", ""); response.Code != http.StatusForbidden {
		t.Fatalf("default-deny hold GET returned %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, manager, http.MethodGet, holdPath, "", ""); response.Code != http.StatusForbidden {
		t.Fatalf("store manager read full company hold: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, manager, http.MethodPut, holdPath, `not-json`, ""); response.Code != http.StatusForbidden {
		t.Fatalf("unauthorized manager request was decoded before authorization: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, staff, http.MethodPut, holdPath, `{"active":true,"reason":"incident","expected_version":0,"idempotency_key":"staff-key"}`, ""); response.Code != http.StatusForbidden {
		t.Fatalf("store staff changed company hold: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, ownerNoMFA, http.MethodPut, holdPath, `{"active":true,"reason":"incident","expected_version":0,"idempotency_key":"no-mfa"}`, ""); response.Code != http.StatusForbidden {
		t.Fatalf("TenantAdmin without MFA changed company hold: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, otherTenantAdmin, http.MethodGet, holdPath, "", ""); response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), "incident") {
		t.Fatalf("cross-tenant admin read company hold: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, owner, http.MethodGet, "/v1/admin/tenants/hold-tenant/menu/products/not-published/emergency-hold", "", ""); response.Code != http.StatusNotFound {
		t.Fatalf("unknown company product GET returned %d %s", response.Code, response.Body.String())
	}

	initial := requestJSON(t, owner, http.MethodGet, holdPath, "", "")
	if initial.Code != http.StatusOK || initial.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("read initial hold state: %d %s", initial.Code, initial.Body.String())
	}
	assertHoldResponse(t, initial.Body.String(), false, 0, "")

	for _, body := range []string{
		`{"reason":"incident","expected_version":0,"idempotency_key":"missing-active"}`,
		`{"active":null,"reason":"incident","expected_version":0,"idempotency_key":"null-active"}`,
		`{"active":"true","reason":"incident","expected_version":0,"idempotency_key":"string-active"}`,
		`{"active":1,"reason":"incident","expected_version":0,"idempotency_key":"number-active"}`,
		`{"active":true,"expected_version":0,"idempotency_key":"blank-reason"}`,
		`{"active":true,"reason":"incident","expected_version":0,"idempotency_key":"  "}`,
		`{"active":true,"reason":"incident","idempotency_key":"missing-version"}`,
		`{"active":true,"reason":"incident","expected_version":null,"idempotency_key":"null-version"}`,
		`{"active":true,"reason":"incident","expected_version":"0","idempotency_key":"string-version"}`,
		`{"active":true,"reason":"incident","expected_version":0,"active":false,"idempotency_key":"duplicate-active"}`,
		`{"Active":true,"reason":"incident","expected_version":0,"active":false,"idempotency_key":"case-duplicate-active"}`,
		`{"active":true,"reason":"incident","expected_version":0,"\u0061ctive":false,"idempotency_key":"escaped-duplicate-active"}`,
		`{"active":true,"reason":"incident","expected_version":0,"idempotency_key":"unknown","unexpected":true}`,
	} {
		if response := requestJSON(t, owner, http.MethodPut, holdPath, body, ""); response.Code != http.StatusBadRequest {
			t.Errorf("invalid hold request returned %d %s for %s", response.Code, response.Body.String(), body)
		}
	}

	onBody := `{"active":true,"reason":"emergency-incident-reason","expected_version":0,"idempotency_key":"hold-on-1"}`
	on := requestJSON(t, owner, http.MethodPut, holdPath, onBody, "")
	if on.Code != http.StatusOK || on.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("activate emergency hold: %d %s", on.Code, on.Body.String())
	}
	assertHoldResponse(t, on.Body.String(), true, 1, "emergency-incident-reason")
	if replay := requestJSON(t, owner, http.MethodPut, holdPath, onBody, ""); replay.Code != http.StatusOK || replay.Body.String() != on.Body.String() {
		t.Fatalf("same-identity exact replay did not return immutable result: %d %s", replay.Code, replay.Body.String())
	}
	if replay := requestJSON(t, secondAdmin, http.MethodPut, holdPath, onBody, ""); replay.Code != http.StatusConflict {
		t.Fatalf("different admin identity reused idempotency key: %d %s", replay.Code, replay.Body.String())
	}
	if replay := requestJSON(t, owner, http.MethodPut, holdPath, `{"active":true,"reason":"different reason","expected_version":0,"idempotency_key":"hold-on-1"}`, ""); replay.Code != http.StatusConflict {
		t.Fatalf("mismatched payload reused idempotency key: %d %s", replay.Code, replay.Body.String())
	}
	if repeat := requestJSON(t, owner, http.MethodPut, holdPath, `{"active":true,"reason":"fresh key same state","expected_version":1,"idempotency_key":"hold-on-duplicate"}`, ""); repeat.Code != http.StatusConflict {
		t.Fatalf("same-state fresh-key transition was accepted: %d %s", repeat.Code, repeat.Body.String())
	}

	offBody := `{"active":false,"reason":"correction verified","expected_version":1,"idempotency_key":"hold-off-1"}`
	off := requestJSON(t, owner, http.MethodPut, holdPath, offBody, "")
	if off.Code != http.StatusOK {
		t.Fatalf("clear emergency hold: %d %s", off.Code, off.Body.String())
	}
	assertHoldResponse(t, off.Body.String(), false, 2, "correction verified")
	onAgain := requestJSON(t, owner, http.MethodPut, holdPath, `{"active":true,"reason":"second incident","expected_version":2,"idempotency_key":"hold-on-2"}`, "")
	if onAgain.Code != http.StatusOK {
		t.Fatalf("reactivate emergency hold: %d %s", onAgain.Code, onAgain.Body.String())
	}
	assertHoldResponse(t, onAgain.Body.String(), true, 3, "second incident")
	if staleReplay := requestJSON(t, owner, http.MethodPut, holdPath, offBody, ""); staleReplay.Code != http.StatusOK || staleReplay.Body.String() != off.Body.String() {
		t.Fatalf("prior immutable replay changed or did not return old result: %d %s", staleReplay.Code, staleReplay.Body.String())
	}
	if current := requestJSON(t, owner, http.MethodGet, holdPath, "", ""); current.Code != http.StatusOK {
		t.Fatalf("read current hold after prior replay: %d %s", current.Code, current.Body.String())
	} else {
		assertHoldResponse(t, current.Body.String(), true, 3, "second incident")
	}

	storeMenuPath := "/v1/admin/tenants/hold-tenant/stores/hold-store/menu"
	for name, handler := range map[string]http.Handler{"tenant admin": owner, "manager": manager, "staff": staff} {
		response := requestJSON(t, handler, http.MethodGet, storeMenuPath, "", "")
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"company_emergency_hold_active":true`) {
			t.Errorf("%s own-store projection omitted safe hold indicator: %d %s", name, response.Code, response.Body.String())
		}
		for _, secret := range []string{"emergency-incident-reason", "second incident", `"activated_by"`, `"cleared_by"`, `"linked_store_count"`} {
			if strings.Contains(response.Body.String(), secret) {
				t.Errorf("%s own-store projection exposed hold audit detail %q: %s", name, secret, response.Body.String())
			}
		}
	}
	if response := requestJSON(t, otherStoreManager, http.MethodGet, storeMenuPath, "", ""); response.Code != http.StatusForbidden {
		t.Fatalf("other-store manager read indicator: %d %s", response.Code, response.Body.String())
	}
	public := requestJSON(t, NewMenuHandler(service, nil, nil), http.MethodGet, "/v1/guest/tables/hold-public/menu", "", "")
	if public.Code != http.StatusOK || !strings.Contains(public.Body.String(), `"selectable":false`) || strings.Contains(public.Body.String(), "company_emergency_hold_active") || strings.Contains(public.Body.String(), "emergency-incident-reason") || strings.Contains(public.Body.String(), "activated_by") {
		t.Fatalf("guest menu exposed hold internals or D1-only indicator: %d %s", public.Code, public.Body.String())
	}

	ownerMemberships.mu.Lock()
	ownerMemberships.values = nil
	ownerMemberships.mu.Unlock()
	if replay := requestJSON(t, owner, http.MethodPut, holdPath, onBody, ""); replay.Code != http.StatusForbidden {
		t.Fatalf("revoked current membership replayed prior hold: %d %s", replay.Code, replay.Body.String())
	}
}

func assertHoldResponse(t *testing.T, body string, active bool, version int64, reason string) {
	t.Helper()
	var response struct {
		Active            bool   `json:"active"`
		Version           int64  `json:"version"`
		Reason            string `json:"reason"`
		PublishedRevision int64  `json:"published_revision"`
		LinkedStoreCount  int    `json:"linked_store_count"`
	}
	if err := json.Unmarshal([]byte(body), &response); err != nil {
		t.Fatalf("decode hold response %q: %v", body, err)
	}
	if response.Active != active || response.Version != version || response.Reason != reason {
		t.Fatalf("hold response state = active:%v version:%d reason:%q; want active:%v version:%d reason:%q; body=%s", response.Active, response.Version, response.Reason, active, version, reason, body)
	}
	if response.PublishedRevision != 1 || response.LinkedStoreCount != 2 {
		t.Fatalf("hold summary = revision:%d linked stores:%d; want revision:1 linked stores:2; body=%s", response.PublishedRevision, response.LinkedStoreCount, body)
	}
}
