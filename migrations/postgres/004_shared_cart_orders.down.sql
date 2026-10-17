DROP TABLE table_order_idempotency;
DROP TABLE table_cart_idempotency;
DROP TRIGGER table_orders_immutable ON table_orders;
DROP FUNCTION reject_table_order_mutation();
DROP TABLE table_orders;
ALTER TABLE table_sessions DROP CONSTRAINT table_sessions_full_scope_unique;
DROP TABLE table_cart_lines;
DROP TABLE table_carts;
