-- One mutable shared cart per table session. A successful commit advances the
-- generation and clears its lines while preserving every immutable order.
CREATE TABLE table_carts (
    session_id text PRIMARY KEY REFERENCES table_sessions(session_id),
    generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, generation)
);

CREATE TABLE table_cart_lines (
    session_id text NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    line_id text NOT NULL CHECK (length(line_id) = 32),
    product_key text NOT NULL CHECK (length(product_key) BETWEEN 1 AND 100),
    product_name text NOT NULL CHECK (length(btrim(product_name)) BETWEEN 1 AND 200),
    quantity integer NOT NULL CHECK (quantity BETWEEN 1 AND 99),
    selections jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(selections) = 'array'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, generation, line_id),
    FOREIGN KEY (session_id) REFERENCES table_sessions(session_id),
    FOREIGN KEY (session_id, generation) REFERENCES table_carts(session_id, generation)
);
CREATE INDEX table_cart_lines_session_generation_idx ON table_cart_lines(session_id, generation);

ALTER TABLE table_sessions ADD CONSTRAINT table_sessions_full_scope_unique
    UNIQUE (session_id, tenant_id, store_id, table_id);

-- quote_snapshot is the immutable accepted menu/tax calculation and includes
-- private menu source provenance. Public handlers project safe fields from it.
CREATE TABLE table_orders (
    order_id text PRIMARY KEY CHECK (length(order_id) = 32),
    session_id text NOT NULL REFERENCES table_sessions(session_id),
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    table_id text NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    payment_method text NOT NULL CHECK (payment_method IN ('STRIPE', 'CASH')),
    requires_staff_acceptance boolean NOT NULL,
    catalog_id bigint NOT NULL CHECK (catalog_id > 0),
    catalog_mode text NOT NULL CHECK (catalog_mode IN ('LINKED', 'DETACHED')),
    catalog_revision bigint NOT NULL CHECK (catalog_revision > 0),
    quote_snapshot jsonb NOT NULL CHECK (jsonb_typeof(quote_snapshot) = 'object'),
    tax_snapshot jsonb NOT NULL CHECK (jsonb_typeof(tax_snapshot) = 'object'),
    total_gross_jpy bigint NOT NULL CHECK (total_gross_jpy >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (session_id, generation),
    UNIQUE (order_id, session_id, generation),
    FOREIGN KEY (tenant_id, store_id, table_id)
        REFERENCES dining_tables(tenant_id, store_id, table_id),
    CONSTRAINT table_orders_session_full_scope_fk
        FOREIGN KEY (session_id, tenant_id, store_id, table_id)
        REFERENCES table_sessions(session_id, tenant_id, store_id, table_id)
);
CREATE INDEX table_orders_session_created_idx ON table_orders(session_id, created_at DESC);

CREATE FUNCTION reject_table_order_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'committed table orders are immutable';
END;
$$;
CREATE TRIGGER table_orders_immutable
    BEFORE UPDATE OR DELETE ON table_orders FOR EACH ROW EXECUTE FUNCTION reject_table_order_mutation();

-- A key binds to one request digest for the duration of its cart generation.
-- The order table unique guard separately arbitrates distinct keys/methods.
CREATE TABLE table_cart_idempotency (
    session_id text NOT NULL REFERENCES table_sessions(session_id),
    generation bigint NOT NULL CHECK (generation > 0),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    result_version bigint NOT NULL CHECK (result_version > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, generation, idempotency_key)
);

CREATE TABLE table_order_idempotency (
    session_id text NOT NULL,
    generation bigint NOT NULL CHECK (generation > 0),
    idempotency_key text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
    request_digest bytea NOT NULL CHECK (octet_length(request_digest) = 32),
    order_id text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (session_id, generation, idempotency_key),
    FOREIGN KEY (order_id, session_id, generation)
        REFERENCES table_orders(order_id, session_id, generation)
);
CREATE TRIGGER table_order_idempotency_immutable
    BEFORE UPDATE OR DELETE ON table_order_idempotency FOR EACH ROW EXECUTE FUNCTION reject_table_order_mutation();
