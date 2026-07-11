// Package ledger contains PayCore's core domain model: money and the
// double-entry bookkeeping rules. It has no knowledge of databases, HTTP, or any
// other I/O — it is pure logic. That means the rules can be tested exhaustively
// in isolation and trusted before they are ever wired to Postgres.
package ledger

import "fmt"

// Money is an amount in a currency's minor unit — paise for INR, cents for USD —
// stored as an int64. Two deliberate choices live in this single type:
//
//   - It is an integer, never a float. Floating point cannot represent values
//     like 0.10 exactly, and rounding drift is unacceptable when the number is
//     money.
//   - It is a distinct type, not a bare int64, so the compiler stops you from
//     accidentally combining a money value with some unrelated integer.
type Money int64

// String renders the amount in major units with two decimal places, e.g.
// Money(50000) -> "500.00". It assumes a 2-decimal currency (INR, USD, EUR, …);
// a zero-decimal currency such as JPY would format differently.
func (m Money) String() string {
	v := int64(m)
	sign := ""
	if v < 0 {
		sign = "-"
		v = -v
	}
	return fmt.Sprintf("%s%d.%02d", sign, v/100, v%100)
}
