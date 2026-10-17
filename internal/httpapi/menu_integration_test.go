package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

func TestMenuHTTPIntegration(t *testing.T) {
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
	schema := "p77_http_" + randomSchemaSuffix(t)
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, dropErr := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE"); dropErr != nil {
			t.Errorf("drop owned menu HTTP schema: %v", dropErr)
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
		INSERT INTO stores(tenant_id,store_id,name) VALUES ('t','s1','One'),('t','s2','Two'),('other','s1','Other one');
		INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES ('t','s1','table','Table','public-menu'),('t','s2','table','Other table','other-store-menu')`); err != nil {
		t.Fatal(err)
	}
	service, err := catalog.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	if status := requestJSON(t, NewMenuHandler(service, nil, nil), http.MethodGet, "/v1/admin/tenants/t/menu/draft", "", ""); status.Code != http.StatusForbidden {
		t.Fatalf("runtime default allowed fake admin: %d %s", status.Code, status.Body.String())
	}
	if status := requestJSON(t, NewMenuHandler(service, nil, nil), http.MethodGet, "/v1/guest/tables/public-menu/menu", "", ""); status.Code != http.StatusNotFound {
		t.Fatalf("unpublished public menu was exposed: %d %s", status.Code, status.Body.String())
	}

	ownerMemberships := &httpMemberships{values: []authz.Membership{{TenantID: "t", Role: authz.TenantAdmin}}}
	owner := NewMenuHandler(service, httpTestIdentity{authz.Actor{SubjectID: "owner", MFA: true}}, authz.PolicyAuthorizer{Directory: ownerMemberships})
	products := []catalog.Product{
		{Key: "listed", Name: "Listed meal", Description: strings.Repeat("x", 4000), BasePriceMinor: 500, Active: true},
		{Key: "hidden", Name: "Unpublished meal", Description: strings.Repeat("x", 4000), BasePriceMinor: 700, Active: false},
	}
	for i := 0; i < 3; i++ {
		products = append(products, catalog.Product{Key: fmt.Sprintf("extra-%d", i), Name: "Extra meal", Description: strings.Repeat("x", 4000), Active: true})
	}
	input := catalog.Draft{Version: 0, CatalogData: catalog.CatalogData{Categories: []catalog.Category{{Key: "food", Name: "Food", Products: products}}}}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) <= 16<<10 {
		t.Fatalf("draft fixture is only %d bytes, expected more than the standard request limit", len(body))
	}
	path := "/v1/admin/tenants/t/menu/draft"
	oversizedBody := string(body) + strings.Repeat(" ", int(menuDraftMaxBodyBytes)-len(body)+1)
	if response := requestJSON(t, owner, http.MethodPut, path, oversizedBody, ""); response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized menu draft returned %d %s", response.Code, response.Body.String())
	}
	chunkedRequest := httptest.NewRequest(http.MethodPut, path, strings.NewReader(oversizedBody))
	chunkedRequest.ContentLength = -1
	chunkedResponse := httptest.NewRecorder()
	owner.ServeHTTP(chunkedResponse, chunkedRequest)
	if chunkedResponse.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized chunked menu draft returned %d %s", chunkedResponse.Code, chunkedResponse.Body.String())
	}
	invalidKey, err := json.Marshal(catalog.Draft{CatalogData: catalog.CatalogData{Categories: []catalog.Category{{Key: strings.Repeat("a", 101), Name: "Food"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if response := requestJSON(t, owner, http.MethodPut, path, string(invalidKey), ""); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"error":"invalid_menu"`) {
		t.Fatalf("overlong key response %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, owner, http.MethodPut, path, string(body), ""); response.Code != http.StatusOK {
		t.Fatalf("save company draft: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, owner, http.MethodPost, "/v1/admin/tenants/t/menu/publish", `{"expected_version":1}`, ""); response.Code != http.StatusOK {
		t.Fatalf("publish company menu: %d %s", response.Code, response.Body.String())
	}
	public := requestJSON(t, NewMenuHandler(service, nil, nil), http.MethodGet, "/v1/guest/tables/public-menu/menu", "", "")
	if public.Code != http.StatusOK || !strings.Contains(public.Body.String(), "Listed meal") || strings.Contains(public.Body.String(), "Unpublished meal") {
		t.Fatalf("safe public menu response %d: %s", public.Code, public.Body.String())
	}
	if strings.Contains(public.Body.String(), "availability_version") || strings.Contains(public.Body.String(), "price_override_version") {
		t.Fatalf("public menu exposed admin metadata: %s", public.Body.String())
	}
	if strings.Contains(public.Body.String(), `"mode"`) || strings.Contains(public.Body.String(), `"revision"`) {
		t.Fatalf("public menu exposed internal revision identity: %s", public.Body.String())
	}
	if !strings.Contains(public.Body.String(), `"currency":"JPY"`) || !strings.Contains(public.Body.String(), `"price_mode":"TAX_INCLUSIVE"`) {
		t.Fatalf("public menu omitted price metadata: %s", public.Body.String())
	}
	if response := requestJSON(t, owner, http.MethodPut, "/v1/admin/tenants/t/menu/draft", `{"version":1,"categories":[],"effective_price_minor":1}`, ""); response.Code != http.StatusBadRequest {
		t.Fatalf("client authoritative price field accepted: %d %s", response.Code, response.Body.String())
	}

	managerMemberships := &httpMemberships{values: []authz.Membership{{TenantID: "t", StoreID: "s1", Role: authz.StoreManager}}}
	manager := NewMenuHandler(service, httpTestIdentity{authz.Actor{SubjectID: "manager", MFA: true}}, authz.PolicyAuthorizer{Directory: managerMemberships})
	if response := requestJSON(t, manager, http.MethodGet, "/v1/admin/tenants/t/stores/s1/menu", "", ""); response.Code != http.StatusOK {
		t.Fatalf("store manager read: %d %s", response.Code, response.Body.String())
	} else if !strings.Contains(response.Body.String(), `"mode":"LINKED"`) || !strings.Contains(response.Body.String(), `"revision":1`) {
		t.Fatalf("store manager response omitted menu version identity: %s", response.Body.String())
	} else if !strings.Contains(response.Body.String(), `"currency":"JPY"`) || !strings.Contains(response.Body.String(), `"price_mode":"TAX_INCLUSIVE"`) {
		t.Fatalf("store manager response omitted price metadata: %s", response.Body.String())
	}
	if response := requestJSON(t, manager, http.MethodGet, "/v1/admin/tenants/t/stores/s2/menu", "", ""); response.Code != http.StatusForbidden {
		t.Fatalf("cross-store manager read allowed: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, manager, http.MethodGet, "/v1/admin/tenants/other/stores/s1/menu", "", ""); response.Code != http.StatusForbidden {
		t.Fatalf("cross-tenant manager read allowed: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, owner, http.MethodPost, "/v1/admin/tenants/t/stores/s1/menu/detach", `{"expected_revision":1}`, ""); response.Code != http.StatusOK {
		t.Fatalf("detach store for draft-body limit test: %d %s", response.Code, response.Body.String())
	}
	storeDraft := input
	storeDraft.Version = 1
	storeBody, err := json.Marshal(storeDraft)
	if err != nil {
		t.Fatal(err)
	}
	if len(storeBody) <= 16<<10 {
		t.Fatalf("store draft fixture is only %d bytes, expected more than the standard request limit", len(storeBody))
	}
	if response := requestJSON(t, manager, http.MethodPut, "/v1/admin/tenants/t/stores/s1/menu/draft", string(storeBody), ""); response.Code != http.StatusOK {
		t.Fatalf("save store draft over 16 KiB: %d %s", response.Code, response.Body.String())
	}
	managerMemberships.mu.Lock()
	managerMemberships.values = nil
	managerMemberships.mu.Unlock()
	if response := requestJSON(t, manager, http.MethodPut, "/v1/admin/tenants/t/stores/s1/menu/prices/listed", `{"price_minor":600,"expected_version":0}`, ""); response.Code != http.StatusForbidden {
		t.Fatalf("revoked current store membership was accepted: %d %s", response.Code, response.Body.String())
	}
}

func randomSchemaSuffix(t *testing.T) string {
	t.Helper()
	var bytes [8]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(bytes[:])
}
