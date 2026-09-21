-- Rollback: 000003_products_and_audit

DROP TABLE IF EXISTS audit_log;

DROP TRIGGER IF EXISTS trg_service_credentials_updated_at ON service_credentials;
DROP TABLE IF EXISTS service_credentials;

DROP TRIGGER IF EXISTS trg_products_updated_at ON products;
DROP TABLE IF EXISTS products;
