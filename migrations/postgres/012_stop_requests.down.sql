-- Refuse to discard human decisions and their audit history.
DO $$ BEGIN IF EXISTS(SELECT 1 FROM cancellation_requests) THEN RAISE EXCEPTION 'cannot discard stop request history'; END IF; END $$;
DROP TABLE cancellation_requests;
DROP FUNCTION protect_cancellation_request();
ALTER TABLE operation_order_events DROP CONSTRAINT operation_order_events_event_type_check;
ALTER TABLE operation_order_events ADD CONSTRAINT operation_order_events_event_type_check CHECK(event_type IN (
 'ORDER_CREATED','STAFF_ACCEPTED','PAYMENT_SETTLED','KDS_RELEASED','FULFILLMENT_ADVANCED','STRIPE_PAID_TEST_FIXTURE','STRIPE_PAID_VERIFIED','STRIPE_REVIEW_REQUIRED','STRIPE_REVIEW_CLEARED','MANAGER_REVIEW_APPROVED',
 'ORDER_ADJUSTED','REFUND_STATUS_CHANGED','REFUND_RETRIED','KDS_UPDATE_ACKNOWLEDGED'));
ALTER TABLE operation_action_idempotency DROP CONSTRAINT operation_action_idempotency_action_check;
ALTER TABLE operation_action_idempotency ADD CONSTRAINT operation_action_idempotency_action_check CHECK(action IN ('ACCEPT','FULFILLMENT','REVIEW_APPROVE','ADJUST','REFUND_RETRY','KDS_ACK'));

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
