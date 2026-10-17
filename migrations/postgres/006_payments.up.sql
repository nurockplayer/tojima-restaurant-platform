-- P-80 durable Stripe attempts, transactional work queue, and review gate.
-- Merchant credential material stays in an external secret store.
CREATE TABLE store_stripe_merchants (
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    enabled boolean NOT NULL DEFAULT false,
    provider_mode text NOT NULL CHECK (provider_mode='CONNECTED_DIRECT_TEST_FIXTURE'),
    stripe_account_id text NOT NULL CHECK (stripe_account_id ~ '^acct_[A-Za-z0-9]+$'),
    credential_ref text NOT NULL CHECK (length(btrim(credential_ref)) BETWEEN 1 AND 200),
    webhook_endpoint_ref text NOT NULL CHECK (length(btrim(webhook_endpoint_ref)) BETWEEN 1 AND 200),
    api_version text NOT NULL CHECK (length(btrim(api_version)) BETWEEN 1 AND 80),
    version bigint NOT NULL CHECK (version > 0),
    updated_by text NOT NULL CHECK (length(btrim(updated_by)) BETWEEN 1 AND 200),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id,store_id),
    FOREIGN KEY (tenant_id,store_id) REFERENCES stores(tenant_id,store_id)
);

CREATE TABLE store_stripe_merchants_audit (
    audit_id bigserial PRIMARY KEY,
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    actor_id text NOT NULL,
    before_enabled boolean,
    after_enabled boolean NOT NULL,
    before_account_id text,
    after_account_id text NOT NULL,
    before_mode text,
    after_mode text NOT NULL,
    before_credential_ref text,
    after_credential_ref text NOT NULL,
    before_endpoint_ref text,
    after_endpoint_ref text NOT NULL,
    before_version bigint NOT NULL CHECK (before_version >= 0),
    after_version bigint NOT NULL CHECK (after_version > before_version),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (tenant_id,store_id) REFERENCES stores(tenant_id,store_id)
);

CREATE TABLE stripe_payment_attempts (
    order_id text PRIMARY KEY,
    attempt_id text NOT NULL UNIQUE CHECK (length(btrim(attempt_id)) BETWEEN 1 AND 255),
    session_id text NOT NULL,
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    table_id text NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    amount_jpy bigint NOT NULL CHECK (amount_jpy > 0),
    currency text NOT NULL CHECK (currency='jpy'),
    tax_snapshot jsonb NOT NULL CHECK (jsonb_typeof(tax_snapshot)='object'),
    provider_mode text NOT NULL CHECK (provider_mode='CONNECTED_DIRECT_TEST_FIXTURE'),
    stripe_account_id text NOT NULL CHECK (stripe_account_id ~ '^acct_[A-Za-z0-9]+$'),
    credential_ref text NOT NULL CHECK (length(btrim(credential_ref)) BETWEEN 1 AND 200),
    merchant_version bigint NOT NULL CHECK (merchant_version > 0),
    request_profile_fingerprint bytea NOT NULL CHECK (octet_length(request_profile_fingerprint)=32),
    api_version text NOT NULL CHECK (length(btrim(api_version)) BETWEEN 1 AND 80),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 255),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest)=32),
    checkout_proof_hash bytea NOT NULL CHECK (octet_length(checkout_proof_hash)=32),
    checkout_proof_expires_at timestamptz NOT NULL,
    continuation_expires_at timestamptz NOT NULL,
    continuation_revoked_at timestamptz,
    state text NOT NULL CHECK (state IN ('CREATE_PENDING','OPEN','PROCESSING','UNKNOWN','PAID','EXPIRED','FAILED','REVIEW_REQUIRED')),
    checkout_session_id text,
    payment_intent_id text,
    checkout_status text CHECK (checkout_status IN ('open','complete','expired')),
    provider_payment_status text CHECK (provider_payment_status IN ('unpaid','paid','no_payment_required')),
    payment_intent_status text CHECK (payment_intent_status IS NULL OR length(payment_intent_status) BETWEEN 1 AND 80),
    provider_expires_at timestamptz,
    retry_not_after timestamptz NOT NULL,
    payment_review_required boolean NOT NULL DEFAULT false,
    last_reconciled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (order_id,session_id,generation) REFERENCES table_orders(order_id,session_id,generation),
    FOREIGN KEY (session_id,tenant_id,store_id,table_id) REFERENCES table_sessions(session_id,tenant_id,store_id,table_id),
    CHECK (continuation_expires_at <= checkout_proof_expires_at),
    CHECK ((state IN ('PAID','REVIEW_REQUIRED') AND provider_payment_status IS NOT DISTINCT FROM 'paid') OR
           (state NOT IN ('PAID','REVIEW_REQUIRED') AND provider_payment_status IS DISTINCT FROM 'paid')),
    CHECK ((state='REVIEW_REQUIRED') = payment_review_required),
    UNIQUE (stripe_account_id,idempotency_key),
    UNIQUE (order_id,tenant_id,store_id,attempt_id)
);
CREATE UNIQUE INDEX stripe_payment_attempt_checkout_session_uq
    ON stripe_payment_attempts(stripe_account_id,checkout_session_id) WHERE checkout_session_id IS NOT NULL;
CREATE UNIQUE INDEX stripe_payment_attempt_payment_intent_uq
    ON stripe_payment_attempts(stripe_account_id,payment_intent_id) WHERE payment_intent_id IS NOT NULL;
CREATE INDEX stripe_payment_attempt_session_idx ON stripe_payment_attempts(session_id,created_at);

CREATE TABLE stripe_payment_attempt_events (
    event_row_id bigserial PRIMARY KEY,
    order_id text NOT NULL,
    event_key text NOT NULL,
    actor_id text NOT NULL CHECK (length(btrim(actor_id)) BETWEEN 1 AND 200),
    before_state text,
    after_state text NOT NULL,
    safe_error_code text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    UNIQUE (order_id,event_key)
);

CREATE TABLE stripe_webhook_events (
    webhook_row_id bigserial PRIMARY KEY,
    stripe_account_id text NOT NULL CHECK (stripe_account_id ~ '^acct_[A-Za-z0-9]+$'),
    stripe_event_id text NOT NULL CHECK (length(btrim(stripe_event_id)) BETWEEN 1 AND 255),
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    order_id text NOT NULL REFERENCES stripe_payment_attempts(order_id),
    attempt_id text NOT NULL CHECK (length(btrim(attempt_id)) BETWEEN 1 AND 255),
    event_type text NOT NULL CHECK (length(btrim(event_type)) BETWEEN 1 AND 200),
    provider_object_id text NOT NULL CHECK (length(btrim(provider_object_id)) BETWEEN 1 AND 255),
    checkout_session_id text,
    payment_intent_id text,
    api_version text NOT NULL CHECK (length(btrim(api_version)) BETWEEN 1 AND 80),
    livemode boolean NOT NULL DEFAULT false CHECK (NOT livemode),
    amount_jpy bigint NOT NULL CHECK (amount_jpy > 0),
    currency text NOT NULL CHECK (currency='jpy'),
    checkout_status text CHECK (checkout_status IN ('open','complete','expired')),
    payment_status text CHECK (payment_status IN ('unpaid','paid','no_payment_required')),
    payment_intent_status text CHECK (payment_intent_status IS NULL OR length(payment_intent_status) BETWEEN 1 AND 80),
    correlation_metadata jsonb NOT NULL CHECK (jsonb_typeof(correlation_metadata)='object'),
    provider_expires_at timestamptz,
    event_created_at timestamptz NOT NULL,
    received_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    processed_at timestamptz,
    safe_error_code text,
    UNIQUE (stripe_account_id,stripe_event_id),
    FOREIGN KEY (order_id,tenant_id,store_id,attempt_id) REFERENCES stripe_payment_attempts(order_id,tenant_id,store_id,attempt_id)
);

CREATE TABLE stripe_payment_work (
    work_id text PRIMARY KEY CHECK (length(work_id)=32),
    dedupe_key text NOT NULL UNIQUE CHECK (length(btrim(dedupe_key)) BETWEEN 1 AND 300),
    work_type text NOT NULL CHECK (work_type IN ('CREATE','WEBHOOK','EXPIRE')),
    order_id text REFERENCES stripe_payment_attempts(order_id),
    webhook_row_id bigint REFERENCES stripe_webhook_events(webhook_row_id),
    status text NOT NULL DEFAULT 'READY' CHECK (status IN ('READY','LEASED','DONE')),
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    lease_token text,
    lease_until timestamptz,
    safe_error_code text,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    CHECK ((work_type='WEBHOOK' AND webhook_row_id IS NOT NULL AND order_id IS NULL) OR
           (work_type<>'WEBHOOK' AND order_id IS NOT NULL AND webhook_row_id IS NULL)),
    CHECK ((status='LEASED') = (lease_token IS NOT NULL AND lease_until IS NOT NULL))
);
CREATE UNIQUE INDEX stripe_payment_work_create_once_uq ON stripe_payment_work(order_id) WHERE work_type='CREATE';
CREATE INDEX stripe_payment_work_claim_idx ON stripe_payment_work(status,available_at,created_at);

ALTER TABLE operation_order_state ADD COLUMN payment_review_required boolean NOT NULL DEFAULT false;
ALTER TABLE operation_order_events DROP CONSTRAINT operation_order_events_event_type_check;
ALTER TABLE operation_order_events ADD CONSTRAINT operation_order_events_event_type_check
    CHECK (event_type IN ('ORDER_CREATED','STAFF_ACCEPTED','PAYMENT_SETTLED','KDS_RELEASED','FULFILLMENT_ADVANCED','STRIPE_PAID_TEST_FIXTURE','STRIPE_PAID_VERIFIED','STRIPE_REVIEW_REQUIRED','STRIPE_REVIEW_CLEARED'));

CREATE FUNCTION reject_payment_history_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'payment history is append-only';
END;
$$;
CREATE TRIGGER store_stripe_merchants_audit_immutable BEFORE UPDATE OR DELETE ON store_stripe_merchants_audit FOR EACH ROW EXECUTE FUNCTION reject_payment_history_mutation();
CREATE TRIGGER stripe_payment_attempt_events_immutable BEFORE UPDATE OR DELETE ON stripe_payment_attempt_events FOR EACH ROW EXECUTE FUNCTION reject_payment_history_mutation();
CREATE FUNCTION reject_stripe_webhook_event_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.webhook_row_id<>OLD.webhook_row_id OR NEW.stripe_account_id<>OLD.stripe_account_id OR NEW.stripe_event_id<>OLD.stripe_event_id
       OR NEW.tenant_id<>OLD.tenant_id OR NEW.store_id<>OLD.store_id OR NEW.order_id<>OLD.order_id OR NEW.attempt_id<>OLD.attempt_id
       OR NEW.event_type<>OLD.event_type OR NEW.provider_object_id<>OLD.provider_object_id OR NEW.checkout_session_id IS DISTINCT FROM OLD.checkout_session_id
       OR NEW.payment_intent_id IS DISTINCT FROM OLD.payment_intent_id OR NEW.api_version<>OLD.api_version OR NEW.livemode<>OLD.livemode
       OR NEW.amount_jpy<>OLD.amount_jpy OR NEW.currency<>OLD.currency OR NEW.checkout_status IS DISTINCT FROM OLD.checkout_status
       OR NEW.payment_status IS DISTINCT FROM OLD.payment_status OR NEW.payment_intent_status IS DISTINCT FROM OLD.payment_intent_status
       OR NEW.correlation_metadata<>OLD.correlation_metadata OR NEW.provider_expires_at IS DISTINCT FROM OLD.provider_expires_at
       OR NEW.event_created_at<>OLD.event_created_at OR NEW.received_at<>OLD.received_at THEN
       RAISE EXCEPTION 'verified webhook event is immutable';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER stripe_webhook_events_immutable_fields BEFORE UPDATE ON stripe_webhook_events FOR EACH ROW EXECUTE FUNCTION reject_stripe_webhook_event_mutation();
CREATE TRIGGER stripe_webhook_events_no_delete BEFORE DELETE ON stripe_webhook_events FOR EACH ROW EXECUTE FUNCTION reject_payment_history_mutation();

CREATE FUNCTION protect_stripe_attempt_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.order_id<>OLD.order_id OR NEW.attempt_id<>OLD.attempt_id OR NEW.session_id<>OLD.session_id OR NEW.tenant_id<>OLD.tenant_id OR NEW.store_id<>OLD.store_id
       OR NEW.table_id<>OLD.table_id OR NEW.generation<>OLD.generation OR NEW.amount_jpy<>OLD.amount_jpy OR NEW.currency<>OLD.currency
       OR NEW.tax_snapshot<>OLD.tax_snapshot OR NEW.provider_mode<>OLD.provider_mode OR NEW.stripe_account_id<>OLD.stripe_account_id
       OR NEW.credential_ref<>OLD.credential_ref OR NEW.merchant_version<>OLD.merchant_version
       OR NEW.request_profile_fingerprint<>OLD.request_profile_fingerprint OR NEW.api_version<>OLD.api_version
       OR NEW.idempotency_key<>OLD.idempotency_key OR NEW.request_digest<>OLD.request_digest
       OR NEW.checkout_proof_hash<>OLD.checkout_proof_hash OR NEW.checkout_proof_expires_at<>OLD.checkout_proof_expires_at
       OR NEW.continuation_expires_at<>OLD.continuation_expires_at OR NEW.retry_not_after<>OLD.retry_not_after
       OR NEW.created_at<>OLD.created_at THEN
       RAISE EXCEPTION 'Stripe attempt snapshot is immutable';
    END IF;
    IF OLD.checkout_session_id IS NOT NULL AND NEW.checkout_session_id IS DISTINCT FROM OLD.checkout_session_id THEN RAISE EXCEPTION 'Stripe Checkout Session binding is immutable'; END IF;
    IF OLD.payment_intent_id IS NOT NULL AND NEW.payment_intent_id IS DISTINCT FROM OLD.payment_intent_id THEN RAISE EXCEPTION 'Stripe PaymentIntent binding is immutable'; END IF;
    IF OLD.state IN ('PAID','REVIEW_REQUIRED') AND NEW.state<>OLD.state THEN RAISE EXCEPTION 'paid or review-required Stripe attempt is terminal'; END IF;
    IF OLD.payment_review_required AND NOT NEW.payment_review_required THEN RAISE EXCEPTION 'payment review requires governed resolution'; END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER stripe_payment_attempt_snapshot_immutable BEFORE UPDATE ON stripe_payment_attempts FOR EACH ROW EXECUTE FUNCTION protect_stripe_attempt_snapshot();
CREATE FUNCTION reject_stripe_attempt_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'Stripe attempt cannot be deleted'; END;
$$;
CREATE TRIGGER stripe_payment_attempt_no_delete BEFORE DELETE ON stripe_payment_attempts FOR EACH ROW EXECUTE FUNCTION reject_stripe_attempt_delete();

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
        IF NOT EXISTS (SELECT 1 FROM operation_order_events e WHERE e.order_id=OLD.order_id AND e.event_type='STRIPE_REVIEW_CLEARED' AND e.state_version=NEW.version) THEN
            RAISE EXCEPTION 'payment review requires a same-version resolution event';
        END IF;
    END IF;
    IF OLD.payment_review_required IS DISTINCT FROM NEW.payment_review_required AND NEW.payment_review_required THEN
        IF NOT EXISTS (SELECT 1 FROM operation_order_events e WHERE e.order_id=OLD.order_id AND e.event_type='STRIPE_REVIEW_REQUIRED' AND e.state_version=NEW.version) THEN
            RAISE EXCEPTION 'payment review requires a same-version audit event';
        END IF;
    END IF;
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

CREATE OR REPLACE FUNCTION validate_kds_card_insert() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE st operation_order_state%ROWTYPE;
BEGIN
    SELECT * INTO st FROM operation_order_state WHERE order_id=NEW.order_id;
    IF NOT FOUND OR NOT ((st.payment_method='STRIPE' AND st.payment_status='PAID') OR
       (st.payment_method='CASH' AND (st.cash_mode='PAY_LATER' OR st.payment_status='PAID'))) OR
       (st.requires_staff_acceptance AND NOT st.staff_accepted) OR st.payment_review_required OR st.operational_hold OR st.fulfillment_status<>'WAITING' OR
       st.session_id<>NEW.session_id OR st.tenant_id<>NEW.tenant_id OR st.store_id<>NEW.store_id OR st.table_id<>NEW.table_id THEN
       RAISE EXCEPTION 'order is not eligible for KDS release';
    END IF;
    RETURN NEW;
END;
$$;
