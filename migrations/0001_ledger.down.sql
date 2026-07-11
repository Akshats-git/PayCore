-- Reverse of 0001_ledger.up.sql. Drop in reverse dependency order:
-- ledger_entries references transactions and accounts, so it goes first.
DROP TABLE IF EXISTS ledger_entries;
DROP TABLE IF EXISTS transactions;
DROP TABLE IF EXISTS accounts;
