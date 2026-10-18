package payments

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
)

var refundProjectionCases = []struct{ provider, local, code string }{
	{"pending", "PROCESSING", ""},
	{"requires_action", "PROCESSING", ""},
	{"succeeded", "SUCCEEDED", ""},
	{"failed", "FAILED", ""},
	{"canceled", "FAILED", ""},
	{"future_status", "PROCESSING", "HOLD_RECONCILIATION_NEEDED"},
	{"", "PROCESSING", "HOLD_RECONCILIATION_NEEDED"},
}

func newRefundProjectionFixture(t *testing.T, partial bool) (*holdFixture, operations.OrderState, stripeprovider.RefundResult) {
	t.Helper()
	h := newHoldFixture(t)
	order := commitC1TwoLineOrder(t, h)
	h.provider.SetNextCreateOutcome(stripeprovider.CheckoutComplete, stripeprovider.PaymentPaid)
	h.work()
	lines := []operations.AdjustmentLine{{LineIndex: 1, CancelledQuantity: 1}}
	if !partial {
		lines = append(lines, operations.AdjustmentLine{LineIndex: 0, CancelledQuantity: 1})
	}
	o, err := h.Operations.AdjustOrder(h.ctx, operations.AdjustmentRequest{Scope: operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}, SessionID: order.Order.SessionID, OrderID: order.Order.ID, Actor: "manager", ExpectedVersion: reviewOrderVersion(t, h, order.Order.ID), IdempotencyKey: "cancel", Reason: "cancel items", Lines: lines})
	if err != nil {
		t.Fatal(err)
	}
	r, err := h.service.loadRefundWork(h.ctx, o.Refunds[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	// Model a created object before its status has been reconciled locally.
	x, err := h.provider.CreateRefund(h.ctx, stripeprovider.CreateRefundRequest{Account: stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: r.Account}, TenantID: r.TenantID, StoreID: r.StoreID, OrderID: r.OrderID, RefundID: r.RefundID, PaymentIntentID: r.PaymentIntent, IdempotencyKey: r.Key, AmountJPY: r.Amount})
	if err != nil {
		t.Fatal(err)
	}
	return h, o, x
}

func leaseProjectionWork(t *testing.T, h *holdFixture, x stripeprovider.RefundResult, webhook bool, key string) workItem {
	t.Helper()
	var w workItem
	var err error
	if webhook {
		accepted, e := h.service.EnqueueVerifiedWebhook(h.ctx, stripeprovider.VerifiedEvent{ID: key, Type: "refund.updated", AccountID: x.AccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now(), ObjectID: x.ID, Refund: &x})
		if e != nil || !accepted {
			t.Fatalf("enqueue=%v %v", accepted, e)
		}
		err = h.Pool.QueryRow(h.ctx, `SELECT w.work_id FROM stripe_payment_work w JOIN stripe_refund_webhook_events e ON e.event_row_id=w.refund_event_row_id WHERE e.stripe_event_id=$1`, key).Scan(&w.ID)
	} else {
		err = h.Pool.QueryRow(h.ctx, `SELECT work_id FROM stripe_payment_work WHERE refund_id=$1 AND work_type='REFUND' AND refund_generation=(SELECT generation FROM operation_refunds WHERE refund_id=$1)`, x.Metadata["refund_id"]).Scan(&w.ID)
	}
	if err != nil {
		t.Fatal(err)
	}
	w.Lease = key
	err = h.Pool.QueryRow(h.ctx, `UPDATE stripe_payment_work SET status='LEASED',lease_token=$2,lease_until=clock_timestamp()+interval '1 minute' WHERE work_id=$1 RETURNING work_type,order_id,refund_id,refund_event_row_id,refund_generation`, w.ID, w.Lease).Scan(&w.Kind, &w.OrderID, &w.RefundID, &w.RefundEventID, &w.RefundGeneration)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func runProjectionWork(t *testing.T, h *holdFixture, w workItem) {
	t.Helper()
	var err error
	if w.Kind == "REFUND_WEBHOOK" {
		err = h.service.processRefundWebhook(h.ctx, w)
	} else {
		err = h.service.processRefund(h.ctx, w)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func assertRefundProjection(t *testing.T, h *holdFixture, o operations.OrderState, status, code string, partial bool) operations.OrderState {
	t.Helper()
	detail, err := h.Operations.ReadSession(h.ctx, operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}, o.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	got := sessionOrderByID(t, detail.Orders, o.ID)
	payment := operations.RefundPending
	if status == "SUCCEEDED" {
		payment = operations.Refunded
	}
	if partial {
		payment = operations.Paid
	}
	if got.Refunds[0].Status != status || got.PaymentStatus != payment || got.Refunds[0].SafeErrorCode != code {
		t.Fatalf("want %s/%s/%s got %+v", status, payment, code, got)
	}
	tx, err := h.Pool.Begin(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(h.ctx)
	err = h.service.CheckClose(h.ctx, tx, tablesession.Session{ID: o.SessionID})
	if status != "SUCCEEDED" && !errors.Is(err, tablesession.ErrCloseBlocked) {
		t.Fatalf("unresolved refund close=%v", err)
	}
	if !partial && status == "SUCCEEDED" && err != nil {
		t.Fatalf("completed refund close=%v", err)
	}
	return got
}

func TestPostgresP82RefundProjectionAllStatusesFromEveryLocalState(t *testing.T) {
	for _, webhook := range []bool{false, true} {
		for _, from := range []string{"REQUESTED", "PROCESSING", "SUCCEEDED", "FAILED"} {
			for _, tc := range refundProjectionCases {
				t.Run(fmt.Sprintf("webhook-%v/%s/%s", webhook, from, tc.provider), func(t *testing.T) {
					h, o, x := newRefundProjectionFixture(t, false)
					if from != "REQUESTED" {
						initial := strings.ToLower(from)
						if from == "PROCESSING" {
							initial = "pending"
						}
						if _, err := h.provider.SetRefundOutcome(x.ID, initial); err != nil {
							t.Fatal(err)
						}
						runProjectionWork(t, h, leaseProjectionWork(t, h, x, false, "initial"))
						o = assertRefundProjection(t, h, o, from, "", false)
					}
					if _, err := h.provider.SetRefundOutcome(x.ID, tc.provider); err != nil {
						t.Fatal(err)
					}
					// Event payload deliberately disagrees with the retrieved object.
					x.Status = "succeeded"
					if tc.provider == "succeeded" {
						x.Status = "failed"
					}
					w := leaseProjectionWork(t, h, x, webhook, "projection")
					runProjectionWork(t, h, w)
					got := assertRefundProjection(t, h, o, tc.local, tc.code, false)
					var active bool
					if err := h.Pool.QueryRow(h.ctx, `SELECT status='READY' AND available_at>updated_at FROM stripe_payment_work WHERE work_id=$1`, w.ID).Scan(&active); err != nil {
						t.Fatal(err)
					}
					if active != (tc.local == "PROCESSING") {
						t.Fatalf("processing work retained=%v", active)
					}
					if from != tc.local {
						var audited bool
						if err := h.Pool.QueryRow(h.ctx, `SELECT EXISTS(SELECT 1 FROM operation_order_events WHERE order_id=$1 AND event_type='REFUND_STATUS_CHANGED' AND state_version=$2 AND after_state->>'refund_status'=$3)`, o.ID, got.Version, tc.local).Scan(&audited); err != nil || !audited {
							t.Fatalf("audit=%v %v", audited, err)
						}
					}
				})
			}
		}
	}
}

func TestPostgresP82RefundReturnedFundsAndStaleObservation(t *testing.T) {
	for _, webhook := range []bool{false, true} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("webhook-%v/partial-%v", webhook, partial), func(t *testing.T) {
				h, o, x := newRefundProjectionFixture(t, partial)
				if _, err := h.provider.SetRefundOutcome(x.ID, "succeeded"); err != nil {
					t.Fatal(err)
				}
				runProjectionWork(t, h, leaseProjectionWork(t, h, x, false, "initial"))
				o = assertRefundProjection(t, h, o, "SUCCEEDED", "", partial)
				// Pause an older retrieval in the other persistence path.
				staleWork := leaseProjectionWork(t, h, x, !webhook, "stale")
				staleRow, err := h.service.loadRefundWork(h.ctx, o.Refunds[0].ID)
				if err != nil {
					t.Fatal(err)
				}
				staleResult, err := h.provider.RetrieveRefund(h.ctx, stripeprovider.AccountContext{Mode: stripeprovider.ModeConnectedDirectFixture, StripeAccountID: x.AccountID}, x.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = h.provider.SetRefundOutcome(x.ID, "requires_action"); err != nil {
					t.Fatal(err)
				}
				currentWork := leaseProjectionWork(t, h, x, webhook, "returned")
				runProjectionWork(t, h, currentWork)
				o = assertRefundProjection(t, h, o, "PROCESSING", "", partial)
				var audits int
				if err = h.Pool.QueryRow(h.ctx, `SELECT count(*) FROM operation_order_events WHERE order_id=$1 AND event_type='REFUND_STATUS_CHANGED' AND before_state->>'refund_status'='SUCCEEDED' AND after_state->>'refund_status'='PROCESSING' AND before_payment_status=$2 AND after_payment_status=$3`, o.ID, map[bool]string{true: "PAID", false: "REFUNDED"}[partial], string(o.PaymentStatus)).Scan(&audits); err != nil || audits != 1 {
					t.Fatalf("return audit=%d %v", audits, err)
				}
				if staleWork.Kind == "REFUND_WEBHOOK" {
					err = h.service.persistRefundWebhookResult(h.ctx, staleWork, staleRow, staleResult)
				} else {
					err = h.service.persistRefundResult(h.ctx, staleWork, staleRow, staleResult)
				}
				if err != nil {
					t.Fatal(err)
				}
				got := assertRefundProjection(t, h, o, "PROCESSING", "", partial)
				if got.Version != o.Version {
					t.Fatal("stale success changed audit/version")
				}
				// A fresh observation of the very same object can succeed again.
				if _, err = h.provider.SetRefundOutcome(x.ID, "succeeded"); err != nil {
					t.Fatal(err)
				}
				w := leaseProjectionWork(t, h, x, webhook, "recovered")
				runProjectionWork(t, h, w)
				assertRefundProjection(t, h, o, "SUCCEEDED", "", partial)
			})
		}
	}
}

func TestPostgresP82RefundCreateStatusNeverOverridesRetrieval(t *testing.T) {
	h := newHoldFixture(t)
	order := createHeldPaidReview(t, h)
	h.work()
	o, err := h.Operations.AdjustOrder(h.ctx, operations.AdjustmentRequest{Scope: operations.TableScope{TenantID: h.TenantID, StoreID: h.StoreID, TableID: h.TableID}, SessionID: order.Order.SessionID, OrderID: order.Order.ID, Actor: "manager", ExpectedVersion: reviewOrderVersion(t, h, order.Order.ID), IdempotencyKey: "cancel", Reason: "cancel", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	h.provider.SetNextRefundOutcome("succeeded_then_failed", false)
	h.work()
	assertRefundProjection(t, h, o, "FAILED", "", false)
}

func TestPostgresP82RefundRetryUsesDurableAttemptBackoff(t *testing.T) {
	for _, webhook := range []bool{false, true} {
		for _, providerStatus := range []string{"pending", "requires_action", "future_status"} {
			t.Run(fmt.Sprintf("webhook-%v/%s", webhook, providerStatus), func(t *testing.T) {
				h, o, x := newRefundProjectionFixture(t, false)
				if _, err := h.provider.SetRefundOutcome(x.ID, providerStatus); err != nil {
					t.Fatal(err)
				}
				var workID, kind string
				if webhook {
					// A single webhook's retries must retain their durable count.
					accepted, err := h.service.EnqueueVerifiedWebhook(h.ctx, stripeprovider.VerifiedEvent{ID: "pending-refund-event", Type: "refund.updated", AccountID: x.AccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now(), ObjectID: x.ID, Refund: &x})
					if err != nil || !accepted {
						t.Fatalf("enqueue=%v %v", accepted, err)
					}
					kind = "REFUND_WEBHOOK"
				} else {
					kind = "REFUND"
				}
				if err := h.Pool.QueryRow(h.ctx, `SELECT work_id FROM stripe_payment_work WHERE refund_id=$1 AND work_type=$2`, o.Refunds[0].ID, kind).Scan(&workID); err != nil {
					t.Fatal(err)
				}
				workCode, projectionCode := "REFUND_PROCESSING", ""
				if providerStatus == "future_status" {
					workCode, projectionCode = "HOLD_RECONCILIATION_NEEDED", "HOLD_RECONCILIATION_NEEDED"
				}
				for i, seconds := range []int{5, 10, 20, 40, 80, 160, 300, 300} {
					h.readyOnly(o.ID, kind)
					h.workWithRetryClock()
					assertRefundRetrySchedule(t, h, workID, i+1, time.Duration(seconds)*time.Second, workCode)
					assertRefundProjection(t, h, o, "PROCESSING", projectionCode, false)
					// Prove scheduler exclusion independently of the short delay.
					if _, err := h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET available_at='infinity' WHERE order_id=$1 AND status='READY'`, o.ID); err != nil {
						t.Fatal(err)
					}
					_, retrieves := h.provider.RefundOperationCounts()
					if worked, err := h.service.ProcessNext(h.ctx); err != nil || worked {
						t.Fatalf("retry ran before available_at: %v %v", worked, err)
					}
					if _, after := h.provider.RefundOperationCounts(); after != retrieves {
						t.Fatal("provider retrieved before available_at")
					}
				}
				// A much higher persisted count must still use the cap.
				if _, err := h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET attempts=59 WHERE work_id=$1`, workID); err != nil {
					t.Fatal(err)
				}
				h.readyOnly(o.ID, kind)
				h.workWithRetryClock()
				assertRefundRetrySchedule(t, h, workID, 60, 300*time.Second, workCode)
			})
		}
	}
}

func TestPostgresP82RefundDispatchRetryUsesDurableAttemptBackoff(t *testing.T) {
	h, o, _ := newRefundProjectionFixture(t, false)
	h.service.resolver = nil
	var workID string
	if err := h.Pool.QueryRow(h.ctx, `SELECT work_id FROM stripe_payment_work WHERE refund_id=$1 AND work_type='REFUND'`, o.Refunds[0].ID).Scan(&workID); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		previousAttempts int
		seconds          int
	}{{0, 5}, {1, 10}, {2, 20}, {59, 300}} {
		if _, err := h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET attempts=$2 WHERE work_id=$1`, workID, tc.previousAttempts); err != nil {
			t.Fatal(err)
		}
		h.readyOnly(o.ID, "REFUND")
		h.workWithRetryClock()
		assertRefundRetrySchedule(t, h, workID, tc.previousAttempts+1, time.Duration(tc.seconds)*time.Second, "REFUND_DISPATCH_PENDING")
	}
	if creates, retrieves := h.provider.RefundOperationCounts(); creates != 1 || retrieves != 0 {
		t.Fatalf("unconfigured worker called provider: creates=%d retrieves=%d", creates, retrieves)
	}
}

func TestPostgresP82FreshRefundWebhookIsImmediatelyEligible(t *testing.T) {
	h, o, x := newRefundProjectionFixture(t, false)
	// Put ordinary polling at the cap, then park it independently of wall time.
	var workID string
	if err := h.Pool.QueryRow(h.ctx, `UPDATE stripe_payment_work SET attempts=59 WHERE refund_id=$1 AND work_type='REFUND' RETURNING work_id`, o.Refunds[0].ID).Scan(&workID); err != nil {
		t.Fatal(err)
	}
	h.readyOnly(o.ID, "REFUND")
	h.workWithRetryClock()
	assertRefundRetrySchedule(t, h, workID, 60, 300*time.Second, "REFUND_PROCESSING")
	if _, err := h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET available_at='infinity' WHERE work_id=$1`, workID); err != nil {
		t.Fatal(err)
	}
	// The webhook payload claims success, but retrieval still says pending.
	x.Status = "succeeded"
	enqueue := func(id string) string {
		t.Helper()
		accepted, err := h.service.EnqueueVerifiedWebhook(h.ctx, stripeprovider.VerifiedEvent{ID: id, Type: "refund.updated", AccountID: x.AccountID, APIVersion: stripeprovider.APIVersion, CreatedAt: time.Now(), ObjectID: x.ID, Refund: &x})
		if err != nil || !accepted {
			t.Fatalf("enqueue=%v %v", accepted, err)
		}
		var idWork string
		var ready bool
		if err = h.Pool.QueryRow(h.ctx, `SELECT work_id,status='READY' AND attempts=0 AND available_at<=clock_timestamp() FROM stripe_payment_work WHERE refund_event_row_id=(SELECT event_row_id FROM stripe_refund_webhook_events WHERE stripe_event_id=$1)`, id).Scan(&idWork, &ready); err != nil || !ready {
			t.Fatalf("fresh webhook was not immediately eligible: %v %v", ready, err)
		}
		return idWork
	}
	webhookWorkID := enqueue("fresh-refund-event")
	_, retrieves := h.provider.RefundOperationCounts()
	h.workWithRetryClock()
	if _, after := h.provider.RefundOperationCounts(); after != retrieves+1 {
		t.Fatalf("webhook retrievals=%d; want %d", after, retrieves+1)
	}
	assertRefundProjection(t, h, o, "PROCESSING", "", false)
	assertRefundRetrySchedule(t, h, webhookWorkID, 1, 5*time.Second, "REFUND_PROCESSING")
	if _, err := h.Pool.Exec(h.ctx, `UPDATE stripe_payment_work SET available_at='infinity' WHERE work_id=$1`, webhookWorkID); err != nil {
		t.Fatal(err)
	}
	if _, err := h.provider.SetRefundOutcome(x.ID, "succeeded"); err != nil {
		t.Fatal(err)
	}
	enqueue("confirmed-refund-event")
	h.work()
	assertRefundProjection(t, h, o, "SUCCEEDED", "", false)
	var processed bool
	if err := h.Pool.QueryRow(h.ctx, `SELECT processed_at IS NOT NULL AND safe_error_code IS NULL FROM stripe_refund_webhook_events WHERE stripe_event_id='confirmed-refund-event'`).Scan(&processed); err != nil || !processed {
		t.Fatalf("confirmed webhook was not reconciled immediately: %v %v", processed, err)
	}
	var pollingStillParked bool
	if err := h.Pool.QueryRow(h.ctx, `SELECT attempts=60 AND available_at='infinity'::timestamptz FROM stripe_payment_work WHERE work_id=$1`, workID).Scan(&pollingStillParked); err != nil || !pollingStillParked {
		t.Fatalf("webhook changed deferred polling: %v %v", pollingStillParked, err)
	}
}

func assertRefundRetrySchedule(t *testing.T, h *holdFixture, workID string, attempts int, delay time.Duration, code string) {
	t.Helper()
	var gotAttempts int
	var gotDelay float64
	var status, gotCode string
	if err := h.Pool.QueryRow(h.ctx, `SELECT attempts,extract(epoch FROM available_at-updated_at)::float8,status,coalesce(safe_error_code,'') FROM stripe_payment_work WHERE work_id=$1`, workID).Scan(&gotAttempts, &gotDelay, &status, &gotCode); err != nil {
		t.Fatal(err)
	}
	if gotAttempts != attempts || gotDelay != delay.Seconds() || status != "READY" || gotCode != code {
		t.Fatalf("retry schedule: attempts=%d delay=%.6f status=%s code=%s; want %d %s READY %s", gotAttempts, gotDelay, status, gotCode, attempts, delay, code)
	}
}

func TestPostgresP82GuestRefundPaymentStates(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial-%v", partial), func(t *testing.T) {
			h, o, x := newRefundProjectionFixture(t, partial)
			grant, err := h.Sessions.DirectJoin(h.ctx, h.Locator, "")
			if err != nil {
				t.Fatal(err)
			}
			if grant.Session.ID != o.SessionID {
				t.Fatal("guest joined a different session")
			}
			for _, status := range []string{"PROCESSING", "SUCCEEDED"} {
				if status == "SUCCEEDED" {
					if _, err := h.provider.SetRefundOutcome(x.ID, "succeeded"); err != nil {
						t.Fatal(err)
					}
				}
				runProjectionWork(t, h, leaseProjectionWork(t, h, x, false, status))
				// Staff and persisted state must retain their refund semantics.
				o = assertRefundProjection(t, h, o, status, "", partial)
				guest, err := h.Operations.ReadGuestOperations(h.ctx, o.SessionID, grant.Token)
				wantPayment := operations.Unknown
				if partial {
					wantPayment = operations.Paid
				}
				if err != nil || len(guest.Orders) != 1 || guest.Orders[0].ID != o.ID || guest.Orders[0].PaymentStatus != wantPayment || guest.Orders[0].FulfillmentStatus != o.FulfillmentStatus {
					t.Fatalf("%s guest=%+v err=%v", status, guest, err)
				}
			}
		})
	}
}
