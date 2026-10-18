-- Old and new application writers lock the company parent before activating a
-- hold. Take those locks BEFORE payment-table DDL: taking the attempt lock first
-- could deadlock with a payment writer holding catalog SHARE. Keep the parents
-- locked until the backfill and activation trigger commit together.
SELECT catalog_id FROM menu_catalogs
WHERE owner_kind = 'COMPANY' ORDER BY catalog_id FOR UPDATE;

-- Retain the ON decision independently of the current company hold. Clearing
-- a hold never restores a previously issued Checkout continuation.
ALTER TABLE stripe_payment_attempts ADD COLUMN company_hold_expire_requested_at timestamptz;
CREATE INDEX stripe_payment_attempt_hold_candidates_idx
    ON stripe_payment_attempts(tenant_id,session_id,order_id)
    WHERE state NOT IN ('EXPIRED','PAID','REVIEW_REQUIRED')
      AND provider_payment_status IS DISTINCT FROM 'paid'
      AND checkout_status IS DISTINCT FROM 'expired';

CREATE FUNCTION protect_company_hold_expiry_request() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.company_hold_expire_requested_at IS NOT NULL AND
       NEW.company_hold_expire_requested_at IS DISTINCT FROM OLD.company_hold_expire_requested_at THEN
        RAISE EXCEPTION 'company hold expiry request cannot be cleared or replaced';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER stripe_payment_attempt_hold_expiry_retained
    BEFORE UPDATE ON stripe_payment_attempts
    FOR EACH ROW EXECUTE FUNCTION protect_company_hold_expiry_request();

-- The database owns this side effect so old binaries also retain expiry work.
-- Hold writers already own catalog UPDATE; taking SHARE here also orders direct
-- SQL activation against payment writers before any session or attempt lock.
CREATE FUNCTION enqueue_company_hold_checkouts(hold_tenant text, hold_product text, hold_actor text)
RETURNS void LANGUAGE plpgsql AS $$
DECLARE
    target record;
    attempt_state text;
    expiry_key text;
BEGIN
    PERFORM catalog_id FROM menu_catalogs
    WHERE tenant_id = hold_tenant AND owner_kind = 'COMPANY' FOR SHARE;

    FOR target IN
        SELECT a.order_id, a.session_id
        FROM stripe_payment_attempts a
        JOIN table_orders o USING (order_id)
        WHERE a.tenant_id = hold_tenant AND o.payment_method = 'STRIPE'
          AND a.state NOT IN ('EXPIRED', 'PAID', 'REVIEW_REQUIRED')
          AND a.provider_payment_status IS DISTINCT FROM 'paid'
          AND a.checkout_status IS DISTINCT FROM 'expired'
          AND NOT EXISTS (
              SELECT 1 FROM store_menu_bindings b
              WHERE b.tenant_id = a.tenant_id AND b.store_id = a.store_id
          )
          AND EXISTS (
              SELECT 1 FROM jsonb_array_elements(o.quote_snapshot->'quote'->'lines') l
              WHERE l->>'product_key' = hold_product
          )
        ORDER BY a.session_id, a.order_id
    LOOP
        PERFORM session_id FROM table_sessions
        WHERE session_id = target.session_id FOR UPDATE;

        -- A direct SQL writer may only hold catalog SHARE. Recheck terminal
        -- state after the session lock if a paid writer won that race.
        UPDATE stripe_payment_attempts
        SET company_hold_expire_requested_at = coalesce(company_hold_expire_requested_at, clock_timestamp()),
            continuation_revoked_at = coalesce(continuation_revoked_at, clock_timestamp()),
            updated_at = clock_timestamp()
        WHERE order_id = target.order_id
          AND state NOT IN ('EXPIRED', 'PAID', 'REVIEW_REQUIRED')
          AND provider_payment_status IS DISTINCT FROM 'paid'
          AND checkout_status IS DISTINCT FROM 'expired'
        RETURNING state INTO attempt_state;
        IF NOT FOUND THEN
            CONTINUE;
        END IF;

        expiry_key := 'company-hold-expire:' || target.order_id;
        INSERT INTO stripe_payment_work(work_id, dedupe_key, work_type, order_id)
        VALUES (md5(expiry_key), expiry_key, 'EXPIRE', target.order_id)
        ON CONFLICT (dedupe_key) DO NOTHING;

        INSERT INTO stripe_payment_attempt_events(order_id, event_key, actor_id, before_state, after_state)
        VALUES (target.order_id, expiry_key, hold_actor, attempt_state, attempt_state)
        ON CONFLICT (order_id, event_key) DO NOTHING;
    END LOOP;
END;
$$;

CREATE FUNCTION expire_checkouts_on_company_hold() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.active AND (TG_OP = 'INSERT' OR NOT OLD.active) THEN
        PERFORM enqueue_company_hold_checkouts(NEW.tenant_id, NEW.product_key, NEW.activated_by);
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER company_product_hold_checkout_expiry
    AFTER INSERT OR UPDATE OF active ON company_product_emergency_holds
    FOR EACH ROW EXECUTE FUNCTION expire_checkouts_on_company_hold();

-- The same selection and dedupe rules cover holds committed before 008.
SELECT enqueue_company_hold_checkouts(tenant_id, product_key, 'migration:008_held_checkout_expiry')
FROM company_product_emergency_holds
WHERE active
ORDER BY company_catalog_id, product_key;
