DROP INDEX staff_sessions_previous_token_idx;
DROP INDEX staff_sessions_auth_state_idx;
ALTER TABLE staff_sessions
    DROP COLUMN previous_token_sha256,
    DROP COLUMN auth_state_sha256;
ALTER TABLE staff_auth_tx
    DROP COLUMN completed_at,
    DROP COLUMN cancelled_at;
