-- Defense in depth for the double-entry invariant.
--
-- The application already validates that a transaction balances before writing
-- it (internal/ledger's Validate). But a bug — or a future code path, or someone
-- poking the database directly — could still attempt an unbalanced write. This
-- deferred constraint trigger makes the DATABASE ITSELF refuse to commit a
-- transaction whose entries don't balance.
--
-- It is DEFERRABLE INITIALLY DEFERRED, so it fires at COMMIT time — after all of
-- a transaction's entries have been inserted — and checks the final sums. That's
-- essential: the invariant is only meaningful once every leg is present, so we
-- cannot check it row-by-row as each entry is inserted.

CREATE OR REPLACE FUNCTION assert_transaction_balanced() RETURNS trigger AS $$
DECLARE
    debit_total  BIGINT;
    credit_total BIGINT;
BEGIN
    SELECT
        COALESCE(SUM(amount) FILTER (WHERE direction = 'debit'), 0),
        COALESCE(SUM(amount) FILTER (WHERE direction = 'credit'), 0)
    INTO debit_total, credit_total
    FROM ledger_entries
    WHERE transaction_id = NEW.transaction_id;

    IF debit_total <> credit_total THEN
        RAISE EXCEPTION 'transaction % is unbalanced: debits=% credits=%',
            NEW.transaction_id, debit_total, credit_total;
    END IF;

    RETURN NULL; -- return value is ignored for AFTER triggers
END;
$$ LANGUAGE plpgsql;

CREATE CONSTRAINT TRIGGER trg_ledger_entries_balanced
    AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW
    EXECUTE FUNCTION assert_transaction_balanced();
