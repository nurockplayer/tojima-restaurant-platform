package ordering

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPostgresCompanyEmergencyHoldBlocksQuoteAndCommitWithoutLosingCart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := newOrderingFixture(t, ctx)
	token := fixture.tokens[0]

	cart, err := fixture.service.Mutate(ctx, Mutation{
		SessionID: fixture.session.ID, GrantToken: token, ExpectedGeneration: 1, ExpectedVersion: 1,
		IdempotencyKey: "hold-cart-add", Kind: AddLine, ProductKey: "tea", Quantity: 1,
	})
	if err != nil || cart.Version != 2 || len(cart.Lines) != 1 {
		t.Fatalf("prepare cart before hold: cart=%+v err=%v", cart, err)
	}
	preview, err := fixture.service.Preview(ctx, fixture.session.ID, token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatalf("preview before hold: %v", err)
	}
	if _, err = fixture.catalog.SetCompanyProductEmergencyHold(ctx, "tenant", "tea", "tenant-admin", true, "safety review", 0, "hold-tea-on"); err != nil {
		t.Fatalf("activate hold: %v", err)
	}

	current, err := fixture.service.ReadCart(ctx, fixture.session.ID, token)
	if err != nil || len(current.Lines) != 1 || current.Lines[0].ProductAvailable || current.Lines[0].UnavailableCode != "COMPANY_EMERGENCY_HOLD" {
		t.Fatalf("hold did not preserve and annotate cart line: cart=%+v err=%v", current, err)
	}
	if _, err = fixture.service.Preview(ctx, fixture.session.ID, token, cart.Generation, cart.Version); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("held product remained quotable: %v", err)
	}
	for _, method := range []PaymentMethod{Cash, Stripe} {
		_, commitErr := fixture.service.Commit(ctx, CommitRequest{
			SessionID: fixture.session.ID, GrantToken: token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
			IdempotencyKey: "hold-commit-" + string(method), PreviewFingerprint: preview.Fingerprint, PaymentMethod: method,
		})
		if !errors.Is(commitErr, ErrUnavailable) {
			t.Fatalf("%s commit was not refused under active company hold: %v", method, commitErr)
		}
	}
	var orderCount, operationCount int
	if err = fixture.pool.QueryRow(ctx, `SELECT count(*) FROM table_orders WHERE session_id=$1`, fixture.session.ID).Scan(&orderCount); err != nil {
		t.Fatal(err)
	}
	if err = fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operation_order_state WHERE session_id=$1`, fixture.session.ID).Scan(&operationCount); err != nil {
		t.Fatal(err)
	}
	if orderCount != 0 || operationCount != 0 {
		t.Fatalf("hold refusal had payment/order side effects: orders=%d operation_states=%d", orderCount, operationCount)
	}

	if _, err = fixture.catalog.SetCompanyProductEmergencyHold(ctx, "tenant", "tea", "tenant-admin", false, "review cleared", 1, "hold-tea-off-1"); err != nil {
		t.Fatalf("clear first hold generation: %v", err)
	}
	if _, err = fixture.service.Commit(ctx, CommitRequest{
		SessionID: fixture.session.ID, GrantToken: token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
		IdempotencyKey: "hold-stale-preview-before-first-on", PreviewFingerprint: preview.Fingerprint, PaymentMethod: Cash,
	}); !errors.Is(err, ErrRequote) {
		t.Fatalf("pre-hold preview survived ON/OFF generation: %v", err)
	}
	secondPreview, err := fixture.service.Preview(ctx, fixture.session.ID, token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatalf("preview after first hold generation: %v", err)
	}
	if _, err = fixture.catalog.SetCompanyProductEmergencyHold(ctx, "tenant", "tea", "tenant-admin", true, "repeat review", 2, "hold-tea-on-2"); err != nil {
		t.Fatalf("activate second hold generation: %v", err)
	}
	if _, err = fixture.catalog.SetCompanyProductEmergencyHold(ctx, "tenant", "tea", "tenant-admin", false, "repeat review cleared", 3, "hold-tea-off-2"); err != nil {
		t.Fatalf("clear second hold generation: %v", err)
	}
	if _, err = fixture.service.Commit(ctx, CommitRequest{
		SessionID: fixture.session.ID, GrantToken: token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
		IdempotencyKey: "hold-stale-preview-before-second-on", PreviewFingerprint: secondPreview.Fingerprint, PaymentMethod: Stripe,
	}); !errors.Is(err, ErrRequote) {
		t.Fatalf("prior OFF-generation preview survived a later ON/OFF: %v", err)
	}
	current, err = fixture.service.ReadCart(ctx, fixture.session.ID, token)
	if err != nil || current.Version != cart.Version || len(current.Lines) != 1 {
		t.Fatalf("stale hold-generation commits mutated the cart: cart=%+v err=%v", current, err)
	}
}

func TestPostgresCompanyHoldSerializesWithCommitInBothOrders(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	fixture := newOrderingFixture(t, ctx)
	token := fixture.tokens[0]
	cart, err := fixture.service.Mutate(ctx, Mutation{
		SessionID: fixture.session.ID, GrantToken: token, ExpectedGeneration: 1, ExpectedVersion: 1,
		IdempotencyKey: "hold-race-add-tea", Kind: AddLine, ProductKey: "tea", Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := fixture.service.Preview(ctx, fixture.session.ID, token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	var catalogID int64
	if err = fixture.pool.QueryRow(ctx, `SELECT catalog_id FROM menu_catalogs WHERE tenant_id='tenant' AND owner_kind='COMPANY'`).Scan(&catalogID); err != nil {
		t.Fatal(err)
	}

	// Queue the real first-ON request behind the company parent lock, then queue
	// Commit behind that updater. Once released, hold publication wins and the
	// actual order admission must see the hold.
	holdFirstLock, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = holdFirstLock.QueryRow(ctx, `SELECT catalog_id FROM menu_catalogs WHERE catalog_id=$1 FOR UPDATE`, catalogID).Scan(&catalogID); err != nil {
		_ = holdFirstLock.Rollback(ctx)
		t.Fatal(err)
	}
	holdDone := make(chan error, 1)
	go func() {
		_, holdErr := fixture.catalog.SetCompanyProductEmergencyHold(ctx, "tenant", "tea", "tenant-admin", true, "first-ON race", 0, "race-tea-hold-on")
		holdDone <- holdErr
	}()
	if err = waitForBlockedQuery(ctx, fixture.pool, "%menu_catalogs%FOR UPDATE%"); err != nil {
		_ = holdFirstLock.Rollback(ctx)
		t.Fatalf("first-ON request never waited on company parent: %v", err)
	}
	commitDone := make(chan error, 1)
	go func() {
		_, commitErr := fixture.service.Commit(ctx, CommitRequest{
			SessionID: fixture.session.ID, GrantToken: token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
			IdempotencyKey: "hold-race-commit-after-on", PreviewFingerprint: preview.Fingerprint, PaymentMethod: Stripe,
		})
		commitDone <- commitErr
	}()
	if err = waitForBlockedQuery(ctx, fixture.pool, "%menu_catalogs%FOR SHARE%"); err != nil {
		_ = holdFirstLock.Rollback(ctx)
		t.Fatalf("commit never waited behind first-ON request: %v", err)
	}
	if err = holdFirstLock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-holdDone; err != nil {
		t.Fatalf("first-ON request failed: %v", err)
	}
	if err = <-commitDone; !errors.Is(err, ErrUnavailable) {
		t.Fatalf("commit queued behind first-ON did not see hold: %v", err)
	}

	// Reverse the order: hold the cart row so a real Commit has already acquired
	// the company admission SHARE lock, then queue the hold updater. The order
	// must finish first and remain immutable after the later hold.
	if _, err = fixture.catalog.SetCompanyProductEmergencyHold(ctx, "tenant", "tea", "tenant-admin", false, "clear first-ON race", 1, "race-tea-hold-off"); err != nil {
		t.Fatal(err)
	}
	cart, err = fixture.service.ReadCart(ctx, fixture.session.ID, token)
	if err != nil || len(cart.Lines) != 1 {
		t.Fatalf("cart not retained after hold-first race: cart=%+v err=%v", cart, err)
	}
	cart, err = fixture.service.Mutate(ctx, Mutation{
		SessionID: fixture.session.ID, GrantToken: token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
		IdempotencyKey: "hold-race-remove-tea", Kind: RemoveLine, LineID: cart.Lines[0].LineID,
	})
	if err != nil {
		t.Fatal(err)
	}
	cart, err = fixture.service.Mutate(ctx, Mutation{
		SessionID: fixture.session.ID, GrantToken: token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
		IdempotencyKey: "hold-race-add-soup", Kind: AddLine, ProductKey: "soup", Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err = fixture.service.Preview(ctx, fixture.session.ID, token, cart.Generation, cart.Version)
	if err != nil {
		t.Fatal(err)
	}
	cartFirstLock, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var cartVersion int64
	if err = cartFirstLock.QueryRow(ctx, `SELECT version FROM table_carts WHERE session_id=$1 FOR UPDATE`, fixture.session.ID).Scan(&cartVersion); err != nil {
		_ = cartFirstLock.Rollback(ctx)
		t.Fatal(err)
	}
	commitResult := make(chan struct {
		result CommitResult
		err    error
	}, 1)
	go func() {
		result, commitErr := fixture.service.Commit(ctx, CommitRequest{
			SessionID: fixture.session.ID, GrantToken: token, ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version,
			IdempotencyKey: "hold-race-commit-before-on", PreviewFingerprint: preview.Fingerprint, PaymentMethod: Stripe,
		})
		commitResult <- struct {
			result CommitResult
			err    error
		}{result, commitErr}
	}()
	if err = waitForBlockedQuery(ctx, fixture.pool, "%table_carts%FOR UPDATE%"); err != nil {
		_ = cartFirstLock.Rollback(ctx)
		t.Fatalf("commit never reached cart lock after company admission: %v", err)
	}
	holdDone = make(chan error, 1)
	go func() {
		_, holdErr := fixture.catalog.SetCompanyProductEmergencyHold(ctx, "tenant", "soup", "tenant-admin", true, "second-ON race", 0, "race-soup-hold-on")
		holdDone <- holdErr
	}()
	if err = waitForBlockedQuery(ctx, fixture.pool, "%menu_catalogs%FOR UPDATE%"); err != nil {
		_ = cartFirstLock.Rollback(ctx)
		t.Fatalf("hold updater never waited behind admitted commit: %v", err)
	}
	if err = cartFirstLock.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	committed := <-commitResult
	if committed.err != nil || committed.result.Outcome != Committed {
		t.Fatalf("commit admitted before first-ON did not complete first: result=%+v err=%v", committed.result, committed.err)
	}
	if err = <-holdDone; err != nil {
		t.Fatalf("second-ON request failed after order commit: %v", err)
	}
	// Capture the already committed order after the first later hold, then
	// perform another real OFF/ON generation before comparing persisted state.
	var snapshotBefore, taxBefore string
	var operationVersionBefore int64
	var cardsBefore int
	if err = fixture.pool.QueryRow(ctx, `SELECT quote_snapshot::text,tax_snapshot::text FROM table_orders WHERE order_id=$1`, committed.result.Order.ID).Scan(&snapshotBefore, &taxBefore); err != nil {
		t.Fatal(err)
	}
	if err = fixture.pool.QueryRow(ctx, `SELECT version FROM operation_order_state WHERE order_id=$1`, committed.result.Order.ID).Scan(&operationVersionBefore); err != nil {
		t.Fatal(err)
	}
	if err = fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operation_kds_cards WHERE order_id=$1`, committed.result.Order.ID).Scan(&cardsBefore); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.catalog.SetCompanyProductEmergencyHold(ctx, "tenant", "soup", "tenant-admin", false, "clear after committed order", 1, "race-soup-hold-off"); err != nil {
		t.Fatal(err)
	}
	if _, err = fixture.catalog.SetCompanyProductEmergencyHold(ctx, "tenant", "soup", "tenant-admin", true, "reapply after committed order", 2, "race-soup-hold-reapply"); err != nil {
		t.Fatal(err)
	}
	var snapshotAfter, taxAfter string
	var operationVersionAfter int64
	var cardsAfter int
	if err = fixture.pool.QueryRow(ctx, `SELECT quote_snapshot::text,tax_snapshot::text FROM table_orders WHERE order_id=$1`, committed.result.Order.ID).Scan(&snapshotAfter, &taxAfter); err != nil {
		t.Fatal(err)
	}
	if err = fixture.pool.QueryRow(ctx, `SELECT version FROM operation_order_state WHERE order_id=$1`, committed.result.Order.ID).Scan(&operationVersionAfter); err != nil {
		t.Fatal(err)
	}
	if err = fixture.pool.QueryRow(ctx, `SELECT count(*) FROM operation_kds_cards WHERE order_id=$1`, committed.result.Order.ID).Scan(&cardsAfter); err != nil {
		t.Fatal(err)
	}
	if snapshotAfter != snapshotBefore || taxAfter != taxBefore || operationVersionAfter != operationVersionBefore || cardsAfter != cardsBefore {
		t.Fatalf("later hold changed committed order state: quote_same=%t tax_same=%t operation_version=%d→%d KDS_cards=%d→%d",
			snapshotBefore == snapshotAfter, taxBefore == taxAfter, operationVersionBefore, operationVersionAfter, cardsBefore, cardsAfter)
	}
}

func waitForBlockedQuery(ctx context.Context, pool *pgxpool.Pool, pattern string) error {
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
			WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE $1
			AND EXISTS(SELECT 1 FROM pg_locks held WHERE held.pid=pg_stat_activity.pid
				AND held.relation=to_regclass('menu_catalogs')::oid)
			AND EXISTS(SELECT 1 FROM pg_locks waiting WHERE waiting.pid=pg_stat_activity.pid AND NOT waiting.granted))`, pattern).Scan(&blocked); err != nil {
			return err
		}
		if blocked {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for blocked SQL matching %q", pattern)
		case <-ticker.C:
		}
	}
}
