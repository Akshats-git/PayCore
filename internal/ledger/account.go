package ledger

// AccountType is the accounting nature of an account. The values match the
// CHECK constraint on the accounts table.
type AccountType string

const (
	// Asset accounts hold value the platform owns (e.g. a bank settlement account).
	Asset AccountType = "asset"
	// Liability accounts hold value the platform owes someone (e.g. a customer's
	// wallet balance). PayCore models user-facing balances as liabilities.
	Liability AccountType = "liability"
)

// Account is a party that money moves between. Its balance is never stored on
// this struct — it is always derived by summing the account's ledger entries.
type Account struct {
	ID       int64
	Name     string
	Type     AccountType
	Currency string
}
