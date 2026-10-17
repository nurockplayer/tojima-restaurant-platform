CREATE TABLE menu_catalogs (
    catalog_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES tenants(tenant_id),
    owner_kind text NOT NULL CHECK (owner_kind IN ('COMPANY', 'STORE')),
    store_id text,
    draft_version bigint NOT NULL DEFAULT 0 CHECK (draft_version >= 0),
    draft_base_revision bigint,
    current_revision bigint,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((owner_kind = 'COMPANY' AND store_id IS NULL)
        OR (owner_kind = 'STORE' AND store_id IS NOT NULL)),
    FOREIGN KEY (tenant_id, store_id) REFERENCES stores(tenant_id, store_id),
    UNIQUE (catalog_id, tenant_id),
    UNIQUE (catalog_id, tenant_id, owner_kind, store_id)
);
CREATE UNIQUE INDEX one_company_catalog_per_tenant ON menu_catalogs(tenant_id) WHERE owner_kind = 'COMPANY';
CREATE UNIQUE INDEX one_store_catalog_per_store ON menu_catalogs(tenant_id, store_id) WHERE owner_kind = 'STORE';

CREATE TABLE menu_catalog_revisions (
    catalog_id bigint NOT NULL REFERENCES menu_catalogs(catalog_id),
    revision bigint NOT NULL CHECK (revision > 0),
    draft_version bigint NOT NULL CHECK (draft_version > 0),
    published_at timestamptz NOT NULL DEFAULT now(),
    published_by text NOT NULL,
    sealed boolean NOT NULL DEFAULT false,
    PRIMARY KEY (catalog_id, revision),
    UNIQUE (catalog_id, draft_version)
);
ALTER TABLE menu_catalogs ADD CONSTRAINT menu_catalog_current_revision_fk
    FOREIGN KEY (catalog_id, current_revision)
    REFERENCES menu_catalog_revisions(catalog_id, revision)
    DEFERRABLE INITIALLY DEFERRED;
ALTER TABLE menu_catalogs ADD CONSTRAINT menu_catalog_draft_base_revision_fk
    FOREIGN KEY (catalog_id, draft_base_revision)
    REFERENCES menu_catalog_revisions(catalog_id, revision)
    DEFERRABLE INITIALLY DEFERRED;

CREATE FUNCTION require_sealed_menu_current_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE revision_sealed boolean;
BEGIN
    IF NEW.current_revision IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT sealed INTO revision_sealed FROM menu_catalog_revisions
        WHERE catalog_id=NEW.catalog_id AND revision=NEW.current_revision;
    IF NOT FOUND OR NOT revision_sealed THEN
        RAISE EXCEPTION 'current menu revision must be sealed before publication';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER menu_catalog_current_revision_sealed
    BEFORE INSERT OR UPDATE OF current_revision ON menu_catalogs
    FOR EACH ROW EXECUTE FUNCTION require_sealed_menu_current_revision();

CREATE TABLE menu_draft_categories (
    category_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    catalog_id bigint NOT NULL REFERENCES menu_catalogs(catalog_id) ON DELETE CASCADE,
    category_key text NOT NULL CHECK (length(category_key) BETWEEN 1 AND 100),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 160),
    position integer NOT NULL CHECK (position >= 0),
    UNIQUE (catalog_id, category_key)
);
CREATE TABLE menu_draft_products (
    product_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    catalog_id bigint NOT NULL REFERENCES menu_catalogs(catalog_id) ON DELETE CASCADE,
    product_key text NOT NULL CHECK (length(product_key) BETWEEN 1 AND 100),
    category_key text NOT NULL,
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    description text NOT NULL DEFAULT '',
    base_price_minor bigint NOT NULL CHECK (base_price_minor >= 0),
    position integer NOT NULL CHECK (position >= 0),
    active boolean NOT NULL DEFAULT true,
    UNIQUE (catalog_id, product_key),
    FOREIGN KEY (catalog_id, category_key) REFERENCES menu_draft_categories(catalog_id, category_key) ON DELETE CASCADE
);
CREATE TABLE menu_draft_option_groups (
    group_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    catalog_id bigint NOT NULL REFERENCES menu_catalogs(catalog_id) ON DELETE CASCADE,
    product_key text NOT NULL,
    group_key text NOT NULL CHECK (length(group_key) BETWEEN 1 AND 100),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 160),
    min_select integer NOT NULL CHECK (min_select >= 0),
    max_select integer NOT NULL CHECK (max_select >= 1 AND max_select >= min_select),
    position integer NOT NULL CHECK (position >= 0),
    UNIQUE (catalog_id, product_key, group_key),
    FOREIGN KEY (catalog_id, product_key) REFERENCES menu_draft_products(catalog_id, product_key) ON DELETE CASCADE
);
CREATE TABLE menu_draft_options (
    option_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    catalog_id bigint NOT NULL REFERENCES menu_catalogs(catalog_id) ON DELETE CASCADE,
    product_key text NOT NULL,
    group_key text NOT NULL,
    option_key text NOT NULL CHECK (length(option_key) BETWEEN 1 AND 100),
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 160),
    price_delta_minor bigint NOT NULL CHECK (price_delta_minor >= 0),
    position integer NOT NULL CHECK (position >= 0),
    active boolean NOT NULL DEFAULT true,
    UNIQUE (catalog_id, product_key, group_key, option_key),
    FOREIGN KEY (catalog_id, product_key, group_key)
        REFERENCES menu_draft_option_groups(catalog_id, product_key, group_key) ON DELETE CASCADE
);

CREATE TABLE menu_revision_categories (
    category_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    catalog_id bigint NOT NULL,
    revision bigint NOT NULL,
    category_key text NOT NULL,
    name text NOT NULL,
    position integer NOT NULL CHECK (position >= 0),
    UNIQUE (catalog_id, revision, category_key),
    FOREIGN KEY (catalog_id, revision) REFERENCES menu_catalog_revisions(catalog_id, revision)
);
CREATE TABLE menu_revision_products (
    product_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    catalog_id bigint NOT NULL,
    revision bigint NOT NULL,
    product_key text NOT NULL,
    category_key text NOT NULL,
    name text NOT NULL,
    description text NOT NULL DEFAULT '',
    base_price_minor bigint NOT NULL CHECK (base_price_minor >= 0),
    position integer NOT NULL CHECK (position >= 0),
    active boolean NOT NULL,
    UNIQUE (catalog_id, revision, product_key),
    FOREIGN KEY (catalog_id, revision, category_key)
        REFERENCES menu_revision_categories(catalog_id, revision, category_key)
);
CREATE TABLE menu_revision_option_groups (
    group_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    catalog_id bigint NOT NULL,
    revision bigint NOT NULL,
    product_key text NOT NULL,
    group_key text NOT NULL,
    name text NOT NULL,
    min_select integer NOT NULL CHECK (min_select >= 0),
    max_select integer NOT NULL CHECK (max_select >= 1 AND max_select >= min_select),
    position integer NOT NULL CHECK (position >= 0),
    UNIQUE (catalog_id, revision, product_key, group_key),
    FOREIGN KEY (catalog_id, revision, product_key)
        REFERENCES menu_revision_products(catalog_id, revision, product_key)
);
CREATE TABLE menu_revision_options (
    option_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    catalog_id bigint NOT NULL,
    revision bigint NOT NULL,
    product_key text NOT NULL,
    group_key text NOT NULL,
    option_key text NOT NULL,
    name text NOT NULL,
    price_delta_minor bigint NOT NULL CHECK (price_delta_minor >= 0),
    position integer NOT NULL CHECK (position >= 0),
    active boolean NOT NULL,
    UNIQUE (catalog_id, revision, product_key, group_key, option_key),
    FOREIGN KEY (catalog_id, revision, product_key, group_key)
        REFERENCES menu_revision_option_groups(catalog_id, revision, product_key, group_key)
);

CREATE TABLE store_menu_bindings (
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    mode text NOT NULL CHECK (mode = 'DETACHED'),
    owner_kind text NOT NULL DEFAULT 'STORE' CHECK (owner_kind = 'STORE'),
    catalog_id bigint NOT NULL UNIQUE,
    detached_at timestamptz NOT NULL DEFAULT now(),
    detached_by text NOT NULL,
    PRIMARY KEY (tenant_id, store_id),
    FOREIGN KEY (tenant_id, store_id) REFERENCES stores(tenant_id, store_id),
    FOREIGN KEY (catalog_id, tenant_id, owner_kind, store_id)
        REFERENCES menu_catalogs(catalog_id, tenant_id, owner_kind, store_id)
);

CREATE TABLE store_product_price_overrides (
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    product_key text NOT NULL,
    price_minor bigint NOT NULL CHECK (price_minor >= 0),
    active boolean NOT NULL DEFAULT true,
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_by text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, store_id, product_key),
    FOREIGN KEY (tenant_id, store_id) REFERENCES stores(tenant_id, store_id)
);
CREATE TABLE store_product_availability (
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    product_key text NOT NULL,
    status text NOT NULL CHECK (status IN ('PENDING_CONFIRMATION', 'AVAILABLE', 'PAUSED')),
    reason text NOT NULL DEFAULT '',
    updated_by text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    PRIMARY KEY (tenant_id, store_id, product_key),
    FOREIGN KEY (tenant_id, store_id) REFERENCES stores(tenant_id, store_id)
);
CREATE TABLE store_option_availability (
    tenant_id text NOT NULL,
    store_id text NOT NULL,
    product_key text NOT NULL,
    group_key text NOT NULL,
    option_key text NOT NULL,
    status text NOT NULL CHECK (status IN ('PENDING_CONFIRMATION', 'AVAILABLE', 'PAUSED')),
    reason text NOT NULL DEFAULT '',
    updated_by text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
    PRIMARY KEY (tenant_id, store_id, product_key, group_key, option_key),
    FOREIGN KEY (tenant_id, store_id) REFERENCES stores(tenant_id, store_id)
);

CREATE TABLE menu_admin_audit (
    audit_id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    tenant_id text NOT NULL REFERENCES tenants(tenant_id),
    store_id text,
    actor text NOT NULL,
    operation text NOT NULL,
    entity_key text NOT NULL,
    before_value text NOT NULL DEFAULT '',
    after_value text NOT NULL DEFAULT '',
    before_amount bigint,
    after_amount bigint,
    before_version bigint,
    after_version bigint,
    occurred_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    FOREIGN KEY (tenant_id, store_id) REFERENCES stores(tenant_id, store_id)
);
CREATE INDEX menu_admin_audit_scope_idx ON menu_admin_audit(tenant_id, store_id, occurred_at DESC);

CREATE FUNCTION protect_menu_revision_seal() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_OP = 'DELETE' THEN
        RAISE EXCEPTION 'menu revisions cannot be deleted';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.sealed THEN
            RAISE EXCEPTION 'menu revisions must be inserted unsealed';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.sealed OR NOT NEW.sealed
       OR NEW.catalog_id IS DISTINCT FROM OLD.catalog_id
       OR NEW.revision IS DISTINCT FROM OLD.revision
       OR NEW.draft_version IS DISTINCT FROM OLD.draft_version
       OR NEW.published_at IS DISTINCT FROM OLD.published_at
       OR NEW.published_by IS DISTINCT FROM OLD.published_by THEN
        RAISE EXCEPTION 'menu revision permits only a one-way seal transition';
    END IF;
    RETURN NEW;
END;
$$;
CREATE TRIGGER menu_revision_seal_transition
    BEFORE INSERT OR UPDATE OR DELETE ON menu_catalog_revisions
    FOR EACH ROW EXECUTE FUNCTION protect_menu_revision_seal();

CREATE FUNCTION reject_child_insert_for_sealed_revision() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE revision_sealed boolean;
BEGIN
    SELECT sealed INTO revision_sealed FROM menu_catalog_revisions
        WHERE catalog_id=NEW.catalog_id AND revision=NEW.revision FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'menu revision must exist before its children';
    END IF;
    IF revision_sealed THEN
        RAISE EXCEPTION 'cannot add a child to a sealed menu revision';
    END IF;
    RETURN NEW;
END;
$$;
CREATE FUNCTION reject_menu_revision_mutation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'published menu revisions are immutable';
END;
$$;
CREATE TRIGGER menu_revision_categories_insert_guard BEFORE INSERT ON menu_revision_categories
    FOR EACH ROW EXECUTE FUNCTION reject_child_insert_for_sealed_revision();
CREATE TRIGGER menu_revision_products_insert_guard BEFORE INSERT ON menu_revision_products
    FOR EACH ROW EXECUTE FUNCTION reject_child_insert_for_sealed_revision();
CREATE TRIGGER menu_revision_groups_insert_guard BEFORE INSERT ON menu_revision_option_groups
    FOR EACH ROW EXECUTE FUNCTION reject_child_insert_for_sealed_revision();
CREATE TRIGGER menu_revision_options_insert_guard BEFORE INSERT ON menu_revision_options
    FOR EACH ROW EXECUTE FUNCTION reject_child_insert_for_sealed_revision();
CREATE TRIGGER menu_revision_categories_immutable BEFORE UPDATE OR DELETE ON menu_revision_categories
    FOR EACH ROW EXECUTE FUNCTION reject_menu_revision_mutation();
CREATE TRIGGER menu_revision_products_immutable BEFORE UPDATE OR DELETE ON menu_revision_products
    FOR EACH ROW EXECUTE FUNCTION reject_menu_revision_mutation();
CREATE TRIGGER menu_revision_groups_immutable BEFORE UPDATE OR DELETE ON menu_revision_option_groups
    FOR EACH ROW EXECUTE FUNCTION reject_menu_revision_mutation();
CREATE TRIGGER menu_revision_options_immutable BEFORE UPDATE OR DELETE ON menu_revision_options
    FOR EACH ROW EXECUTE FUNCTION reject_menu_revision_mutation();
CREATE TRIGGER menu_admin_audit_immutable BEFORE UPDATE OR DELETE ON menu_admin_audit
    FOR EACH ROW EXECUTE FUNCTION reject_menu_revision_mutation();
