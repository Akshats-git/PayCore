package ledger

import (
	"errors"
	"fmt"
)

// Direction is which side of the ledger an entry sits on. The values match the
// CHECK constraint in the database schema exactly, so the domain and the storage
// layer speak the same language.
type Direction string

const (
	Debit  Direction = "debit"  // value leaving an account
	Credit Direction = "credit" // value arriving at an account
)

// Kind is the type of business event a transaction represents.
type Kind string

const (
	Charge Kind = "charge"
	Refund Kind = "refund"
)

// Entry is one leg of a transaction: a single debit or credit against one
// account. Amount is always a positive magnitude — the Direction, not the sign,
// carries whether value is leaving or arriving.
type Entry struct {
	AccountID int64
	Direction Direction
	Amount    Money
	Currency  string
}

// Transaction is a proposed movement of money: a set of entries that must be
// committed together, atomically, and that is valid only if its debits and
// credits balance.
//
// Note the word "transaction" has two senses in this project. Here it is the
// *financial* transaction (a charge or refund made of ledger entries). In the
// storage layer it will be persisted inside a *database* transaction (an atomic
// commit). Increment 5 connects the two: a validated ledger.Transaction is
// written within a single database transaction.
type Transaction struct {
	Kind    Kind
	Entries []Entry
}

// Validation errors. They are exported and comparable with errors.Is so that
// callers — and tests — can react to a specific failure rather than parsing a
// string.
var (
	ErrTooFewEntries     = errors.New("ledger: transaction needs at least two entries")
	ErrInvalidKind       = errors.New("ledger: invalid transaction kind")
	ErrInvalidDirection  = errors.New("ledger: entry has invalid direction")
	ErrNonPositiveAmount = errors.New("ledger: entry amount must be positive")
	ErrMixedCurrency     = errors.New("ledger: transaction mixes currencies")
	ErrUnbalanced        = errors.New("ledger: debits and credits do not balance")
)

// Validate enforces the double-entry rules. A Transaction that passes Validate is
// safe to persist; one that fails must never be written. The rules:
//
//   - a real double-entry has at least two legs;
//   - every amount is a positive magnitude;
//   - every leg shares one currency (no INR-vs-USD mixing);
//   - every direction is debit or credit;
//   - and — the rule the whole system rests on — the debits sum to exactly the
//     credits.
func (t Transaction) Validate() error {
	if t.Kind != Charge && t.Kind != Refund {
		return fmt.Errorf("%w: %q", ErrInvalidKind, t.Kind)
	}
	if len(t.Entries) < 2 {
		return ErrTooFewEntries
	}

	var debits, credits Money
	currency := t.Entries[0].Currency

	for i, e := range t.Entries {
		if e.Amount <= 0 {
			return fmt.Errorf("%w: entry %d has amount %d", ErrNonPositiveAmount, i, e.Amount)
		}
		if e.Currency != currency {
			return fmt.Errorf("%w: entry %d is %q, expected %q", ErrMixedCurrency, i, e.Currency, currency)
		}
		switch e.Direction {
		case Debit:
			debits += e.Amount
		case Credit:
			credits += e.Amount
		default:
			return fmt.Errorf("%w: entry %d has direction %q", ErrInvalidDirection, i, e.Direction)
		}
	}

	if debits != credits {
		return fmt.Errorf("%w: debits=%d credits=%d", ErrUnbalanced, debits, credits)
	}
	return nil
}

// NewTransfer builds the common case: a balanced two-entry transaction that moves
// amount from one account (debited — value leaving) to another (credited — value
// arriving), in a single currency. By construction it always balances.
func NewTransfer(kind Kind, fromAccountID, toAccountID int64, amount Money, currency string) Transaction {
	return Transaction{
		Kind: kind,
		Entries: []Entry{
			{AccountID: fromAccountID, Direction: Debit, Amount: amount, Currency: currency},
			{AccountID: toAccountID, Direction: Credit, Amount: amount, Currency: currency},
		},
	}
}
