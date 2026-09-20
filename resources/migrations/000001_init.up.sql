-- Migration: 000001_init
-- Shared building blocks every later migration relies on.

-- set_updated_at keeps updated_at honest without the application having to
-- remember. Attach it to any table carrying an updated_at column:
--
--   CREATE TRIGGER trg_<table>_updated_at
--   BEFORE UPDATE ON <table>
--   FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE OR REPLACE FUNCTION set_updated_at()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
