ALTER TABLE staff_auth_tx
    ADD COLUMN cancelled_at timestamptz,
    ADD COLUMN completed_at timestamptz;

ALTER TABLE staff_sessions
    ADD COLUMN auth_state_sha256 bytea,
    ADD COLUMN previous_token_sha256 bytea;

CREATE INDEX staff_sessions_auth_state_idx
    ON staff_sessions (auth_state_sha256) WHERE revoked_at IS NULL;
CREATE INDEX staff_sessions_previous_token_idx
    ON staff_sessions (previous_token_sha256) WHERE previous_token_sha256 IS NOT NULL;
