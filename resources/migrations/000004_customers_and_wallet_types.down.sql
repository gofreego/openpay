-- Rollback: 000004_customers_and_wallet_types

DROP TRIGGER IF EXISTS trg_wallet_types_updated_at ON wallet_types;
DROP TABLE IF EXISTS wallet_types;

DROP TRIGGER IF EXISTS trg_customers_updated_at ON customers;
DROP TABLE IF EXISTS customers;
