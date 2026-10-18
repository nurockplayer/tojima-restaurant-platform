package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/authz"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testpayments"
)

// Observe actual PostgreSQL lock waits in this fixture's schema. No sleep is
// used to decide which HTTP request wins the race.
func p84BWaitBlocked(t *testing.T, pool *pgxpool.Pool, pattern string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a
   WHERE a.pid<>pg_backend_pid() AND a.query LIKE $1 AND cardinality(pg_blocking_pids(a.pid))>0
   AND EXISTS(SELECT 1 FROM pg_locks l JOIN pg_class c ON c.oid=l.relation
    JOIN pg_namespace n ON n.oid=c.relnamespace WHERE l.pid=a.pid AND n.nspname=current_schema()))`, pattern).Scan(&blocked)
		if err != nil {
			t.Fatalf("wait for blocked %s: %v", pattern, err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("no blocked query matching %s", pattern)
		case <-ticker.C:
		}
	}
}

func p84BHoldHandler(pf *paymentHTTPFixture) http.Handler {
	return NewMenuHandler(pf.fixture.Menus, httpTestIdentity{authz.Actor{SubjectID: "p84-hq", MFA: true}}, authz.PolicyAuthorizer{Directory: &httpMemberships{values: []authz.Membership{{TenantID: pf.fixture.TenantID, Role: authz.TenantAdmin}}}})
}

func p84BHold(h http.Handler, active bool, version int) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`{"active":%t,"reason":"P84 safety review","expected_version":%d,"idempotency_key":"p84-hold-%d"}`, active, version, version)
	r := httptest.NewRequest(http.MethodPut, "/v1/admin/tenants/test-tenant/menu/products/tea/emergency-hold", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func p84BWork(t *testing.T, pf *paymentHTTPFixture) {
	t.Helper()
	if worked, err := pf.service.ProcessNext(context.Background()); err != nil || !worked {
		t.Fatalf("worker: worked=%v err=%v", worked, err)
	}
}

func TestP84BHQHoldQuoteAndCommitHTTPSerialization(t *testing.T) {
	for _, action := range []string{"preview", "commit"} {
		for _, holdFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/hold-first-%v", action, holdFirst), func(t *testing.T) {
				pf := newPaymentHTTPFixture(t)
				ctx := context.Background()
				f := pf.fixture
				guest, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
				if err != nil {
					t.Fatal(err)
				}
				base := "/v1/guest/sessions/" + guest.Session.ID
				add := pf.requestWithKey(t, "POST", base+"/cart/lines", `{"expected_generation":1,"expected_version":1,"product_key":"tea","quantity":1,"options":[]}`, guest.Token, nil, "", "", "add")
				decodePaymentHTTP(t, add, 200, nil)
				var preview map[string]any
				decodePaymentHTTP(t, pf.request(t, "POST", base+"/cart/preview", `{"expected_generation":1,"expected_version":2}`, guest.Token, nil, "", ""), 200, &preview)
				body := `{"expected_generation":1,"expected_version":2}`
				if action == "commit" {
					body = fmt.Sprintf(`{"expected_generation":1,"expected_version":2,"preview_fingerprint":%q,"payment_method":"CASH"}`, preview["fingerprint"])
				}
				holdHandler := p84BHoldHandler(pf)
				tx, err := f.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(ctx)
				if holdFirst {
					_, err = tx.Exec(ctx, `SELECT catalog_id FROM menu_catalogs WHERE owner_kind='COMPANY' FOR UPDATE`)
				} else {
					_, err = tx.Exec(ctx, `SELECT session_id FROM table_carts WHERE session_id=$1 FOR UPDATE`, guest.Session.ID)
				}
				if err != nil {
					t.Fatal(err)
				}
				holdDone, actionDone := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
				runHold := func() { holdDone <- p84BHold(holdHandler, true, 0) }
				runAction := func() {
					actionDone <- pf.requestWithKey(t, "POST", base+"/cart/"+action, body, guest.Token, nil, "", "", "admission")
				}
				if holdFirst {
					go runHold()
					p84BWaitBlocked(t, f.Pool, "%menu_catalogs%FOR UPDATE%")
					go runAction()
					p84BWaitBlocked(t, f.Pool, "%menu_catalogs%FOR SHARE%")
				} else {
					go runAction()
					p84BWaitBlocked(t, f.Pool, "%table_carts%FOR UPDATE%")
					go runHold()
					p84BWaitBlocked(t, f.Pool, "%menu_catalogs%FOR UPDATE%")
				}
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				decodePaymentHTTP(t, <-holdDone, 200, nil)
				response := <-actionDone
				want := 200
				if holdFirst {
					want = 409
				}
				decodePaymentHTTP(t, response, want, nil)
				if holdFirst && !strings.Contains(response.Body.String(), `"error":"unavailable"`) {
					t.Fatal(response.Body.String())
				}
				var orders int
				if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM table_orders`).Scan(&orders); err != nil {
					t.Fatal(err)
				}
				wantOrders := 0
				if !holdFirst && action == "commit" {
					wantOrders = 1
				}
				if orders != wantOrders {
					t.Fatalf("orders=%d want=%d", orders, wantOrders)
				}
				if action == "preview" {
					// A quote that won before ON is not permission to commit afterward.
					commitBody := fmt.Sprintf(`{"expected_generation":1,"expected_version":2,"preview_fingerprint":%q,"payment_method":"CASH"}`, preview["fingerprint"])
					decodePaymentHTTP(t, pf.requestWithKey(t, "POST", base+"/cart/commit", commitBody, guest.Token, nil, "", "", "held-commit"), 409, nil)
					decodePaymentHTTP(t, p84BHold(holdHandler, false, 1), 200, nil)
					stale := pf.requestWithKey(t, "POST", base+"/cart/commit", commitBody, guest.Token, nil, "", "", "stale-after-off")
					decodePaymentHTTP(t, stale, 409, nil)
					if !strings.Contains(stale.Body.String(), `"error":"requote_required"`) {
						t.Fatal(stale.Body.String())
					}
				}
			})
		}
	}
}

func TestP84BHQHoldInflightCheckoutHTTP(t *testing.T) {
	for _, lostResponse := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost-response-%v", lostResponse), func(t *testing.T) {
			pf := newPaymentHTTPFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			guest, err := pf.fixture.Sessions.DirectJoin(ctx, pf.fixture.Locator, "")
			if err != nil {
				t.Fatal(err)
			}
			orderID := createP82HTTPOrder(t, pf, guest.Session.ID, guest.Token, "tea", 2, ordering.Stripe, "inflight")
			entered, release := make(chan struct{}), make(chan struct{})
			releaseOnce := sync.OnceFunc(func() { close(release) })
			defer releaseOnce()
			pf.provider.SetNextCreateHook(func(stripeprovider.CheckoutResult) { close(entered); <-release })
			if lostResponse {
				pf.provider.LoseNextCreateResponse()
			}
			done := make(chan error, 1)
			go func() { _, err := pf.service.ProcessNext(ctx); done <- err }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			holdHandler := p84BHoldHandler(pf)
			decodePaymentHTTP(t, p84BHold(holdHandler, true, 0), 200, nil)
			// OFF cannot erase the obligation queued during the remote create.
			decodePaymentHTTP(t, p84BHold(holdHandler, false, 1), 200, nil)
			releaseOnce()
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			if lostResponse {
				if _, err = pf.fixture.Pool.Exec(ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp() WHERE work_type='CREATE'`); err != nil {
					t.Fatal(err)
				}
				p84BWork(t, pf)
			}
			p84BWork(t, pf)
			var state, work, checkoutID string
			var revoked bool
			if err = pf.fixture.Pool.QueryRow(ctx, `SELECT a.state,w.status,a.checkout_session_id,a.continuation_revoked_at IS NOT NULL AND a.company_hold_expire_requested_at IS NOT NULL FROM stripe_payment_attempts a JOIN stripe_payment_work w USING(order_id) WHERE a.order_id=$1 AND w.work_type='EXPIRE'`, orderID).Scan(&state, &work, &checkoutID, &revoked); err != nil {
				t.Fatal(err)
			}
			checkout, err := pf.provider.RetrieveCheckout(ctx, pf.account, checkoutID)
			if err != nil {
				t.Fatal(err)
			}
			if state != "EXPIRED" || work != "DONE" || !revoked || checkout.Status != stripeprovider.CheckoutExpired {
				t.Fatalf("expiry state=%s work=%s revoked=%v provider=%s", state, work, revoked, checkout.Status)
			}
			creates, sessions := pf.provider.Counts()
			wantCreates := 1
			if lostResponse {
				wantCreates = 2
			}
			if creates != wantCreates || sessions != 1 {
				t.Fatalf("creates=%d sessions=%d", creates, sessions)
			}
			safe := pf.request(t, "GET", "/v1/guest/sessions/"+guest.Session.ID+"/orders/"+orderID+"/checkout", "", guest.Token, nil, "", "")
			decodePaymentHTTP(t, safe, 200, nil)
			if !strings.Contains(safe.Body.String(), `"state":"EXPIRED"`) || !strings.Contains(safe.Body.String(), `"resume_available":false`) {
				t.Fatal(safe.Body.String())
			}
		})
	}
}

func TestP84BOrderReadCannotReplaceCheckoutProofHTTP(t *testing.T) {
	pf := newPaymentHTTPFixture(t)
	ctx := context.Background()
	guest, err := pf.fixture.Sessions.DirectJoin(ctx, pf.fixture.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	base := "/v1/guest/sessions/" + guest.Session.ID
	original := pf.request(t, "POST", base+"/checkout-context", "", guest.Token, nil, paymentHTTPOrigin, "")
	decodePaymentHTTP(t, original, 204, nil)
	originalCookie := original.Result().Cookies()[0]
	// Build the real order with that same browser proof (the shared helper issues
	// its own cookie, so use the complete HTTP sequence explicitly here).
	decodePaymentHTTP(t, pf.requestWithKey(t, "POST", base+"/cart/lines", `{"expected_generation":1,"expected_version":1,"product_key":"tea","quantity":1,"options":[]}`, guest.Token, nil, "", "", "add"), 200, nil)
	var quote map[string]any
	decodePaymentHTTP(t, pf.request(t, "POST", base+"/cart/preview", `{"expected_generation":1,"expected_version":2}`, guest.Token, nil, "", ""), 200, &quote)
	var result struct {
		Order struct {
			ID string `json:"order_id"`
		} `json:"order"`
		Grant ordering.OrderReadGrant `json:"order_read_grant"`
	}
	body := fmt.Sprintf(`{"expected_generation":1,"expected_version":2,"preview_fingerprint":%q,"payment_method":"STRIPE"}`, quote["fingerprint"])
	decodePaymentHTTP(t, pf.requestWithKey(t, "POST", base+"/cart/commit", body, guest.Token, originalCookie, paymentHTTPOrigin, "", "commit"), 200, &result)
	p84BWork(t, pf)
	if result.Grant.Token == "" {
		t.Fatal("missing ORDER_READ control")
	}
	decodePaymentHTTP(t, pf.request(t, "GET", "/v1/guest/orders/"+result.Order.ID+"/status", "", result.Grant.Token, nil, "", ""), 200, nil)
	second := pf.request(t, "POST", base+"/checkout-context", "", guest.Token, nil, paymentHTTPOrigin, "")
	decodePaymentHTTP(t, second, 204, nil)
	otherCookie := second.Result().Cookies()[0]
	path := base + "/orders/" + result.Order.ID + "/checkout/resume"
	before := p84BDurablePaymentSnapshot(t, pf, result.Order.ID)
	for _, cookie := range []*http.Cookie{nil, otherCookie} {
		denied := pf.request(t, "POST", path, "", result.Grant.Token, cookie, paymentHTTPOrigin, "")
		control := pf.request(t, "POST", path, "", guest.Token, cookie, paymentHTTPOrigin, "")
		want := 403
		if cookie != nil {
			want = 409
		}
		decodePaymentHTTP(t, denied, want, nil)
		if denied.Code != control.Code || denied.Body.String() != control.Body.String() || denied.Header().Get("Location") != "" || strings.Contains(denied.Body.String(), "url") {
			t.Fatalf("ORDER_READ refusal leaks or grants authority: %s", denied.Body.String())
		}
	}
	if after := p84BDurablePaymentSnapshot(t, pf, result.Order.ID); before != after {
		t.Fatal("refused capability changed payment state")
	}
	// Resume authority is the independent original cookie, never the bearer.
	success := pf.request(t, "POST", path, "", "", originalCookie, paymentHTTPOrigin, "")
	decodePaymentHTTP(t, success, 200, nil)
	if !strings.Contains(success.Body.String(), "https://checkout.example.invalid/") {
		t.Fatal("original proof did not resume the existing Checkout")
	}
	if creates, sessions := pf.provider.Counts(); creates != 1 || sessions != 1 {
		t.Fatalf("resume recreated checkout: %d/%d", creates, sessions)
	}
}

func p84BDurablePaymentSnapshot(t *testing.T, pf *paymentHTTPFixture, orderID string) string {
	t.Helper()
	var snapshot string
	err := pf.fixture.Pool.QueryRow(context.Background(), `SELECT jsonb_build_array(
 (SELECT to_jsonb(o) FROM table_orders o WHERE order_id=$1),
 (SELECT to_jsonb(a) FROM stripe_payment_attempts a WHERE order_id=$1),
 (SELECT to_jsonb(s) FROM operation_order_state s WHERE order_id=$1),
 (SELECT jsonb_agg(to_jsonb(w) ORDER BY work_id) FROM stripe_payment_work w WHERE order_id=$1))::text`, orderID).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestP84BReviewApproveCancelPartialRefundHTTP(t *testing.T) {
	for _, cancelQuantity := range []int{1, 2} {
		for _, adjustFirst := range []bool{true, false} {
			t.Run(fmt.Sprintf("cancel-quantity-%d/adjust-first-%v", cancelQuantity, adjustFirst), func(t *testing.T) {
				pf := newPaymentHTTPFixture(t)
				f := pf.fixture
				ctx := context.Background()
				guest, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
				if err != nil {
					t.Fatal(err)
				}
				orderID := createP82HTTPOrder(t, pf, guest.Session.ID, guest.Token, "tea", 2, ordering.Stripe, "review")
				p84BWork(t, pf)
				holdHandler := p84BHoldHandler(pf)
				decodePaymentHTTP(t, p84BHold(holdHandler, true, 0), 200, nil)
				var checkoutID string
				if err = f.Pool.QueryRow(ctx, `SELECT checkout_session_id FROM stripe_payment_attempts WHERE order_id=$1`, orderID).Scan(&checkoutID); err != nil {
					t.Fatal(err)
				}
				paid, err := pf.provider.SetOutcome(checkoutID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
				if err != nil {
					t.Fatal(err)
				}
				raw, signature, err := testpayments.SignedCheckoutEvent(paid, "evt_p84_review", "checkout.session.completed", paymentHTTPSecret, time.Now())
				if err != nil {
					t.Fatal(err)
				}
				decodePaymentHTTP(t, pf.webhook(t, raw, signature), 200, nil)
				p84BWork(t, pf)
				p84BWork(t, pf) // signed PAID, then retained expiry observes PAID
				decodePaymentHTTP(t, p84BHold(holdHandler, false, 1), 200, nil)
				order := readP82HTTPOrder(t, pf, guest.Session.ID, orderID)
				if order["payment_status"] != "PAID" || order["payment_review_required"] != true {
					t.Fatalf("not under review: %v", order)
				}
				version := int64JSON(order["version"])
				path := pf.staffPath("table-01", guest.Session.ID, "/orders/"+orderID)
				adjustBody := fmt.Sprintf(`{"expected_version":%d,"reason":"P84 cancel","lines":[{"line_index":0,"cancelled_quantity":%d}]}`, version, cancelQuantity)
				approveBody := fmt.Sprintf(`{"expected_version":%d,"reason":"P84 kitchen verified"}`, version)
				snapshot := p84BDurableOrderSnapshot(t, pf, orderID)
				tx, err := f.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(ctx)
				if _, err = tx.Exec(ctx, `SELECT session_id FROM table_sessions WHERE session_id=$1 FOR UPDATE`, guest.Session.ID); err != nil {
					t.Fatal(err)
				}
				firstDone, secondDone := make(chan *httptest.ResponseRecorder, 1), make(chan *httptest.ResponseRecorder, 1)
				adjust := func() *httptest.ResponseRecorder {
					return pf.requestWithKey(t, "POST", path+"/adjustments", adjustBody, pf.creds.Manager.Token, nil, "", "", "adjust")
				}
				approve := func() *httptest.ResponseRecorder {
					return pf.requestWithKey(t, "POST", path+"/approve-fulfillment", approveBody, pf.creds.Manager.Token, nil, "", "", "approve")
				}
				first, second := adjust, approve
				if !adjustFirst {
					first, second = approve, adjust
				}
				go func() { firstDone <- first() }()
				p84BWaitBlocked(t, f.Pool, "%table_sessions%FOR UPDATE%")
				go func() { secondDone <- second() }()
				// Both operations must actually be queued, not merely launched.
				p84BWaitTwoSessionWaiters(t, f.Pool)
				if err = tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				winning := <-firstDone
				decodePaymentHTTP(t, winning, 200, nil)
				decodePaymentHTTP(t, <-secondDone, 409, nil)
				replay := first()
				decodePaymentHTTP(t, replay, 200, nil)
				if replay.Body.String() != winning.Body.String() {
					t.Fatal("winner response differs on replay")
				}
				order = readP82HTTPOrder(t, pf, guest.Session.ID, orderID)
				var cards, adjustments, refunds int
				if err = f.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM operation_kds_cards WHERE order_id=$1),(SELECT count(*) FROM order_adjustments WHERE order_id=$1),(SELECT count(*) FROM operation_refunds WHERE order_id=$1)`, orderID).Scan(&cards, &adjustments, &refunds); err != nil {
					t.Fatal(err)
				}
				if !adjustFirst {
					// Manager approval also satisfies reception and releases one KDS card.
					if order["review_decision"] != "APPROVED" || adjustments != 0 || refunds != 0 || cards != 1 {
						t.Fatalf("approval winner: %v cards=%d adjustments=%d refunds=%d", order, cards, adjustments, refunds)
					}
				} else {
					if adjustments != 1 || refunds != 1 || cards != 0 {
						t.Fatalf("duplicate effects: %d/%d/%d", cards, adjustments, refunds)
					}
					wantDecision := "UNRESOLVED"
					if cancelQuantity == 2 {
						wantDecision = "CANCELLED"
					}
					if order["review_decision"] != wantDecision {
						t.Fatalf("wrong review disposition: %v", order)
					}
					var refundID string
					if err = f.Pool.QueryRow(ctx, `SELECT refund_id FROM operation_refunds WHERE order_id=$1`, orderID).Scan(&refundID); err != nil {
						t.Fatal(err)
					}
					// Confirmed failure -> authorized HTTP retry -> lost successful response ->
					// same-key recovery. A timeout alone must never assert refund success.
					pf.provider.SetNextRefundOutcome("failed", false)
					p84BWork(t, pf)
					order = readP82HTTPOrder(t, pf, guest.Session.ID, orderID)
					if order["refunds"].([]any)[0].(map[string]any)["status"] != "FAILED" {
						t.Fatalf("failure not projected: %v", order)
					}
					retryBody := fmt.Sprintf(`{"expected_version":%d}`, int64JSON(order["version"]))
					retryPath := path + "/refunds/" + refundID + "/retry"
					decodePaymentHTTP(t, pf.requestWithKey(t, "POST", retryPath, retryBody, pf.creds.Cashier.Token, nil, "", "", "denied"), 403, nil)
					decodePaymentHTTP(t, pf.requestWithKey(t, "POST", retryPath, retryBody, pf.creds.Manager.Token, nil, "", "", "retry"), 200, nil)
					pf.provider.SetNextRefundOutcome("succeeded", true)
					p84BWork(t, pf)
					order = readP82HTTPOrder(t, pf, guest.Session.ID, orderID)
					if order["refunds"].([]any)[0].(map[string]any)["status"] != "PROCESSING" {
						t.Fatalf("timeout guessed success: %v", order)
					}
					if _, err = f.Pool.Exec(ctx, `UPDATE stripe_payment_work SET available_at=clock_timestamp() WHERE work_type='REFUND' AND status='READY'`); err != nil {
						t.Fatal(err)
					}
					p84BWork(t, pf)
					order = readP82HTTPOrder(t, pf, guest.Session.ID, orderID)
					refund := order["refunds"].([]any)[0].(map[string]any)
					if refund["status"] != "SUCCEEDED" || refund["amount_jpy"] != fmt.Sprint(330*cancelQuantity) || order["total_gross_jpy"] != "660" || order["effective_total_gross_jpy"] != fmt.Sprint(660-330*cancelQuantity) {
						t.Fatalf("refund outcome: %v", order)
					}
					for _, table := range []string{"order_adjustments", "operation_refunds", "operation_refund_attempt_history", "operation_order_events"} {
						p84BAssertHistoryGuard(t, pf, table, orderID)
					}
					creates, _ := pf.provider.RefundOperationCounts()
					if creates != 3 {
						t.Fatalf("failure + retry + same-key recovery creates=%d", creates)
					}
				}
				if snapshot != p84BDurableOrderSnapshot(t, pf, orderID) {
					t.Fatal("review/refund mutated original order or attempt identity")
				}
			})
		}
	}
}

func p84BDurableOrderSnapshot(t *testing.T, pf *paymentHTTPFixture, orderID string) string {
	t.Helper()
	var snapshot string
	err := pf.fixture.Pool.QueryRow(context.Background(), `SELECT jsonb_build_array(to_jsonb(o),a.amount_jpy,a.attempt_id,a.checkout_session_id,a.payment_intent_id,a.stripe_account_id)::text FROM table_orders o JOIN stripe_payment_attempts a USING(order_id) WHERE o.order_id=$1`, orderID).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func p84BWaitTwoSessionWaiters(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var count int
		err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity a WHERE a.pid<>pg_backend_pid() AND a.query LIKE '%table_sessions%FOR UPDATE%' AND cardinality(pg_blocking_pids(a.pid))>0 AND EXISTS(SELECT 1 FROM pg_locks l JOIN pg_class c ON c.oid=l.relation JOIN pg_namespace n ON n.oid=c.relnamespace WHERE l.pid=a.pid AND n.nspname=current_schema())`).Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		if count == 2 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("session waiters=%d want=2", count)
		case <-ticker.C:
		}
	}
}

func p84BAssertHistoryGuard(t *testing.T, pf *paymentHTTPFixture, table, orderID string) {
	t.Helper()
	where := "order_id=$1"
	column := "actor_id"
	if table == "operation_refunds" {
		column = "amount_jpy"
	}
	if table == "operation_refund_attempt_history" {
		column = "provider_key"
		where = "refund_id IN (SELECT refund_id FROM operation_refunds WHERE order_id=$1)"
	}
	var count int
	if err := pf.fixture.Pool.QueryRow(context.Background(), "SELECT count(*) FROM "+table+" WHERE "+where, orderID).Scan(&count); err != nil || count == 0 {
		t.Fatalf("history control %s count=%d err=%v", table, count, err)
	}
	set := column + "='rewritten'"
	if column == "amount_jpy" {
		set = "amount_jpy=amount_jpy+1"
	}
	for _, sql := range []string{"UPDATE " + table + " SET " + set + " WHERE " + where, "DELETE FROM " + table + " WHERE " + where} {
		_, err := pf.fixture.Pool.Exec(context.Background(), sql, orderID)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "P0001" {
			t.Fatalf("expected immutable-history trigger rejection for %s: %v", sql, err)
		}
	}
}

func TestP84BCashRefundLedgerHistoryHTTP(t *testing.T) {
	pf := newPaymentHTTPFixture(t)
	f := pf.fixture
	ctx := context.Background()
	guest, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	orderID := createP82HTTPOrder(t, pf, guest.Session.ID, guest.Token, "tea", 2, ordering.Cash, "cash-ledger")
	scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}
	preview, err := f.Operations.PreviewCash(ctx, scope, guest.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := f.Operations.SettleCash(ctx, operations.CashSettlementRequest{Scope: scope, SessionID: guest.Session.ID, Actor: "cashier-fixture", Fingerprint: preview.Fingerprint, TenderedJPY: 660, RegisterSessionRef: "fixture", IdempotencyKey: "settle"})
	if err != nil {
		t.Fatal(err)
	}
	order := readP82HTTPOrder(t, pf, guest.Session.ID, orderID)
	body := fmt.Sprintf(`{"expected_version":%d,"reason":"customer correction","lines":[{"line_index":0,"cancelled_quantity":1}],"cash_hand_back_confirmed":true}`, int64JSON(order["version"]))
	path := pf.staffPath("table-01", guest.Session.ID, "/orders/"+orderID+"/adjustments")
	adjusted := pf.requestWithKey(t, "POST", path, body, pf.creds.Manager.Token, nil, "", "", "cash-refund")
	decodePaymentHTTP(t, adjusted, 200, nil)
	replay := pf.requestWithKey(t, "POST", path, body, pf.creds.Manager.Token, nil, "", "", "cash-refund")
	decodePaymentHTTP(t, replay, 200, nil)
	if adjusted.Body.String() != replay.Body.String() {
		t.Fatal("cash adjustment replay changed")
	}
	var amount int64
	var count int
	var actor, receiptID string
	if err = f.Pool.QueryRow(ctx, `SELECT count(*),sum(amount_jpy),min(actor_id),min(receipt_id) FROM cash_refund_ledger WHERE order_id=$1`, orderID).Scan(&count, &amount, &actor, &receiptID); err != nil {
		t.Fatal(err)
	}
	if count != 1 || amount != 330 || actor != pf.creds.Manager.SubjectID || receiptID != receipt.ID {
		t.Fatalf("cash refund ledger count=%d amount=%d actor=%s receipt=%s", count, amount, actor, receiptID)
	}
	p84BAssertHistoryGuard(t, pf, "cash_refund_ledger", orderID)
	p84BAssertHistoryGuard(t, pf, "order_adjustments", orderID)
	p84BAssertHistoryGuard(t, pf, "operation_order_events", orderID)
}
