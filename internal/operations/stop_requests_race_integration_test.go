package operations_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

func TestPostgresC2DirectStopInsertFulfillmentRace(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	for _, insertFirst := range []bool{true, false} {
		name := "update-first"
		if insertFirst {
			name = "insert-first"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			f, grant, scope, state, stop := newRequestedStop(t, ctx, dsn, "c2-direct-race")
			defer f.Close(context.Background())
			// Retain existing request history while making room for the racing insert.
			if _, err := f.Operations.ActOnStopRequest(ctx, operations.StopActionRequest{
				OrderActionRequest: operations.OrderActionRequest{Scope: scope, SessionID: grant.Session.ID, OrderID: state.ID, Actor: "manager", ExpectedVersion: state.Version, IdempotencyKey: "withdraw"},
				RequestID:          stop.ID, ExpectedRequestVersion: stop.Version, Action: "withdraw",
			}); err != nil {
				t.Fatal(err)
			}

			// Model an existing populated 012 database, then upgrade via the real runner.
			conn, err := f.Pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			down, err := os.ReadFile("../../migrations/postgres/013_stop_request_state_lock.down.sql")
			if err == nil {
				_, err = conn.Exec(ctx, string(down))
			}
			if err == nil {
				_, err = conn.Exec(ctx, `DELETE FROM schema_migrations WHERE version=13`)
			}
			if err == nil {
				err = postgres.Up(ctx, conn.Conn())
			}
			conn.Release()
			if err != nil {
				t.Fatal(err)
			}

			// Explicit READ COMMITTED ensures the waiting UPDATE's outer statement
			// snapshot predates the insert commit. Its VOLATILE BEFORE ROW trigger must
			// nevertheless recheck open requests with a fresh snapshot after the wait.
			var volatile bool
			if err = f.Pool.QueryRow(ctx, `SELECT provolatile='v' FROM pg_proc WHERE oid='validate_operation_order_transition()'::regprocedure`).Scan(&volatile); err != nil || !volatile {
				t.Fatalf("fulfillment trigger must be volatile: %v, %v", volatile, err)
			}
			first, err := f.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			if err != nil {
				t.Fatal(err)
			}
			defer first.Rollback(context.Background())
			second, err := f.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			if err != nil {
				t.Fatal(err)
			}
			defer second.Rollback(context.Background())
			firstPID, secondPID := first.Conn().PgConn().PID(), second.Conn().PgConn().PID()
			insert := `INSERT INTO cancellation_requests(request_id,order_id,line_index,quantity,reason,requesting_manager,idempotency_key) VALUES('direct-race',$1,0,1,'direct stop','manager','direct-race')`
			update := `UPDATE operation_order_state SET fulfillment_status='READY',version=version+1 WHERE order_id=$1`
			firstSQL, secondSQL := update, insert
			wantStatus, wantRequests, wantError := "READY", 0, "stop request requires preparing KDS card"
			if insertFirst {
				firstSQL, secondSQL = insert, update
				wantStatus, wantRequests, wantError = "PREPARING", 1, "unresolved stop request blocks fulfillment"
			}
			if tag, err := first.Exec(ctx, firstSQL, state.ID); err != nil || tag.RowsAffected() != 1 {
				t.Fatalf("first writer: affected=%d err=%v", tag.RowsAffected(), err)
			}
			waitCtx, stopWait := context.WithCancel(ctx)
			results := make(chan error, 1)
			go func() { _, err := second.Exec(waitCtx, secondSQL, state.ID); results <- err }()
			consumed := false
			defer func() {
				stopWait()
				if !consumed {
					<-results
				}
			}()
			// Observe the exact backend blocked by the successful uncommitted writer.
			// A missing lock fails at the barrier, even if scheduling would hide the race.
			deadline := time.Now().Add(5 * time.Second)
			for {
				var blocked bool
				if err := f.Pool.QueryRow(ctx, `SELECT $1::integer=ANY(pg_blocking_pids($2::integer))`, firstPID, secondPID).Scan(&blocked); err != nil {
					t.Fatal(err)
				}
				if blocked {
					break
				}
				select {
				case err := <-results:
					consumed = true
					t.Fatalf("second writer did not wait on first writer: %v", err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("direct writers did not reach row-lock barrier")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := first.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			err = <-results
			consumed = true
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || !strings.Contains(pgErr.Message, wantError) {
				t.Fatalf("second writer must fail the business guard, got %v", err)
			}
			if err := second.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			var status string
			var requests, history int
			if err := f.Pool.QueryRow(ctx, `SELECT fulfillment_status,(SELECT count(*) FROM cancellation_requests WHERE order_id=$1 AND status IN ('REQUESTED','KITCHEN_CONFIRMED')),(SELECT count(*) FROM cancellation_requests WHERE order_id=$1 AND status='WITHDRAWN') FROM operation_order_state WHERE order_id=$1`, state.ID).Scan(&status, &requests, &history); err != nil || status != wantStatus || requests != wantRequests || history != 1 {
				t.Fatalf("status=%s requests=%d retained history=%d err=%v", status, requests, history, err)
			}
		})
	}
}
