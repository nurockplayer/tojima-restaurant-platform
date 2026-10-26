ALTER TABLE staff_auth_tx
    ADD COLUMN cancelled_at timestamptz,
    ADD COLUMN completed_at timestamptz;

DELETE FROM staff_auth_tx;
ALTER TABLE staff_auth_tx ADD COLUMN browser_sha256 bytea NOT NULL;

ALTER TABLE staff_sessions
    ADD COLUMN auth_state_sha256 bytea,
    ADD COLUMN browser_sha256 bytea;

CREATE INDEX staff_sessions_auth_state_idx
    ON staff_sessions (auth_state_sha256) WHERE revoked_at IS NULL;

CREATE TABLE staff_session_tokens (
    token_sha256 bytea PRIMARY KEY CHECK (octet_length(token_sha256)=32),
    session_id uuid NOT NULL REFERENCES staff_sessions(id) ON DELETE CASCADE,
    issued_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO staff_session_tokens(token_sha256,session_id)
    SELECT token_sha256,id FROM staff_sessions;
