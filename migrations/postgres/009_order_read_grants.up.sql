-- Separate capability; no raw bearer material or checkout proof is persisted.
ALTER TABLE table_orders ADD CONSTRAINT table_orders_read_scope_unique
    UNIQUE (order_id,session_id,tenant_id,store_id,table_id);
CREATE TABLE order_read_grants (
    token_hash bytea PRIMARY KEY CHECK (octet_length(token_hash)=32),
    order_id text NOT NULL,
    session_id text NOT NULL,
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    table_id text NOT NULL,
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL CHECK (expires_at>created_at),
    revoked_at timestamptz,
    FOREIGN KEY (order_id,session_id,tenant_id,store_id,table_id)
        REFERENCES table_orders(order_id,session_id,tenant_id,store_id,table_id)
);
CREATE INDEX order_read_grants_order_idx ON order_read_grants(order_id);
