package operations_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
)

func TestPostgresC2DuplicateFinalizeAndHistoryGuards(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f, grant, scope, state, stop := newRequestedStop(t, ctx, dsn, "c2-finalize-double-click")
	defer f.Close(ctx)
	// The SQL boundary also rejects a stale/out-of-band progress writer.
	if _, err := f.Pool.Exec(ctx, `UPDATE operation_order_state SET fulfillment_status='READY',version=version+1 WHERE order_id=$1`, state.ID); err == nil {
		t.Fatal("SQL bypassed pending request gate")
	}
	for _, query := range []string{`DELETE FROM cancellation_requests WHERE request_id=$1`, `UPDATE cancellation_requests SET reason='rewritten',version=version+1 WHERE request_id=$1`, `UPDATE cancellation_requests SET status='FINALIZED',version=version+1 WHERE request_id=$1`} {
		if _, err := f.Pool.Exec(ctx, query, stop.ID); err == nil {
			t.Fatalf("request history guard allowed %s", query)
		}
	}
	down, err := os.ReadFile("../../migrations/postgres/012_stop_requests.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, string(down)); err == nil {
		t.Fatal("rollback erased populated request history")
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	state, err = f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: state.ID, Actor: "cook", ExpectedVersion: state.Version, IdempotencyKey: "confirm"}, RequestID: stop.ID, ExpectedRequestVersion: stop.Version, Action: "confirm"})
	if err != nil {
		t.Fatal(err)
	}
	stop = state.CancellationRequests[0]
	input := operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: state.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "finalize"}, RequestID: stop.ID, ExpectedRequestVersion: stop.Version, Action: "finalize", NonDeliveryConfirmed: true}
	block, err := f.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer block.Rollback(context.Background())
	var pid int32
	if err = block.QueryRow(ctx, `SELECT pg_backend_pid() FROM table_sessions WHERE session_id=$1 FOR UPDATE`, grant.Session.ID).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	type result struct {
		order operations.OrderState
		err   error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() { o, e := f.Operations.ActOnStopRequest(ctx, input); results <- result{o, e} }()
	}
	// Both requests must reach the database while the parent lock is held.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var blocked int
		if err = f.Pool.QueryRow(ctx, `WITH RECURSIVE blocked(pid) AS (SELECT pid FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) UNION SELECT a.pid FROM pg_stat_activity a JOIN blocked b ON b.pid=ANY(pg_blocking_pids(a.pid))) SELECT count(*) FROM blocked`, pid).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("duplicate finalize did not reach lock barrier")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = block.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.order.Version != second.order.Version || first.order.FulfillmentStatus != operations.Cancelled {
		t.Fatalf("duplicate finalize did not replay: %+v %+v", first, second)
	}
	var adjustments, refunds, events int
	if err = f.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM order_adjustments WHERE order_id=$1),(SELECT count(*) FROM operation_refunds WHERE order_id=$1),(SELECT count(*) FROM operation_order_events WHERE order_id=$1 AND event_type='STOP_FINALIZE')`, state.ID).Scan(&adjustments, &refunds, &events); err != nil || adjustments != 1 || refunds != 0 || events != 1 {
		t.Fatalf("duplicate facts adjustments=%d refunds=%d events=%d err=%v", adjustments, refunds, events, err)
	}
	// Whole-order cancellation retains C1's cancelled-card acknowledgement.
	cards, err := f.Operations.ListKDS(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID})
	if err != nil || len(cards) != 1 || !cards[0].OrderUpdated || cards[0].FulfillmentStatus != operations.Cancelled {
		t.Fatalf("cancelled card disappeared before acknowledgement: %+v %v", cards, err)
	}
	if _, err = f.Operations.AcknowledgeOrderUpdate(ctx, operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: state.ID, Actor: "cook", ExpectedVersion: first.order.Version, IdempotencyKey: "ack-cancelled"}); err != nil {
		t.Fatal(err)
	}
	cards, err = f.Operations.ListKDS(ctx, operations.StoreScope{TenantID: scope.TenantID, StoreID: scope.StoreID})
	if err != nil || len(cards) != 0 {
		t.Fatalf("acknowledged cancelled card stayed active: %+v %v", cards, err)
	}
	input.Actor = "other-manager"
	if _, err = f.Operations.ActOnStopRequest(ctx, input); !errors.Is(err, operations.ErrIdempotencyConflict) {
		t.Fatalf("different actor reused finalization key: %v", err)
	}
	input.Actor = "manager"
	input.NonDeliveryConfirmed = false
	input.Reason = "changed payload"
	if _, err = f.Operations.ActOnStopRequest(ctx, input); !errors.Is(err, operations.ErrIdempotencyConflict) {
		t.Fatalf("different payload reused finalization key: %v", err)
	}
}

func TestPostgresC2BatchRequestAtomicityAndScopes(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f, grant, scope, state, stop := newRequestedStop(t, ctx, dsn, "c2-batch")
	defer f.Close(ctx)
	// Explicit withdrawal reopens a non-exception line for a fresh request.
	state, err := f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: state.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "withdraw"}, RequestID: stop.ID, ExpectedRequestVersion: stop.Version, Action: "withdraw"})
	if err != nil {
		t.Fatal(err)
	}
	input := operations.AdjustmentRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: state.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "batch", Reason: "batch reason", Lines: []operations.AdjustmentLine{{LineIndex: 0, CancelledQuantity: 1}, {LineIndex: 999, CancelledQuantity: 1}}}
	if _, err = f.Operations.RequestStop(ctx, input); !errors.Is(err, operations.ErrInvalid) {
		t.Fatalf("invalid batch=%v", err)
	}
	var count int
	if err = f.Pool.QueryRow(ctx, `SELECT count(*) FROM cancellation_requests WHERE order_id=$1`, state.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("partial batch leaked count=%d err=%v", count, err)
	}
	input.Lines = input.Lines[:1]
	for _, wrong := range []operations.TableScope{{TenantID: scope.TenantID, StoreID: "other-store", TableID: scope.TableID}, {TenantID: "other-tenant", StoreID: scope.StoreID, TableID: scope.TableID}, {TenantID: scope.TenantID, StoreID: scope.StoreID, TableID: "other-table"}} {
		input.Scope = wrong
		if _, err = f.Operations.RequestStop(ctx, input); !errors.Is(err, operations.ErrNotFound) {
			t.Fatalf("cross-scope request=%v", err)
		}
	}
	input.Scope = scope
	state, err = f.Operations.RequestStop(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.CancellationRequests) != 2 || state.CancellationRequests[1].Status != "REQUESTED" {
		t.Fatalf("withdrawn normal request could not restart: %+v", state)
	}
	input.ExpectedVersion = state.Version
	input.IdempotencyKey = "overlap"
	if _, err = f.Operations.RequestStop(ctx, input); !errors.Is(err, operations.ErrStopRequestPending) {
		t.Fatalf("overlapping line was allowed: %v", err)
	}
}

func TestPostgresC2PendingRequestsPreventDirectSessionClosure(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	for _, status := range []string{"REQUESTED", "KITCHEN_CONFIRMED"} {
		t.Run(status, func(t *testing.T) {
			f, grant, scope, state, stop := newRequestedStop(t, ctx, dsn, "c2-session-close-"+status)
			defer f.Close(ctx)
			if status == "KITCHEN_CONFIRMED" {
				var err error
				state, err = f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{
					OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: state.ID, Actor: "cook", ExpectedVersion: state.Version, IdempotencyKey: "confirm"},
					RequestID:          stop.ID, ExpectedRequestVersion: stop.Version, Action: "confirm",
				})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.Pool.Exec(ctx, `UPDATE table_sessions SET status='CLOSED',closed_at=clock_timestamp(),closed_by='test',version=version+1 WHERE session_id=$1`, grant.Session.ID); err == nil || !strings.Contains(err.Error(), "unresolved stop request blocks session closure") {
				t.Fatalf("direct closure was not rejected by pending-request guard for %s: %v", status, err)
			}
			var sessionStatus string
			if err := f.Pool.QueryRow(ctx, `SELECT status FROM table_sessions WHERE session_id=$1`, grant.Session.ID).Scan(&sessionStatus); err != nil || sessionStatus != "OPEN" {
				t.Fatalf("rejected closure changed session status=%s err=%v", sessionStatus, err)
			}
		})
	}
}

func TestPostgresC2TerminalOrUnrelatedRequestsDoNotPreventSessionClosure(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	for _, terminal := range []string{"REJECTED", "WITHDRAWN"} {
		t.Run("terminal "+terminal, func(t *testing.T) {
			f, grant, scope, state, stop := newRequestedStop(t, ctx, dsn, "c2-session-close-terminal-"+terminal)
			defer f.Close(ctx)
			action, actor, reason := "reject", "kitchen", "already plated"
			if terminal == "WITHDRAWN" {
				action, actor, reason = "withdraw", "manager", ""
			}
			_, err := f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{
				OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: state.ID, Actor: actor, ExpectedVersion: state.Version, IdempotencyKey: "terminal", Reason: reason},
				RequestID:          stop.ID, ExpectedRequestVersion: stop.Version, Action: action,
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.Pool.Exec(ctx, `UPDATE table_sessions SET status='CLOSED',closed_at=clock_timestamp(),closed_by='test',version=version+1 WHERE session_id=$1`, grant.Session.ID); err != nil {
				t.Fatalf("terminal request prevented closure: %v", err)
			}
		})
	}

	t.Run("request on another session", func(t *testing.T) {
		f, grant, scope, _, _ := newRequestedStop(t, ctx, dsn, "c2-session-close-other-request")
		defer f.Close(ctx)
		// Create a second table session in the same fixture schema. The original
		// session has a pending request; this target session has no order.
		targetSessionID := grant.Session.ID + "-unrelated"
		if _, err := f.Pool.Exec(ctx, `INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES($1,$2,$3,'Unrelated table',$4)`, scope.TenantID, scope.StoreID, scope.TableID+"-unrelated", "locator-"+targetSessionID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Pool.Exec(ctx, `INSERT INTO table_sessions(session_id,tenant_id,store_id,table_id,status,qr_mode,expires_at,opened_by) SELECT $1,tenant_id,store_id,$2,'OPEN',qr_mode,expires_at,opened_by FROM table_sessions WHERE session_id=$3`, targetSessionID, scope.TableID+"-unrelated", grant.Session.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Pool.Exec(ctx, `UPDATE table_sessions SET status='CLOSED',closed_at=clock_timestamp(),closed_by='test',version=version+1 WHERE session_id=$1`, targetSessionID); err != nil {
			t.Fatalf("request on unrelated session prevented closure: %v", err)
		}
	})
}
