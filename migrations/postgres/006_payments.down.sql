DROP TRIGGER IF EXISTS stripe_payment_attempt_no_delete ON stripe_payment_attempts;
DROP TRIGGER IF EXISTS stripe_payment_attempt_snapshot_immutable ON stripe_payment_attempts;
DROP TRIGGER IF EXISTS stripe_webhook_events_immutable_fields ON stripe_webhook_events;
DROP TRIGGER IF EXISTS stripe_webhook_events_no_delete ON stripe_webhook_events;
DROP TRIGGER IF EXISTS stripe_payment_attempt_events_immutable ON stripe_payment_attempt_events;
DROP TRIGGER IF EXISTS store_stripe_merchants_audit_immutable ON store_stripe_merchants_audit;
DROP FUNCTION IF EXISTS reject_stripe_attempt_delete();
DROP FUNCTION IF EXISTS protect_stripe_attempt_snapshot();
DROP FUNCTION IF EXISTS reject_stripe_webhook_event_mutation();
DROP FUNCTION IF EXISTS reject_payment_history_mutation();

DROP TABLE IF EXISTS stripe_payment_work;
DROP TABLE IF EXISTS stripe_webhook_events;
DROP TABLE IF EXISTS stripe_payment_attempt_events;
DROP TABLE IF EXISTS stripe_payment_attempts;
DROP TABLE IF EXISTS store_stripe_merchants_audit;
DROP TABLE IF EXISTS store_stripe_merchants;

ALTER TABLE operation_order_events DROP CONSTRAINT operation_order_events_event_type_check;
ALTER TABLE operation_order_events ADD CONSTRAINT operation_order_events_event_type_check
    CHECK (event_type IN ('ORDER_CREATED','STAFF_ACCEPTED','PAYMENT_SETTLED','KDS_RELEASED','FULFILLMENT_ADVANCED','STRIPE_PAID_TEST_FIXTURE'));

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
        (OLD.payment_method='STRIPE' AND OLD.payment_status='PENDING' AND NEW.payment_status='PAID')) THEN
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
    IF OLD.fulfillment_status <> NEW.fulfillment_status AND NEW.fulfillment_status='ACCEPTED' THEN
        IF NOT ((OLD.payment_method='STRIPE' AND OLD.payment_status='PAID') OR
            (OLD.payment_method='CASH' AND (OLD.cash_mode='PAY_LATER' OR OLD.payment_status='PAID'))) OR
            (OLD.requires_staff_acceptance AND NOT NEW.staff_accepted) THEN
            RAISE EXCEPTION 'order is not eligible for KDS release';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION validate_kds_card_insert() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE st operation_order_state%ROWTYPE;
BEGIN
    SELECT * INTO st FROM operation_order_state WHERE order_id=NEW.order_id;
    IF NOT FOUND OR NOT ((st.payment_method='STRIPE' AND st.payment_status='PAID') OR
       (st.payment_method='CASH' AND (st.cash_mode='PAY_LATER' OR st.payment_status='PAID'))) OR
       (st.requires_staff_acceptance AND NOT st.staff_accepted) OR st.fulfillment_status<>'WAITING' OR
       st.session_id<>NEW.session_id OR st.tenant_id<>NEW.tenant_id OR st.store_id<>NEW.store_id OR st.table_id<>NEW.table_id THEN
       RAISE EXCEPTION 'order is not eligible for KDS release';
    END IF;
    RETURN NEW;
END;
$$;
ALTER TABLE operation_order_state DROP COLUMN payment_review_required;
