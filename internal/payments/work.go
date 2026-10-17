package payments

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/operations"
	"github.com/nurockplayer/tojima-restaurant-platform/internal/stripeprovider"
)

type workItem struct {
	ID, Lease, Kind, OrderID string
	WebhookID                *int64
}

func (s *Service) processNext(ctx context.Context) (bool, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var item workItem
	err = tx.QueryRow(ctx, `SELECT work_id,work_type,coalesce(order_id,''),webhook_row_id FROM stripe_payment_work
		WHERE (status='READY' AND available_at<=clock_timestamp()) OR (status='LEASED' AND lease_until<=clock_timestamp())
		ORDER BY available_at,created_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&item.ID, &item.Kind, &item.OrderID, &item.WebhookID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	item.Lease, err = s.randomID()
	if err != nil {
		return false, err
	}
	_, err = tx.Exec(ctx, `UPDATE stripe_payment_work SET status='LEASED',lease_token=$2,lease_until=clock_timestamp()+$3::interval,attempts=attempts+1,updated_at=clock_timestamp() WHERE work_id=$1`, item.ID, item.Lease, intervalString(s.cfg.WorkLeaseTTL))
	if err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	switch item.Kind {
	case "CREATE":
		err = s.processCreate(ctx, item)
	case "WEBHOOK":
		err = s.processWebhook(ctx, item)
	case "EXPIRE":
		err = s.processExpire(ctx, item)
	default:
		err = ErrInvalid
	}
	return true, err
}

func intervalString(d time.Duration) string { return d.String() }

type createRow struct {
	order, attempt, session, tenant, store, table, mode, account, credential, api, key string
	profile, digest                                                                    []byte
	amount, generation                                                                 int64
	retry                                                                              time.Time
	checkout, paymentIntent                                                            string
}

func (s *Service) processCreate(ctx context.Context, w workItem) error {
	var a createRow
	err := s.db.QueryRow(ctx, `SELECT order_id,attempt_id,session_id,tenant_id,store_id,table_id,provider_mode,stripe_account_id,credential_ref,api_version,idempotency_key,request_profile_fingerprint,request_digest,amount_jpy,generation,retry_not_after,coalesce(checkout_session_id,''),coalesce(payment_intent_id,'')
		FROM stripe_payment_attempts WHERE order_id=$1`, w.OrderID).Scan(&a.order, &a.attempt, &a.session, &a.tenant, &a.store, &a.table, &a.mode, &a.account, &a.credential, &a.api, &a.key, &a.profile, &a.digest, &a.amount, &a.generation, &a.retry, &a.checkout, &a.paymentIntent)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.finishWork(ctx, w, false, 0, "MISSING_ATTEMPT")
	}
	if err != nil {
		return err
	}
	resolved, resolveErr := s.resolve(ctx, a.credential, a.account, a.mode)
	if resolveErr != nil || a.api != stripeprovider.APIVersion || !bytesEqual(a.profile, resolved.ProfileFingerprint[:]) {
		if e := s.setAttemptUnknown(ctx, w, a.order, "UNCONFIGURED_OR_PROFILE_CHANGED"); e != nil {
			return e
		}
		return s.finishWork(ctx, w, false, 0, "UNCONFIGURED_OR_PROFILE_CHANGED")
	}
	var result stripeprovider.CheckoutResult
	if a.checkout != "" {
		result, err = resolved.Provider.RetrieveCheckout(ctx, resolved.Account, a.checkout)
	} else {
		var now time.Time
		if err = s.db.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
			return err
		}
		if !now.Before(a.retry) {
			if e := s.setAttemptUnknown(ctx, w, a.order, "RETRY_WINDOW_ELAPSED"); e != nil {
				return e
			}
			return s.finishWork(ctx, w, false, 0, "RETRY_WINDOW_ELAPSED")
		}
		if !s.orderStillAdmissible(ctx, a) {
			if e := s.updateAttemptFailure(ctx, w, a.order, "SESSION_UNAVAILABLE", false); e != nil {
				return e
			}
			return s.finishWork(ctx, w, false, 0, "SESSION_UNAVAILABLE")
		}
		result, err = resolved.Provider.CreateCheckout(ctx, stripeprovider.CreateCheckoutRequest{
			Account: resolved.Account, TenantID: a.tenant, StoreID: a.store, OrderID: a.order, AttemptID: a.attempt, IdempotencyKey: a.key, AmountJPY: a.amount, Description: "Restaurant order",
		})
	}
	if err != nil {
		var now time.Time
		if e := s.db.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); e != nil {
			return e
		}
		if now.Before(a.retry) {
			if e := s.setAttemptUnknown(ctx, w, a.order, "PROVIDER_OUTCOME_UNKNOWN"); e != nil {
				return e
			}
			return s.finishWork(ctx, w, true, time.Second, "PROVIDER_OUTCOME_UNKNOWN")
		}
		if e := s.setAttemptUnknown(ctx, w, a.order, "PROVIDER_OUTCOME_UNKNOWN"); e != nil {
			return e
		}
		return s.finishWork(ctx, w, false, 0, "PROVIDER_OUTCOME_UNKNOWN")
	}
	if !validCreateResult(result, a) || (a.checkout != "" && result.SessionID != a.checkout) || (a.paymentIntent != "" && result.PaymentIntentID != a.paymentIntent) {
		if e := s.setAttemptUnknown(ctx, w, a.order, "UNSAFE_PROVIDER_RESPONSE"); e != nil {
			return e
		}
		return s.finishWork(ctx, w, false, 0, "UNSAFE_PROVIDER_RESPONSE")
	}
	return s.persistCheckout(ctx, w, a, result)
}

func validCreateResult(x stripeprovider.CheckoutResult, a createRow) bool {
	return x.AccountID == a.account && x.SessionID != "" && x.AmountJPY == a.amount && x.Currency == stripeprovider.CurrencyJPY && !x.LiveMode && !x.ExpiresAt.IsZero() &&
		x.Metadata["tenant_id"] == a.tenant && x.Metadata["store_id"] == a.store && x.Metadata["order_id"] == a.order && x.Metadata["attempt_id"] == a.attempt &&
		x.PaymentStatus != stripeprovider.PaymentNoPaymentRequired &&
		((x.Status == stripeprovider.CheckoutOpen && x.PaymentStatus == stripeprovider.PaymentUnpaid) ||
			(x.Status == stripeprovider.CheckoutComplete && x.PaymentStatus != stripeprovider.PaymentNoPaymentRequired) ||
			(x.Status == stripeprovider.CheckoutExpired)) &&
		(x.PaymentStatus != stripeprovider.PaymentPaid || x.PaymentIntentID != "")
}

func (s *Service) orderStillAdmissible(ctx context.Context, a createRow) bool {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT ts.status='OPEN' AND ts.expires_at>clock_timestamp() AND st.enabled AND st.operating AND t.enabled AND dt.enabled AND NOT os.operational_hold AND NOT os.payment_review_required
		FROM table_sessions ts JOIN stores st USING(tenant_id,store_id) JOIN tenants t USING(tenant_id) JOIN dining_tables dt USING(tenant_id,store_id,table_id)
		JOIN table_orders o ON o.session_id=ts.session_id AND o.order_id=$5 JOIN operation_order_state os ON os.order_id=o.order_id
		WHERE ts.session_id=$1 AND ts.tenant_id=$2 AND ts.store_id=$3 AND ts.table_id=$4`, a.session, a.tenant, a.store, a.table, a.order).Scan(&ok)
	return err == nil && ok
}

func (s *Service) persistCheckout(ctx context.Context, w workItem, a createRow, x stripeprovider.CheckoutResult) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockPaymentSessionAny(ctx, tx, a.order); err != nil {
		return err
	}
	var state string
	var currentSession string
	var review, hold bool
	var sessionActive bool
	err = tx.QueryRow(ctx, `SELECT a.state,a.session_id,coalesce(os.payment_review_required,true),coalesce(os.operational_hold,true),ts.status='OPEN' AND ts.expires_at>clock_timestamp()
		FROM stripe_payment_attempts a LEFT JOIN operation_order_state os USING(order_id) JOIN table_sessions ts ON ts.session_id=a.session_id WHERE a.order_id=$1 FOR UPDATE OF a`, a.order).Scan(&state, &currentSession, &review, &hold, &sessionActive)
	if err != nil {
		return err
	}
	if currentSession != a.session || state == string(StatePaid) || state == string(StateReviewRequired) {
		if err = s.completeLeased(ctx, tx, w); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return err
	}
	newState := StateOpen
	if x.Status == stripeprovider.CheckoutComplete {
		newState = StateProcessing
	}
	if x.Status == stripeprovider.CheckoutExpired {
		newState = StateExpired
	}
	if !now.Before(x.ExpiresAt) && x.Status == stripeprovider.CheckoutOpen {
		newState = StateExpired
	}
	if hold || review || !sessionActive {
		newState = StateUnknown
	}
	if x.PaymentStatus == stripeprovider.PaymentPaid {
		_, err = s.ops.ApplyStripePaidTx(ctx, tx, operations.VerifiedStripePayment{OrderID: a.order, SessionID: a.session, TenantID: a.tenant, StoreID: a.store, TableID: a.table, AmountJPY: a.amount, Currency: stripeprovider.CurrencyJPY, PaymentIntent: x.PaymentIntentID})
		if err != nil {
			return err
		}
		var needsReview bool
		if err = tx.QueryRow(ctx, `SELECT payment_review_required FROM operation_order_state WHERE order_id=$1`, a.order).Scan(&needsReview); err != nil {
			return err
		}
		if needsReview {
			newState = StateReviewRequired
		} else {
			newState = StatePaid
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE stripe_payment_attempts SET state=$2,checkout_session_id=$3,payment_intent_id=NULLIF($4,''),checkout_status=$5,provider_payment_status=$6,provider_expires_at=$7,payment_review_required=$8,last_reconciled_at=clock_timestamp(),updated_at=clock_timestamp() WHERE order_id=$1`, a.order, string(newState), x.SessionID, x.PaymentIntentID, string(x.Status), string(x.PaymentStatus), x.ExpiresAt, newState == StateReviewRequired); err != nil {
		return err
	}
	if err = s.appendAttemptEvent(ctx, tx, a.order, "CREATE_RESULT:"+x.SessionID, state, string(newState), "provider"); err != nil {
		return err
	}
	if newState == StateUnknown && x.Status == stripeprovider.CheckoutOpen && x.PaymentStatus == stripeprovider.PaymentUnpaid {
		if err = s.enqueueWork(ctx, tx, "EXPIRE", a.order, nil, "expire:"+a.attempt+":"+x.SessionID); err != nil {
			return err
		}
	}
	if err = s.completeLeased(ctx, tx, w); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) updateAttemptFailure(ctx context.Context, w workItem, orderID, code string, unknown bool) error {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lockPaymentSessionAny(ctx, tx, orderID); e != nil {
		return e
	}
	var leaseUntil time.Time
	if e = tx.QueryRow(ctx, `SELECT lease_until FROM stripe_payment_work WHERE work_id=$1 AND status='LEASED' AND lease_token=$2 FOR UPDATE`, w.ID, w.Lease).Scan(&leaseUntil); e != nil {
		return e
	}
	var dbNow time.Time
	if e = tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&dbNow); e != nil {
		return e
	}
	if !leaseUntil.After(dbNow) {
		return ErrConflict
	}
	state := StateFailed
	if unknown {
		state = StateUnknown
	}
	var before string
	e = tx.QueryRow(ctx, `SELECT state FROM stripe_payment_attempts WHERE order_id=$1 FOR UPDATE`, orderID).Scan(&before)
	if e != nil {
		return e
	}
	if before == string(StatePaid) || before == string(StateReviewRequired) {
		return tx.Commit(ctx)
	}
	_, e = tx.Exec(ctx, `UPDATE stripe_payment_attempts SET state=$2,updated_at=clock_timestamp() WHERE order_id=$1 AND state NOT IN ('PAID','REVIEW_REQUIRED')`, orderID, string(state))
	if e != nil {
		return e
	}
	if e = s.appendAttemptEvent(ctx, tx, orderID, "WORK_STATE:"+string(state)+":"+code, before, string(state), "provider"); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) setAttemptUnknown(ctx context.Context, w workItem, orderID, code string) error {
	return s.updateAttemptFailure(ctx, w, orderID, code, true)
}

func (s *Service) finishWork(ctx context.Context, w workItem, retry bool, after time.Duration, code string) error {
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var cmd pgconn.CommandTag
	if retry {
		cmd, e = tx.Exec(ctx, `UPDATE stripe_payment_work SET status='READY',lease_token=NULL,lease_until=NULL,available_at=clock_timestamp()+$3::interval,safe_error_code=$4,updated_at=clock_timestamp() WHERE work_id=$1 AND status='LEASED' AND lease_token=$2 AND lease_until>clock_timestamp()`, w.ID, w.Lease, intervalString(after), code)
	} else {
		cmd, e = tx.Exec(ctx, `UPDATE stripe_payment_work SET status='DONE',lease_token=NULL,lease_until=NULL,safe_error_code=$3,updated_at=clock_timestamp() WHERE work_id=$1 AND status='LEASED' AND lease_token=$2 AND lease_until>clock_timestamp()`, w.ID, w.Lease, code)
	}
	if e != nil {
		return e
	}
	if cmd.RowsAffected() != 1 {
		return ErrConflict
	}
	return tx.Commit(ctx)
}
func (s *Service) completeLeased(ctx context.Context, tx pgx.Tx, w workItem) error {
	cmd, e := tx.Exec(ctx, `UPDATE stripe_payment_work SET status='DONE',lease_token=NULL,lease_until=NULL,updated_at=clock_timestamp() WHERE work_id=$1 AND status='LEASED' AND lease_token=$2 AND lease_until>clock_timestamp()`, w.ID, w.Lease)
	if e != nil {
		return e
	}
	if cmd.RowsAffected() != 1 {
		return ErrConflict
	}
	return nil
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var n byte
	for i := range a {
		n |= a[i] ^ b[i]
	}
	return n == 0
}

func (s *Service) enqueueVerifiedWebhook(ctx context.Context, event stripeprovider.VerifiedEvent) (bool, error) {
	if event.ID == "" || event.AccountID == "" || event.APIVersion != stripeprovider.APIVersion || event.LiveMode || event.CreatedAt.IsZero() {
		return false, stripeprovider.ErrWebhook
	}
	var tenant, store, order, attempt, checkoutID, paymentIntentID, checkoutStatus, paymentStatus, piStatus string
	var amount int64
	metadata := map[string]string{}
	switch {
	case event.Checkout != nil:
		x := event.Checkout
		if !strings.HasPrefix(event.Type, "checkout.session.") || x.AccountID != event.AccountID || x.SessionID != event.ObjectID || x.Currency != stripeprovider.CurrencyJPY || x.LiveMode {
			return false, stripeprovider.ErrWebhook
		}
		metadata = x.Metadata
		checkoutID = x.SessionID
		paymentIntentID = x.PaymentIntentID
		checkoutStatus = string(x.Status)
		paymentStatus = string(x.PaymentStatus)
		amount = x.AmountJPY
	case event.PaymentIntent != nil:
		x := event.PaymentIntent
		if !strings.HasPrefix(event.Type, "payment_intent.") || x.AccountID != event.AccountID || x.ID != event.ObjectID || x.Currency != stripeprovider.CurrencyJPY || x.LiveMode {
			return false, stripeprovider.ErrWebhook
		}
		metadata = x.Metadata
		paymentIntentID = x.ID
		piStatus = x.Status
		amount = x.AmountJPY
		if piStatus == "succeeded" {
			paymentStatus = string(stripeprovider.PaymentPaid)
		} else {
			paymentStatus = string(stripeprovider.PaymentUnpaid)
		}
	default:
		return false, stripeprovider.ErrWebhook
	}
	tenant, store, order, attempt = metadata["tenant_id"], metadata["store_id"], metadata["order_id"], metadata["attempt_id"]
	if !validRef(tenant, 200) || !validRef(store, 200) || !validRef(order, 255) || !validRef(attempt, 255) || amount <= 0 || event.ObjectID == "" ||
		(checkoutStatus != "" && !validCheckoutStatus(checkoutStatus)) || (paymentStatus != "" && !validPaymentStatus(paymentStatus)) || (piStatus != "" && !validPIStatus(piStatus)) || !supportedEvent(event.Type) {
		return false, stripeprovider.ErrWebhook
	}
	metadata = map[string]string{"tenant_id": tenant, "store_id": store, "order_id": order, "attempt_id": attempt}
	metaJSON, err := json.Marshal(metadata)
	if err != nil {
		return false, ErrInvalid
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var expectedAmount int64
	var expectedTenant, expectedStore, expectedAttempt, expectedAccount, sessionID, tableID string
	err = tx.QueryRow(ctx, `SELECT amount_jpy,tenant_id,store_id,attempt_id,stripe_account_id,session_id,table_id FROM stripe_payment_attempts WHERE order_id=$1`, order).
		Scan(&expectedAmount, &expectedTenant, &expectedStore, &expectedAttempt, &expectedAccount, &sessionID, &tableID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if expectedAmount != amount || expectedTenant != tenant || expectedStore != store || expectedAttempt != attempt || expectedAccount != event.AccountID {
		return false, ErrDenied
	}
	if checkoutID != "" {
		var existing string
		err = tx.QueryRow(ctx, `SELECT coalesce(checkout_session_id,'') FROM stripe_payment_attempts WHERE order_id=$1`, order).Scan(&existing)
		if err != nil {
			return false, err
		}
		if existing != "" && existing != checkoutID {
			return false, ErrDenied
		}
	}
	if paymentIntentID != "" {
		var existing string
		err = tx.QueryRow(ctx, `SELECT coalesce(payment_intent_id,'') FROM stripe_payment_attempts WHERE order_id=$1`, order).Scan(&existing)
		if err != nil {
			return false, err
		}
		if existing != "" && existing != paymentIntentID {
			return false, ErrDenied
		}
	}
	var webhookID int64
	err = tx.QueryRow(ctx, `INSERT INTO stripe_webhook_events(stripe_account_id,stripe_event_id,tenant_id,store_id,order_id,attempt_id,event_type,provider_object_id,checkout_session_id,payment_intent_id,api_version,livemode,amount_jpy,currency,checkout_status,payment_status,payment_intent_status,correlation_metadata,event_created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,NULLIF($9,''),NULLIF($10,''),$11,false,$12,'jpy',NULLIF($13,''),NULLIF($14,''),NULLIF($15,''),$16,$17)
		ON CONFLICT(stripe_account_id,stripe_event_id) DO NOTHING RETURNING webhook_row_id`, event.AccountID, event.ID, tenant, store, order, attempt, event.Type, event.ObjectID, checkoutID, paymentIntentID, event.APIVersion, amount, checkoutStatus, paymentStatus, piStatus, metaJSON, event.CreatedAt.UTC()).Scan(&webhookID)
	if errors.Is(err, pgx.ErrNoRows) {
		var oldOrder, oldAttempt, oldType, oldObject string
		var oldAmount int64
		if e := tx.QueryRow(ctx, `SELECT order_id,attempt_id,amount_jpy,event_type,provider_object_id FROM stripe_webhook_events WHERE stripe_account_id=$1 AND stripe_event_id=$2`, event.AccountID, event.ID).Scan(&oldOrder, &oldAttempt, &oldAmount, &oldType, &oldObject); e != nil {
			return false, e
		}
		if oldOrder != order || oldAttempt != attempt || oldAmount != amount || oldType != event.Type || oldObject != event.ObjectID {
			return false, ErrDenied
		}
		return false, tx.Commit(ctx)
	}
	if err != nil {
		return false, err
	}
	if err = s.enqueueWork(ctx, tx, "WEBHOOK", "", &webhookID, "webhook:"+event.AccountID+":"+event.ID); err != nil {
		return false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

func supportedEvent(t string) bool {
	switch t {
	case "checkout.session.completed", "checkout.session.async_payment_succeeded", "checkout.session.async_payment_failed", "checkout.session.expired", "payment_intent.succeeded", "payment_intent.payment_failed", "payment_intent.processing":
		return true
	}
	return false
}
func validCheckoutStatus(s string) bool { return s == "open" || s == "complete" || s == "expired" }
func validPaymentStatus(s string) bool {
	return s == "unpaid" || s == "paid" || s == "no_payment_required"
}
func validPIStatus(s string) bool {
	switch s {
	case "requires_payment_method", "requires_confirmation", "requires_action", "processing", "requires_capture", "canceled", "succeeded":
		return true
	}
	return false
}

type inboxRow struct {
	rowID                                                                                                                int64
	account, eventID, tenant, store, order, attempt, kind, object, checkout, pi, checkoutStatus, paymentStatus, piStatus string
	amount                                                                                                               int64
	created                                                                                                              time.Time
}

func (s *Service) processWebhook(ctx context.Context, w workItem) error {
	if w.WebhookID == nil {
		return s.finishWork(ctx, w, false, 0, "MISSING_EVENT")
	}
	var e inboxRow
	err := s.db.QueryRow(ctx, `SELECT webhook_row_id,stripe_account_id,stripe_event_id,tenant_id,store_id,order_id,attempt_id,event_type,provider_object_id,coalesce(checkout_session_id,''),coalesce(payment_intent_id,''),coalesce(checkout_status,''),coalesce(payment_status,''),coalesce(payment_intent_status,''),amount_jpy,event_created_at FROM stripe_webhook_events WHERE webhook_row_id=$1`, *w.WebhookID).
		Scan(&e.rowID, &e.account, &e.eventID, &e.tenant, &e.store, &e.order, &e.attempt, &e.kind, &e.object, &e.checkout, &e.pi, &e.checkoutStatus, &e.paymentStatus, &e.piStatus, &e.amount, &e.created)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockPaymentSessionAny(ctx, tx, e.order); err != nil {
		return err
	}
	var currentState, expectedAttempt, mode, account, tenant, store, table, session string
	var amount, generation int64
	var existingCheckout, existingPI string
	err = tx.QueryRow(ctx, `SELECT state,attempt_id,provider_mode,stripe_account_id,tenant_id,store_id,table_id,session_id,amount_jpy,generation,coalesce(checkout_session_id,''),coalesce(payment_intent_id,'') FROM stripe_payment_attempts WHERE order_id=$1 FOR UPDATE`, e.order).
		Scan(&currentState, &expectedAttempt, &mode, &account, &tenant, &store, &table, &session, &amount, &generation, &existingCheckout, &existingPI)
	if err != nil {
		return err
	}
	if expectedAttempt != e.attempt || account != e.account || tenant != e.tenant || store != e.store || amount != e.amount || (e.checkout != "" && existingCheckout != "" && e.checkout != existingCheckout) || (e.pi != "" && existingPI != "" && e.pi != existingPI) {
		return ErrDenied
	}
	paid := e.paymentStatus == "paid" || e.piStatus == "succeeded"
	newState := AttemptState(currentState)
	terminalNonPaid := !paid && (currentState == string(StateFailed) || currentState == string(StateExpired))
	if paid {
		changed, applyErr := s.ops.ApplyStripePaidTx(ctx, tx, operations.VerifiedStripePayment{OrderID: e.order, SessionID: session, TenantID: tenant, StoreID: store, TableID: table, AmountJPY: amount, Currency: "jpy", PaymentIntent: e.pi})
		if applyErr != nil {
			return applyErr
		}
		var review bool
		if err = tx.QueryRow(ctx, `SELECT payment_review_required FROM operation_order_state WHERE order_id=$1`, e.order).Scan(&review); err != nil {
			return err
		}
		if review {
			newState = StateReviewRequired
		} else {
			newState = StatePaid
		}
		if changed || currentState != string(newState) {
			if err = s.appendAttemptEvent(ctx, tx, e.order, "WEBHOOK:"+e.eventID, currentState, string(newState), "stripe-webhook"); err != nil {
				return err
			}
		}
	} else if terminalNonPaid {
		// Provider event delivery can be out of order. FAILED/EXPIRED remains
		// terminal unless an authoritative paid observation arrives.
		newState = AttemptState(currentState)
	} else if currentState != string(StatePaid) && currentState != string(StateReviewRequired) {
		switch {
		case e.kind == "checkout.session.async_payment_failed" || e.kind == "payment_intent.payment_failed":
			newState = StateFailed
		case e.checkoutStatus == "expired" || e.kind == "checkout.session.expired":
			newState = StateExpired
		case e.checkoutStatus == "complete" || e.piStatus == "processing":
			newState = StateProcessing
		case e.checkoutStatus == "open":
			newState = StateOpen
		default:
			newState = StateUnknown
		}
		if err = s.appendAttemptEvent(ctx, tx, e.order, "WEBHOOK:"+e.eventID, currentState, string(newState), "stripe-webhook"); err != nil {
			return err
		}
	}
	if currentState != string(StatePaid) && currentState != string(StateReviewRequired) && !terminalNonPaid {
		_, err = tx.Exec(ctx, `UPDATE stripe_payment_attempts SET state=$2,checkout_session_id=coalesce(checkout_session_id,NULLIF($3,'')),payment_intent_id=coalesce(payment_intent_id,NULLIF($4,'')),checkout_status=coalesce(NULLIF($5,''),checkout_status),provider_payment_status=CASE WHEN $6='paid' THEN 'paid' WHEN $6<>'' THEN $6 ELSE provider_payment_status END,provider_expires_at=coalesce($7,provider_expires_at),payment_intent_status=coalesce(NULLIF($8,''),payment_intent_status),payment_review_required=$9,last_reconciled_at=clock_timestamp(),updated_at=clock_timestamp() WHERE order_id=$1`, e.order, string(newState), e.checkout, e.pi, e.checkoutStatus, e.paymentStatus, nil, e.piStatus, newState == StateReviewRequired)
		if err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE stripe_webhook_events SET processed_at=clock_timestamp(),safe_error_code=NULL WHERE webhook_row_id=$1`, e.rowID); err != nil {
		return err
	}
	if err = s.completeLeased(ctx, tx, w); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func lockPaymentSessionAny(ctx context.Context, tx pgx.Tx, order string) error {
	var sid, tenant, store, table string
	err := tx.QueryRow(ctx, `SELECT session_id,tenant_id,store_id,table_id FROM stripe_payment_attempts WHERE order_id=$1`, order).Scan(&sid, &tenant, &store, &table)
	if err != nil {
		return err
	}
	var found string
	err = tx.QueryRow(ctx, `SELECT session_id FROM table_sessions WHERE session_id=$1 AND tenant_id=$2 AND store_id=$3 AND table_id=$4 FOR UPDATE`, sid, tenant, store, table).Scan(&found)
	return err
}

func (s *Service) processExpire(ctx context.Context, w workItem) error {
	var a createRow
	err := s.db.QueryRow(ctx, `SELECT order_id,attempt_id,session_id,tenant_id,store_id,table_id,provider_mode,stripe_account_id,credential_ref,api_version,
		request_profile_fingerprint,idempotency_key,amount_jpy,coalesce(checkout_session_id,''),coalesce(payment_intent_id,''),retry_not_after
		FROM stripe_payment_attempts WHERE order_id=$1`, w.OrderID).Scan(&a.order, &a.attempt, &a.session, &a.tenant, &a.store, &a.table, &a.mode, &a.account, &a.credential, &a.api, &a.profile, &a.key, &a.amount, &a.checkout, &a.paymentIntent, &a.retry)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.finishWork(ctx, w, false, 0, "MISSING_ATTEMPT")
	}
	if err != nil {
		return err
	}
	if a.checkout == "" {
		return s.finishWork(ctx, w, false, 0, "NO_CHECKOUT_TO_EXPIRE")
	}
	resolved, err := s.resolve(ctx, a.credential, a.account, a.mode)
	if err != nil || a.api != stripeprovider.APIVersion || !bytesEqual(a.profile, resolved.ProfileFingerprint[:]) {
		return s.finishWork(ctx, w, true, 10*time.Second, "PROVIDER_UNCONFIGURED")
	}
	// Expire is only valid for an existing open/unpaid Checkout. Stripe rejects
	// complete sessions, so retrieve first and re-read after an ambiguous expire
	// result before deciding whether to retry or record a terminal observation.
	current, err := resolved.Provider.RetrieveCheckout(ctx, resolved.Account, a.checkout)
	if err != nil {
		return s.finishWork(ctx, w, true, 5*time.Second, "CHECKOUT_RETRIEVE_UNKNOWN")
	}
	if !validExpiryObservation(current, a) {
		return s.finishWork(ctx, w, true, 10*time.Second, "UNSAFE_CHECKOUT_RETRIEVE")
	}
	if current.Status != stripeprovider.CheckoutOpen || current.PaymentStatus != stripeprovider.PaymentUnpaid {
		return s.persistExpireObservation(ctx, w, a, current)
	}
	expired, expireErr := resolved.Provider.ExpireCheckout(ctx, resolved.Account, a.checkout, "p80-expire:"+a.attempt)
	if expireErr != nil {
		expired, err = resolved.Provider.RetrieveCheckout(ctx, resolved.Account, a.checkout)
		if err != nil {
			return s.finishWork(ctx, w, true, 5*time.Second, "EXPIRE_OUTCOME_UNKNOWN")
		}
	}
	if !validExpiryObservation(expired, a) {
		return s.finishWork(ctx, w, true, 10*time.Second, "UNSAFE_EXPIRE_RESPONSE")
	}
	return s.persistExpireObservation(ctx, w, a, expired)
}

func validExpiryObservation(x stripeprovider.CheckoutResult, a createRow) bool {
	return x.AccountID == a.account && x.SessionID == a.checkout && x.AmountJPY == a.amount && x.Currency == stripeprovider.CurrencyJPY && !x.LiveMode && !x.ExpiresAt.IsZero() &&
		(a.paymentIntent == "" || x.PaymentIntentID == a.paymentIntent) &&
		x.Metadata["tenant_id"] == a.tenant && x.Metadata["store_id"] == a.store && x.Metadata["order_id"] == a.order && x.Metadata["attempt_id"] == a.attempt &&
		(x.Status == stripeprovider.CheckoutOpen || x.Status == stripeprovider.CheckoutComplete || x.Status == stripeprovider.CheckoutExpired) &&
		(x.PaymentStatus == stripeprovider.PaymentUnpaid || x.PaymentStatus == stripeprovider.PaymentPaid)
}

func (s *Service) persistExpireObservation(ctx context.Context, w workItem, a createRow, x stripeprovider.CheckoutResult) error {
	if x.PaymentStatus == stripeprovider.PaymentPaid {
		_, err := s.enqueueVerifiedWebhook(ctx, stripeprovider.VerifiedEvent{
			ID: "expire-reconcile:" + a.attempt + ":" + a.checkout, Type: "checkout.session.completed", AccountID: a.account,
			APIVersion: stripeprovider.APIVersion, CreatedAt: s.cfg.Now().UTC(), ObjectID: a.checkout, Checkout: &x,
		})
		if err != nil {
			return err
		}
		return s.finishWork(ctx, w, false, 0, "PAID_RECONCILIATION_QUEUED")
	}
	if x.Status == stripeprovider.CheckoutOpen {
		return s.finishWork(ctx, w, true, 5*time.Second, "EXPIRE_NOT_CONFIRMED")
	}
	next := StateExpired
	if x.Status == stripeprovider.CheckoutComplete {
		// Completed-but-unpaid remains unresolved and must not be cancelled.
		next = StateProcessing
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = lockPaymentSessionAny(ctx, tx, a.order); e != nil {
		return e
	}
	var state string
	if e = tx.QueryRow(ctx, `SELECT state FROM stripe_payment_attempts WHERE order_id=$1 FOR UPDATE`, a.order).Scan(&state); e != nil {
		return e
	}
	if state != string(StatePaid) && state != string(StateReviewRequired) {
		if state != string(next) {
			if e = s.appendAttemptEvent(ctx, tx, a.order, "EXPIRE_OBSERVATION:"+a.checkout+":"+string(x.Status), state, string(next), "provider"); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE stripe_payment_attempts SET state=$2,checkout_status=$3,provider_payment_status='unpaid',provider_expires_at=$4,payment_intent_id=coalesce(payment_intent_id,NULLIF($5,'')),last_reconciled_at=clock_timestamp(),updated_at=clock_timestamp() WHERE order_id=$1`, a.order, string(next), string(x.Status), x.ExpiresAt, x.PaymentIntentID); e != nil {
			return e
		}
	}
	if e = s.completeLeased(ctx, tx, w); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
