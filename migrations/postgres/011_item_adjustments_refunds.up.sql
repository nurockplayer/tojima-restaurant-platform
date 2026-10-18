-- P-82 C1: immutable item reductions and separate original-payment refunds.
ALTER TABLE operation_order_state ADD COLUMN effective_total_gross_jpy bigint CHECK(effective_total_gross_jpy>=0);
ALTER TABLE operation_order_state ADD COLUMN effective_tax_snapshot jsonb;
ALTER TABLE operation_order_state ADD COLUMN cancelled_quantities jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(cancelled_quantities)='array');
ALTER TABLE operation_order_state ADD COLUMN order_updated boolean NOT NULL DEFAULT false;
ALTER TABLE operation_order_state DROP CONSTRAINT operation_order_state_check;
ALTER TABLE operation_order_state ADD CONSTRAINT operation_order_state_check CHECK (
 (payment_method='CASH' AND cash_mode IS NOT NULL AND payment_status IN ('UNPAID','PAID','REFUNDED')) OR
 (payment_method='STRIPE' AND cash_mode IS NULL AND payment_status IN ('PENDING','PAID','UNKNOWN','REFUND_PENDING','REFUNDED')));

CREATE TABLE order_adjustments (
 adjustment_id text PRIMARY KEY,
 order_id text NOT NULL REFERENCES operation_order_state(order_id),
 actor_id text NOT NULL CHECK(length(btrim(actor_id))>0),
 reason text NOT NULL CHECK(length(btrim(reason)) BETWEEN 1 AND 500),
 state_version bigint NOT NULL CHECK(state_version>1),
 original_snapshot jsonb NOT NULL,
 original_tax_snapshot jsonb NOT NULL,
 lines jsonb NOT NULL CHECK(jsonb_typeof(lines)='array'),
 cancelled_quantities jsonb NOT NULL CHECK(jsonb_typeof(cancelled_quantities)='array'),
 previous_total_gross_jpy bigint NOT NULL CHECK(previous_total_gross_jpy>=0),
 effective_total_gross_jpy bigint NOT NULL CHECK(effective_total_gross_jpy>=0 AND effective_total_gross_jpy<=previous_total_gross_jpy),
 effective_tax_snapshot jsonb,
 refund_amount_jpy bigint NOT NULL CHECK(refund_amount_jpy>=0 AND refund_amount_jpy<=previous_total_gross_jpy-effective_total_gross_jpy),
 whole_order_cancel boolean NOT NULL,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(order_id,state_version)
);
CREATE TRIGGER order_adjustments_immutable BEFORE UPDATE OR DELETE ON order_adjustments FOR EACH ROW EXECUTE FUNCTION reject_operation_history_mutation();
CREATE TABLE operation_refunds (
 refund_id text PRIMARY KEY,
 adjustment_id text NOT NULL UNIQUE REFERENCES order_adjustments(adjustment_id),
 order_id text NOT NULL REFERENCES operation_order_state(order_id),
 amount_jpy bigint NOT NULL CHECK(amount_jpy>0),
 method text NOT NULL CHECK(method IN ('CASH','STRIPE')),
 status text NOT NULL CHECK(status IN ('REQUESTED','PROCESSING','SUCCEEDED','FAILED')),
 actor_id text NOT NULL CHECK(length(btrim(actor_id))>0),
 receipt_id text REFERENCES cash_settlements(receipt_id),
 provider_refund_id text,
 generation bigint NOT NULL DEFAULT 1 CHECK(generation>0),
 provider_key text NOT NULL UNIQUE,
 -- Unset until first configured dispatch; immutable once anchored per generation.
 retry_not_after timestamptz,
 safe_error_code text,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CHECK((method='CASH' AND status='SUCCEEDED' AND receipt_id IS NOT NULL AND provider_refund_id IS NULL) OR (method='STRIPE' AND receipt_id IS NULL))
);
CREATE INDEX operation_refunds_order_idx ON operation_refunds(order_id);
CREATE TABLE operation_refund_attempt_history (
 refund_id text NOT NULL REFERENCES operation_refunds(refund_id),
 generation bigint NOT NULL,
 provider_refund_id text NOT NULL,
 provider_key text NOT NULL,
 status text NOT NULL CHECK(status='FAILED'),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 PRIMARY KEY(refund_id,generation)
);
CREATE TRIGGER operation_refund_attempt_history_immutable BEFORE UPDATE OR DELETE ON operation_refund_attempt_history FOR EACH ROW EXECUTE FUNCTION reject_operation_history_mutation();
CREATE TABLE cash_refund_ledger (
 refund_id text PRIMARY KEY REFERENCES operation_refunds(refund_id),
 receipt_id text NOT NULL REFERENCES cash_settlements(receipt_id),
 order_id text NOT NULL REFERENCES operation_order_state(order_id),
 actor_id text NOT NULL,
 amount_jpy bigint NOT NULL CHECK(amount_jpy>0),
 effective_tax_invoice jsonb,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TRIGGER cash_refund_ledger_immutable BEFORE UPDATE OR DELETE ON cash_refund_ledger FOR EACH ROW EXECUTE FUNCTION reject_operation_history_mutation();
CREATE FUNCTION protect_operation_refund() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE paid bigint; reserved numeric;
BEGIN
 IF TG_OP='INSERT' THEN
  PERFORM 1 FROM operation_order_state WHERE order_id=NEW.order_id FOR UPDATE;
  IF NOT EXISTS(SELECT 1 FROM order_adjustments a WHERE a.adjustment_id=NEW.adjustment_id AND a.order_id=NEW.order_id AND a.refund_amount_jpy=NEW.amount_jpy) THEN RAISE EXCEPTION 'refund requires matching adjustment'; END IF;
  IF NEW.method='CASH' THEN SELECT amount_jpy INTO paid FROM cash_settlement_allocations WHERE order_id=NEW.order_id AND receipt_id=NEW.receipt_id;
  ELSE SELECT amount_jpy INTO paid FROM stripe_payment_attempts WHERE order_id=NEW.order_id AND state IN ('PAID','REVIEW_REQUIRED') AND payment_intent_id IS NOT NULL; END IF;
  SELECT coalesce(sum(amount_jpy),0) INTO reserved FROM operation_refunds WHERE order_id=NEW.order_id;
  IF paid IS NULL OR reserved+NEW.amount_jpy>paid THEN RAISE EXCEPTION 'refund exceeds paid remainder'; END IF;
  RETURN NEW;
 END IF;
 IF NEW.refund_id<>OLD.refund_id OR NEW.adjustment_id<>OLD.adjustment_id OR NEW.order_id<>OLD.order_id OR NEW.amount_jpy<>OLD.amount_jpy OR NEW.method<>OLD.method OR NEW.actor_id<>OLD.actor_id OR NEW.receipt_id IS DISTINCT FROM OLD.receipt_id OR NEW.created_at<>OLD.created_at THEN RAISE EXCEPTION 'refund snapshot is immutable'; END IF;
 IF NEW.generation<>OLD.generation THEN
  IF OLD.status<>'FAILED' OR OLD.provider_refund_id IS NULL OR NEW.status<>'REQUESTED' OR NEW.generation<>OLD.generation+1 OR NEW.provider_key=OLD.provider_key OR NEW.provider_refund_id IS NOT NULL OR NEW.retry_not_after IS NOT NULL THEN RAISE EXCEPTION 'only confirmed failure may retry'; END IF;
 ELSE
  IF NEW.provider_key<>OLD.provider_key OR (OLD.retry_not_after IS NOT NULL AND NEW.retry_not_after IS DISTINCT FROM OLD.retry_not_after) OR (OLD.provider_refund_id IS NOT NULL AND NEW.provider_refund_id IS DISTINCT FROM OLD.provider_refund_id) THEN RAISE EXCEPTION 'refund provider attempt is immutable'; END IF;
  IF NEW.status<>OLD.status AND NOT ((OLD.status='REQUESTED' AND NEW.status='PROCESSING') OR (OLD.status='PROCESSING' AND NEW.status IN ('SUCCEEDED','FAILED')) OR (OLD.method='STRIPE' AND OLD.status='SUCCEEDED' AND NEW.status='FAILED' AND OLD.provider_refund_id IS NOT NULL)) THEN RAISE EXCEPTION 'unsupported refund transition'; END IF;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER operation_refunds_guard BEFORE INSERT OR UPDATE ON operation_refunds FOR EACH ROW EXECUTE FUNCTION protect_operation_refund();
CREATE TRIGGER operation_refunds_no_delete BEFORE DELETE ON operation_refunds FOR EACH ROW EXECUTE FUNCTION reject_operation_history_mutation();

CREATE TABLE stripe_refund_webhook_events (
 event_row_id bigserial PRIMARY KEY,
 generation bigint NOT NULL,
 stripe_account_id text NOT NULL,
 stripe_event_id text NOT NULL,
 refund_id text NOT NULL REFERENCES operation_refunds(refund_id),
 provider_object_id text NOT NULL,
 event_type text NOT NULL,
 amount_jpy bigint NOT NULL CHECK(amount_jpy>0),
 payment_intent_id text NOT NULL,
 provider_status text NOT NULL,
 api_version text NOT NULL,
 livemode boolean NOT NULL CHECK(NOT livemode),
 event_created_at timestamptz NOT NULL,
 processed_at timestamptz,
 safe_error_code text,
 UNIQUE(stripe_account_id,stripe_event_id,refund_id)
);
CREATE FUNCTION protect_refund_webhook() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF (to_jsonb(NEW)-'processed_at'-'safe_error_code')<>(to_jsonb(OLD)-'processed_at'-'safe_error_code') THEN RAISE EXCEPTION 'refund webhook is immutable'; END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER stripe_refund_webhook_immutable BEFORE UPDATE ON stripe_refund_webhook_events FOR EACH ROW EXECUTE FUNCTION protect_refund_webhook();
CREATE TRIGGER stripe_refund_webhook_no_delete BEFORE DELETE ON stripe_refund_webhook_events FOR EACH ROW EXECUTE FUNCTION reject_payment_history_mutation();
ALTER TABLE stripe_payment_work ADD COLUMN refund_generation bigint;
ALTER TABLE stripe_payment_work ADD COLUMN refund_id text REFERENCES operation_refunds(refund_id);
ALTER TABLE stripe_payment_work ADD COLUMN refund_event_row_id bigint REFERENCES stripe_refund_webhook_events(event_row_id);
ALTER TABLE stripe_payment_work DROP CONSTRAINT stripe_payment_work_work_type_check;
ALTER TABLE stripe_payment_work ADD CONSTRAINT stripe_payment_work_work_type_check CHECK(work_type IN ('CREATE','WEBHOOK','EXPIRE','REFUND','REFUND_WEBHOOK'));
ALTER TABLE stripe_payment_work ADD CONSTRAINT stripe_payment_work_refund_shape CHECK (
 (work_type='REFUND' AND refund_generation IS NOT NULL AND refund_id IS NOT NULL AND refund_event_row_id IS NULL) OR
 (work_type='REFUND_WEBHOOK' AND refund_generation IS NOT NULL AND refund_id IS NOT NULL AND refund_event_row_id IS NOT NULL) OR
 (work_type NOT IN ('REFUND','REFUND_WEBHOOK') AND refund_generation IS NULL AND refund_id IS NULL AND refund_event_row_id IS NULL));
ALTER TABLE operation_order_events DROP CONSTRAINT operation_order_events_event_type_check;
ALTER TABLE operation_order_events ADD CONSTRAINT operation_order_events_event_type_check CHECK(event_type IN (
 'ORDER_CREATED','STAFF_ACCEPTED','PAYMENT_SETTLED','KDS_RELEASED','FULFILLMENT_ADVANCED','STRIPE_PAID_TEST_FIXTURE','STRIPE_PAID_VERIFIED','STRIPE_REVIEW_REQUIRED','STRIPE_REVIEW_CLEARED','MANAGER_REVIEW_APPROVED',
 'ORDER_ADJUSTED','REFUND_STATUS_CHANGED','REFUND_RETRIED','KDS_UPDATE_ACKNOWLEDGED'));
ALTER TABLE operation_action_idempotency DROP CONSTRAINT operation_action_idempotency_action_check;
ALTER TABLE operation_action_idempotency ADD CONSTRAINT operation_action_idempotency_action_check CHECK(action IN ('ACCEPT','FULFILLMENT','REVIEW_APPROVE','ADJUST','REFUND_RETRY','KDS_ACK'));

CREATE OR REPLACE FUNCTION protect_stripe_attempt_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.order_id<>OLD.order_id OR NEW.attempt_id<>OLD.attempt_id OR NEW.session_id<>OLD.session_id OR NEW.tenant_id<>OLD.tenant_id OR NEW.store_id<>OLD.store_id
       OR NEW.table_id<>OLD.table_id OR NEW.generation<>OLD.generation OR NEW.amount_jpy<>OLD.amount_jpy OR NEW.currency<>OLD.currency
       OR NEW.tax_snapshot<>OLD.tax_snapshot OR NEW.provider_mode<>OLD.provider_mode OR NEW.stripe_account_id<>OLD.stripe_account_id
       OR NEW.credential_ref<>OLD.credential_ref OR NEW.merchant_version<>OLD.merchant_version
       OR NEW.request_profile_fingerprint<>OLD.request_profile_fingerprint OR NEW.api_version<>OLD.api_version
       OR NEW.idempotency_key<>OLD.idempotency_key OR NEW.request_digest<>OLD.request_digest
       OR NEW.checkout_proof_hash<>OLD.checkout_proof_hash OR NEW.checkout_proof_expires_at<>OLD.checkout_proof_expires_at
       OR NEW.continuation_expires_at<>OLD.continuation_expires_at OR NEW.retry_not_after<>OLD.retry_not_after
       OR NEW.created_at<>OLD.created_at THEN RAISE EXCEPTION 'Stripe attempt snapshot is immutable'; END IF;
    IF OLD.checkout_session_id IS NOT NULL AND NEW.checkout_session_id IS DISTINCT FROM OLD.checkout_session_id THEN RAISE EXCEPTION 'Stripe Checkout Session binding is immutable'; END IF;
    IF OLD.payment_intent_id IS NOT NULL AND NEW.payment_intent_id IS DISTINCT FROM OLD.payment_intent_id THEN RAISE EXCEPTION 'Stripe PaymentIntent binding is immutable'; END IF;
    IF OLD.state IN ('PAID','REVIEW_REQUIRED') AND NEW.state<>OLD.state AND NOT (OLD.state='REVIEW_REQUIRED' AND NEW.state='PAID'
       AND EXISTS (SELECT 1 FROM stripe_payment_attempt_events e JOIN operation_order_events a USING(order_id) JOIN operation_order_state os USING(order_id)
         WHERE e.order_id=OLD.order_id AND e.event_key LIKE 'manager-review:%' AND e.before_state='REVIEW_REQUIRED' AND e.after_state='PAID' AND a.event_type IN ('MANAGER_REVIEW_APPROVED','ORDER_ADJUSTED') AND a.reason IS NOT NULL AND a.state_version=os.version+1 AND os.review_decision='UNRESOLVED' AND os.payment_review_required)) THEN
       RAISE EXCEPTION 'paid or review-required Stripe attempt is terminal'; END IF;
    IF OLD.payment_review_required AND NOT NEW.payment_review_required AND NOT EXISTS (
       SELECT 1 FROM stripe_payment_attempt_events e JOIN operation_order_events a USING(order_id) JOIN operation_order_state os USING(order_id)
       WHERE e.order_id=OLD.order_id AND e.event_key LIKE 'manager-review:%' AND e.after_state='PAID' AND a.event_type IN ('MANAGER_REVIEW_APPROVED','ORDER_ADJUSTED') AND a.reason IS NOT NULL AND a.state_version=os.version+1 AND os.review_decision='UNRESOLVED' AND os.payment_review_required) THEN
       RAISE EXCEPTION 'payment review requires governed resolution'; END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION validate_operation_order_transition() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.order_id <> OLD.order_id OR NEW.session_id <> OLD.session_id OR NEW.tenant_id <> OLD.tenant_id
       OR NEW.store_id <> OLD.store_id OR NEW.table_id <> OLD.table_id OR NEW.generation <> OLD.generation
       OR NEW.payment_method <> OLD.payment_method OR NEW.cash_mode IS DISTINCT FROM OLD.cash_mode
       OR NEW.requires_staff_acceptance <> OLD.requires_staff_acceptance THEN
        RAISE EXCEPTION 'operation order snapshot is immutable';
    END IF;
    IF NEW.version <> OLD.version + 1 THEN RAISE EXCEPTION 'operation state version must advance once'; END IF;
    IF NEW.payment_status <> OLD.payment_status AND NOT (
        (OLD.payment_method='CASH' AND OLD.payment_status='UNPAID' AND NEW.payment_status='PAID') OR
        (OLD.payment_method='STRIPE' AND OLD.payment_status IN ('PENDING','UNKNOWN') AND NEW.payment_status='PAID') OR
        (OLD.payment_status='PAID' AND NEW.payment_status IN ('REFUND_PENDING','REFUNDED') AND NEW.fulfillment_status='CANCELLED') OR
        (OLD.payment_method='STRIPE' AND OLD.payment_status='REFUNDED' AND NEW.payment_status='REFUND_PENDING' AND NEW.fulfillment_status='CANCELLED' AND EXISTS(SELECT 1 FROM operation_refunds r WHERE r.order_id=OLD.order_id AND r.status='FAILED')) OR
        (OLD.payment_status='REFUND_PENDING' AND NEW.payment_status='REFUNDED' AND NOT EXISTS(SELECT 1 FROM operation_refunds r WHERE r.order_id=OLD.order_id AND r.status<>'SUCCEEDED'))) THEN
        RAISE EXCEPTION 'unsupported payment transition';
    END IF;
    IF NEW.payment_status='REFUNDED' AND (
        EXISTS(SELECT 1 FROM operation_refunds r WHERE r.order_id=OLD.order_id AND r.status<>'SUCCEEDED') OR
        (SELECT coalesce(sum(r.amount_jpy),0) FROM operation_refunds r WHERE r.order_id=OLD.order_id AND r.status='SUCCEEDED') <
        CASE WHEN OLD.payment_method='STRIPE' THEN (SELECT amount_jpy FROM stripe_payment_attempts WHERE order_id=OLD.order_id)
             ELSE (SELECT amount_jpy FROM cash_settlement_allocations WHERE order_id=OLD.order_id) END) THEN
        RAISE EXCEPTION 'full refund requires confirmed repayment of original paid amount';
    END IF;
    IF NEW.fulfillment_status <> OLD.fulfillment_status AND NOT (
        (OLD.fulfillment_status='WAITING' AND NEW.fulfillment_status='ACCEPTED') OR
        (OLD.fulfillment_status='ACCEPTED' AND NEW.fulfillment_status='PREPARING') OR
        (OLD.fulfillment_status='PREPARING' AND NEW.fulfillment_status='READY') OR
        (OLD.fulfillment_status='READY' AND NEW.fulfillment_status='SERVED') OR
        (OLD.fulfillment_status IN ('WAITING','ACCEPTED') AND NEW.fulfillment_status='CANCELLED' AND EXISTS(SELECT 1 FROM order_adjustments a WHERE a.order_id=OLD.order_id AND a.state_version=NEW.version AND a.whole_order_cancel))) THEN
        RAISE EXCEPTION 'unsupported fulfillment transition';
    END IF;
    IF OLD.staff_accepted AND NOT NEW.staff_accepted THEN RAISE EXCEPTION 'staff acceptance cannot be revoked'; END IF;
    IF NEW.payment_review_required AND NOT (NEW.payment_method='STRIPE' AND NEW.payment_status='PAID') THEN
        RAISE EXCEPTION 'payment review applies only to a paid Stripe order';
    END IF;
    IF OLD.payment_review_required AND NOT NEW.payment_review_required THEN
        IF NOT EXISTS (SELECT 1 FROM operation_order_events e WHERE e.order_id=OLD.order_id AND e.event_type IN ('STRIPE_REVIEW_CLEARED','MANAGER_REVIEW_APPROVED','ORDER_ADJUSTED') AND e.state_version=NEW.version) THEN
            RAISE EXCEPTION 'payment review requires a same-version resolution event';
        END IF;
    END IF;
    IF OLD.payment_review_required IS DISTINCT FROM NEW.payment_review_required AND NEW.payment_review_required THEN
        IF NOT EXISTS (SELECT 1 FROM operation_order_events e WHERE e.order_id=OLD.order_id AND e.event_type='STRIPE_REVIEW_REQUIRED' AND e.state_version=NEW.version) THEN
            RAISE EXCEPTION 'payment review requires a same-version audit event';
        END IF;
    END IF;
    IF NEW.review_decision <> OLD.review_decision AND NOT (OLD.review_decision='UNRESOLVED' AND NEW.review_decision IN ('APPROVED','CANCELLED')
       AND EXISTS (SELECT 1 FROM operation_order_events e WHERE e.order_id=OLD.order_id AND e.event_type=CASE WHEN NEW.review_decision='APPROVED' THEN 'MANAGER_REVIEW_APPROVED' ELSE 'ORDER_ADJUSTED' END AND e.state_version=NEW.version)) THEN
        RAISE EXCEPTION 'review decision requires governed resolution';
    END IF;
    IF OLD.review_decision IN ('APPROVED','CANCELLED') AND NEW.payment_review_required THEN RAISE EXCEPTION 'approved review decision is terminal'; END IF;
    IF (NEW.effective_total_gross_jpy IS DISTINCT FROM OLD.effective_total_gross_jpy OR NEW.effective_tax_snapshot IS DISTINCT FROM OLD.effective_tax_snapshot OR NEW.cancelled_quantities<>OLD.cancelled_quantities) AND NOT EXISTS (
        SELECT 1 FROM order_adjustments a WHERE a.order_id=OLD.order_id AND a.state_version=NEW.version AND a.effective_total_gross_jpy=NEW.effective_total_gross_jpy AND a.cancelled_quantities=NEW.cancelled_quantities AND a.effective_tax_snapshot IS NOT DISTINCT FROM NEW.effective_tax_snapshot) THEN
        RAISE EXCEPTION 'effective totals require immutable adjustment';
    END IF;
    IF NEW.fulfillment_status='PREPARING' AND NEW.order_updated THEN RAISE EXCEPTION 'kitchen must acknowledge updated order'; END IF;
    IF OLD.order_updated AND NOT NEW.order_updated AND NOT EXISTS(SELECT 1 FROM operation_order_events e WHERE e.order_id=OLD.order_id AND e.state_version=NEW.version AND e.event_type='KDS_UPDATE_ACKNOWLEDGED') THEN RAISE EXCEPTION 'kitchen acknowledgement requires audit'; END IF;
    IF OLD.fulfillment_status <> NEW.fulfillment_status AND NEW.fulfillment_status='ACCEPTED' THEN
        IF NOT ((OLD.payment_method='STRIPE' AND OLD.payment_status='PAID') OR
            (OLD.payment_method='CASH' AND (OLD.cash_mode='PAY_LATER' OR OLD.payment_status='PAID'))) OR
            (OLD.requires_staff_acceptance AND NOT NEW.staff_accepted) OR NEW.payment_review_required OR NEW.operational_hold THEN
            RAISE EXCEPTION 'order is not eligible for KDS release';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
