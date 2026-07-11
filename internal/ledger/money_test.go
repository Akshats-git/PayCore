package ledger

import "testing"

func TestMoneyString(t *testing.T) {
	tests := []struct {
		name string
		m    Money
		want string
	}{
		{"whole amount", 50000, "500.00"},
		{"with paise", 50075, "500.75"},
		{"single paisa", 1, "0.01"},
		{"less than one unit", 5, "0.05"},
		{"zero", 0, "0.00"},
		{"negative balance", -12345, "-123.45"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.m.String(); got != tc.want {
				t.Errorf("Money(%d).String() = %q, want %q", int64(tc.m), got, tc.want)
			}
		})
	}
}
