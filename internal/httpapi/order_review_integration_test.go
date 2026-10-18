package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testpayments"
)

func TestPostgresGuestPaidReviewDisplay(t *testing.T) {
	for _, held := range []bool{false, true} {
		name := "released"
		if held {
			name = "under_review"
		}
		t.Run(name, func(t *testing.T) {
			h := newPaymentHTTPFixture(t)
			f := h.fixture
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			join, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
			if err != nil {
				t.Fatal(err)
			}
			cart, err := f.Orders.Mutate(ctx, ordering.Mutation{SessionID: join.Session.ID, GrantToken: join.Token, Kind: ordering.AddLine, ProductKey: "tea", Quantity: 1, ExpectedGeneration: 1, ExpectedVersion: 1, IdempotencyKey: "add"})
			if err != nil {
				t.Fatal(err)
			}
			preview, err := f.Orders.Preview(ctx, join.Session.ID, join.Token, cart.Generation, cart.Version)
			if err != nil {
				t.Fatal(err)
			}
			commit, err := f.Orders.Commit(ctx, ordering.CommitRequest{SessionID: join.Session.ID, GrantToken: join.Token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Stripe, IdempotencyKey: "commit", CheckoutProofHash: [32]byte{1}, CheckoutProofExpiresAt: time.Now().Add(20 * time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			if commit.OrderReadGrant == nil {
				t.Fatal("commit missing ORDER_READ grant")
			}
			scope := operations.TableScope{TenantID: f.TenantID, StoreID: f.StoreID, TableID: f.TableID}
			if _, err = f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{Scope: scope, SessionID: join.Session.ID, OrderID: commit.Order.ID, Actor: "fixture", ExpectedVersion: 1, IdempotencyKey: "accept"}); err != nil {
				t.Fatal(err)
			}
			if worked, err := h.service.ProcessNext(ctx); err != nil || !worked {
				t.Fatalf("create worker: worked=%v err=%v", worked, err)
			}
			var checkoutID string
			if err = f.Pool.QueryRow(ctx, `SELECT checkout_session_id FROM stripe_payment_attempts WHERE order_id=$1`, commit.Order.ID).Scan(&checkoutID); err != nil {
				t.Fatal(err)
			}
			if held {
				// Reuse the operational-hold fixture from payments integration tests;
				// the signed paid webhook must derive review itself.
				if _, err = f.Pool.Exec(ctx, `UPDATE operation_order_state SET operational_hold=true,version=version+1,updated_at=clock_timestamp() WHERE order_id=$1`, commit.Order.ID); err != nil {
					t.Fatal(err)
				}
			}
			checkout, err := h.provider.SetOutcome(checkoutID, stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
			if err != nil {
				t.Fatal(err)
			}
			body, signature, err := testpayments.SignedCheckoutEvent(checkout, "evt_review_display", "checkout.session.completed", paymentHTTPSecret, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			decodePaymentHTTP(t, h.webhook(t, body, signature), http.StatusOK, nil)
			if worked, err := h.service.ProcessNext(ctx); err != nil || !worked {
				t.Fatalf("webhook worker: worked=%v err=%v", worked, err)
			}
			var review bool
			var cards int
			if err = f.Pool.QueryRow(ctx, `SELECT payment_review_required,(SELECT count(*) FROM operation_kds_cards WHERE order_id=$1) FROM operation_order_state WHERE order_id=$1`, commit.Order.ID).Scan(&review, &cards); err != nil {
				t.Fatal(err)
			}
			if review != held || (held && cards != 0) || (!held && cards != 1) {
				t.Fatalf("fixture review=%v cards=%d held=%v", review, cards, held)
			}
			wantDisplay, wantKitchen := "STANDARD", "ACCEPTED"
			if held {
				wantDisplay, wantKitchen = "PAID_UNDER_REVIEW", "WAITING"
			}
			response := h.request(t, http.MethodGet, "/v1/guest/orders/"+commit.Order.ID+"/status", "", commit.OrderReadGrant.Token, nil, "", "")
			var status map[string]any
			decodePaymentHTTP(t, response, http.StatusOK, &status)
			if len(status) != 7 || status["display_state"] != wantDisplay || status["payment_status"] != "PAID" || status["fulfillment_status"] != wantKitchen || status["is_terminal"] != false || status["total_gross_jpy"] != "330" || status["currency"] != "JPY" || status["version"] == nil {
				t.Fatalf("unexpected status DTO: %v", status)
			}
			shared := h.request(t, http.MethodGet, "/v1/guest/sessions/"+join.Session.ID+"/operations", "", join.Token, nil, "", "")
			var history struct {
				Orders []map[string]any `json:"orders"`
			}
			decodePaymentHTTP(t, shared, http.StatusOK, &history)
			if len(history.Orders) != 1 {
				t.Fatalf("unexpected history: %v", history)
			}
			order := history.Orders[0]
			if len(order) != 6 || order["id"] != commit.Order.ID || order["display_state"] != wantDisplay || order["payment_status"] != "PAID" || order["fulfillment_status"] != wantKitchen || order["requires_staff_acceptance"] == nil || order["staff_accepted"] != true {
				t.Fatalf("unexpected history DTO: %v", order)
			}
			for _, wire := range []string{response.Body.String(), shared.Body.String()} {
				assertPaymentResponsePrivate(t, wire, "payment_review_required", "operational_hold", "kds", "reason", "stripe", "checkout", checkoutID, h.account.StripeAccountID, commit.OrderReadGrant.Token)
			}
		})
	}
}
