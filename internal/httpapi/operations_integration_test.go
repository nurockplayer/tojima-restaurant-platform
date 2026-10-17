package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testoperations"
)

func TestOperationsHTTPPermissionsAndCashJourney(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	f, err := testguest.NewWithOptions(ctx, dsn, testguest.Options{CashEnabled: true, CashMode: operations.Prepaid})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close(ctx)
	identity, directory, creds, err := testoperations.NewCredentialSet("test-tenant", "test-store")
	if err != nil {
		t.Fatal(err)
	}
	authorizer := authz.PolicyAuthorizer{Directory: directory}
	h := NewOperationsRuntime(f.Sessions, f.Menus, f.Orders, f.Operations, identity, authorizer)
	request := func(method, path, body, token, key string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	decode := func(w *httptest.ResponseRecorder, status int) map[string]any {
		t.Helper()
		if w.Code != status {
			t.Fatalf("HTTP%d want%d: %s", w.Code, status, w.Body.String())
		}
		var v map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	const base = "/v1/staff/tenants/test-tenant/stores/test-store"
	cashier, kitchen, manager := creds.Cashier.Token, creds.Kitchen.Token, creds.Manager.Token
	caps := decode(request("GET", base+"/operations/access", "", cashier, ""), 200)
	if caps["can_cash"] != true || caps["can_kitchen"] != false || caps["can_manage"] != false {
		t.Fatalf("bad cashier caps %v", caps)
	}
	caps = decode(request("GET", base+"/operations/access", "", kitchen, ""), 200)
	if caps["can_cash"] != false || caps["can_kitchen"] != true {
		t.Fatalf("bad kitchen caps %v", caps)
	}
	decode(request("GET", base+"/operations/tables", "", "", ""), 403)
	decode(request("GET", strings.Replace(base, "test-store", "other-store", 1)+"/operations/tables", "", cashier, ""), 404)
	settings := decode(request("GET", base+"/operations/cash-settings", "", manager, ""), 200)
	settingBody := fmt.Sprintf(`{"enabled":true,"mode":"PREPAID","expected_version":%.0f}`, settings["version"])
	decode(request("PUT", base+"/operations/cash-settings", settingBody, cashier, ""), 403)
	decode(request("PUT", base+"/operations/cash-settings", settingBody, kitchen, ""), 403)
	// Membership is read again for every request, including an already known token.
	directory.Remove(creds.Cashier.SubjectID)
	decode(request("GET", base+"/operations/tables", "", cashier, ""), 403)
	directory.Set(creds.Cashier.SubjectID, []authz.Membership{{TenantID: "test-tenant", StoreID: "different-store", Role: authz.Staff}})
	decode(request("GET", base+"/operations/tables", "", cashier, ""), 403)
	directory.Set(creds.Cashier.SubjectID, []authz.Membership{{TenantID: "test-tenant", StoreID: "test-store", Role: authz.TenantAdmin}})
	caps = decode(request("GET", base+"/operations/access", "", cashier, ""), 403)
	_ = caps // no verified MFA for a promoted tenant-admin membership
	directory.Set(creds.Cashier.SubjectID, []authz.Membership{{TenantID: "test-tenant", StoreID: "test-store", Role: authz.Staff}})
	join := decode(request("POST", "/v1/guest/tables/test-table/join", "{}", "", ""), 200)
	guest := join["table_join_token"].(string)
	sid := join["session"].(map[string]any)["id"].(string)
	gb := "/v1/guest/sessions/" + sid
	sb := base + "/tables/table-01/sessions/" + sid
	directory.Set(creds.Manager.SubjectID, []authz.Membership{{TenantID: "test-tenant", StoreID: "", Role: authz.TenantAdmin}})
	adminCaps := decode(request("GET", base+"/operations/access", "", manager, ""), 200)
	if adminCaps["can_manage"] != true || adminCaps["can_cash"] != false || adminCaps["can_serve"] != false {
		t.Fatalf("tenant admin gained cashier access: %v", adminCaps)
	}
	decode(request("GET", base+"/operations/kds", "", cashier, ""), 403)
	decode(request("GET", base+"/operations/kds", "", manager, ""), 403)
	decode(request("POST", sb+"/cash/preview", "{}", manager, ""), 403)
	decode(request("POST", sb+"/close", "{}", manager, ""), 403)
	directory.Set(creds.Manager.SubjectID, []authz.Membership{{TenantID: "test-tenant", StoreID: "test-store", Role: authz.StoreManager}})
	decode(request("GET", base+"/operations/kds", "", manager, ""), 403)
	decode(request("GET", base+"/operations/tables", "", guest, ""), 403)
	decode(request("GET", gb+"/operations", "", cashier, ""), 403)
	decode(request("POST", gb+"/cart/lines", `{"expected_generation":1,"expected_version":1,"product_key":"tea","quantity":2,"options":[]}`, guest, "add"), 200)
	preview := decode(request("POST", gb+"/cart/preview", `{"expected_generation":1,"expected_version":2}`, guest, ""), 200)
	order := decode(request("POST", gb+"/cart/commit", fmt.Sprintf(`{"expected_generation":1,"expected_version":2,"preview_fingerprint":%q,"payment_method":"CASH"}`, preview["fingerprint"]), guest, "commit"), 200)["order"].(map[string]any)
	oid := order["order_id"].(string)
	if order["requires_staff_acceptance"] != true {
		t.Fatalf("DIRECT_SCAN order omitted staff-acceptance snapshot: %v", order)
	}
	ob := sb + "/orders/" + oid
	states := decode(request("GET", sb+"/operations", "", cashier, ""), 200)
	first := states["orders"].([]any)[0].(map[string]any)
	if first["payment_status"] != "UNPAID" || first["fulfillment_status"] != "WAITING" || first["cash_mode"] != "PREPAID" {
		t.Fatalf("bad initial %v", first)
	}
	decode(request("POST", sb+"/close", "{}", cashier, ""), 409)
	action := fmt.Sprintf(`{"expected_version":%.0f}`, first["version"])
	decode(request("POST", ob+"/accept", action, kitchen, "accept"), 403)
	decode(request("POST", ob+"/accept", strings.TrimSuffix(action, "}")+`,"actor":"forged"}`, cashier, "accept"), 400)
	accepted := decode(request("POST", ob+"/accept", action, cashier, "accept"), 200)
	if accepted["fulfillment_status"] != "WAITING" {
		t.Fatal("unpaid prepaid released")
	}
	cards := decode(request("GET", base+"/operations/kds", "", kitchen, ""), 200)
	if len(cards["orders"].([]any)) != 0 {
		t.Fatal("prepaid KDS before payment")
	}
	decode(request("POST", sb+"/cash/preview", "{}", kitchen, ""), 403)
	cash := decode(request("POST", sb+"/cash/preview", "{}", cashier, ""), 200)
	if cash["due_jpy"] != "660" || cash["tax"].(map[string]any)["tax_jpy"] != "60" {
		t.Fatalf("bad cash invoice %v", cash)
	}
	pay := fmt.Sprintf(`{"fingerprint":%q,"tendered_jpy":"1000","register_session_ref":"test-shift"}`, cash["fingerprint"])
	decode(request("POST", sb+"/cash/settle", pay, kitchen, "settle"), 403)
	decode(request("POST", sb+"/cash/settle", strings.Replace(pay, `"1000"`, `1000`, 1), cashier, "wrong-type"), 400)
	receipt := decode(request("POST", sb+"/cash/settle", pay, cashier, "settle"), 200)
	if receipt["due_jpy"] != "660" || receipt["change_jpy"] != "340" || receipt["cashier_id"] != creds.Cashier.SubjectID {
		t.Fatalf("receipt %v", receipt)
	}
	replay := decode(request("POST", sb+"/cash/settle", pay, cashier, "settle"), 200)
	if replay["id"] != receipt["id"] {
		t.Fatal("settlement replay duplicated")
	}
	// Only guest-safe state crosses the TABLE_JOIN endpoint; receipts and staff
	// identities remain staff-only, amounts remain exact decimal strings.
	g := request("GET", gb+"/operations", "", guest, "")
	guestState := decode(g, 200)
	guestOrders := guestState["orders"].([]any)
	if len(guestOrders) != 1 || guestOrders[0].(map[string]any)["requires_staff_acceptance"] != true {
		t.Fatalf("guest order omitted staff-acceptance snapshot: %v", guestState)
	}
	for _, private := range []string{"cashier_id", "tenant_id", "store_id", "register_session_ref", "receipts", cashier} {
		if strings.Contains(g.Body.String(), private) {
			t.Fatalf("guest leak %s", private)
		}
	}
	cards = decode(request("GET", base+"/operations/kds", "", kitchen, ""), 200)
	if len(cards["orders"].([]any)) != 1 {
		t.Fatal("expected one KDS card")
	}
	state := cards["orders"].([]any)[0].(map[string]any)
	if state["id"] != oid || state["fulfillment_status"] != "ACCEPTED" {
		t.Fatalf("KDS %v", state)
	}
	for i, target := range []string{"PREPARING", "READY"} {
		body := fmt.Sprintf(`{"expected_version":%.0f,"target":%q}`, state["version"], target)
		decode(request("POST", ob+"/fulfillment", body, cashier, "not-kitchen"), 403)
		state = decode(request("POST", ob+"/fulfillment", body, kitchen, fmt.Sprintf("step%d", i)), 200)
	}
	decode(request("POST", sb+"/close", "{}", cashier, ""), 409) // READY is not SERVED.
	serve := fmt.Sprintf(`{"expected_version":%.0f,"target":"SERVED"}`, state["version"])
	decode(request("POST", ob+"/fulfillment", serve, kitchen, "serve"), 403)
	decode(request("POST", ob+"/fulfillment", serve, cashier, "serve"), 200)
	decode(request("POST", sb+"/close", "{}", kitchen, ""), 403)
	decode(request("POST", sb+"/close", "{}", cashier, ""), 200)
	closedGuest := request("GET", gb+"/operations", "", guest, "")
	if closedGuest.Code != 403 && closedGuest.Code != 410 {
		t.Fatalf("closed grant survived: %d %s", closedGuest.Code, closedGuest.Body.String())
	}
	history := decode(request("GET", sb+"/operations", "", cashier, ""), 200)
	if history["session"].(map[string]any)["status"] != "CLOSED" || len(history["receipts"].([]any)) != 1 {
		t.Fatalf("history missing %v", history)
	}
	// A next seating is a new identity, never the closed session's grant.
	next := decode(request("POST", "/v1/guest/tables/test-table/join", "{}", "", ""), 200)
	if next["session"].(map[string]any)["id"] == sid {
		t.Fatal("reused closed seating")
	}
	nextBase := "/v1/guest/sessions/" + next["session"].(map[string]any)["id"].(string)
	nextToken := next["table_join_token"].(string)
	decode(request("PUT", base+"/operations/cash-settings", strings.Replace(settingBody, `"enabled":true`, `"enabled":false`, 1), manager, ""), 200)
	nextState := decode(request("GET", nextBase+"/operations", "", nextToken, ""), 200)
	if nextState["cash_enabled"] != false {
		t.Fatal("disabled cash still advertised")
	}
	decode(request("POST", nextBase+"/cart/lines", `{"expected_generation":1,"expected_version":1,"product_key":"tea","quantity":1,"options":[]}`, nextToken, "disabled-add"), 200)
	nextPreview := decode(request("POST", nextBase+"/cart/preview", `{"expected_generation":1,"expected_version":2}`, nextToken, ""), 200)
	disabled := decode(request("POST", nextBase+"/cart/commit", fmt.Sprintf(`{"expected_generation":1,"expected_version":2,"preview_fingerprint":%q,"payment_method":"CASH"}`, nextPreview["fingerprint"]), nextToken, "disabled-cash"), 409)
	if disabled["error"] != "cash_unavailable" || len(disabled["cart"].(map[string]any)["lines"].([]any)) != 1 {
		t.Fatalf("disabled cash did not preserve draft: %v", disabled)
	}
}
