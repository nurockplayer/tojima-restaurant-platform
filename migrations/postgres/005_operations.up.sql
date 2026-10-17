-- P-79 payment, receipt, fulfillment and KDS state. Accepted orders remain
-- immutable in table_orders; operational transitions are append-only events.
CREATE TABLE store_cash_settings (
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    enabled boolean NOT NULL,
    mode text NOT NULL CHECK (mode IN ('PREPAID','PAY_LATER')),
    version bigint NOT NULL CHECK (version > 0),
    updated_by text NOT NULL CHECK (length(btrim(updated_by)) > 0),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id,store_id),
    FOREIGN KEY (tenant_id,store_id) REFERENCES stores(tenant_id,store_id)
);

CREATE TABLE store_cash_settings_audit (
    audit_id bigserial PRIMARY KEY,
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    actor_id text NOT NULL,
    before_enabled boolean,
    before_mode text,
    before_version bigint NOT NULL CHECK (before_version >= 0),
    after_enabled boolean NOT NULL,
    after_mode text NOT NULL CHECK (after_mode IN ('PREPAID','PAY_LATER')),
    after_version bigint NOT NULL CHECK (after_version > before_version),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (tenant_id,store_id) REFERENCES stores(tenant_id,store_id)
);

CREATE TABLE operation_order_state (
    order_id text PRIMARY KEY,
    session_id text NOT NULL,
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    table_id text NOT NULL,
    generation bigint NOT NULL,
    payment_method text NOT NULL CHECK (payment_method IN ('STRIPE','CASH')),
    payment_status text NOT NULL CHECK (payment_status IN ('UNPAID','PENDING','PAID','UNKNOWN','REFUND_PENDING','REFUNDED')),
    cash_mode text CHECK (cash_mode IN ('PREPAID','PAY_LATER')),
    fulfillment_status text NOT NULL CHECK (fulfillment_status IN ('WAITING','ACCEPTED','PREPARING','READY','SERVED','CANCELLED')),
    requires_staff_acceptance boolean NOT NULL,
    staff_accepted boolean NOT NULL,
    operational_hold boolean NOT NULL DEFAULT false,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (order_id,session_id,generation) REFERENCES table_orders(order_id,session_id,generation),
    FOREIGN KEY (session_id,tenant_id,store_id,table_id) REFERENCES table_sessions(session_id,tenant_id,store_id,table_id),
    CHECK ((payment_method='CASH' AND cash_mode IS NOT NULL AND payment_status IN ('UNPAID','PAID'))
        OR (payment_method='STRIPE' AND cash_mode IS NULL AND payment_status IN ('PENDING','PAID','UNKNOWN','REFUND_PENDING','REFUNDED'))),
    CHECK (NOT staff_accepted OR requires_staff_acceptance),
    UNIQUE (order_id,session_id,tenant_id,store_id,table_id)
);
CREATE INDEX operation_order_state_scope_idx ON operation_order_state(tenant_id,store_id,table_id,session_id,updated_at DESC);

CREATE TABLE operation_order_events (
    event_id bigserial PRIMARY KEY,
    order_id text NOT NULL REFERENCES operation_order_state(order_id),
    actor_id text NOT NULL,
    event_type text NOT NULL CHECK (event_type IN ('ORDER_CREATED','STAFF_ACCEPTED','PAYMENT_SETTLED','KDS_RELEASED','FULFILLMENT_ADVANCED','STRIPE_PAID_TEST_FIXTURE')),
    before_payment_status text,
    after_payment_status text,
    before_fulfillment_status text,
    after_fulfillment_status text,
    state_version bigint NOT NULL CHECK (state_version > 0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX operation_order_events_order_idx ON operation_order_events(order_id,event_id);

CREATE TABLE cash_settlements (
    receipt_id text PRIMARY KEY CHECK (length(receipt_id)=32),
    session_id text NOT NULL,
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    table_id text NOT NULL,
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest)=32),
    order_ids text[] NOT NULL CHECK (cardinality(order_ids)>0),
    due_jpy bigint NOT NULL CHECK (due_jpy>=0),
    tendered_jpy bigint NOT NULL CHECK (tendered_jpy>=due_jpy),
    change_jpy bigint NOT NULL CHECK (change_jpy=tendered_jpy-due_jpy),
    cashier_id text NOT NULL CHECK (length(btrim(cashier_id))>0),
    register_session_ref text NOT NULL CHECK (length(btrim(register_session_ref)) BETWEEN 1 AND 200),
    tax_invoice jsonb NOT NULL CHECK (jsonb_typeof(tax_invoice)='object'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (session_id,tenant_id,store_id,table_id) REFERENCES table_sessions(session_id,tenant_id,store_id,table_id),
    UNIQUE (session_id,idempotency_key),
    UNIQUE (receipt_id,session_id,tenant_id,store_id,table_id)
);
CREATE INDEX cash_settlements_scope_idx ON cash_settlements(session_id,created_at DESC);

CREATE TABLE cash_settlement_allocations (
    receipt_id text NOT NULL,
    order_id text NOT NULL UNIQUE,
    session_id text NOT NULL,
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    table_id text NOT NULL,
    amount_jpy bigint NOT NULL CHECK (amount_jpy>=0),
    PRIMARY KEY (receipt_id,order_id),
    FOREIGN KEY (receipt_id,session_id,tenant_id,store_id,table_id) REFERENCES cash_settlements(receipt_id,session_id,tenant_id,store_id,table_id),
    FOREIGN KEY (order_id,session_id,tenant_id,store_id,table_id) REFERENCES operation_order_state(order_id,session_id,tenant_id,store_id,table_id)
);

CREATE TABLE operation_kds_cards (
    order_id text PRIMARY KEY,
    session_id text NOT NULL,
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    table_id text NOT NULL,
    released_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (order_id,session_id,tenant_id,store_id,table_id) REFERENCES operation_order_state(order_id,session_id,tenant_id,store_id,table_id)
);

CREATE TABLE operation_outbox (
    outbox_id bigserial PRIMARY KEY,
    order_id text NOT NULL REFERENCES operation_kds_cards(order_id),
    event_type text NOT NULL CHECK (event_type='KDS_RELEASED'),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts>=0),
    delivered_at timestamptz,
    UNIQUE (order_id,event_type)
);

CREATE TABLE operation_action_idempotency (
    session_id text NOT NULL,
    order_id text NOT NULL,
    action text NOT NULL CHECK (action IN ('ACCEPT','FULFILLMENT')),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest)=32),
    result_version bigint NOT NULL CHECK (result_version>0),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (session_id,order_id,action,idempotency_key),
    FOREIGN KEY (order_id) REFERENCES table_orders(order_id)
);

CREATE FUNCTION reject_operation_history_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'operation history is append-only';
END;
$$;
CREATE TRIGGER store_cash_settings_audit_immutable BEFORE UPDATE OR DELETE ON store_cash_settings_audit FOR EACH ROW EXECUTE FUNCTION reject_operation_history_mutation();
CREATE TRIGGER operation_order_events_immutable BEFORE UPDATE OR DELETE ON operation_order_events FOR EACH ROW EXECUTE FUNCTION reject_operation_history_mutation();
CREATE TRIGGER cash_settlements_immutable BEFORE UPDATE OR DELETE ON cash_settlements FOR EACH ROW EXECUTE FUNCTION reject_operation_history_mutation();
CREATE TRIGGER cash_settlement_allocations_immutable BEFORE UPDATE OR DELETE ON cash_settlement_allocations FOR EACH ROW EXECUTE FUNCTION reject_operation_history_mutation();
CREATE TRIGGER operation_kds_cards_immutable BEFORE UPDATE OR DELETE ON operation_kds_cards FOR EACH ROW EXECUTE FUNCTION reject_operation_history_mutation();
CREATE TRIGGER operation_action_idempotency_immutable BEFORE UPDATE OR DELETE ON operation_action_idempotency FOR EACH ROW EXECUTE FUNCTION reject_operation_history_mutation();

CREATE FUNCTION validate_operation_order_transition() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER operation_order_state_transition BEFORE UPDATE ON operation_order_state FOR EACH ROW EXECUTE FUNCTION validate_operation_order_transition();

CREATE FUNCTION validate_kds_card_insert() RETURNS trigger LANGUAGE plpgsql AS $$
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
CREATE TRIGGER operation_kds_card_gate BEFORE INSERT ON operation_kds_cards FOR EACH ROW EXECUTE FUNCTION validate_kds_card_insert();

CREATE FUNCTION reject_operation_state_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'operation state cannot be deleted'; END;
$$;
CREATE TRIGGER operation_order_state_no_delete BEFORE DELETE ON operation_order_state FOR EACH ROW EXECUTE FUNCTION reject_operation_state_delete();
