package money

import "testing"

func mustNew(t *testing.T, minor int64, currency string) Amount {
	t.Helper()
	a, err := New(minor, currency)
	if err != nil {
		t.Fatalf("New(%d, %q): %v", minor, currency, err)
	}
	return a
}

func TestNewRejectsBadCurrency(t *testing.T) {
	for _, c := range []string{"", "IN", "INRR", "in1", "12"} {
		if _, err := New(100, c); err == nil {
			t.Errorf("New with currency %q: expected error, got nil", c)
		}
	}
	a, err := New(100, "inr")
	if err != nil {
		t.Fatalf("lowercase currency should normalize: %v", err)
	}
	if a.Currency() != INR {
		t.Errorf("Currency() = %q, want %q", a.Currency(), INR)
	}
}

func TestArithmeticRejectsCurrencyMismatch(t *testing.T) {
	inr := mustNew(t, 100, INR)
	usd := mustNew(t, 100, "USD")

	if _, err := inr.Add(usd); err == nil {
		t.Error("Add across currencies: expected error")
	}
	if _, err := inr.Sub(usd); err == nil {
		t.Error("Sub across currencies: expected error")
	}
	if _, err := inr.Cmp(usd); err == nil {
		t.Error("Cmp across currencies: expected error")
	}
}

func TestAddSub(t *testing.T) {
	a := mustNew(t, 1050, INR)
	b := mustNew(t, 250, INR)

	sum, err := a.Add(b)
	if err != nil || sum.Minor() != 1300 {
		t.Errorf("Add = %v (err %v), want 1300", sum.Minor(), err)
	}

	diff, err := a.Sub(b)
	if err != nil || diff.Minor() != 800 {
		t.Errorf("Sub = %v (err %v), want 800", diff.Minor(), err)
	}
}

func TestSumRequiresAtLeastOne(t *testing.T) {
	if _, err := Sum(); err == nil {
		t.Error("Sum() with no amounts: expected error, currency would be unknown")
	}
	total, err := Sum(mustNew(t, 100, INR), mustNew(t, 200, INR), mustNew(t, 300, INR))
	if err != nil || total.Minor() != 600 {
		t.Errorf("Sum = %v (err %v), want 600", total.Minor(), err)
	}
}

// The property that matters: the parts must add back up to the whole, with no
// minor unit created or destroyed.
func TestAllocateConservesTotal(t *testing.T) {
	cases := []struct {
		name   string
		minor  int64
		ratios []int64
		want   []int64
	}{
		{"even split", 1000, []int64{1, 1}, []int64{500, 500}},
		{"indivisible three way", 1000, []int64{1, 1, 1}, []int64{334, 333, 333}},
		{"weighted", 1000, []int64{70, 30}, []int64{700, 300}},
		{"single share", 999, []int64{1}, []int64{999}},
		{"zero amount", 0, []int64{1, 1, 1}, []int64{0, 0, 0}},
		{"negative amount", -1000, []int64{1, 1, 1}, []int64{-334, -333, -333}},
		{"zero ratio gets nothing", 1000, []int64{1, 0}, []int64{1000, 0}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := mustNew(t, tc.minor, INR)
			shares, err := a.Allocate(tc.ratios...)
			if err != nil {
				t.Fatalf("Allocate: %v", err)
			}

			var total int64
			for i, s := range shares {
				if s.Minor() != tc.want[i] {
					t.Errorf("share[%d] = %d, want %d", i, s.Minor(), tc.want[i])
				}
				if s.Currency() != INR {
					t.Errorf("share[%d] currency = %q, want INR", i, s.Currency())
				}
				total += s.Minor()
			}
			if total != tc.minor {
				t.Errorf("shares total %d, want %d — allocation lost or created money", total, tc.minor)
			}
		})
	}
}

func TestAllocateRejectsBadRatios(t *testing.T) {
	a := mustNew(t, 1000, INR)
	if _, err := a.Allocate(); err == nil {
		t.Error("Allocate with no ratios: expected error")
	}
	if _, err := a.Allocate(0, 0); err == nil {
		t.Error("Allocate with all-zero ratios: expected error")
	}
	if _, err := a.Allocate(1, -1); err == nil {
		t.Error("Allocate with negative ratio: expected error")
	}
}

func TestDecimalAndString(t *testing.T) {
	cases := []struct {
		minor    int64
		currency string
		decimal  string
		full     string
	}{
		{1050, INR, "10.50", "INR 10.50"},
		{5, INR, "0.05", "INR 0.05"},
		{0, INR, "0.00", "INR 0.00"},
		{-1050, INR, "-10.50", "INR -10.50"},
		{-5, INR, "-0.05", "INR -0.05"},
		{123456789, INR, "1234567.89", "INR 1234567.89"},
		{1050, "JPY", "1050", "JPY 1050"},
		{1050, "KWD", "1.050", "KWD 1.050"},
	}

	for _, tc := range cases {
		a := mustNew(t, tc.minor, tc.currency)
		if got := a.Decimal(); got != tc.decimal {
			t.Errorf("Decimal(%d %s) = %q, want %q", tc.minor, tc.currency, got, tc.decimal)
		}
		if got := a.String(); got != tc.full {
			t.Errorf("String(%d %s) = %q, want %q", tc.minor, tc.currency, got, tc.full)
		}
	}
}

func TestUninitializedAmountIsObvious(t *testing.T) {
	var a Amount
	if got := a.String(); got != "<uninitialized amount>" {
		t.Errorf("zero Amount String() = %q, want an obvious marker", got)
	}
}
