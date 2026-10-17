package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
)

func TestOrderingGuestHTTP(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	f, err := testguest.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close(ctx)
	h := NewGuestHandler(f.Sessions, f.Menus, f.Orders)
	request := func(method, path, body, token, key string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	decode := func(w *httptest.ResponseRecorder, status int) map[string]any {
		t.Helper()
		if w.Code != status {
			t.Fatalf("HTTP %d want%d: %s", w.Code, status, w.Body.String())
		}
		var v map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	join := decode(request("POST", "/v1/guest/tables/test-table/join", "{}", "", ""), 200)
	token := join["table_join_token"].(string)
	sid := join["session"].(map[string]any)["id"].(string)
	base := "/v1/guest/sessions/" + sid
	joinB := decode(request("POST", "/v1/guest/tables/test-table/join", fmt.Sprintf(`{"expected_session_id":%q}`, sid), "", ""), 200)
	tokenB := joinB["table_join_token"].(string)
	if w := request("POST", "/v1/admin/tenants/test-tenant/menu/publish", "{}", token, ""); w.Code != 404 {
		t.Fatalf("guest surface exposed admin route: %d", w.Code)
	}
	decode(request("GET", base+"/cart", "", "invalid", ""), 403)
	decode(request("GET", base+"/menu", "", "", ""), 403)
	if w := request("GET", base+"/cart", "", token, ""); w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("guest response can be cached")
	}
	// Keep monetary integers exact beyond JavaScript's safe-integer boundary.
	const price int64 = 9007199254740993
	if _, err := f.Menus.SetStorePriceOverride(ctx, catalog.StoreScope{TenantID: "test-tenant", StoreID: "test-store"}, "fixture-bootstrap", "tea", price, 0); err != nil {
		t.Fatal(err)
	}
	menu := decode(request("GET", base+"/menu?amounts=string", "", token, ""), 200)
	products := menu["categories"].([]any)[0].(map[string]any)["products"].([]any)
	found := false
	for _, p := range products {
		item := p.(map[string]any)
		if item["key"] == "tea" {
			found = true
			if item["effective_price_minor"] != "9007199254740993" {
				t.Fatalf("inexact menu price: %#v", item)
			}
		}
	}
	if !found {
		t.Fatal("tea missing")
	}
	decode(request("POST", base+"/cart/lines", `{"expected_generation":1,"expected_version":1,"product_key":"tea","quantity":1,"options":[],"tenant_id":"other"}`, token, "bad-body"), 400)
	add := `{"expected_generation":1,"expected_version":1,"product_key":"tea","quantity":1,"options":[]}`
	cart := decode(request("POST", base+"/cart/lines", add, token, "add-one"), 200)
	if cart["version"] != float64(2) || len(cart["lines"].([]any)) != 1 {
		t.Fatalf("unexpected cart %#v", cart)
	}
	decode(request("POST", base+"/cart/lines", add, tokenB, "add-conflict"), 409)
	replayed := decode(request("POST", base+"/cart/lines", add, token, "add-one"), 200)
	if len(replayed["lines"].([]any)) != 1 {
		t.Fatal("retry duplicated line")
	}
	preview := decode(request("POST", base+"/cart/preview", `{"expected_generation":1,"expected_version":2}`, token, ""), 200)
	q := preview["quote"].(map[string]any)
	tax := q["tax"].(map[string]any)
	if tax["total_gross_jpy"] != "9007199254740993" {
		t.Fatalf("inexact quote: %#v", tax)
	}
	for _, forbidden := range []string{"catalog_mode", "revision", "catalog_id", "tenant_id", "store_id", "submitter", "table_join_token"} {
		if strings.Contains(mustJSON(t, preview), `"`+forbidden+`"`) {
			t.Fatalf("public quote leaked %s", forbidden)
		}
	}
	body := fmt.Sprintf(`{"expected_generation":1,"expected_version":2,"preview_fingerprint":%q,"payment_method":"CASH"}`, preview["fingerprint"])
	committed := decode(request("POST", base+"/cart/commit", body, token, "commit-one"), 200)
	if committed["outcome"] != "COMMITTED" {
		t.Fatalf("not committed: %#v", committed)
	}
	order := committed["order"].(map[string]any)
	if order["requires_staff_acceptance"] != true {
		t.Fatal("DIRECT_SCAN admission flag lost")
	}
	winner := decode(request("POST", base+"/cart/commit", strings.Replace(body, "CASH", "STRIPE", 1), tokenB, "competing-method"), 200)
	if winner["outcome"] != "ALREADY_COMMITTED" || winner["order"].(map[string]any)["order_id"] != order["order_id"] || winner["order"].(map[string]any)["payment_method"] != "CASH" {
		t.Fatalf("winner changed: %#v", winner)
	}
	listed := decode(request("GET", base+"/orders", "", tokenB, ""), 200)
	if len(listed["orders"].([]any)) != 1 {
		t.Fatal("duplicate shared order")
	}
	if strings.Contains(mustJSON(t, listed), "catalog_mode") || strings.Contains(mustJSON(t, listed), token) {
		t.Fatal("shared order leaks private data")
	}
	decode(request("POST", base+"/cart/preview", strings.Repeat(" ", 16<<10)+"{}", token, ""), 413)
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, e := json.Marshal(v)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func TestMoneyProjectionPreservesInt64(t *testing.T) {
	w := httptest.NewRecorder()
	writeMoneyJSON(w, http.StatusOK, map[string]any{"price_minor": int64(9223372036854775807), "nested": []any{map[string]any{"gross_jpy": int64(9007199254740993), "quantity": 2}}, "version": 1})
	var v map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v["price_minor"] != "9223372036854775807" || v["nested"].([]any)[0].(map[string]any)["gross_jpy"] != "9007199254740993" || v["version"] != float64(1) {
		t.Fatalf("precision projection %s", w.Body.String())
	}
}

func TestCartEditAcceptsPublishedUnicodeSelections(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	f, err := testguest.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close(ctx)
	groups := make([]catalog.OptionGroup, 21)
	picks := make([]catalog.OptionSelection, 21)
	for i := range groups {
		g := fmt.Sprintf("%02d", i) + strings.Repeat("🍣", 98)
		o := strings.Repeat("🍚", 100)
		groups[i] = catalog.OptionGroup{Key: g, Name: fmt.Sprintf("Group %d", i), MinSelect: 1, MaxSelect: 1, Options: []catalog.Option{{Key: o, Name: "Required", Active: true}}}
		picks[i] = catalog.OptionSelection{GroupKey: g, OptionKey: o}
	}
	draft, err := f.Menus.SaveCompanyDraft(ctx, "test-tenant", "fixture-bootstrap", 1, catalog.CatalogData{Categories: []catalog.Category{{Key: "c", Name: "Menu", Products: []catalog.Product{{Key: "many", Name: "Many options", Active: true, BasePriceMinor: 1100, Groups: groups}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Menus.PublishCompanyDraft(ctx, "test-tenant", "fixture-bootstrap", draft.Version); err != nil {
		t.Fatal(err)
	}
	scope := catalog.StoreScope{TenantID: "test-tenant", StoreID: "test-store"}
	menu, err := f.Menus.ReadStoreMenu(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range menu.Categories {
		for _, p := range c.Products {
			if _, err = f.Menus.SetStoreAvailability(ctx, scope, "fixture-bootstrap", catalog.AvailabilityChange{Target: catalog.AvailabilityTarget{Kind: catalog.ProductAvailability, ProductKey: p.Key}, Status: catalog.Available, ExpectedVersion: p.AvailabilityVersion}); err != nil {
				t.Fatal(err)
			}
			for _, g := range p.Groups {
				for _, o := range g.Options {
					if _, err = f.Menus.SetStoreAvailability(ctx, scope, "fixture-bootstrap", catalog.AvailabilityChange{Target: catalog.AvailabilityTarget{Kind: catalog.OptionAvailability, ProductKey: p.Key, GroupKey: g.Key, OptionKey: o.Key}, Status: catalog.Available, ExpectedVersion: o.AvailabilityVersion}); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	body := mustJSON(t, map[string]any{"expected_generation": 1, "expected_version": 1, "product_key": "many", "quantity": 1, "options": picks})
	if len(body) <= 16<<10 {
		t.Fatalf("fixture doesn't exceed old limit: %d", len(body))
	}
	r := httptest.NewRequest("POST", "/v1/guest/sessions/"+grant.Session.ID+"/cart/lines", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+grant.Token)
	r.Header.Set("Idempotency-Key", "unicode-selections")
	w := httptest.NewRecorder()
	NewGuestHandler(f.Sessions, f.Menus, f.Orders).ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("valid published selections rejected: %d %s", w.Code, w.Body.String())
	}
	preview, err := f.Orders.Preview(ctx, grant.Session.ID, grant.Token, 1, 2)
	if err != nil || len(preview.Quote.Lines[0].Options) != 21 {
		t.Fatalf("preview options rejected: %v", err)
	}
}

func TestCartEditWireBudget(t *testing.T) {
	key := strings.Repeat(`\ud83c\udf63`, 100)
	pick := `{"group_key":"` + key + `","option_key":"` + key + `"}`
	// The decoder must accept the largest permitted escaped Unicode key shape.
	body := `{"expected_generation":1,"expected_version":1,"product_key":"p","quantity":1,"options":[` + strings.TrimSuffix(strings.Repeat(pick+",", 5000), ",") + `]}`
	var input cartLineRequest
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/", strings.NewReader(body))
	if !decodeCartLimit(w, r, &input, cartEditMaxBodyBytes) || len(input.Options) != 5000 {
		t.Fatalf("valid wire budget rejected: bytes=%d code=%d", len(body), w.Code)
	}
	for _, unknownLength := range []bool{false, true} {
		oversized := httptest.NewRequest("POST", "/", strings.NewReader(strings.Repeat(" ", int(cartEditMaxBodyBytes))+"{}"))
		if unknownLength {
			oversized.ContentLength = -1
		}
		response := httptest.NewRecorder()
		if decodeCartLimit(response, oversized, &cartLineRequest{}, cartEditMaxBodyBytes) || response.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("oversize unknownLength=%v code=%d", unknownLength, response.Code)
		}
	}
}
