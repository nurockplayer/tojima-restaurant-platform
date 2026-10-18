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

-- A company hold may predate this migration. Preserve the same selection as
-- enqueueHeldCheckoutsTx so those old Checkouts receive the same durable work.
WITH eligible AS (
    SELECT DISTINCT a.order_id, a.state
    FROM stripe_payment_attempts a
    JOIN table_orders o USING (order_id)
    JOIN company_product_emergency_holds h
      ON h.tenant_id = a.tenant_id AND h.active
    WHERE o.payment_method = 'STRIPE'
      AND a.state NOT IN ('EXPIRED', 'PAID', 'REVIEW_REQUIRED')
      AND a.provider_payment_status IS DISTINCT FROM 'paid'
      AND a.checkout_status IS DISTINCT FROM 'expired'
      AND NOT EXISTS (
          SELECT 1 FROM store_menu_bindings b
          WHERE b.tenant_id = a.tenant_id AND b.store_id = a.store_id
      )
      AND EXISTS (
          SELECT 1
          FROM jsonb_array_elements(o.quote_snapshot->'quote'->'lines') l
          WHERE l->>'product_key' = h.product_key
      )
), marked AS (
    UPDATE stripe_payment_attempts a
    SET company_hold_expire_requested_at = coalesce(a.company_hold_expire_requested_at, clock_timestamp()),
        continuation_revoked_at = coalesce(a.continuation_revoked_at, clock_timestamp()),
        updated_at = clock_timestamp()
    FROM eligible e
    WHERE a.order_id = e.order_id
    RETURNING a.order_id, a.state
), work AS (
    INSERT INTO stripe_payment_work(work_id, dedupe_key, work_type, order_id)
    SELECT md5('company-hold-expire:' || m.order_id),
           'company-hold-expire:' || m.order_id, 'EXPIRE', m.order_id
    FROM marked m
    ON CONFLICT (dedupe_key) DO NOTHING
    RETURNING order_id
)
INSERT INTO stripe_payment_attempt_events(order_id, event_key, actor_id, before_state, after_state)
SELECT m.order_id, 'company-hold-expire:' || m.order_id,
       'migration:008_held_checkout_expiry', m.state, m.state
FROM marked m
ON CONFLICT (order_id, event_key) DO NOTHING;
