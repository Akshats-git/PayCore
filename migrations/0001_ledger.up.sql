-- The double-entry ledger schema. See SPEC.md section 4.
--
-- Money is stored as BIGINT in the currency's minor unit (paise, cents) — never
-- as a float. All amounts are positive magnitudes; whether an entry adds or
-- removes value is carried by its `direction` (debit/credit).

-- accounts: the parties money moves between. Balances are DERIVED from
-- ledger_entries, never stored here as an overwritable number.
CREATE TABLE accounts (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT NOT NULL,
    type       TEXT NOT NULL CHECK (type IN ('asset', 'liability')),
    currency   TEXT NOT NULL DEFAULT 'INR',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- transactions: one business event (a charge or a refund). It groups the two or
-- more ledger_entries that together move the money.
CREATE TABLE transactions (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    kind       TEXT NOT NULL CHECK (kind IN ('charge', 'refund')),
    status     TEXT NOT NULL CHECK (status IN ('succeeded', 'failed')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ledger_entries: the append-only heart of the system. Each row is one leg of a
-- transaction. Rows are NEVER updated or deleted; a correction is a new
-- transaction with new entries. The per-transaction balance invariant
-- (sum of debits == sum of credits) is enforced when we write entries, in the
-- next increment.
CREATE TABLE ledger_entries (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    transaction_id BIGINT NOT NULL REFERENCES transactions(id),
    account_id     BIGINT NOT NULL REFERENCES accounts(id),
    direction      TEXT   NOT NULL CHECK (direction IN ('debit', 'credit')),
    amount         BIGINT NOT NULL CHECK (amount > 0),
    currency       TEXT   NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Balance lookups scan one account's entries; transaction reads fetch all legs
-- of a transaction. Index both access paths.
CREATE INDEX idx_ledger_entries_account ON ledger_entries (account_id);
CREATE INDEX idx_ledger_entries_transaction ON ledger_entries (transaction_id);
