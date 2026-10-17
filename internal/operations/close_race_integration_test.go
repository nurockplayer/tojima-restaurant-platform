package operations_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/ordering"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/testguest"
)

// This integration test exercises the shared PostgreSQL row-lock boundary
// between guest order commit and staff close. Each race has exactly one valid
// result: either the unpaid order commits and close is blocked, or close wins
// and the guest commit is denied without creating an order.
func TestPostgresGuestCommitSerializesWithClose(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	for i := 0; i < 6; i++ {
		t.Run(fmt.Sprintf("iteration-%d", i+1), func(t *testing.T) {
			f, err := testguest.NewWithOptions(ctx, dsn, testguest.Options{CashEnabled: true, CashMode: operations.Prepaid})
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close(ctx)
			grant, e := f.Sessions.DirectJoin(ctx, f.Locator, "")
			if e != nil {
				t.Fatal(e)
			}
			cart, e := f.Orders.Mutate(ctx, ordering.Mutation{
				SessionID: grant.Session.ID, GrantToken: grant.Token,
				ExpectedGeneration: 1, ExpectedVersion: 1,
				IdempotencyKey: fmt.Sprintf("close-race-add-%d", i), Kind: ordering.AddLine,
				ProductKey: "tea", Quantity: 1,
			})
			if e != nil {
				t.Fatal(e)
			}
			preview, e := f.Orders.Preview(ctx, grant.Session.ID, grant.Token, 1, cart.Version)
			if e != nil {
				t.Fatal(e)
			}
			scope := grant.Session.Scope
			start := make(chan struct{})
			commitCh := make(chan error, 1)
			closeCh := make(chan error, 1)
			var wg sync.WaitGroup
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, commitErr := f.Orders.Commit(ctx, ordering.CommitRequest{
					SessionID: grant.Session.ID, GrantToken: grant.Token,
					ExpectedGeneration: 1, ExpectedVersion: cart.Version,
					IdempotencyKey:     fmt.Sprintf("close-race-commit-%d", i),
					PreviewFingerprint: preview.Fingerprint, PaymentMethod: ordering.Cash,
				})
				commitCh <- commitErr
			}()
			go func() {
				defer wg.Done()
				<-start
				closeCh <- f.Sessions.Close(ctx, scope, grant.Session.ID, "test-cashier")
			}()
			close(start)
			wg.Wait()
			commitErr, closeErr := <-commitCh, <-closeCh

			var status tablesession.Status
			var orderCount int
			var paymentStatus string
			err = f.Pool.QueryRow(ctx, `SELECT ts.status,
				(SELECT count(*) FROM table_orders o WHERE o.session_id=ts.session_id),
				COALESCE((SELECT st.payment_status FROM operation_order_state st JOIN table_orders o USING(order_id) WHERE o.session_id=ts.session_id LIMIT 1),'')
				FROM table_sessions ts WHERE ts.session_id=$1`, grant.Session.ID).Scan(&status, &orderCount, &paymentStatus)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case commitErr == nil && errors.Is(closeErr, tablesession.ErrCloseBlocked):
				if status != tablesession.Open || orderCount != 1 || paymentStatus != string(operations.Unpaid) {
					t.Fatalf("commit-wins result violated close invariant: status=%s orders=%d payment=%s", status, orderCount, paymentStatus)
				}
			case errors.Is(commitErr, ordering.ErrDenied) && closeErr == nil:
				if status != tablesession.Closed || orderCount != 0 || paymentStatus != "" {
					t.Fatalf("close-wins result left an order or open session: status=%s orders=%d payment=%s", status, orderCount, paymentStatus)
				}
			default:
				t.Fatalf("unexpected race outcome: commit=%v close=%v status=%s orders=%d payment=%s", commitErr, closeErr, status, orderCount, paymentStatus)
			}
		})
	}
}

func TestPostgresCashSettlementStillRequiresServedBeforeClose(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f, err := testguest.NewWithOptions(ctx, dsn, testguest.Options{CashEnabled: true, CashMode: operations.Prepaid})
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close(ctx)
	grant, err := f.Sessions.DirectJoin(ctx, f.Locator, "")
	if err != nil {
		t.Fatal(err)
	}
	scope := operations.TableScope{TenantID: grant.Session.Scope.TenantID, StoreID: grant.Session.Scope.StoreID, TableID: grant.Session.Scope.TableID}
	order := commitCashOrder(t, ctx, f, grant, 1, "close-after-settlement")
	preview, err := f.Operations.PreviewCash(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	settlementInput := operations.CashSettlementRequest{
		Scope: scope, SessionID: grant.Session.ID, Actor: "test-cashier", Fingerprint: preview.Fingerprint,
		TenderedJPY: preview.DueJPY, RegisterSessionRef: "test-register-session", IdempotencyKey: "close-settlement",
	}
	originalReceipt, err := f.Operations.SettleCash(ctx, settlementInput)
	if err != nil {
		t.Fatal(err)
	}
	state, err := f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil || len(state.Orders) != 1 || state.Orders[0].ID != order.ID || state.Orders[0].PaymentStatus != operations.Paid {
		t.Fatalf("settlement did not persist paid order: %+v err=%v", state, err)
	}
	if err = f.Sessions.Close(ctx, grant.Session.Scope, grant.Session.ID, "test-cashier"); !errors.Is(err, tablesession.ErrCloseBlocked) {
		t.Fatalf("paid but unserved order did not block close: %v", err)
	}
	accepted, err := f.Operations.AcceptOrder(ctx, operations.OrderActionRequest{
		Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "test-cashier",
		ExpectedVersion: state.Orders[0].Version, IdempotencyKey: "close-order-accept",
	})
	if err != nil || accepted.FulfillmentStatus != operations.Accepted {
		t.Fatalf("paid order did not enter fulfillment: state=%+v err=%v", accepted, err)
	}
	state.Orders[0] = accepted
	for _, target := range []operations.FulfillmentStatus{operations.Preparing, operations.Ready, operations.Served} {
		var updated operations.OrderState
		updated, err = f.Operations.AdvanceFulfillment(ctx, operations.OrderActionRequest{
			Scope: scope, SessionID: grant.Session.ID, OrderID: order.ID, Actor: "test-kitchen",
			ExpectedVersion: state.Orders[0].Version, IdempotencyKey: "close-fulfillment-" + string(target), Target: target,
		})
		if err != nil || updated.FulfillmentStatus != target {
			t.Fatalf("advance to %s: state=%+v err=%v", target, updated, err)
		}
		state.Orders[0] = updated
	}
	setOperationalHold(t, ctx, f, grant.Session.ID, order.ID, true)
	detail, err := f.Operations.ReadSession(ctx, scope, grant.Session.ID)
	if err != nil {
		t.Fatal(err)
	}
	hasHoldBlocker := false
	for _, blocker := range detail.CloseBlockers {
		if blocker == "OPERATION_HOLD" {
			hasHoldBlocker = true
		}
	}
	if !hasHoldBlocker {
		t.Fatalf("paid and served held order must be shown as a close blocker: %+v", detail.CloseBlockers)
	}
	if err = f.Sessions.Close(ctx, grant.Session.Scope, grant.Session.ID, "test-cashier"); !errors.Is(err, tablesession.ErrCloseBlocked) {
		t.Fatalf("operational hold did not block close after payment and service: %v", err)
	}
	setOperationalHold(t, ctx, f, grant.Session.ID, order.ID, false)
	if err = f.Sessions.Close(ctx, grant.Session.Scope, grant.Session.ID, "test-cashier"); err != nil {
		t.Fatalf("close failed after all orders were paid and served: %v", err)
	}
	retry, err := f.Operations.SettleCash(ctx, settlementInput)
	if err != nil || retry.ID != originalReceipt.ID || retry.DueJPY != originalReceipt.DueJPY || retry.CreatedAt != originalReceipt.CreatedAt {
		t.Fatalf("closed-session retry did not return original immutable receipt: retry=%+v original=%+v err=%v", retry, originalReceipt, err)
	}
	conflictingBody := settlementInput
	conflictingBody.TenderedJPY++
	if _, err = f.Operations.SettleCash(ctx, conflictingBody); !errors.Is(err, operations.ErrIdempotencyConflict) {
		t.Fatalf("closed-session same-key different body must conflict: %v", err)
	}
	differentKey := settlementInput
	differentKey.IdempotencyKey = "closed-new-settlement"
	if _, err = f.Operations.SettleCash(ctx, differentKey); !errors.Is(err, operations.ErrConflict) {
		t.Fatalf("closed-session new settlement must be denied: %v", err)
	}
}
