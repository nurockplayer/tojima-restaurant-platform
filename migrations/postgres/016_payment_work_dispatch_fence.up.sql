SET LOCAL lock_timeout = '3s';
SET LOCAL statement_timeout = '30s';

ALTER TABLE stripe_payment_work ADD COLUMN dispatched_at timestamptz NULL;

-- Earlier claims did not record dispatch, so treat them as possibly dispatched.
UPDATE stripe_payment_work SET dispatched_at=updated_at WHERE work_type='CREATE' AND attempts>0;

SET LOCAL lock_timeout = DEFAULT;
SET LOCAL statement_timeout = DEFAULT;
