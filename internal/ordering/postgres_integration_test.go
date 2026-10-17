package ordering

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/catalog"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/tablesession"
	"github.com/nurockplayer/tojima-restaurant-platform/migrations/postgres"
)

type allowOrderingClose struct{}

func (allowOrderingClose) CheckClose(context.Context, pgx.Tx, tablesession.Session) error { return nil }

// p78TestCommitPolicy initializes only the state row needed by P-79 close
// safeguards. It is a test adapter, not production cash-settings policy.
type p78TestCommitPolicy struct{}

func (p78TestCommitPolicy) InitOrderTx(ctx context.Context, tx pgx.Tx, order CommittedOrder) error {
	status := "PENDING"
	var cashMode any
	if order.PaymentMethod == string(Cash) {
		status = "UNPAID"
		cashMode = "PREPAID"
	}
	_, err := tx.Exec(ctx, `INSERT INTO operation_order_state(order_id,session_id,tenant_id,store_id,table_id,generation,payment_method,payment_status,cash_mode,fulfillment_status,requires_staff_acceptance,staff_accepted,version)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'WAITING',$10,false,1)`, order.ID, order.SessionID, order.Scope.TenantID, order.Scope.StoreID, order.Scope.TableID, order.Generation, order.PaymentMethod, status, cashMode, order.RequiresStaffAcceptance)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO operation_order_events(order_id,actor_id,event_type,after_payment_status,after_fulfillment_status,state_version) VALUES($1,'p78-test','ORDER_CREATED',$2,'WAITING',1)`, order.ID, status)
	return err
}

type orderingFixture struct {
	pool     *pgxpool.Pool
	sessions *tablesession.Service
	catalog  *catalog.Service
	service  *Service
	session  tablesession.Session
	tokens   []string
}

func TestPostgresSharedCartAndOrderAcceptance(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	fixture := newOrderingFixture(t, ctx)
	firstToken, secondToken, thirdToken := fixture.tokens[0], fixture.tokens[1], fixture.tokens[2]

	t.Run("concurrent add CAS, idempotency, immutable order, and next generation", func(t *testing.T) {
		start := make(chan struct{})
		type mutationResult struct {
			input Mutation
			cart  Cart
			err   error
		}
		results := make(chan mutationResult, 2)
		for i, product := range []string{"tea", "soup"} {
			input := Mutation{SessionID: fixture.session.ID, GrantToken: fixture.tokens[i], ExpectedGeneration: 1, ExpectedVersion: 1,
				IdempotencyKey: fmt.Sprintf("add-%d", i), Kind: AddLine, ProductKey: product, Quantity: 1}
			if product == "tea" {
				input.Options = []catalog.OptionSelection{{GroupKey: "extras", OptionKey: "milk"}}
			}
			go func() {
				<-start
				cart, err := fixture.service.Mutate(ctx, input)
				results <- mutationResult{input: input, cart: cart, err: err}
			}()
		}
		close(start)
		first, second := <-results, <-results
		var loser mutationResult
		if first.err == nil && errors.Is(second.err, ErrConflict) {
			loser = second
		} else if second.err == nil && errors.Is(first.err, ErrConflict) {
			loser = first
		} else {
			t.Fatalf("expected one write and one CAS conflict: first=%v second=%v", first.err, second.err)
		}
		if len(loser.cart.Lines) != 1 || loser.cart.Version != 2 {
			t.Fatalf("conflict did not return latest cart: %+v", loser.cart)
		}
		// Retry with the returned version; the same key and payload can be
		// replayed later without duplicating a line.
		loser.input.ExpectedVersion = loser.cart.Version
		afterRetry, err := fixture.service.Mutate(ctx, loser.input)
		if err != nil || len(afterRetry.Lines) != 2 || afterRetry.Version != 3 {
			t.Fatalf("retry losing add: cart=%+v err=%v", afterRetry, err)
		}
		replayed, err := fixture.service.Mutate(ctx, loser.input)
		if err != nil || len(replayed.Lines) != 2 || replayed.Version != 3 {
			t.Fatalf("idempotent replay changed cart: cart=%+v err=%v", replayed, err)
		}
		changed := loser.input
		changed.Quantity = 2
		if _, err = fixture.service.Mutate(ctx, changed); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("same key with different payload returned %v", err)
		}

		stalePreview, err := fixture.service.Preview(ctx, fixture.session.ID, firstToken, 1, 3)
		if err != nil {
			t.Fatal(err)
		}
		line := afterRetry.Lines[0]
		updated, err := fixture.service.Mutate(ctx, Mutation{SessionID: fixture.session.ID, GrantToken: secondToken,
			ExpectedGeneration: 1, ExpectedVersion: 3, IdempotencyKey: "set-line", Kind: SetLine,
			LineID: line.LineID, Quantity: 2, Options: lineSelections(line.Options)})
		if err != nil || updated.Version != 4 {
			t.Fatalf("absolute quantity edit: cart=%+v err=%v", updated, err)
		}
		stale, err := fixture.service.Commit(ctx, CommitRequest{SessionID: fixture.session.ID, GrantToken: firstToken,
			ExpectedGeneration: 1, ExpectedVersion: 3, IdempotencyKey: "stale-commit", PreviewFingerprint: stalePreview.Fingerprint, PaymentMethod: Stripe})
		if !errors.Is(err, ErrConflict) || stale.Cart.Version != 4 {
			t.Fatalf("stale preview was not rejected with latest cart: result=%+v err=%v", stale, err)
		}
		// Store price, publication, availability generations, legal rule version,
		// and the JST civil-date boundary each invalidate an old confirmation.
		preview, err := fixture.service.Preview(ctx, fixture.session.ID, firstToken, 1, 4)
		if err != nil {
			t.Fatal(err)
		}
		override, err := fixture.catalog.SetStorePriceOverride(ctx, catalog.StoreScope{TenantID: "tenant", StoreID: "store"}, "manager", "tea", 150, 0)
		if err != nil {
			t.Fatal(err)
		}
		stale, err = fixture.service.Commit(ctx, CommitRequest{SessionID: fixture.session.ID, GrantToken: firstToken,
			ExpectedGeneration: 1, ExpectedVersion: 4, IdempotencyKey: "price-stale", PreviewFingerprint: preview.Fingerprint, PaymentMethod: Stripe})
		if !errors.Is(err, ErrRequote) || stale.CurrentPreview == nil {
			t.Fatalf("price change did not require reconfirmation: result=%+v err=%v", stale, err)
		}

		preview, err = fixture.service.Preview(ctx, fixture.session.ID, firstToken, 1, 4)
		if err != nil {
			t.Fatal(err)
		}
		override, err = fixture.catalog.SetStorePriceOverride(ctx, catalog.StoreScope{TenantID: "tenant", StoreID: "store"}, "manager", "tea", 175, override.Version)
		if err != nil {
			t.Fatal(err)
		}
		override, err = fixture.catalog.SetStorePriceOverride(ctx, catalog.StoreScope{TenantID: "tenant", StoreID: "store"}, "manager", "tea", 150, override.Version)
		if err != nil {
			t.Fatal(err)
		}
		stale, err = fixture.service.Commit(ctx, CommitRequest{SessionID: fixture.session.ID, GrantToken: firstToken,
			ExpectedGeneration: 1, ExpectedVersion: 4, IdempotencyKey: "reverted-price-stale", PreviewFingerprint: preview.Fingerprint, PaymentMethod: Cash})
		if !errors.Is(err, ErrRequote) || stale.CurrentPreview == nil || !reflect.DeepEqual(stale.CurrentPreview.Quote.Lines, preview.Quote.Lines) {
			t.Fatalf("price reversion without an amount change did not require reconfirmation: result=%+v err=%v", stale, err)
		}

		preview, err = fixture.service.Preview(ctx, fixture.session.ID, firstToken, 1, 4)
		if err != nil {
			t.Fatal(err)
		}
		draft, err := fixture.catalog.GetCompanyDraft(ctx, "tenant")
		if err != nil {
			t.Fatal(err)
		}
		for ci := range draft.Categories {
			for pi := range draft.Categories[ci].Products {
				if draft.Categories[ci].Products[pi].Key == "soup" {
					draft.Categories[ci].Products[pi].BasePriceMinor = 250
				}
			}
		}
		draft, err = fixture.catalog.SaveCompanyDraft(ctx, "tenant", "owner", draft.Version, draft.CatalogData)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.catalog.PublishCompanyDraft(ctx, "tenant", "owner", draft.Version); err != nil {
			t.Fatal(err)
		}
		stale, err = fixture.service.Commit(ctx, CommitRequest{SessionID: fixture.session.ID, GrantToken: secondToken,
			ExpectedGeneration: 1, ExpectedVersion: 4, IdempotencyKey: "publication-stale", PreviewFingerprint: preview.Fingerprint, PaymentMethod: Stripe})
		if !errors.Is(err, ErrRequote) || stale.CurrentPreview == nil {
			t.Fatalf("published revision change did not require reconfirmation: result=%+v err=%v", stale, err)
		}

		preview, err = fixture.service.Preview(ctx, fixture.session.ID, firstToken, 1, 4)
		if err != nil {
			t.Fatal(err)
		}
		availabilityScope := catalog.StoreScope{TenantID: "tenant", StoreID: "store"}
		if _, err = fixture.catalog.SetStoreAvailability(ctx, availabilityScope, "manager", catalog.AvailabilityChange{Target: catalog.AvailabilityTarget{Kind: catalog.ProductAvailability, ProductKey: "tea"}, Status: catalog.Paused, Reason: "test pause", ExpectedVersion: 2}); err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.catalog.SetStoreAvailability(ctx, availabilityScope, "manager", catalog.AvailabilityChange{Target: catalog.AvailabilityTarget{Kind: catalog.ProductAvailability, ProductKey: "tea"}, Status: catalog.Available, ExpectedVersion: 3}); err != nil {
			t.Fatal(err)
		}
		stale, err = fixture.service.Commit(ctx, CommitRequest{SessionID: fixture.session.ID, GrantToken: firstToken,
			ExpectedGeneration: 1, ExpectedVersion: 4, IdempotencyKey: "availability-stale", PreviewFingerprint: preview.Fingerprint, PaymentMethod: Cash})
		if !errors.Is(err, ErrRequote) || stale.CurrentPreview == nil {
			t.Fatalf("availability generation change did not require reconfirmation: result=%+v err=%v", stale, err)
		}

		preview, err = fixture.service.Preview(ctx, fixture.session.ID, firstToken, 1, 4)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.pool.Exec(ctx, `UPDATE tax_rate_schedule SET rule_version='test-schedule-v2' WHERE tax_rule_id='jp-standard-10-2019-10-01'`); err != nil {
			t.Fatal(err)
		}
		stale, err = fixture.service.Commit(ctx, CommitRequest{SessionID: fixture.session.ID, GrantToken: firstToken,
			ExpectedGeneration: 1, ExpectedVersion: 4, IdempotencyKey: "tax-stale", PreviewFingerprint: preview.Fingerprint, PaymentMethod: Stripe})
		if !errors.Is(err, ErrRequote) || stale.CurrentPreview == nil {
			t.Fatalf("tax schedule change did not require reconfirmation: result=%+v err=%v", stale, err)
		}

		originalNow := fixture.service.now
		boundary := time.Now().UTC().In(time.FixedZone("JST", 9*60*60)).Truncate(24 * time.Hour).Add(15 * time.Hour)
		fixture.service.now = atCall(originalNow, 2, boundary.Add(-time.Second))
		preview, err = fixture.service.Preview(ctx, fixture.session.ID, firstToken, 1, 4)
		fixture.service.now = originalNow
		if err != nil {
			t.Fatal(err)
		}
		fixture.service.now = atCall(originalNow, 2, boundary.Add(time.Second))
		stale, err = fixture.service.Commit(ctx, CommitRequest{SessionID: fixture.session.ID, GrantToken: firstToken,
			ExpectedGeneration: 1, ExpectedVersion: 4, IdempotencyKey: "jst-date-stale", PreviewFingerprint: preview.Fingerprint, PaymentMethod: Stripe})
		fixture.service.now = originalNow
		if !errors.Is(err, ErrRequote) || stale.CurrentPreview == nil {
			t.Fatalf("JST date change did not require reconfirmation: result=%+v err=%v", stale, err)
		}

		preview, err = fixture.service.Preview(ctx, fixture.session.ID, firstToken, 1, 4)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.pool.Exec(ctx, `CREATE FUNCTION test_reject_order_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'forced ordering rollback test'; END $$;
			CREATE TRIGGER test_reject_order_insert BEFORE INSERT ON table_orders FOR EACH ROW EXECUTE FUNCTION test_reject_order_insert()`); err != nil {
			t.Fatal(err)
		}
		_, rollbackErr := fixture.service.Commit(ctx, CommitRequest{SessionID: fixture.session.ID, GrantToken: firstToken,
			ExpectedGeneration: 1, ExpectedVersion: 4, IdempotencyKey: "forced-rollback", PreviewFingerprint: preview.Fingerprint, PaymentMethod: Cash})
		if rollbackErr == nil {
			t.Fatal("forced order-write failure unexpectedly committed")
		}
		if _, err = fixture.pool.Exec(ctx, `DROP TRIGGER test_reject_order_insert ON table_orders; DROP FUNCTION test_reject_order_insert()`); err != nil {
			t.Fatal(err)
		}
		unchanged, err := fixture.service.ReadCart(ctx, fixture.session.ID, thirdToken)
		if err != nil || unchanged.Generation != 1 || unchanged.Version != 4 || len(unchanged.Lines) != 2 {
			t.Fatalf("failed order transaction changed cart: cart=%+v err=%v", unchanged, err)
		}
		ordersBeforeCommit, err := fixture.service.ListOrders(ctx, fixture.session.ID, thirdToken)
		if err != nil || len(ordersBeforeCommit) != 0 {
			t.Fatalf("failed order transaction left an order: orders=%+v err=%v", ordersBeforeCommit, err)
		}
		start = make(chan struct{})
		commits := make(chan struct {
			result CommitResult
			err    error
		}, 2)
		requests := make([]CommitRequest, 2)
		for i, method := range []PaymentMethod{Stripe, Cash} {
			request := CommitRequest{SessionID: fixture.session.ID, GrantToken: fixture.tokens[i], ExpectedGeneration: 1,
				ExpectedVersion: 4, IdempotencyKey: fmt.Sprintf("commit-%d", i), PreviewFingerprint: preview.Fingerprint, PaymentMethod: method}
			requests[i] = request
			go func() {
				<-start
				result, err := fixture.service.Commit(ctx, request)
				commits <- struct {
					result CommitResult
					err    error
				}{result, err}
			}()
		}
		close(start)
		left, right := <-commits, <-commits
		if left.err != nil || right.err != nil {
			t.Fatalf("competing commits failed: left=%v right=%v", left.err, right.err)
		}
		if left.result.Order.ID != right.result.Order.ID || left.result.Order.PaymentMethod != right.result.Order.PaymentMethod {
			t.Fatalf("committers did not converge on one winner: left=%+v right=%+v", left.result, right.result)
		}
		if !((left.result.Outcome == Committed && right.result.Outcome == AlreadyCommitted) || (right.result.Outcome == Committed && left.result.Outcome == AlreadyCommitted)) {
			t.Fatalf("unexpected commit outcomes: %s / %s", left.result.Outcome, right.result.Outcome)
		}
		replay, err := fixture.service.Commit(ctx, requests[0])
		if err != nil || replay.Order.ID != left.result.Order.ID || replay.Outcome != AlreadyCommitted {
			t.Fatalf("winning idempotent retry changed the order: result=%+v err=%v", replay, err)
		}
		mismatchedCommit := requests[0]
		if mismatchedCommit.PaymentMethod == Stripe {
			mismatchedCommit.PaymentMethod = Cash
		} else {
			mismatchedCommit.PaymentMethod = Stripe
		}
		if _, err = fixture.service.Commit(ctx, mismatchedCommit); !errors.Is(err, ErrIdempotencyConflict) {
			t.Fatalf("committed idempotency key accepted different payment method: %v", err)
		}
		if left.result.Cart.Generation != 2 || right.result.Cart.Generation != 2 || len(left.result.Cart.Lines) != 0 || len(right.result.Cart.Lines) != 0 {
			t.Fatalf("commit did not atomically start an empty next generation: left=%+v right=%+v", left.result.Cart, right.result.Cart)
		}
		order := left.result.Order
		if order.RequiresStaffAcceptance || len(order.Quote.Lines) != 2 || order.Quote.Tax.Total().GrossJPY == 0 || order.CatalogID == 0 {
			t.Fatalf("immutable quote/provenance incomplete: %+v", order)
		}
		if _, err = fixture.pool.Exec(ctx, `UPDATE table_orders SET payment_method='CASH' WHERE order_id=$1`, order.ID); err == nil {
			t.Fatal("database allowed committed order mutation")
		}
		if _, err = fixture.pool.Exec(ctx, `DELETE FROM table_orders WHERE order_id=$1`, order.ID); err == nil {
			t.Fatal("database allowed committed order deletion")
		}
		if _, err = fixture.pool.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES ('other-tenant','Other');
			INSERT INTO stores(tenant_id,store_id,name) VALUES ('other-tenant','other-store','Other');
			INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES ('other-tenant','other-store','other-table','Other','other-locator')`); err != nil {
			t.Fatal(err)
		}
		_, malformedErr := fixture.pool.Exec(ctx, `INSERT INTO table_orders(order_id,session_id,tenant_id,store_id,table_id,generation,payment_method,requires_staff_acceptance,catalog_id,catalog_mode,catalog_revision,quote_snapshot,tax_snapshot,total_gross_jpy)
			VALUES ('ffffffffffffffffffffffffffffffff',$1,'other-tenant','other-store','other-table',99,'CASH',false,$2,'LINKED',1,'{}','{}',0)`, order.SessionID, order.CatalogID)
		var pgErr *pgconn.PgError
		if !errors.As(malformedErr, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != "table_orders_session_full_scope_fk" {
			t.Fatalf("database accepted or failed on wrong constraint for cross-scope order: %v", malformedErr)
		}
		// A later store price override cannot rewrite the historical quote.
		if _, err = fixture.catalog.SetStorePriceOverride(ctx, catalog.StoreScope{TenantID: "tenant", StoreID: "store"}, "manager", "soup", 999, 0); err != nil {
			t.Fatal(err)
		}
		orders, err := fixture.service.ListOrders(ctx, fixture.session.ID, thirdToken)
		if err != nil || len(orders) != 1 || orders[0].ID != order.ID || len(orders[0].Quote.Lines) != 2 {
			t.Fatalf("list full order snapshot: orders=%+v err=%v", orders, err)
		}
		foundTea, foundSoup := false, false
		for _, quoted := range orders[0].Quote.Lines {
			if quoted.ProductKey == "tea" && quoted.BaseGrossJPY == 150 {
				foundTea = true
				if len(quoted.Options) != 1 || quoted.Options[0].Name != "Milk" || quoted.Options[0].PriceGrossJPY != 25 {
					t.Fatalf("server option snapshot incorrect: %+v", quoted.Options)
				}
			}
			if quoted.ProductKey == "soup" && quoted.BaseGrossJPY == 250 {
				foundSoup = true
			}
		}
		if !foundTea || !foundSoup {
			t.Fatalf("order did not preserve published/override prices: %+v", orders[0].Quote.Lines)
		}
	})

	t.Run("unavailable retained line remains removable and stale grant is denied", func(t *testing.T) {
		cart, err := fixture.service.ReadCart(ctx, fixture.session.ID, firstToken)
		if err != nil || cart.Generation != 2 || cart.Version != 1 || len(cart.Lines) != 0 {
			t.Fatalf("new cart: cart=%+v err=%v", cart, err)
		}
		cart, err = fixture.service.Mutate(ctx, Mutation{SessionID: fixture.session.ID, GrantToken: firstToken, ExpectedGeneration: 2,
			ExpectedVersion: 1, IdempotencyKey: "add-paused", Kind: AddLine, ProductKey: "tea", Quantity: 1})
		if err != nil || len(cart.Lines) != 1 {
			t.Fatalf("add next-generation line: cart=%+v err=%v", cart, err)
		}
		line := cart.Lines[0]
		start := make(chan struct{})
		type raceResult struct {
			cart Cart
			err  error
		}
		results := make(chan raceResult, 2)
		go func() {
			<-start
			updated, updateErr := fixture.service.Mutate(ctx, Mutation{SessionID: fixture.session.ID, GrantToken: firstToken,
				ExpectedGeneration: 2, ExpectedVersion: cart.Version, IdempotencyKey: "race-quantity", Kind: SetLine,
				LineID: line.LineID, Quantity: 2, Options: lineSelections(line.Options)})
			results <- raceResult{updated, updateErr}
		}()
		go func() {
			<-start
			removed, removeErr := fixture.service.Mutate(ctx, Mutation{SessionID: fixture.session.ID, GrantToken: secondToken,
				ExpectedGeneration: 2, ExpectedVersion: cart.Version, IdempotencyKey: "race-remove", Kind: RemoveLine, LineID: line.LineID})
			results <- raceResult{removed, removeErr}
		}()
		close(start)
		left, right := <-results, <-results
		var raceWinner, raceLoser raceResult
		if left.err == nil && errors.Is(right.err, ErrConflict) {
			raceWinner, raceLoser = left, right
		} else if right.err == nil && errors.Is(left.err, ErrConflict) {
			raceWinner, raceLoser = right, left
		} else {
			t.Fatalf("remove/quantity race did not produce one CAS winner: left=%v right=%v", left.err, right.err)
		}
		if len(raceWinner.cart.Lines) == 0 && len(raceLoser.cart.Lines) != 0 {
			t.Fatalf("stale quantity response resurrected removed line: loser=%+v", raceLoser.cart)
		}
		cart = raceWinner.cart
		if len(cart.Lines) == 0 {
			cart, err = fixture.service.Mutate(ctx, Mutation{SessionID: fixture.session.ID, GrantToken: thirdToken, ExpectedGeneration: 2,
				ExpectedVersion: cart.Version, IdempotencyKey: "add-paused-again", Kind: AddLine, ProductKey: "tea", Quantity: 1})
			if err != nil || len(cart.Lines) != 1 {
				t.Fatalf("re-add after removal race: cart=%+v err=%v", cart, err)
			}
		}
		if _, err = fixture.catalog.SetStoreAvailability(ctx, catalog.StoreScope{TenantID: "tenant", StoreID: "store"}, "manager", catalog.AvailabilityChange{
			Target: catalog.AvailabilityTarget{Kind: catalog.ProductAvailability, ProductKey: "tea"}, Status: catalog.Paused, Reason: "test pause", ExpectedVersion: 4}); err != nil {
			t.Fatal(err)
		}
		retained, err := fixture.service.ReadCart(ctx, fixture.session.ID, secondToken)
		if err != nil || len(retained.Lines) != 1 || retained.Lines[0].ProductName != "Tea" || retained.Lines[0].ProductAvailable || retained.Lines[0].UnavailableCode != "PRODUCT_UNAVAILABLE" {
			t.Fatalf("unavailable line was removed or misreported: cart=%+v err=%v", retained, err)
		}
		if _, err = fixture.service.Preview(ctx, fixture.session.ID, firstToken, 2, retained.Version); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("unavailable line preview returned %v", err)
		}
		removed, err := fixture.service.Mutate(ctx, Mutation{SessionID: fixture.session.ID, GrantToken: thirdToken, ExpectedGeneration: 2,
			ExpectedVersion: retained.Version, IdempotencyKey: "remove-paused", Kind: RemoveLine, LineID: retained.Lines[0].LineID})
		if err != nil || len(removed.Lines) != 0 {
			t.Fatalf("unavailable line could not be removed: cart=%+v err=%v", removed, err)
		}
		if _, err = fixture.pool.Exec(ctx, `INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES ('tenant','store','other-table','Other','other-table-locator')`); err != nil {
			t.Fatal(err)
		}
		other, err := fixture.sessions.Open(ctx, tablesession.Scope{TenantID: "tenant", StoreID: "store", TableID: "other-table"}, "staff")
		if err != nil {
			t.Fatal(err)
		}
		otherGrant, err := fixture.sessions.Join(ctx, other.Session.ID, other.Session.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.service.ReadCart(ctx, fixture.session.ID, otherGrant.Token); !errors.Is(err, ErrDenied) {
			t.Fatalf("cross-table grant read cart: %v", err)
		}
		expiredHash := sha256.Sum256([]byte(firstToken))
		if _, err = fixture.pool.Exec(ctx, `UPDATE table_join_grants SET expires_at=clock_timestamp()-interval '1 second' WHERE token_hash=$1`, expiredHash[:]); err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.service.ReadCart(ctx, fixture.session.ID, firstToken); !errors.Is(err, ErrDenied) {
			t.Fatalf("expired table grant read cart: %v", err)
		}
		if err = fixture.sessions.Close(ctx, tablesession.Scope{TenantID: "tenant", StoreID: "store", TableID: "table"}, fixture.session.ID, "staff"); err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.service.ReadCart(ctx, fixture.session.ID, firstToken); !errors.Is(err, ErrDenied) {
			t.Fatalf("closed session grant remained usable: %v", err)
		}
		newSeating, err := fixture.sessions.Open(ctx, tablesession.Scope{TenantID: "tenant", StoreID: "store", TableID: "table"}, "staff")
		if err != nil || newSeating.Session.ID == fixture.session.ID {
			t.Fatalf("new seating reused old session: old=%s new=%s err=%v", fixture.session.ID, newSeating.Session.ID, err)
		}
		newGrant, err := fixture.sessions.Join(ctx, newSeating.Session.ID, newSeating.Session.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.service.ReadCart(ctx, newSeating.Session.ID, secondToken); !errors.Is(err, ErrDenied) {
			t.Fatalf("old grant read next seating: %v", err)
		}
		if cart, err := fixture.service.ReadCart(ctx, newSeating.Session.ID, newGrant.Token); err != nil || cart.Generation != 1 || len(cart.Lines) != 0 {
			t.Fatalf("new seating did not get isolated cart: cart=%+v err=%v", cart, err)
		}
	})

	t.Run("PIN reset revokes every old cart grant", func(t *testing.T) {
		if _, err := fixture.pool.Exec(ctx, `INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES ('tenant','store','pin-table','PIN','pin-locator');
			UPDATE stores SET qr_entry_mode='SESSION_PIN' WHERE tenant_id='tenant' AND store_id='store'`); err != nil {
			t.Fatal(err)
		}
		opened, err := fixture.sessions.Open(ctx, tablesession.Scope{TenantID: "tenant", StoreID: "store", TableID: "pin-table"}, "staff")
		if err != nil || opened.PIN == "" {
			t.Fatalf("PIN session open=%+v err=%v", opened, err)
		}
		grant, err := fixture.sessions.Join(ctx, opened.Session.ID, opened.Session.ID, opened.PIN)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.service.ReadCart(ctx, opened.Session.ID, grant.Token); err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.sessions.ResetPIN(ctx, tablesession.Scope{TenantID: "tenant", StoreID: "store", TableID: "pin-table"}, opened.Session.ID, "staff"); err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.service.ReadCart(ctx, opened.Session.ID, grant.Token); !errors.Is(err, ErrDenied) {
			t.Fatalf("PIN reset left old cart grant usable: %v", err)
		}
	})

	t.Run("grant expiry during cart lock wait is rechecked", func(t *testing.T) {
		if _, err := fixture.pool.Exec(ctx, `INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES ('tenant','store','wait-table','Wait','wait-locator');
			UPDATE stores SET qr_entry_mode='STAFF_OPEN' WHERE tenant_id='tenant' AND store_id='store'`); err != nil {
			t.Fatal(err)
		}
		opened, err := fixture.sessions.Open(ctx, tablesession.Scope{TenantID: "tenant", StoreID: "store", TableID: "wait-table"}, "staff")
		if err != nil {
			t.Fatal(err)
		}
		grant, err := fixture.sessions.Join(ctx, opened.Session.ID, opened.Session.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.service.Mutate(ctx, Mutation{SessionID: opened.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: 1,
			IdempotencyKey: "wait-seed", Kind: AddLine, ProductKey: "soup", Quantity: 1}); err != nil {
			t.Fatal(err)
		}
		grantHash := sha256.Sum256([]byte(grant.Token))
		if _, err = fixture.pool.Exec(ctx, `UPDATE table_join_grants SET expires_at=clock_timestamp()+interval '1500 milliseconds' WHERE token_hash=$1`, grantHash[:]); err != nil {
			t.Fatal(err)
		}

		cartLockTx, err := fixture.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = cartLockTx.Rollback(context.Background()) }()
		var lockedSessionID string
		if err = cartLockTx.QueryRow(ctx, `SELECT session_id FROM table_carts WHERE session_id=$1 FOR UPDATE`, opened.Session.ID).Scan(&lockedSessionID); err != nil {
			t.Fatal(err)
		}
		readResult := make(chan error, 1)
		go func() {
			_, readErr := fixture.service.ReadCart(ctx, opened.Session.ID, grant.Token)
			readResult <- readErr
		}()

		deadline := time.Now().Add(5 * time.Second)
		waiting := false
		for time.Now().Before(deadline) {
			var count int
			if err = fixture.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
				WHERE datname=current_database() AND state='active' AND wait_event_type='Lock'
				AND query LIKE '%FROM table_carts WHERE session_id=%FOR UPDATE%'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count > 0 {
				waiting = true
				break
			}
			select {
			case err = <-readResult:
				t.Fatalf("read completed before cart lock was released: %v", err)
			case <-time.After(10 * time.Millisecond):
			}
		}
		if !waiting {
			t.Fatal("cart read never reached the held row lock")
		}
		time.Sleep(1600 * time.Millisecond)
		if err = cartLockTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err = <-readResult; !errors.Is(err, ErrDenied) {
			t.Fatalf("grant that expired during cart lock wait was accepted: %v", err)
		}
	})

	t.Run("PIN reset during admission lock wait invalidates prechecked grant", func(t *testing.T) {
		if _, err := fixture.pool.Exec(ctx, `INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES ('tenant','store','reset-wait-table','Reset wait','reset-wait-locator');
			UPDATE stores SET qr_entry_mode='SESSION_PIN' WHERE tenant_id='tenant' AND store_id='store'`); err != nil {
			t.Fatal(err)
		}
		opened, err := fixture.sessions.Open(ctx, tablesession.Scope{TenantID: "tenant", StoreID: "store", TableID: "reset-wait-table"}, "staff")
		if err != nil || opened.PIN == "" {
			t.Fatalf("open PIN session: session=%+v err=%v", opened, err)
		}
		grant, err := fixture.sessions.Join(ctx, opened.Session.ID, opened.Session.ID, opened.PIN)
		if err != nil {
			t.Fatal(err)
		}
		blockTx, err := fixture.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = blockTx.Rollback(context.Background()) }()
		if _, err = blockTx.Exec(ctx, `LOCK TABLE tax_rate_schedule IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal(err)
		}
		readResult := make(chan error, 1)
		go func() {
			_, readErr := fixture.service.ReadCart(ctx, opened.Session.ID, grant.Token)
			readResult <- readErr
		}()
		deadline := time.Now().Add(5 * time.Second)
		waiting := false
		for time.Now().Before(deadline) {
			var count int
			if err = fixture.pool.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
				WHERE datname=current_database() AND state='active' AND wait_event_type='Lock'
				AND query LIKE '%LOCK TABLE tax_rate_schedule IN SHARE MODE%'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count > 0 {
				waiting = true
				break
			}
			select {
			case err = <-readResult:
				t.Fatalf("read completed before admission lock was released: %v", err)
			case <-time.After(10 * time.Millisecond):
			}
		}
		if !waiting {
			t.Fatal("cart read never reached the held catalog admission lock")
		}
		if _, err = fixture.sessions.ResetPIN(ctx, tablesession.Scope{TenantID: "tenant", StoreID: "store", TableID: "reset-wait-table"}, opened.Session.ID, "staff"); err != nil {
			t.Fatalf("reset PIN while request waits before session lock: %v", err)
		}
		if err = blockTx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if err = <-readResult; !errors.Is(err, ErrDenied) {
			t.Fatalf("prechecked grant remained valid after PIN reset during admission wait: %v", err)
		}
	})

	t.Run("direct scan acceptance snapshot is immutable on the order", func(t *testing.T) {
		if _, err := fixture.pool.Exec(ctx, `INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES ('tenant','store','direct-table','Direct','direct-locator');
			UPDATE stores SET qr_entry_mode='DIRECT_SCAN' WHERE tenant_id='tenant' AND store_id='store'`); err != nil {
			t.Fatal(err)
		}
		grant, err := fixture.sessions.DirectJoin(ctx, "direct-locator", "")
		if err != nil || !grant.Session.RequiresStaffAcceptance {
			t.Fatalf("direct join grant=%+v err=%v", grant, err)
		}
		cart, err := fixture.service.Mutate(ctx, Mutation{SessionID: grant.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: 1,
			IdempotencyKey: "direct-add", Kind: AddLine, ProductKey: "soup", Quantity: 1})
		if err != nil {
			t.Fatal(err)
		}
		preview, err := fixture.service.Preview(ctx, grant.Session.ID, grant.Token, cart.Generation, cart.Version)
		if err != nil {
			t.Fatal(err)
		}
		result, err := fixture.service.Commit(ctx, CommitRequest{SessionID: grant.Session.ID, GrantToken: grant.Token,
			ExpectedGeneration: cart.Generation, ExpectedVersion: cart.Version, IdempotencyKey: "direct-commit",
			PreviewFingerprint: preview.Fingerprint, PaymentMethod: Cash})
		if err != nil || !result.Order.RequiresStaffAcceptance {
			t.Fatalf("order lost direct-scan staff acceptance seam: order=%+v err=%v", result.Order, err)
		}
	})

	t.Run("catalog-sized option selections and aggregate cart cap", func(t *testing.T) {
		draft, err := fixture.catalog.GetCompanyDraft(ctx, "tenant")
		if err != nil {
			t.Fatal(err)
		}
		complexGroups := make([]catalog.OptionGroup, 0, 21)
		selections := make([]catalog.OptionSelection, 0, 121)
		for groupIndex := 0; groupIndex < 20; groupIndex++ {
			groupKey := fmt.Sprintf("required-%02d", groupIndex)
			complexGroups = append(complexGroups, catalog.OptionGroup{Key: groupKey, Name: "Required", MinSelect: 1, MaxSelect: 1,
				Options: []catalog.Option{{Key: "only", Name: "Only", Active: true}}})
			selections = append(selections, catalog.OptionSelection{GroupKey: groupKey, OptionKey: "only"})
		}
		largeOptions := make([]catalog.Option, 101)
		for i := range largeOptions {
			key := fmt.Sprintf("pick-%03d", i)
			largeOptions[i] = catalog.Option{Key: key, Name: "Choice", Active: true}
			selections = append(selections, catalog.OptionSelection{GroupKey: "many", OptionKey: key})
		}
		complexGroups = append(complexGroups, catalog.OptionGroup{Key: "many", Name: "Many choices", MinSelect: 101, MaxSelect: 101, Options: largeOptions})
		bulkOptions := make([]catalog.Option, 100)
		bulkSelections := make([]catalog.OptionSelection, 50)
		for i := range bulkOptions {
			key := fmt.Sprintf("bulk-%03d", i)
			bulkOptions[i] = catalog.Option{Key: key, Name: "Bulk choice", Active: true}
			if i < len(bulkSelections) {
				bulkSelections[i] = catalog.OptionSelection{GroupKey: "bulk-group", OptionKey: key}
			}
		}
		for i := range draft.Categories {
			if draft.Categories[i].Key == "meals" {
				draft.Categories[i].Products = append(draft.Categories[i].Products,
					catalog.Product{Key: "complex", Name: "Complex", BasePriceMinor: 100, Active: true, Groups: complexGroups},
					catalog.Product{Key: "bulk", Name: "Bulk", BasePriceMinor: 100, Active: true, Groups: []catalog.OptionGroup{{Key: "bulk-group", Name: "Bulk choices", MinSelect: 0, MaxSelect: 100, Options: bulkOptions}}})
			}
		}
		draft, err = fixture.catalog.SaveCompanyDraft(ctx, "tenant", "owner", draft.Version, draft.CatalogData)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.catalog.PublishCompanyDraft(ctx, "tenant", "owner", draft.Version); err != nil {
			t.Fatal(err)
		}
		if _, err = fixture.pool.Exec(ctx, `UPDATE stores SET qr_entry_mode='STAFF_OPEN' WHERE tenant_id='tenant' AND store_id='store';
			UPDATE store_product_availability SET status='AVAILABLE',updated_by='fixture',version=version+1 WHERE tenant_id='tenant' AND store_id='store' AND product_key IN ('complex','bulk');
			UPDATE store_option_availability SET status='AVAILABLE',updated_by='fixture',version=version+1 WHERE tenant_id='tenant' AND store_id='store' AND product_key IN ('complex','bulk')`); err != nil {
			t.Fatal(err)
		}
		if len(selections) <= 100 {
			t.Fatalf("fixture should exercise more than 100 selections; got %d", len(selections))
		}
		if _, err = fixture.pool.Exec(ctx, `INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES ('tenant','store','capacity-table','Capacity','capacity-locator')`); err != nil {
			t.Fatal(err)
		}
		session, err := fixture.sessions.Open(ctx, tablesession.Scope{TenantID: "tenant", StoreID: "store", TableID: "capacity-table"}, "staff")
		if err != nil {
			t.Fatal(err)
		}
		grant, err := fixture.sessions.Join(ctx, session.Session.ID, session.Session.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		cart, err := fixture.service.Mutate(ctx, Mutation{SessionID: session.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: 1,
			IdempotencyKey: "add-complex", Kind: AddLine, ProductKey: "complex", Quantity: 1, Options: selections})
		complexOptionCount := 0
		if len(cart.Lines) > 0 {
			complexOptionCount = len(cart.Lines[0].Options)
		}
		if err != nil || len(cart.Lines) != 1 || complexOptionCount != 121 {
			t.Fatalf("required groups plus 101-choice multiselect: cart options=%d err=%v", complexOptionCount, err)
		}
		cart, err = fixture.service.Mutate(ctx, Mutation{SessionID: session.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: cart.Version,
			IdempotencyKey: "remove-complex", Kind: RemoveLine, LineID: cart.Lines[0].LineID})
		if err != nil || len(cart.Lines) != 0 {
			t.Fatalf("remove large selection fixture: cart=%+v err=%v", cart, err)
		}
		for i := 0; i < MaxCartLines; i++ {
			cart, err = fixture.service.Mutate(ctx, Mutation{SessionID: session.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: cart.Version,
				IdempotencyKey: fmt.Sprintf("bulk-%03d", i), Kind: AddLine, ProductKey: "bulk", Quantity: 1, Options: bulkSelections})
			if err != nil {
				t.Fatalf("add bulk line %d: %v", i, err)
			}
		}
		if optionCount := cartOptionCount(cart); optionCount != MaxCartOptions {
			t.Fatalf("aggregate cart options=%d, want cap %d", optionCount, MaxCartOptions)
		}
		overCap := make([]catalog.OptionSelection, 51)
		copy(overCap, bulkSelections)
		for i := 50; i < len(overCap); i++ {
			overCap[i] = catalog.OptionSelection{GroupKey: "bulk-group", OptionKey: fmt.Sprintf("bulk-%03d", i)}
		}
		unchanged, err := fixture.service.Mutate(ctx, Mutation{SessionID: session.Session.ID, GrantToken: grant.Token, ExpectedGeneration: 1, ExpectedVersion: cart.Version,
			IdempotencyKey: "exceed-cart-option-cap", Kind: SetLine, LineID: cart.Lines[0].LineID, Quantity: 1, Options: overCap})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("cart option cap overflow returned %v, want ErrInvalid", err)
		}
		persisted, readErr := fixture.service.ReadCart(ctx, session.Session.ID, grant.Token)
		if readErr != nil || persisted.Version != cart.Version || len(persisted.Lines) != MaxCartLines || cartOptionCount(persisted) != MaxCartOptions {
			t.Fatalf("over-cap replacement partially mutated cart: returned=%+v persisted=%+v err=%v readErr=%v", unchanged, persisted, err, readErr)
		}
	})
}

func TestPostgresOrderingMigrationDownUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, schema, pool := newOrderingDatabase(t, ctx)
	defer closeOrderingDatabase(t, ctx, admin, schema, pool)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = postgres.Down(ctx, conn.Conn()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	if err = postgres.Up(ctx, conn.Conn()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	if err = postgres.Down(ctx, conn.Conn()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	var absent bool
	if err = conn.QueryRow(ctx, `SELECT to_regclass('table_orders') IS NULL AND to_regclass('table_carts') IS NULL`).Scan(&absent); err != nil || !absent {
		conn.Release()
		t.Fatalf("down migration left P78 tables: absent=%v err=%v", absent, err)
	}
	if err = postgres.Reapply(ctx, conn.Conn()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()
}

func newOrderingFixture(t *testing.T, ctx context.Context) *orderingFixture {
	t.Helper()
	admin, schema, pool := newOrderingDatabase(t, ctx)
	t.Cleanup(func() { closeOrderingDatabase(t, ctx, admin, schema, pool) })
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = postgres.Up(ctx, conn.Conn()); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()
	if _, err = pool.Exec(ctx, `INSERT INTO tenants(tenant_id,name) VALUES ('tenant','Tenant');
		INSERT INTO stores(tenant_id,store_id,name) VALUES ('tenant','store','Store');
		INSERT INTO dining_tables(tenant_id,store_id,table_id,name,public_locator) VALUES ('tenant','store','table','Table','table-locator');`); err != nil {
		t.Fatal(err)
	}
	menuService, err := catalog.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	data := catalog.CatalogData{Categories: []catalog.Category{{Key: "meals", Name: "Meals", Products: []catalog.Product{
		{Key: "tea", Name: "Tea", BasePriceMinor: 100, Active: true, Groups: []catalog.OptionGroup{{Key: "extras", Name: "Extras", MinSelect: 0, MaxSelect: 1,
			Options: []catalog.Option{{Key: "milk", Name: "Milk", PriceDeltaMinor: 25, Active: true}}}}},
		{Key: "soup", Name: "Soup", BasePriceMinor: 200, Active: true},
	}}}}
	draft, err := menuService.SaveCompanyDraft(ctx, "tenant", "owner", 0, data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = menuService.PublishCompanyDraft(ctx, "tenant", "owner", draft.Version); err != nil {
		t.Fatal(err)
	}
	scope := catalog.StoreScope{TenantID: "tenant", StoreID: "store"}
	for _, key := range []string{"tea", "soup"} {
		if _, err = menuService.SetStoreAvailability(ctx, scope, "manager", catalog.AvailabilityChange{
			Target: catalog.AvailabilityTarget{Kind: catalog.ProductAvailability, ProductKey: key}, Status: catalog.Available, ExpectedVersion: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = menuService.SetStoreAvailability(ctx, scope, "manager", catalog.AvailabilityChange{
		Target: catalog.AvailabilityTarget{Kind: catalog.OptionAvailability, ProductKey: "tea", GroupKey: "extras", OptionKey: "milk"}, Status: catalog.Available, ExpectedVersion: 1}); err != nil {
		t.Fatal(err)
	}
	sessionService, err := tablesession.New(pool, tablesession.Options{
		SessionTTL: 2 * time.Hour, GrantTTL: 2 * time.Hour, GrantWindow: time.Hour, GrantWindowMax: 100,
		PINTTL: time.Hour, PINLockout: time.Minute, PINMaxAttempts: 5, PINLength: 6, PINPepper: []byte("0123456789abcdef0123456789abcdef"),
	}, allowOrderingClose{})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := sessionService.Open(ctx, tablesession.Scope{TenantID: "tenant", StoreID: "store", TableID: "table"}, "staff")
	if err != nil {
		t.Fatal(err)
	}
	tokens := make([]string, 3)
	for i := range tokens {
		grant, joinErr := sessionService.Join(ctx, opened.Session.ID, opened.Session.ID, "")
		if joinErr != nil {
			t.Fatal(joinErr)
		}
		tokens[i] = grant.Token
	}
	service, err := NewWithOrderPolicy(pool, sessionService, menuService, p78TestCommitPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	return &orderingFixture{pool: pool, sessions: sessionService, catalog: menuService, service: service, session: opened.Session, tokens: tokens}
}

func newOrderingDatabase(t *testing.T, ctx context.Context) (*pgx.Conn, string, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL ordering integration tests")
	}
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var random [8]byte
	if _, err = rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	schema := "p78_ordering_" + hex.EncodeToString(random[:])
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close(ctx)
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(ctx)
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close(ctx)
		t.Fatal(err)
	}
	return admin, schema, pool
}

func closeOrderingDatabase(t *testing.T, ctx context.Context, admin *pgx.Conn, schema string, pool *pgxpool.Pool) {
	t.Helper()
	pool.Close()
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE"); err != nil {
		t.Errorf("drop owned P78 schema: %v", err)
	}
	if err := admin.Close(cleanupCtx); err != nil {
		t.Errorf("close P78 test admin connection: %v", err)
	}
}

func atCall(original func(context.Context, pgx.Tx) (time.Time, error), target int, replacement time.Time) func(context.Context, pgx.Tx) (time.Time, error) {
	calls := 0
	return func(ctx context.Context, tx pgx.Tx) (time.Time, error) {
		calls++
		if calls == target {
			return replacement, nil
		}
		return original(ctx, tx)
	}
}
