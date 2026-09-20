-- Runs once when the Postgres container initialises its data directory.
-- Integration tests TRUNCATE the tables they exercise, so they get their own
-- database rather than wiping whatever is sitting in the development one.
CREATE DATABASE openpay_test OWNER openpay;
