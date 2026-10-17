DROP TRIGGER company_product_emergency_holds_no_delete ON company_product_emergency_holds;
DROP FUNCTION reject_company_product_emergency_hold_delete();
DROP TABLE company_product_emergency_holds;
DROP INDEX menu_admin_audit_tenant_request_key_uq;
ALTER TABLE menu_admin_audit DROP CONSTRAINT menu_admin_audit_request_replay_shape;
ALTER TABLE menu_admin_audit
    DROP COLUMN after_json,
    DROP COLUMN request_fingerprint,
    DROP COLUMN request_key;
