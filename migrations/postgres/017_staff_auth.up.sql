CREATE TABLE staff_subjects (
    subject_id text PRIMARY KEY CHECK (btrim(subject_id) <> ''),
    created_at timestamptz NOT NULL DEFAULT now(),
    hold_at timestamptz,
    hold_reason text,
    restored_at timestamptz,
    auth_epoch bigint NOT NULL DEFAULT 0,
    epoch_changed_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((hold_at IS NULL) = (hold_reason IS NULL))
);

CREATE TABLE staff_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    token_sha256 bytea NOT NULL UNIQUE CHECK (octet_length(token_sha256) = 32),
    subject_id text NOT NULL REFERENCES staff_subjects(subject_id),
    idp_sid text NOT NULL CHECK (btrim(idp_sid) <> ''),
    strong_login boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    absolute_expires_at timestamptz NOT NULL,
    last_strong_auth_at timestamptz,
    revoked_at timestamptz,
    revoked_reason text,
    CHECK (absolute_expires_at > created_at AND absolute_expires_at <= created_at + interval '30 days'),
    CHECK (strong_login OR last_strong_auth_at IS NULL),
    CHECK ((revoked_at IS NULL) = (revoked_reason IS NULL))
);
CREATE INDEX staff_sessions_subject_live_idx ON staff_sessions (subject_id) WHERE revoked_at IS NULL;
CREATE INDEX staff_sessions_idp_sid_live_idx ON staff_sessions (idp_sid) WHERE revoked_at IS NULL;

CREATE TABLE staff_auth_tx (
    state_sha256 bytea PRIMARY KEY CHECK (octet_length(state_sha256) = 32),
    binding_sha256 bytea NOT NULL CHECK (octet_length(binding_sha256) = 32),
    nonce text NOT NULL CHECK (btrim(nonce) <> ''),
    pkce_verifier text NOT NULL CHECK (btrim(pkce_verifier) <> ''),
    purpose text NOT NULL CHECK (purpose IN ('login', 'step_up')),
    session_id uuid REFERENCES staff_sessions(id),
    subject_id text,
    return_path text NOT NULL CHECK (left(return_path, 1) = '/' AND left(return_path, 2) NOT IN ('//', '/\')),
    created_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    used_at timestamptz,
    CHECK (expires_at > created_at AND expires_at <= created_at + interval '10 minutes'),
    CHECK ((purpose = 'login' AND session_id IS NULL AND subject_id IS NULL)
        OR (purpose = 'step_up' AND session_id IS NOT NULL AND subject_id IS NOT NULL))
);

-- IdP sessions ended by back-channel logout; a delayed login callback for one
-- of them is refused. Kept 31 days (longer than any staff session).
CREATE TABLE staff_revoked_idp_sessions (
    idp_sid text PRIMARY KEY CHECK (btrim(idp_sid) <> ''),
    revoked_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE staff_logout_jti (
    jti text PRIMARY KEY CHECK (btrim(jti) <> ''),
    seen_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE staff_memberships (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_id text NOT NULL REFERENCES staff_subjects(subject_id),
    tenant_id text NOT NULL CHECK (btrim(tenant_id) <> ''),
    store_id text CHECK (store_id IS NULL OR btrim(store_id) <> ''),
    role text NOT NULL CHECK (role IN ('STAFF_CASHIER', 'STORE_MANAGER', 'KITCHEN', 'TENANT_ADMIN', 'FINANCE_VIEWER')),
    granted_at timestamptz NOT NULL DEFAULT now(),
    granted_by text NOT NULL CHECK (btrim(granted_by) <> ''),
    revoked_at timestamptz,
    revoked_by text,
    CHECK (store_id IS NOT NULL OR role = 'TENANT_ADMIN'),
    CHECK ((revoked_at IS NULL) = (revoked_by IS NULL))
);
CREATE UNIQUE INDEX staff_memberships_live_uq
    ON staff_memberships (subject_id, tenant_id, coalesce(store_id, ''), role) WHERE revoked_at IS NULL;
CREATE INDEX staff_memberships_lookup_idx ON staff_memberships (subject_id, tenant_id) WHERE revoked_at IS NULL;

CREATE TABLE staff_auth_events (
    id bigserial PRIMARY KEY,
    at timestamptz NOT NULL DEFAULT now(),
    subject_id text,
    session_id uuid,
    kind text NOT NULL CHECK (kind IN ('login', 'login_rejected', 'logout', 'step_up', 'session_revoked',
        'backchannel_revoke', 'hold', 'restore', 'membership_grant', 'membership_revoke')),
    detail jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(detail) = 'object')
);

-- Append-only: no UPDATE; DELETE only past the 90-day minimum retention.
CREATE FUNCTION staff_auth_events_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'UPDATE' THEN
        RAISE EXCEPTION 'staff_auth_events is append-only';
    END IF;
    IF OLD.at > now() - interval '90 days' THEN
        RAISE EXCEPTION 'staff_auth_events rows are retained for at least 90 days';
    END IF;
    RETURN OLD;
END $$;
CREATE TRIGGER staff_auth_events_guard BEFORE UPDATE OR DELETE ON staff_auth_events
    FOR EACH ROW EXECUTE FUNCTION staff_auth_events_guard();

CREATE FUNCTION staff_auth_events_truncate_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'staff_auth_events is append-only';
    RETURN NULL;
END $$;
CREATE TRIGGER staff_auth_events_truncate_guard BEFORE TRUNCATE ON staff_auth_events
    FOR EACH STATEMENT EXECUTE FUNCTION staff_auth_events_truncate_guard();
