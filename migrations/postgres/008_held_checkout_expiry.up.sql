-- Retain the ON decision independently of the current company hold. Clearing
-- a hold never restores a previously issued Checkout continuation.
ALTER TABLE stripe_payment_attempts ADD COLUMN company_hold_expire_requested_at timestamptz;
CREATE INDEX stripe_payment_attempt_hold_candidates_idx
    ON stripe_payment_attempts(tenant_id,session_id,order_id)
    WHERE state IN ('CREATE_PENDING','OPEN','UNKNOWN','PROCESSING')
       OR (state='FAILED' AND checkout_session_id IS NOT NULL);

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
