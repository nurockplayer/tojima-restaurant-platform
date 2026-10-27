package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
)

// Validate each route's successful, populated shape BEFORE checking absence.
// Otherwise a denied request could vacuously satisfy the privacy contract.
func d1GuestBody(t *testing.T, w *httptest.ResponseRecorder, shape string) map[string]any {
	t.Helper()
	var body map[string]any
	decodePaymentHTTP(t, w, http.StatusOK, &body)
	valid := false
	switch shape {
	case "table":
		valid = body["mode"] == "DIRECT_SCAN" && body["open"] == true && body["current_session_id"] != "" && body["current_session_id"] != nil
	case "menu":
		categories, ok := body["categories"].([]any)
		valid = ok && len(categories) > 0 && len(categories[0].(map[string]any)["products"].([]any)) > 0 && body["currency"] == "JPY"
	case "join":
		session, ok := body["session"].(map[string]any)
		valid = ok && session["status"] == "OPEN" && session["id"] != "" && body["table_join_token"] != nil && body["table_join_token"] != ""
	case "session":
		valid = body["status"] == "OPEN" && body["id"] != nil && body["id"] != ""
	case "cart":
		lines, ok := body["lines"].([]any)
		valid = ok && len(lines) > 0 && body["session_id"] != nil && int64JSON(body["generation"]) > 0 && int64JSON(body["version"]) > 0
	case "preview":
		quote, ok := body["quote"].(map[string]any)
		valid = ok && len(quote["lines"].([]any)) > 0 && body["fingerprint"] != nil && body["fingerprint"] != ""
	case "commit":
		order, ok := body["order"].(map[string]any)
		valid = ok && body["outcome"] == "COMMITTED" && order["order_id"] != nil && order["order_id"] != "" && len(order["quote"].(map[string]any)["lines"].([]any)) > 0
	case "orders", "operations":
		orders, ok := body["orders"].([]any)
		valid = ok && len(orders) > 0
		if shape == "operations" {
			_, ok = body["cash_enabled"].(bool)
			valid = valid && ok
		}
	case "checkout":
		_, ok := body["resume_available"].(bool)
		valid = ok && body["state"] != nil && body["state"] != ""
	case "resume":
		url, ok := body["url"].(string)
		valid = ok && strings.HasPrefix(url, "https://checkout.stripe.com/c/pay/") && body["expires_at"] != nil
	case "read-grant":
		valid = body["order_id"] != nil && body["order_id"] != "" && body["order_read_token"] != nil && body["order_read_token"] != "" && body["expires_at"] != nil
	case "order-status":
		lines, ok := body["effective_lines"].([]any)
		valid = ok && len(lines) > 0 && body["currency"] == "JPY" && body["payment_status"] != nil && body["fulfillment_status"] != nil
	}
	if !valid {
		t.Fatalf("%s response shape missing: %s", shape, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatal("guest response missing no-store")
	}
	return body
}

func d1AssertPrivacy(t *testing.T, raw string, name string, designatedRead bool) {
	t.Helper()
	for _, sentinel := range []string{"INTERNAL-STORE-SENTINEL", "INTERNAL-TENANT-SENTINEL"} {
		if strings.Contains(raw, sentinel) {
			t.Fatalf("internal name leaked %q: %s", sentinel, raw)
		}
	}
	present := strings.Contains(raw, `"store_display_name"`)
	if present != (designatedRead && name != "") {
		t.Fatalf("display-name key in wrong projection: %s", raw)
	}
	if present {
		var body map[string]any
		if err := json.Unmarshal([]byte(raw), &body); err != nil {
			t.Fatal(err)
		}
		if body["store_display_name"] != name {
			t.Fatalf("wrong display name: %s", raw)
		}
	} else if name != "" && strings.Contains(raw, name) {
		t.Fatalf("name outside designated read: %s", raw)
	}
}

func TestGuestDisplayNameEveryGuestRouteAllowlistPostgres(t *testing.T) {
	// Drive the real adapter and inspect its wire form, rather than metadata
	// invented by an in-process provider's response fixture.
	forms := make(chan url.Values, 2)
	var mu sync.Mutex
	sessions := make(map[string]map[string]any)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodPost && r.URL.Path == "/v1/checkout/sessions" {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
				http.Error(w, "invalid form", http.StatusBadRequest)
				return
			}
			forms <- r.PostForm
			amount, err := strconv.ParseInt(r.PostForm.Get("line_items[0][price_data][unit_amount]"), 10, 64)
			if err != nil {
				t.Error(err)
				http.Error(w, "invalid amount", http.StatusBadRequest)
				return
			}
			metadata := make(map[string]string)
			for _, key := range []string{"tenant_id", "store_id", "order_id", "attempt_id"} {
				metadata[key] = r.PostForm.Get("metadata[" + key + "]")
			}
			id := fmt.Sprintf("cs_d1fixture%d", len(sessions)+1)
			session := map[string]any{
				"id": id, "object": "checkout.session", "livemode": false, "mode": "payment",
				"currency": "jpy", "amount_total": amount, "status": "open", "payment_status": "unpaid",
				"expires_at": time.Now().Add(time.Hour).Unix(), "url": "https://checkout.stripe.com/c/pay/" + id + "#fidSynthetic",
				"metadata": metadata,
			}
			sessions["/v1/checkout/sessions/"+id] = session
			if err := json.NewEncoder(w).Encode(session); err != nil {
				t.Error(err)
			}
			return
		}
		if session, ok := sessions[r.URL.Path]; r.Method == http.MethodGet && ok {
			if err := json.NewEncoder(w).Encode(session); err != nil {
				t.Error(err)
			}
			return
		}
		t.Errorf("unexpected Stripe request: %s %s", r.Method, r.URL.Path)
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := stripeprovider.New(stripeprovider.Config{
		APIKey: "sk_test_d1fixtureonly", Mode: stripeprovider.ModeConnectedDirectFixture,
		RequestTimeout: 2 * time.Second, MaxAmountJPY: 100000,
		SuccessURL: "https://example.test/success", CancelURL: "https://example.test/cancel",
		HTTPClient: &http.Client{Transport: d1StripeTransport{target: target}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := newPaymentHTTPFixtureWithProvider(t, provider)
	f := h.fixture
	ctx := context.Background()
	if _, err := f.Pool.Exec(ctx, `UPDATE tenants SET name='INTERNAL-TENANT-SENTINEL' WHERE tenant_id=$1`, f.TenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Pool.Exec(ctx, `UPDATE stores SET name='INTERNAL-STORE-SENTINEL' WHERE tenant_id=$1 AND store_id=$2`, f.TenantID, f.StoreID); err != nil {
		t.Fatal(err)
	}
	// Both phases exercise all routes on populated data. The second is the same
	// store/session after controlled SQL sets the public name.
	for phase, name := range []string{"", "PUBLIC-STORE-NAME"} {
		t.Run(fmt.Sprintf("phase-%d", phase), func(t *testing.T) {
			if name != "" {
				if _, err := f.Pool.Exec(ctx, `UPDATE stores SET guest_display_name=$3 WHERE tenant_id=$1 AND store_id=$2`, f.TenantID, f.StoreID, name); err != nil {
					t.Fatal(err)
				}
			}
			check := func(w *httptest.ResponseRecorder, shape string, designated bool) map[string]any {
				t.Helper()
				body := d1GuestBody(t, w, shape)
				d1AssertPrivacy(t, w.Body.String(), name, designated)
				return body
			}
			joined := check(h.request(t, "POST", "/v1/guest/tables/"+f.Locator+"/join", "{}", "", nil, "", ""), "join", false)
			sid := joined["session"].(map[string]any)["id"].(string)
			token := joined["table_join_token"].(string)
			base := "/v1/guest/sessions/" + sid
			check(h.request(t, "POST", base+"/join", "{}", "", nil, "", ""), "join", false)
			for _, route := range []struct {
				path, shape string
				designated  bool
			}{
				{"/v1/guest/tables/" + f.Locator, "table", true},
				{"/v1/guest/tables/" + f.Locator + "/menu", "menu", true},
				{base, "session", false}, {base + "/menu", "menu", true},
			} {
				check(h.request(t, "GET", route.path, "", token, nil, "", ""), route.shape, route.designated)
			}
			cart, err := f.Orders.ReadCart(ctx, sid, token)
			if err != nil {
				t.Fatal(err)
			}
			mutate := func(method, suffix, product string, quantity int64) map[string]any {
				t.Helper()
				body := fmt.Sprintf(`{"expected_generation":%d,"expected_version":%d,"quantity":%d,"options":[]`, cart.Generation, cart.Version, quantity)
				if product != "" {
					body += fmt.Sprintf(`,"product_key":%q`, product)
				}
				body += "}"
				result := check(h.requestWithKey(t, method, base+"/cart/lines"+suffix, body, token, nil, "", "", fmt.Sprintf("d1-%d-%d-%s", phase, cart.Version, method)), "cart", false)
				cart.Generation, cart.Version = int64JSON(result["generation"]), int64JSON(result["version"])
				return result
			}
			added := mutate("POST", "", "tea", 1)
			lineID := added["lines"].([]any)[0].(map[string]any)["line_id"].(string)
			mutate("PUT", "/"+lineID, "", 2)
			// A second distinct product keeps the DELETE response populated.
			addBody := fmt.Sprintf(`{"expected_generation":%d,"expected_version":%d,"product_key":"teishoku","quantity":1,"options":[{"group_key":"rice","option_key":"regular"}]}`, cart.Generation, cart.Version)
			added = check(h.requestWithKey(t, "POST", base+"/cart/lines", addBody, token, nil, "", "", fmt.Sprintf("d1-%d-meal", phase)), "cart", false)
			cart.Version = int64JSON(added["version"])
			removed := check(h.requestWithKey(t, "DELETE", fmt.Sprintf("%s/cart/lines/%s?generation=%d&version=%d", base, lineID, cart.Generation, cart.Version), "", token, nil, "", "", fmt.Sprintf("d1-%d-remove", phase)), "cart", false)
			cart.Version = int64JSON(removed["version"])
			check(h.request(t, "GET", base+"/cart", "", token, nil, "", ""), "cart", false)
			preview := check(h.request(t, "POST", base+"/cart/preview", fmt.Sprintf(`{"expected_generation":%d,"expected_version":%d}`, cart.Generation, cart.Version), token, nil, "", ""), "preview", false)
			contextResponse := h.request(t, "POST", base+"/checkout-context", "", token, nil, paymentHTTPOrigin, "")
			if contextResponse.Code != 204 || contextResponse.Body.Len() != 0 || len(contextResponse.Result().Cookies()) != 1 {
				t.Fatalf("checkout-context: %d %s", contextResponse.Code, contextResponse.Body.String())
			}
			cookie := contextResponse.Result().Cookies()[0]
			if cookie.Name != fixtureCheckoutProofCookieName || cookie.Value == "" || !cookie.HttpOnly {
				t.Fatal("proof cookie missing")
			}
			d1AssertPrivacy(t, contextResponse.Body.String(), name, false)
			again := h.request(t, "POST", base+"/checkout-context", "", token, cookie, paymentHTTPOrigin, "")
			if again.Code != 204 || again.Body.Len() != 0 || len(again.Result().Cookies()) != 0 {
				t.Fatal("valid context was reset")
			}
			d1AssertPrivacy(t, again.Body.String(), name, false)
			commit := check(h.requestWithKey(t, "POST", base+"/cart/commit", fmt.Sprintf(`{"expected_generation":%d,"expected_version":%d,"preview_fingerprint":%q,"payment_method":"STRIPE"}`, cart.Generation, cart.Version, preview["fingerprint"]), token, cookie, paymentHTTPOrigin, "", fmt.Sprintf("d1-%d-commit", phase)), "commit", false)
			order := commit["order"].(map[string]any)
			oid := order["order_id"].(string)
			if order["requires_staff_acceptance"] != true {
				t.Fatal("DIRECT_SCAN admission changed")
			}
			for _, shape := range []string{"orders", "operations"} {
				body := check(h.request(t, "GET", base+"/"+shape, "", token, nil, "", ""), shape, false)
				idKey := "order_id"
				if shape == "operations" {
					idKey = "id"
				}
				found := false
				for _, item := range body["orders"].([]any) {
					if item.(map[string]any)[idKey] == oid {
						found = true
					}
				}
				if !found {
					t.Fatalf("%s missing committed order %s", shape, oid)
				}
			}
			readGrant := check(h.request(t, "POST", base+"/orders/"+oid+"/read-grant", "", token, nil, "", ""), "read-grant", false)
			if readGrant["order_id"] != oid {
				t.Fatal("wrong read grant order")
			}
			check(h.request(t, "GET", "/v1/guest/orders/"+oid+"/status", "", readGrant["order_read_token"].(string), nil, "", ""), "order-status", false)
			if worked, err := h.service.ProcessNext(ctx); err != nil || !worked {
				t.Fatalf("checkout worker: %v %v", worked, err)
			}
			select {
			case form := <-forms:
				for _, prefix := range []string{"metadata[", "payment_intent_data[metadata]["} {
					for key, want := range map[string]string{"tenant_id": f.TenantID, "store_id": f.StoreID, "order_id": oid, "attempt_id": ""} {
						got := form.Get(prefix + key + "]")
						if got == "" || (want != "" && got != want) || got != form.Get("metadata["+key+"]") {
							t.Fatalf("wrong outbound Stripe scope %s%s: %q", prefix, key, got)
						}
					}
				}
				// Check the complete decoded form so extra metadata keys and
				// URL-encoded name values cannot escape either map's assertion.
				raw, err := json.Marshal(form)
				if err != nil {
					t.Fatal(err)
				}
				d1AssertPrivacy(t, string(raw), "PUBLIC-STORE-NAME", false)
			default:
				t.Fatal("Checkout creation did not reach the Stripe HTTP server")
			}
			checkout := check(h.request(t, "GET", base+"/orders/"+oid+"/checkout", "", token, cookie, "", ""), "checkout", false)
			if checkout["resume_available"] != true {
				t.Fatal("original browser cannot resume")
			}
			check(h.request(t, "POST", base+"/orders/"+oid+"/checkout/resume", "", "", cookie, paymentHTTPOrigin, ""), "resume", false)
		})
	}
}

// Same transport redirection pattern as internal/stripeprovider/provider_test.go.
type d1StripeTransport struct{ target *url.URL }

func (transport d1StripeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	u := *req.URL
	u.Scheme, u.Host = transport.target.Scheme, transport.target.Host
	clone.URL, clone.Host = &u, transport.target.Host
	return http.DefaultTransport.RoundTrip(clone)
}

func d1Fixture(t *testing.T, name *string) *testguest.Fixture {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	f, err := testguest.NewWithOptions(context.Background(), dsn, testguest.Options{CashEnabled: true, CashMode: operations.PayLater, GuestDisplayName: name})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close(context.Background()) })
	return f
}

func TestGuestDisplayNameGatingPostgres(t *testing.T) {
	f := d1Fixture(t, stringPointer("PUBLIC-STORE-NAME"))
	ctx := context.Background()
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	h := NewOperationsRuntime(f.Sessions, f.Menus, f.Orders, f.Operations, nil, nil)
	for _, gate := range []struct{ table, column string }{{"dining_tables", "enabled"}, {"stores", "enabled"}, {"stores", "operating"}, {"tenants", "enabled"}} {
		t.Run(gate.table+"-"+gate.column, func(t *testing.T) {
			// Restore and verify all three reads before each individual gate closes.
			for _, disabled := range []bool{false, true} {
				if _, err := f.Pool.Exec(ctx, "UPDATE "+gate.table+" SET "+gate.column+"=$2 WHERE tenant_id=$1", f.TenantID, !disabled); err != nil {
					t.Fatal(err)
				}
				for i, path := range []string{"/v1/guest/tables/" + f.Locator, "/v1/guest/tables/" + f.Locator + "/menu", "/v1/guest/sessions/" + grant.Session.ID + "/menu"} {
					w := requestJSON(t, h, "GET", path, "", "Authorization: Bearer "+grant.Token)
					if !disabled {
						shape := "menu"
						if i == 0 {
							shape = "table"
						}
						d1GuestBody(t, w, shape)
						d1AssertPrivacy(t, w.Body.String(), "PUBLIC-STORE-NAME", true)
					} else {
						if i == 0 {
							var body map[string]any
							decodePaymentHTTP(t, w, 200, &body)
							if body["mode"] != "DIRECT_SCAN" || body["open"] != false || body["current_session_id"] != nil {
								t.Fatal("disabled public status disclosed seating")
							}
						} else {
							want := 404
							if i == 2 {
								want = 403
							}
							var body map[string]any
							decodePaymentHTTP(t, w, want, &body)
							if body["error"] == nil {
								t.Fatal("missing denied response shape")
							}
						}
						d1AssertPrivacy(t, w.Body.String(), "PUBLIC-STORE-NAME", false)
					}
				}
			}
			if _, err := f.Pool.Exec(ctx, "UPDATE "+gate.table+" SET "+gate.column+"=true WHERE tenant_id=$1", f.TenantID); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestGuestDisplayNameSameStoreIDDifferentTenantsPostgres(t *testing.T) {
	f := d1Fixture(t, nil)
	ctx := context.Background()
	for i, name := range []string{"A店", "B店"} {
		tenant, locator := fmt.Sprintf("tenant-%d", i), fmt.Sprintf("locator-%d", i)
		if _, err := f.Pool.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES($1,$2)`, tenant, "INTERNAL-TENANT-SENTINEL-"+tenant); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Pool.Exec(ctx, `INSERT INTO stores(tenant_id,store_id,name,qr_entry_mode,guest_display_name) VALUES($1,'s1',$2,'DIRECT_SCAN',$3)`, tenant, "INTERNAL-STORE-SENTINEL-"+tenant, name); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Pool.Exec(ctx, `INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES($1,'s1','table','Table',$2)`, tenant, locator); err != nil {
			t.Fatal(err)
		}
		draft, err := f.Menus.SaveCompanyDraft(ctx, tenant, "fixture", 0, catalog.CatalogData{Categories: []catalog.Category{{Key: "food", Name: "Food", Products: []catalog.Product{{Key: "tea", Name: "Tea", Active: true, BasePriceMinor: 330}}}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Menus.PublishCompanyDraft(ctx, tenant, "fixture", draft.Version); err != nil {
			t.Fatal(err)
		}
	}
	h := NewOperationsRuntime(f.Sessions, f.Menus, f.Orders, f.Operations, nil, nil)
	sids, tokens := make([]string, 2), make([]string, 2)
	for i, name := range []string{"A店", "B店"} {
		locator := fmt.Sprintf("locator-%d", i)
		join := requestJSON(t, h, "POST", "/v1/guest/tables/"+locator+"/join", "{}", "")
		body := d1GuestBody(t, join, "join")
		sids[i], tokens[i] = body["session"].(map[string]any)["id"].(string), body["table_join_token"].(string)
		for j, path := range []string{"/v1/guest/tables/" + locator, "/v1/guest/tables/" + locator + "/menu", "/v1/guest/sessions/" + sids[i] + "/menu"} {
			w := requestJSON(t, h, "GET", path, "", "Authorization: Bearer "+tokens[i])
			shape := "menu"
			if j == 0 {
				shape = "table"
			}
			d1GuestBody(t, w, shape)
			d1AssertPrivacy(t, w.Body.String(), name, true)
			if strings.Contains(w.Body.String(), []string{"B店", "A店"}[i]) {
				t.Fatal("cross-tenant public name")
			}
		}
	}
	for _, suffix := range []string{"menu", "cart", "operations"} {
		w := requestJSON(t, h, "GET", "/v1/guest/sessions/"+sids[1]+"/"+suffix, "", "Authorization: Bearer "+tokens[0])
		var body map[string]any
		decodePaymentHTTP(t, w, 403, &body)
		if body["error"] != "forbidden" {
			t.Fatalf("unexpected denial: %s", w.Body.String())
		}
		d1AssertPrivacy(t, w.Body.String(), "B店", false)
	}
}

func TestGuestDisplayNameAdminProjectionAndAdmissionUnchangedPostgres(t *testing.T) {
	f := d1Fixture(t, nil)
	ctx := context.Background()
	scope := catalog.StoreScope{TenantID: f.TenantID, StoreID: f.StoreID}
	readTx := func() catalog.PublishedMenu {
		t.Helper()
		tx, err := f.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		menu, err := f.Menus.ReadStoreMenuTx(ctx, tx, scope)
		if err != nil {
			t.Fatal(err)
		}
		if menu.GuestDisplayName != nil {
			t.Fatal("admission read acquired a public name")
		}
		return menu
	}
	before := readTx()
	if _, err := f.Pool.Exec(ctx, `UPDATE stores SET guest_display_name='PUBLIC-STORE-NAME' WHERE tenant_id=$1 AND store_id=$2`, f.TenantID, f.StoreID); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, readTx()) {
		t.Fatal("ReadStoreMenuTx output changed after public-name update")
	}
	menu, err := f.Menus.ReadStoreMenu(ctx, scope)
	if err != nil || menu.GuestDisplayName == nil || *menu.GuestDisplayName != "PUBLIC-STORE-NAME" {
		t.Fatalf("read name: %#v %v", menu, err)
	}
	// Exercise the real admin/store route for both staff and manager projections,
	// then company published/draft responses with the tenant administrator.
	memberships := &httpMemberships{values: []authz.Membership{{TenantID: f.TenantID, StoreID: f.StoreID, Role: authz.Staff}}}
	h := NewMenuHandler(f.Menus, httpTestIdentity{authz.Actor{SubjectID: "fixture", MFA: true}}, authz.PolicyAuthorizer{Directory: memberships})
	for _, role := range []authz.Role{authz.Staff, authz.StoreManager} {
		memberships.values = []authz.Membership{{TenantID: f.TenantID, StoreID: f.StoreID, Role: role}}
		w := requestJSON(t, h, "GET", "/v1/admin/tenants/"+f.TenantID+"/stores/"+f.StoreID+"/menu", "", "")
		var body map[string]any
		decodePaymentHTTP(t, w, 200, &body)
		if len(body["categories"].([]any)) == 0 || body["mode"] != "LINKED" {
			t.Fatal("empty admin projection")
		}
		d1AssertPrivacy(t, w.Body.String(), "PUBLIC-STORE-NAME", false)
	}
	memberships.values = []authz.Membership{{TenantID: f.TenantID, Role: authz.TenantAdmin}}
	for _, suffix := range []string{"published", "draft"} {
		w := requestJSON(t, h, "GET", "/v1/admin/tenants/"+f.TenantID+"/menu/"+suffix, "", "")
		var body map[string]any
		decodePaymentHTTP(t, w, 200, &body)
		if len(body["categories"].([]any)) == 0 {
			t.Fatal("empty company projection")
		}
		d1AssertPrivacy(t, w.Body.String(), "PUBLIC-STORE-NAME", false)
	}
}
