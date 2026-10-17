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
		{Key: "listed", Name: "Listed meal", Description: strings.Repeat("x", 4000), BasePriceMinor: 500, Active: true, Groups: []catalog.OptionGroup{{
			Key: "size", Name: "Size", MinSelect: 0, MaxSelect: 1,
			Options: []catalog.Option{{Key: "large", Name: "Large", Active: true}},
		}}},
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

	storeMenu, err := service.ReadStoreMenu(ctx, catalog.StoreScope{TenantID: "t", StoreID: "s1"})
	if err != nil {
		t.Fatal(err)
	}
	productVersion := int64(0)
	optionVersion := int64(0)
	for _, category := range storeMenu.Categories {
		for _, product := range category.Products {
			if product.Key == "listed" {
				productVersion = product.AvailabilityVersion
				optionVersion = product.Groups[0].Options[0].AvailabilityVersion
			}
		}
	}
	if productVersion == 0 || optionVersion == 0 {
		t.Fatalf("published product or option has no availability version: product=%d option=%d", productVersion, optionVersion)
	}
	availabilityPath := "/v1/admin/tenants/t/stores/s1/menu/availability"
	productTarget := catalog.AvailabilityTarget{Kind: catalog.ProductAvailability, ProductKey: "listed"}
	optionTarget := catalog.AvailabilityTarget{Kind: catalog.OptionAvailability, ProductKey: "listed", GroupKey: "size", OptionKey: "large"}
	changeAvailability := func(target catalog.AvailabilityTarget, status catalog.Availability, version int64, reason string) string {
		body, marshalErr := json.Marshal(catalog.AvailabilityChange{
			Target: target,
			Status: status, Reason: reason, ExpectedVersion: version,
		})
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		return string(body)
	}
	staffMemberships := &httpMemberships{values: []authz.Membership{{TenantID: "t", StoreID: "s1", Role: authz.Staff}}}
	staff := NewMenuHandler(service, httpTestIdentity{authz.Actor{SubjectID: "staff"}}, authz.PolicyAuthorizer{Directory: staffMemberships})
	kitchenMemberships := &httpMemberships{values: []authz.Membership{{TenantID: "t", StoreID: "s1", Role: authz.Kitchen}}}
	kitchen := NewMenuHandler(service, httpTestIdentity{authz.Actor{SubjectID: "kitchen"}}, authz.PolicyAuthorizer{Directory: kitchenMemberships})
	noMFAManager := NewMenuHandler(service, httpTestIdentity{authz.Actor{SubjectID: "manager", MFA: false}}, authz.PolicyAuthorizer{Directory: managerMemberships})
	otherStoreManagerMemberships := &httpMemberships{values: []authz.Membership{{TenantID: "t", StoreID: "s2", Role: authz.StoreManager}}}
	otherStoreManager := NewMenuHandler(service, httpTestIdentity{authz.Actor{SubjectID: "other-store-manager", MFA: true}}, authz.PolicyAuthorizer{Directory: otherStoreManagerMemberships})
	otherTenantAdminMemberships := &httpMemberships{values: []authz.Membership{{TenantID: "other", Role: authz.TenantAdmin}}}
	otherTenantAdmin := NewMenuHandler(service, httpTestIdentity{authz.Actor{SubjectID: "other-owner", MFA: true}}, authz.PolicyAuthorizer{Directory: otherTenantAdminMemberships})
	defaultDeny := NewMenuHandler(service, nil, nil)
	availableBody := changeAvailability(productTarget, catalog.Available, productVersion, "")
	if response := requestJSON(t, staff, http.MethodPut, availabilityPath, availableBody, ""); response.Code != http.StatusForbidden {
		t.Fatalf("staff could confirm product sale: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, noMFAManager, http.MethodPut, availabilityPath, availableBody, ""); response.Code != http.StatusForbidden {
		t.Fatalf("manager without MFA could confirm product sale: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, otherStoreManager, http.MethodPut, availabilityPath, availableBody, ""); response.Code != http.StatusForbidden {
		t.Fatalf("other-store manager changed availability: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, otherTenantAdmin, http.MethodPut, availabilityPath, availableBody, ""); response.Code != http.StatusForbidden {
		t.Fatalf("other-tenant manager changed availability: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, kitchen, http.MethodPut, availabilityPath, changeAvailability(productTarget, catalog.Paused, productVersion, "out of stock"), ""); response.Code != http.StatusForbidden {
		t.Fatalf("kitchen role changed availability: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, staff, http.MethodPut, availabilityPath, changeAvailability(productTarget, catalog.Paused, productVersion, ""), ""); response.Code != http.StatusBadRequest {
		t.Fatalf("pause without a reason was accepted: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, defaultDeny, http.MethodPut, availabilityPath, availableBody, ""); response.Code != http.StatusForbidden {
		t.Fatalf("default-deny runtime accepted availability change: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, staff, http.MethodPut, availabilityPath, strings.TrimSuffix(availableBody, "}")+`,"unexpected":true}`, ""); response.Code != http.StatusBadRequest {
		t.Fatalf("unknown availability field was not rejected by strict decode before role selection: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, manager, http.MethodPut, availabilityPath, availableBody, ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"AVAILABLE"`) {
		t.Fatalf("MFA manager did not confirm sale: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, manager, http.MethodPut, availabilityPath, changeAvailability(optionTarget, catalog.Available, optionVersion, ""), ""); response.Code != http.StatusOK {
		t.Fatalf("MFA manager did not confirm new option: %d %s", response.Code, response.Body.String())
	}
	staffPauseBody := changeAvailability(productTarget, catalog.Paused, productVersion+1, "ingredient unavailable")
	if response := requestJSON(t, staff, http.MethodPut, availabilityPath, staffPauseBody, ""); response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"PAUSED"`) {
		t.Fatalf("staff could not pause own-store product: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, staff, http.MethodPut, availabilityPath, changeAvailability(optionTarget, catalog.Paused, optionVersion+1, "equipment unavailable"), ""); response.Code != http.StatusOK {
		t.Fatalf("staff could not pause own-store option: %d %s", response.Code, response.Body.String())
	}
	staffReopenBody := changeAvailability(productTarget, catalog.Available, productVersion+2, "")
	if response := requestJSON(t, staff, http.MethodPut, availabilityPath, staffReopenBody, ""); response.Code != http.StatusForbidden {
		t.Fatalf("staff reactivated paused product: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, noMFAManager, http.MethodPut, availabilityPath, staffReopenBody, ""); response.Code != http.StatusForbidden {
		t.Fatalf("manager without MFA reactivated paused product: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, manager, http.MethodPut, availabilityPath, staffReopenBody, ""); response.Code != http.StatusOK {
		t.Fatalf("MFA manager did not reopen own-store product: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, manager, http.MethodPut, availabilityPath, changeAvailability(productTarget, catalog.Paused, productVersion+3, "manager pause"), ""); response.Code != http.StatusOK {
		t.Fatalf("MFA manager could not pause own-store product: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, manager, http.MethodPut, availabilityPath, changeAvailability(productTarget, catalog.Available, productVersion+4, ""), ""); response.Code != http.StatusOK {
		t.Fatalf("MFA manager could not reopen paused product: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, manager, http.MethodPut, availabilityPath, changeAvailability(optionTarget, catalog.Available, optionVersion+2, ""), ""); response.Code != http.StatusOK {
		t.Fatalf("MFA manager did not reopen own-store option: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, manager, http.MethodPut, availabilityPath, changeAvailability(optionTarget, catalog.Paused, optionVersion+3, "manager pause"), ""); response.Code != http.StatusOK {
		t.Fatalf("MFA manager could not pause own-store option: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, manager, http.MethodPut, availabilityPath, changeAvailability(optionTarget, catalog.Available, optionVersion+4, ""), ""); response.Code != http.StatusOK {
		t.Fatalf("MFA manager could not reopen paused option: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, manager, http.MethodPut, availabilityPath, changeAvailability(productTarget, catalog.Paused, productVersion+3, "stale pause"), ""); response.Code != http.StatusConflict {
		t.Fatalf("stale availability CAS was not rejected: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, manager, http.MethodPut, availabilityPath, changeAvailability(productTarget, catalog.PendingConfirmation, productVersion+5, ""), ""); response.Code != http.StatusBadRequest {
		t.Fatalf("client could set pending confirmation: %d %s", response.Code, response.Body.String())
	}
	var availabilityAuditCount int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM menu_admin_audit WHERE tenant_id='t' AND store_id='s1' AND operation='AVAILABILITY_PRODUCT' AND entity_key='listed'`).Scan(&availabilityAuditCount); err != nil {
		t.Fatal(err)
	}
	if availabilityAuditCount != 5 {
		t.Fatalf("availability updates did not use the audited service path: got %d audit rows, want confirm/staff-pause/reopen/manager-pause/reopen", availabilityAuditCount)
	}
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM menu_admin_audit WHERE tenant_id='t' AND store_id='s1' AND operation='AVAILABILITY_OPTION' AND entity_key='listed/size/large'`).Scan(&availabilityAuditCount); err != nil {
		t.Fatal(err)
	}
	if availabilityAuditCount != 5 {
		t.Fatalf("option availability updates did not use the audited service path: got %d audit rows, want confirm/staff-pause/reopen/manager-pause/reopen", availabilityAuditCount)
	}

	invalidPublication := input.CatalogData
	invalidPublication.Categories = append([]catalog.Category(nil), input.Categories...)
	invalidPublication.Categories[0].Products = append([]catalog.Product(nil), input.Categories[0].Products...)
	invalidPublication.Categories[0].Products[0].Groups = append(invalidPublication.Categories[0].Products[0].Groups, catalog.OptionGroup{
		Key: "new-required", Name: "Required", MinSelect: 1, MaxSelect: 1,
		Options: []catalog.Option{{Key: "choice", Name: "Choice", Active: true}},
	})
	if _, err = service.SaveCompanyDraft(ctx, "t", "owner", 1, invalidPublication); err != nil {
		t.Fatalf("save draft with disallowed newly required group: %v", err)
	}
	if response := requestJSON(t, manager, http.MethodPost, "/v1/admin/tenants/t/menu/publish", `{"expected_version":2}`, ""); response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), `"issues"`) {
		t.Fatalf("unauthorized store manager saw company publication validation: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, otherTenantAdmin, http.MethodPost, "/v1/admin/tenants/t/menu/publish", `{"expected_version":2}`, ""); response.Code != http.StatusForbidden || strings.Contains(response.Body.String(), `"issues"`) {
		t.Fatalf("other tenant saw company publication validation: %d %s", response.Code, response.Body.String())
	}
	if response := requestJSON(t, owner, http.MethodPost, "/v1/admin/tenants/t/menu/publish", `{"expected_version":2}`, ""); response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"error":"publication_blocked"`) || !strings.Contains(response.Body.String(), `"code":"NEW_REQUIRED_GROUP"`) || !strings.Contains(response.Body.String(), `"product_key":"listed"`) || !strings.Contains(response.Body.String(), `"group_key":"new-required"`) {
		t.Fatalf("authorized publication failure omitted structured issues: %d %s", response.Code, response.Body.String())
	}
	publicAfterBlocked, err := service.ReadPublishedByTableLocator(ctx, "public-menu")
	if err != nil {
		t.Fatal(err)
	}
	listedAfterBlocked := false
	for _, category := range publicAfterBlocked.Categories {
		for _, product := range category.Products {
			if product.Key == "listed" && len(product.Groups) == 1 && product.Groups[0].Key == "size" {
				listedAfterBlocked = true
			}
		}
	}
	if publicAfterBlocked.Revision != 1 || !listedAfterBlocked {
		t.Fatalf("blocked company publish changed current revision: revision=%d listedWithOriginalGroup=%v err=%v", publicAfterBlocked.Revision, listedAfterBlocked, err)
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
