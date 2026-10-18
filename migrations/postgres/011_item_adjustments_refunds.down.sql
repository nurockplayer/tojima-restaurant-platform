-- Disposable-schema rollback refuses to erase cancellation/refund history.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM order_adjustments) OR EXISTS(SELECT 1 FROM operation_refunds) THEN RAISE EXCEPTION 'cannot discard adjustment/refund history'; END IF;
END $$;
ALTER TABLE stripe_payment_work DROP CONSTRAINT stripe_payment_work_refund_shape;
ALTER TABLE stripe_payment_work DROP COLUMN refund_event_row_id;
ALTER TABLE stripe_payment_work DROP COLUMN refund_id;
ALTER TABLE stripe_payment_work DROP COLUMN refund_generation;
ALTER TABLE stripe_payment_work DROP CONSTRAINT stripe_payment_work_work_type_check;
ALTER TABLE stripe_payment_work ADD CONSTRAINT stripe_payment_work_work_type_check CHECK(work_type IN ('CREATE','WEBHOOK','EXPIRE'));
DROP TABLE stripe_refund_webhook_events;
DROP FUNCTION protect_refund_webhook();
DROP TABLE cash_refund_ledger;
DROP TABLE operation_refund_attempt_history;
DROP TABLE operation_refunds;
DROP FUNCTION protect_operation_refund();
DROP TABLE order_adjustments;
ALTER TABLE operation_order_state DROP COLUMN effective_total_gross_jpy;
ALTER TABLE operation_order_state DROP COLUMN effective_tax_snapshot;
ALTER TABLE operation_order_state DROP COLUMN cancelled_quantities;
ALTER TABLE operation_order_state DROP COLUMN order_updated;
ALTER TABLE operation_order_state DROP CONSTRAINT operation_order_state_check;
ALTER TABLE operation_order_state ADD CONSTRAINT operation_order_state_check CHECK((payment_method='CASH' AND cash_mode IS NOT NULL AND payment_status IN ('UNPAID','PAID')) OR (payment_method='STRIPE' AND cash_mode IS NULL AND payment_status IN ('PENDING','PAID','UNKNOWN','REFUND_PENDING','REFUNDED')));
ALTER TABLE operation_order_events DROP CONSTRAINT operation_order_events_event_type_check;
ALTER TABLE operation_order_events ADD CONSTRAINT operation_order_events_event_type_check CHECK(event_type IN ('ORDER_CREATED','STAFF_ACCEPTED','PAYMENT_SETTLED','KDS_RELEASED','FULFILLMENT_ADVANCED','STRIPE_PAID_TEST_FIXTURE','STRIPE_PAID_VERIFIED','STRIPE_REVIEW_REQUIRED','STRIPE_REVIEW_CLEARED','MANAGER_REVIEW_APPROVED'));
ALTER TABLE operation_action_idempotency DROP CONSTRAINT operation_action_idempotency_action_check;
ALTER TABLE operation_action_idempotency ADD CONSTRAINT operation_action_idempotency_action_check CHECK(action IN ('ACCEPT','FULFILLMENT','REVIEW_APPROVE'));

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
         WHERE e.order_id=OLD.order_id AND e.event_key LIKE 'manager-review:%' AND e.before_state='REVIEW_REQUIRED' AND e.after_state='PAID' AND a.event_type='MANAGER_REVIEW_APPROVED' AND a.reason IS NOT NULL AND a.state_version=os.version+1 AND os.review_decision='UNRESOLVED' AND os.payment_review_required)) THEN
       RAISE EXCEPTION 'paid or review-required Stripe attempt is terminal'; END IF;
    IF OLD.payment_review_required AND NOT NEW.payment_review_required AND NOT EXISTS (
       SELECT 1 FROM stripe_payment_attempt_events e JOIN operation_order_events a USING(order_id) JOIN operation_order_state os USING(order_id)
       WHERE e.order_id=OLD.order_id AND e.event_key LIKE 'manager-review:%' AND e.after_state='PAID' AND a.event_type='MANAGER_REVIEW_APPROVED' AND a.reason IS NOT NULL AND a.state_version=os.version+1 AND os.review_decision='UNRESOLVED' AND os.payment_review_required) THEN
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
        (OLD.payment_method='STRIPE' AND OLD.payment_status IN ('PENDING','UNKNOWN') AND NEW.payment_status='PAID')) THEN
        RAISE EXCEPTION 'unsupported payment transition';
    END IF;
    IF NEW.fulfillment_status <> OLD.fulfillment_status AND NOT (
        (OLD.fulfillment_status='WAITING' AND NEW.fulfillment_status='ACCEPTED') OR
        (OLD.fulfillment_status='ACCEPTED' AND NEW.fulfillment_status='PREPARING') OR
        (OLD.fulfillment_status='PREPARING' AND NEW.fulfillment_status='READY') OR
        (OLD.fulfillment_status='READY' AND NEW.fulfillment_status='SERVED')) THEN
        RAISE EXCEPTION 'unsupported fulfillment transition';
    END IF;
    IF OLD.staff_accepted AND NOT NEW.staff_accepted THEN RAISE EXCEPTION 'staff acceptance cannot be revoked'; END IF;
    IF NEW.payment_review_required AND NOT (NEW.payment_method='STRIPE' AND NEW.payment_status='PAID') THEN
        RAISE EXCEPTION 'payment review applies only to a paid Stripe order';
    END IF;
    IF OLD.payment_review_required AND NOT NEW.payment_review_required THEN
        IF NOT EXISTS (SELECT 1 FROM operation_order_events e WHERE e.order_id=OLD.order_id AND e.event_type IN ('STRIPE_REVIEW_CLEARED','MANAGER_REVIEW_APPROVED') AND e.state_version=NEW.version) THEN
            RAISE EXCEPTION 'payment review requires a same-version resolution event';
        END IF;
    END IF;
    IF OLD.payment_review_required IS DISTINCT FROM NEW.payment_review_required AND NEW.payment_review_required THEN
        IF NOT EXISTS (SELECT 1 FROM operation_order_events e WHERE e.order_id=OLD.order_id AND e.event_type='STRIPE_REVIEW_REQUIRED' AND e.state_version=NEW.version) THEN
            RAISE EXCEPTION 'payment review requires a same-version audit event';
        END IF;
    END IF;
    IF NEW.review_decision <> OLD.review_decision AND NOT (OLD.review_decision='UNRESOLVED' AND NEW.review_decision='APPROVED'
       AND EXISTS (SELECT 1 FROM operation_order_events e WHERE e.order_id=OLD.order_id AND e.event_type='MANAGER_REVIEW_APPROVED' AND e.state_version=NEW.version)) THEN
        RAISE EXCEPTION 'review decision requires governed resolution';
    END IF;
    IF OLD.review_decision='APPROVED' AND NEW.payment_review_required THEN RAISE EXCEPTION 'approved review decision is terminal'; END IF;
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
