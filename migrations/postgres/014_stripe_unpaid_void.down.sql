-- Preserve accepted void and late-payment audit history on rollback.
DO $$ BEGIN
 IF EXISTS(SELECT 1 FROM operation_order_state WHERE payment_status='VOIDED') OR
    EXISTS(SELECT 1 FROM operation_order_events WHERE event_type IN ('STRIPE_UNPAID_VOIDED','STRIPE_LATE_PAYMENT_REFUND')) OR
    EXISTS(SELECT 1 FROM operation_action_idempotency WHERE action='STRIPE_VOID') THEN
  RAISE EXCEPTION 'cannot roll back Stripe unpaid void history';
 END IF;
END $$;
ALTER TABLE operation_order_state DROP CONSTRAINT operation_order_state_payment_status_check;
ALTER TABLE operation_order_state ADD CONSTRAINT operation_order_state_payment_status_check CHECK (payment_status IN ('UNPAID','PENDING','PAID','UNKNOWN','REFUND_PENDING','REFUNDED'));
ALTER TABLE operation_order_state DROP CONSTRAINT operation_order_state_check;
ALTER TABLE operation_order_state ADD CONSTRAINT operation_order_state_check CHECK (
 (payment_method='CASH' AND cash_mode IS NOT NULL AND payment_status IN ('UNPAID','PAID','REFUNDED')) OR
 (payment_method='STRIPE' AND cash_mode IS NULL AND payment_status IN ('PENDING','PAID','UNKNOWN','REFUND_PENDING','REFUNDED')));
ALTER TABLE operation_order_events DROP CONSTRAINT operation_order_events_event_type_check;
ALTER TABLE operation_order_events ADD CONSTRAINT operation_order_events_event_type_check CHECK(event_type IN (
 'ORDER_CREATED','STAFF_ACCEPTED','PAYMENT_SETTLED','KDS_RELEASED','FULFILLMENT_ADVANCED','STRIPE_PAID_TEST_FIXTURE','STRIPE_PAID_VERIFIED','STRIPE_REVIEW_REQUIRED','STRIPE_REVIEW_CLEARED','MANAGER_REVIEW_APPROVED',
 'ORDER_ADJUSTED','REFUND_STATUS_CHANGED','REFUND_RETRIED','KDS_UPDATE_ACKNOWLEDGED','STOP_REQUESTED','STOP_CONFIRM','STOP_REJECT','STOP_WITHDRAW','STOP_FINALIZE','STOP_MANUAL_EXCEPTION'));
ALTER TABLE operation_action_idempotency DROP CONSTRAINT operation_action_idempotency_action_check;
ALTER TABLE operation_action_idempotency ADD CONSTRAINT operation_action_idempotency_action_check CHECK(action IN ('ACCEPT','FULFILLMENT','REVIEW_APPROVE','ADJUST','REFUND_RETRY','KDS_ACK','STOP_REQUEST','STOP_CONFIRM','STOP_REJECT','STOP_WITHDRAW','STOP_FINALIZE'));
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
        (OLD.payment_method='STRIPE' AND OLD.payment_status='REFUNDED' AND NEW.payment_status='REFUND_PENDING' AND NEW.fulfillment_status='CANCELLED' AND EXISTS(SELECT 1 FROM operation_refunds r WHERE r.order_id=OLD.order_id AND r.status<>'SUCCEEDED')) OR
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
        (OLD.fulfillment_status IN ('WAITING','ACCEPTED','PREPARING') AND NEW.fulfillment_status='CANCELLED' AND EXISTS(SELECT 1 FROM order_adjustments a WHERE a.order_id=OLD.order_id AND a.state_version=NEW.version AND a.whole_order_cancel))) THEN
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
    IF NEW.review_decision <> OLD.review_decision AND NOT ((OLD.review_decision='UNRESOLVED' OR (OLD.review_decision='APPROVED' AND NEW.review_decision='CANCELLED' AND EXISTS(SELECT 1 FROM cancellation_requests c JOIN order_adjustments a ON a.adjustment_id=c.adjustment_id WHERE c.order_id=OLD.order_id AND c.status='FINALIZED' AND a.state_version=NEW.version))) AND NEW.review_decision IN ('APPROVED','CANCELLED')
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

    IF NEW.fulfillment_status IN ('READY','SERVED') AND EXISTS(SELECT 1 FROM cancellation_requests c WHERE c.order_id=OLD.order_id AND c.status IN ('REQUESTED','KITCHEN_CONFIRMED')) THEN
        RAISE EXCEPTION 'unresolved stop request blocks fulfillment';
    END IF;
    IF OLD.fulfillment_status='PREPARING' AND (NEW.cancelled_quantities<>OLD.cancelled_quantities OR NEW.fulfillment_status='CANCELLED') AND NOT EXISTS (
        SELECT 1 FROM cancellation_requests c JOIN order_adjustments a ON a.adjustment_id=c.adjustment_id
        WHERE c.order_id=OLD.order_id AND c.status='FINALIZED' AND c.kitchen_confirmer<>'' AND c.non_delivery_confirmer<>'' AND c.manual_exception_reason=''
        AND a.order_id=OLD.order_id AND a.state_version=NEW.version
        AND coalesce((NEW.cancelled_quantities->>c.line_index)::bigint,0)-coalesce((OLD.cancelled_quantities->>c.line_index)::bigint,0)=c.quantity
        AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements_text(NEW.cancelled_quantities) WITH ORDINALITY q(n,i) WHERE i-1<>c.line_index AND n::bigint<>coalesce((OLD.cancelled_quantities->>(i-1)::integer)::bigint,0))) THEN
        RAISE EXCEPTION 'preparing cancellation requires finalized stop request';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION protect_operation_refund() RETURNS trigger LANGUAGE plpgsql AS $$
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
  IF NEW.status<>OLD.status AND NOT ((OLD.status='REQUESTED' AND NEW.status='PROCESSING') OR (OLD.method='STRIPE' AND NEW.provider_refund_id IS NOT NULL AND NEW.status IN ('PROCESSING','SUCCEEDED','FAILED'))) THEN RAISE EXCEPTION 'unsupported refund transition'; END IF;
 END IF;
 RETURN NEW;
END;
$$;
