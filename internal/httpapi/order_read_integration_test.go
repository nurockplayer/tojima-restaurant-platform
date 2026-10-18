package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/checkoutproof"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/payments"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
)

func TestPostgresOrderReadGrantBoundary(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f, err := testguest.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close(ctx)
	// Exercise the complete payment runtime too: ORDER_READ cannot become the
	// original browser's independent CHECKOUT_RESUME cookie proof.
	pay, err := payments.New(f.Pool, f.Operations, nil, payments.Config{CreateRetryWindow: time.Hour, ContinuationTTL: time.Minute, WorkLeaseTTL: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	proofs, err := checkoutproof.New(checkoutproof.Config{SigningKeys: [][]byte{[]byte(strings.Repeat("k", 32))}, TTL: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	h, err := NewPaymentsRuntime(f.Sessions, f.Menus, f.Orders, f.Operations, pay, nil, nil, PaymentsHTTPConfig{Proofs: proofs, Origin: "http://127.0.0.1:8082", AllowLoopbackHTTP: true, WebhookSecrets: []string{"fixture-only"}})
	if err != nil {
		t.Fatal(err)
	}
	req := func(method, path, body, token, key string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "http://127.0.0.1:8082"+path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Idempotency-Key", key)
		r.Header.Set("Origin", "http://127.0.0.1:8082")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	decode := func(w *httptest.ResponseRecorder, want int, dst any) {
		t.Helper()
		if w.Code != want {
			t.Fatalf("unexpected HTTP status: got %d want %d", w.Code, want)
		}
		if dst != nil {
			if err := json.Unmarshal(w.Body.Bytes(), dst); err != nil {
				t.Fatal(err)
			}
		}
	}
	guests := make([]tablesession.Grant, 3)
	for i := range guests {
		expected := ""
		if i > 0 {
			expected = guests[0].Session.ID
		}
		guests[i], err = f.Sessions.DirectJoin(ctx, f.Locator, expected)
		if err != nil {
			t.Fatal(err)
		}
	}
	sid := guests[0].Session.ID
	base := "/v1/guest/sessions/" + sid
	// Check exact money above JS's safe integer limit, captured at commitment.
	const amount int64 = 9007199254740993
	if _, err = f.Menus.SetStorePriceOverride(ctx, catalog.StoreScope{TenantID: f.TenantID, StoreID: f.StoreID}, "fixture", "tea", amount, 0); err != nil {
		t.Fatal(err)
	}
	decode(req("POST", base+"/cart/lines", `{"expected_generation":1,"expected_version":1,"product_key":"tea","quantity":1,"options":[]}`, guests[0].Token, "add"), 200, nil)
	var preview struct {
		Fingerprint string `json:"fingerprint"`
	}
	decode(req("POST", base+"/cart/preview", `{"expected_generation":1,"expected_version":2}`, guests[0].Token, ""), 200, &preview)
	body := fmt.Sprintf(`{"expected_generation":1,"expected_version":2,"preview_fingerprint":%q,"payment_method":"CASH"}`, preview.Fingerprint)
	var commit struct {
		Outcome string `json:"outcome"`
		Order   struct {
			ID string `json:"order_id"`
		} `json:"order"`
		Grant *ordering.OrderReadGrant `json:"order_read_grant"`
	}
	decode(req("POST", base+"/cart/commit", body, guests[0].Token, "commit"), 200, &commit)
	if commit.Outcome != "COMMITTED" || commit.Grant == nil || commit.Grant.OrderID != commit.Order.ID {
		t.Fatal("new commit did not issue the per-order grant")
	}
	original := *commit.Grant
	oid := commit.Order.ID
	statusPath := "/v1/guest/orders/" + oid + "/status"
	for i := range guests {
		decode(req("GET", statusPath, "", guests[i].Token, ""), 403, nil)
		var replay struct {
			Grant *ordering.OrderReadGrant `json:"order_read_grant"`
		}
		decode(req("POST", base+"/cart/commit", body, guests[i].Token, fmt.Sprintf("replay-%d", i)), 200, &replay)
		if replay.Grant != nil {
			t.Fatal("commit replay disclosed a grant without an explicit per-order request")
		}
	}
	read := req("GET", statusPath, "", original.Token, "")
	var state map[string]any
	decode(read, 200, &state)
	if read.Header().Get("Cache-Control") != "private, no-store" || read.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("unsafe status response headers")
	}
	if len(state) != 7 || state["display_state"] != "STANDARD" || state["version"] != float64(1) || state["payment_status"] != "UNPAID" || state["fulfillment_status"] != "WAITING" || state["is_terminal"] != false || state["total_gross_jpy"] != "9007199254740993" || state["currency"] != "JPY" {
		t.Fatal("status projection differs from the actual state/snapshot")
	}
	// Current joined guests opt into an individual grant. Tokens are independent.
	var joined ordering.OrderReadGrant
	decode(req("POST", base+"/orders/"+oid+"/read-grant", "", guests[1].Token, ""), 200, &joined)
	if joined.Token == original.Token || len(joined.Token) != 43 || joined.ExpiresAt.Sub(time.Now()) > ordering.DefaultOrderReadTTL {
		t.Fatal("invalid grant randomness or TTL")
	}
	hash := sha256.Sum256([]byte(original.Token))
	var stored []byte
	var rowJSON string
	if err = f.Pool.QueryRow(ctx, `SELECT token_hash,row_to_json(g)::text FROM order_read_grants g WHERE token_hash=$1`, hash[:]).Scan(&stored, &rowJSON); err != nil {
		t.Fatal(err)
	}
	if string(stored) != string(hash[:]) || strings.Contains(rowJSON, original.Token) || strings.Contains(rowJSON, guests[0].Token) {
		t.Fatal("database contains raw capability material")
	}
	// Wrong order, nonexistent order and bad token all have the identical denial.
	denial := req("GET", statusPath, "", guests[2].Token, "")
	for _, path := range []string{"/v1/guest/orders/" + strings.Repeat("0", 32) + "/status", statusPath + "?token=unsupported"} {
		w := req("GET", path, "", original.Token, "")
		decode(w, 403, nil)
		if w.Body.String() != denial.Body.String() || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("denial leaks existence or is cacheable")
		}
	}
	// Other tenant/store orders are real orders created through the same service.
	for i, scope := range []catalog.StoreScope{{TenantID: f.TenantID, StoreID: "other-store"}, {TenantID: "other-tenant", StoreID: "other-store"}} {
		if i == 1 {
			if _, err = f.Pool.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES($1,'Other')`, scope.TenantID); err != nil {
				t.Fatal(err)
			}
			draft, e := f.Menus.SaveCompanyDraft(ctx, scope.TenantID, "fixture", 0, catalog.CatalogData{Categories: []catalog.Category{{Key: "c", Name: "Menu", Products: []catalog.Product{{Key: "tea", Name: "Tea", Active: true, BasePriceMinor: 330}}}}})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.Menus.PublishCompanyDraft(ctx, scope.TenantID, "fixture", draft.Version); e != nil {
				t.Fatal(e)
			}
		}
		locator := fmt.Sprintf("other-%d", i)
		if _, err = f.Pool.Exec(ctx, `INSERT INTO stores(tenant_id,store_id,name,qr_entry_mode) VALUES($1,$2,'Other','DIRECT_SCAN')`, scope.TenantID, scope.StoreID); err != nil {
			t.Fatal(err)
		}
		if _, err = f.Pool.Exec(ctx, `INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES($1,$2,'table','Other',$3)`, scope.TenantID, scope.StoreID, locator); err != nil {
			t.Fatal(err)
		}
		if _, err = f.Operations.SetCashSettings(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID}, "fixture", true, operations.PayLater, 0); err != nil {
			t.Fatal(err)
		}
		if _, err = f.Menus.SetStoreAvailability(ctx, scope, "fixture", catalog.AvailabilityChange{Target: catalog.AvailabilityTarget{Kind: catalog.ProductAvailability, ProductKey: "tea"}, Status: catalog.Available, ExpectedVersion: 0}); err != nil {
			t.Fatal(err)
		}
		other, e := f.Sessions.DirectJoin(ctx, locator, "")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Orders.Mutate(ctx, ordering.Mutation{SessionID: other.Session.ID, GrantToken: other.Token, Kind: ordering.AddLine, ProductKey: "tea", Quantity: 1, ExpectedGeneration: 1, ExpectedVersion: 1, IdempotencyKey: "add"}); e != nil {
			t.Fatal(e)
		}
		p, e := f.Orders.Preview(ctx, other.Session.ID, other.Token, 1, 2)
		if e != nil {
			t.Fatal(e)
		}
		o, e := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: other.Session.ID, GrantToken: other.Token, ExpectedGeneration: 1, ExpectedVersion: 2, PreviewFingerprint: p.Fingerprint, PaymentMethod: ordering.Cash, IdempotencyKey: "commit"})
		if e != nil {
			t.Fatal(e)
		}
		w := req("GET", "/v1/guest/orders/"+o.Order.ID+"/status", "", original.Token, "")
		decode(w, 403, nil)
		if w.Body.String() != denial.Body.String() {
			t.Fatal("cross-scope existence leak")
		}
		decode(req("GET", statusPath, "", o.OrderReadGrant.Token, ""), 403, nil)
		decode(req("POST", base+"/orders/"+o.Order.ID+"/read-grant", "", guests[2].Token, ""), 403, nil)
	}
	decode(req("POST", base+"/cart/commit", body, original.Token, "read-cannot-commit"), 403, nil)
	decode(req("POST", base+"/orders/"+oid+"/read-grant", "", original.Token, ""), 403, nil)
	decode(req("POST", base+"/checkout-context", "", original.Token, ""), 403, nil)
	decode(req("POST", base+"/orders/"+oid+"/checkout/resume", "", original.Token, ""), 403, nil)
	// The commit grant and joined grant consume the same per-order budget.
	grantPath := base + "/orders/" + oid + "/read-grant"
	checkLimit := func(w *httptest.ResponseRecorder) {
		t.Helper()
		var body apiError
		decode(w, 429, &body)
		if body.Error != "order_read_limit_reached" || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("unstable or cacheable issuance limit response")
		}
	}
	checkCounts := func(wantTotal int) {
		t.Helper()
		var total, active int
		if err := f.Pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER (WHERE revoked_at IS NULL AND expires_at>clock_timestamp())
  FROM order_read_grants WHERE session_id=$1 AND order_id=$2`, sid, oid).Scan(&total, &active); err != nil {
			t.Fatal(err)
		}
		if total != wantTotal || active != ordering.MaxActiveOrderReadGrants {
			t.Fatalf("issuance exceeded budget or inserted rejected grants: total=%d active=%d", total, active)
		}
	}
	extra := make([]ordering.OrderReadGrant, ordering.MaxActiveOrderReadGrants-2)
	for i := range extra {
		decode(req("POST", grantPath, "", guests[2].Token, ""), 200, &extra[i])
	}
	for i := range guests {
		checkLimit(req("POST", grantPath, "", guests[i].Token, ""))
	}
	checkCounts(ordering.MaxActiveOrderReadGrants)
	// Full budgets must not change wrong-scope or invalid-credential denials.
	decode(req("POST", base+"/orders/"+strings.Repeat("0", 32)+"/read-grant", "", guests[0].Token, ""), 403, nil)
	decode(req("POST", grantPath, "", original.Token, ""), 403, nil)
	for _, g := range extra {
		if err = f.Orders.RevokeOrderRead(ctx, oid, g.Token); err != nil {
			t.Fatal(err)
		}
	}
	// All joined guests race for the restored slots. The session lock must
	// serialize the count and insert, even for different TABLE_JOIN tokens.
	const requests = 2 * ordering.MaxActiveOrderReadGrants
	start := make(chan struct{})
	results := make(chan *httptest.ResponseRecorder, requests)
	for i := 0; i < requests; i++ {
		go func(token string) {
			<-start
			results <- req("POST", grantPath, "", token, "")
		}(guests[i%len(guests)].Token)
	}
	close(start)
	issued := 0
	for i := 0; i < requests; i++ {
		w := <-results
		if w.Code == 200 {
			issued++
		} else {
			checkLimit(w)
		}
	}
	if issued != len(extra) {
		t.Fatalf("concurrent issuance: got %d grants want %d", issued, len(extra))
	}
	checkCounts(ordering.MaxActiveOrderReadGrants + len(extra))
	// Expiry and revocation are immediately effective for that token only.
	joinedHash := sha256.Sum256([]byte(joined.Token))
	if _, err = f.Pool.Exec(ctx, `UPDATE order_read_grants SET created_at=clock_timestamp()-interval '2 seconds',expires_at=clock_timestamp()-interval '1 second' WHERE token_hash=$1`, joinedHash[:]); err != nil {
		t.Fatal(err)
	}
	decode(req("GET", statusPath, "", joined.Token, ""), 403, nil)
	decode(req("POST", base+"/orders/"+oid+"/read-grant", "", guests[2].Token, ""), 200, &joined)
	if err = f.Orders.RevokeOrderRead(ctx, oid, joined.Token); err != nil {
		t.Fatal(err)
	}
	decode(req("GET", statusPath, "", joined.Token, ""), 403, nil)
	decode(req("GET", statusPath, "", original.Token, ""), 200, nil)
	// Drive real operation transitions and close; no direct state fabrication.
	scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}
	accepted, err := f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: sid, OrderID: oid, Actor: "fixture", ExpectedVersion: 1, IdempotencyKey: "accept"})
	if err != nil {
		t.Fatal(err)
	}
	cash, err := f.Operations.PreviewCash(ctx, scope, sid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Operations.SettleCash(ctx, operations.CashSettlementRequest{Scope: scope, SessionID: sid, Actor: "fixture", Fingerprint: cash.Fingerprint, TenderedJPY: amount, RegisterSessionRef: "fixture", IdempotencyKey: "cash"}); err != nil {
		t.Fatal(err)
	}
	version := accepted.Version + 1 // settlement advances the authoritative version
	for _, target := range []operations.FulfillmentStatus{operations.Preparing, operations.Ready, operations.Served} {
		next, e := f.Operations.AdvanceFulfillment(ctx, operations.OrderActionRequest{Scope: scope, SessionID: sid, OrderID: oid, Actor: "fixture", ExpectedVersion: version, IdempotencyKey: string(target), Target: target})
		if e != nil {
			t.Fatal(e)
		}
		version = next.Version
	}
	decode(req("GET", statusPath, "", original.Token, ""), 200, &state)
	if state["is_terminal"] != true || state["payment_status"] != "PAID" || state["fulfillment_status"] != "SERVED" || state["version"] != float64(version) {
		t.Fatal("read projection did not follow real transitions")
	}
	configuredTTL, e := ordering.NewWithOrderReadTTL(f.Pool, f.Sessions, f.Menus, f.Operations, 2*time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	customGrant, e := configuredTTL.RequestOrderRead(ctx, sid, guests[0].Token, oid)
	if e != nil {
		t.Fatal(e)
	}
	remaining := time.Until(customGrant.ExpiresAt)
	if remaining < 119*time.Second || remaining > 2*time.Minute {
		t.Fatal("configured TTL was not applied")
	}
	if err = f.Sessions.Close(ctx, guests[0].Session.Scope, sid, "fixture"); err != nil {
		t.Fatal(err)
	}
	next, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	if next.Session.ID == sid {
		t.Fatal("physical QR reused a closed session")
	}
	decode(req("GET", statusPath, "", next.Token, ""), 403, nil)
	decode(req("POST", "/v1/guest/sessions/"+next.Session.ID+"/orders/"+oid+"/read-grant", "", next.Token, ""), 403, nil)
	decode(req("POST", base+"/orders/"+oid+"/read-grant", "", guests[1].Token, ""), 403, nil)
	decode(req("GET", statusPath, "", original.Token, ""), 200, nil)
	for _, ttl := range []time.Duration{0, ordering.MinOrderReadTTL - 1, ordering.MaxOrderReadTTL + 1} {
		if _, err = ordering.NewWithOrderReadTTL(f.Pool, f.Sessions, f.Menus, f.Operations, ttl); err == nil {
			t.Fatal("unbounded TTL accepted")
		}
	}
	configured, err := ordering.NewWithOrderReadTTL(f.Pool, f.Sessions, f.Menus, f.Operations, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit configured TTL is used by the same issuance path as commit.
	// Request the old order fails regardless of this service's new configuration.
	if _, err = configured.RequestOrderRead(ctx, sid, guests[0].Token, oid); err == nil {
		t.Fatal("configuration resurrected a closed grant")
	}
}
