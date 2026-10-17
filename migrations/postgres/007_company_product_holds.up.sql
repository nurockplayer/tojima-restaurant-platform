ALTER TABLE menu_admin_audit
    ADD COLUMN request_key text,
    ADD COLUMN request_fingerprint bytea,
    ADD COLUMN after_json jsonb;

ALTER TABLE menu_admin_audit
    ADD CONSTRAINT menu_admin_audit_request_replay_shape CHECK (
        (request_key IS NULL AND request_fingerprint IS NULL AND after_json IS NULL)
        OR
        (request_key IS NOT NULL AND length(request_key) BETWEEN 1 AND 128
            AND request_fingerprint IS NOT NULL AND octet_length(request_fingerprint) = 32
            AND after_json IS NOT NULL)
    );

CREATE UNIQUE INDEX menu_admin_audit_tenant_request_key_uq
    ON menu_admin_audit(tenant_id, request_key)
    WHERE request_key IS NOT NULL;

CREATE TABLE company_product_emergency_holds (
    tenant_id text NOT NULL,
    company_catalog_id bigint NOT NULL,
    product_key text NOT NULL CHECK (length(product_key) BETWEEN 1 AND 100),
    active boolean NOT NULL,
    version bigint NOT NULL CHECK (version > 0),
    reason text NOT NULL CHECK (length(btrim(reason)) BETWEEN 1 AND 1000),
    activated_by text NOT NULL,
    activated_at timestamptz NOT NULL,
    cleared_by text,
    cleared_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY (tenant_id, company_catalog_id, product_key),
    FOREIGN KEY (company_catalog_id, tenant_id)
        REFERENCES menu_catalogs(catalog_id, tenant_id),
    CHECK ((active AND cleared_by IS NULL AND cleared_at IS NULL)
        OR (NOT active AND cleared_by IS NOT NULL AND cleared_at IS NOT NULL))
);

CREATE INDEX company_product_emergency_holds_active_idx
    ON company_product_emergency_holds(tenant_id, company_catalog_id, product_key)
    WHERE active;

CREATE FUNCTION reject_company_product_emergency_hold_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'company product emergency hold history cannot be deleted';
END;
$$;
CREATE TRIGGER company_product_emergency_holds_no_delete
    BEFORE DELETE ON company_product_emergency_holds
    FOR EACH ROW EXECUTE FUNCTION reject_company_product_emergency_hold_delete();
