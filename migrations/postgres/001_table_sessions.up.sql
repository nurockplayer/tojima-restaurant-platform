CREATE TABLE tenants (
    tenant_id text PRIMARY KEY,
    name text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE stores (
    tenant_id text NOT NULL REFERENCES tenants(tenant_id),
    store_id text NOT NULL,
    name text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    operating boolean NOT NULL DEFAULT true,
    qr_entry_mode text NOT NULL DEFAULT 'STAFF_OPEN'
        CHECK (qr_entry_mode IN ('STAFF_OPEN', 'SESSION_PIN', 'DIRECT_SCAN')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, store_id)
);

CREATE TABLE dining_tables (
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    table_id text NOT NULL,
    name text NOT NULL,
    public_locator text NOT NULL UNIQUE,
    enabled boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, store_id, table_id),
    FOREIGN KEY (tenant_id, store_id) REFERENCES stores(tenant_id, store_id)
);

CREATE TABLE table_sessions (
    session_id text PRIMARY KEY,
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    table_id text NOT NULL,
    status text NOT NULL CHECK (status IN ('OPEN', 'CLOSED')),
    qr_mode text NOT NULL CHECK (qr_mode IN ('STAFF_OPEN', 'SESSION_PIN', 'DIRECT_SCAN')),
    requires_staff_acceptance boolean NOT NULL DEFAULT false,
    opened_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL,
    opened_by text,
    closed_at timestamptz,
    closed_by text,
    grant_generation bigint NOT NULL DEFAULT 1 CHECK (grant_generation > 0),
    pin_generation bigint NOT NULL DEFAULT 1 CHECK (pin_generation > 0),
    pin_hash bytea,
    pin_expires_at timestamptz,
    pin_attempts integer NOT NULL DEFAULT 0 CHECK (pin_attempts >= 0),
    pin_locked_until timestamptz,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    FOREIGN KEY (tenant_id, store_id, table_id)
        REFERENCES dining_tables(tenant_id, store_id, table_id),
    CHECK ((status = 'OPEN' AND closed_at IS NULL) OR (status = 'CLOSED' AND closed_at IS NOT NULL)),
    CHECK ((status = 'CLOSED' AND pin_hash IS NULL AND pin_expires_at IS NULL)
        OR (status = 'OPEN' AND qr_mode = 'SESSION_PIN' AND pin_hash IS NOT NULL AND pin_expires_at IS NOT NULL)
        OR (status = 'OPEN' AND qr_mode <> 'SESSION_PIN' AND pin_hash IS NULL AND pin_expires_at IS NULL))
);

CREATE UNIQUE INDEX one_open_session_per_physical_table
    ON table_sessions(tenant_id, store_id, table_id) WHERE status = 'OPEN';
CREATE INDEX table_sessions_scope_idx ON table_sessions(tenant_id, store_id, table_id, opened_at DESC);

CREATE FUNCTION protect_table_session_snapshot() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.tenant_id <> NEW.tenant_id OR OLD.store_id <> NEW.store_id OR OLD.table_id <> NEW.table_id
       OR OLD.qr_mode <> NEW.qr_mode OR OLD.requires_staff_acceptance <> NEW.requires_staff_acceptance
       OR OLD.opened_at <> NEW.opened_at OR OLD.expires_at <> NEW.expires_at THEN
        RAISE EXCEPTION 'table session scope and entry snapshot are immutable';
    END IF;
    IF OLD.status = 'CLOSED' AND NEW.status <> 'CLOSED' THEN
        RAISE EXCEPTION 'closed table sessions cannot be reopened';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER table_session_snapshot_immutable
    BEFORE UPDATE ON table_sessions FOR EACH ROW EXECUTE FUNCTION protect_table_session_snapshot();

CREATE TABLE table_join_grants (
    token_hash bytea PRIMARY KEY,
    session_id text NOT NULL REFERENCES table_sessions(session_id),
    generation bigint NOT NULL CHECK (generation > 0),
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX table_join_grants_session_idx ON table_join_grants(session_id, expires_at);

-- One current window row per physical table bounds capability issuance across
-- all app instances and both guest entry endpoints.
CREATE TABLE table_join_grant_limits (
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    table_id text NOT NULL,
    window_started_at timestamptz NOT NULL,
    grant_count integer NOT NULL CHECK (grant_count > 0),
    PRIMARY KEY (tenant_id, store_id, table_id),
    FOREIGN KEY (tenant_id, store_id, table_id)
        REFERENCES dining_tables(tenant_id, store_id, table_id)
);
