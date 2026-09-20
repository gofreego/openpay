// Package money represents monetary values as integer minor units with an
// explicit currency. Floating point is never used: 0.1 + 0.2 != 0.3 is not an
// acceptable property for a ledger.
package money

import (
	"fmt"
	"strings"

	"github.com/gofreego/goutils/customerrors"
)

const INR = "INR"

// defaultExponent is the number of minor units per major unit for currencies
// not listed in exponents. Most ISO-4217 currencies use 2.
const defaultExponent = 2

// exponents holds the currencies whose minor-unit count is not 2.
// JPY has no minor unit; KWD and BHD use 3. Extend as currencies are added.
var exponents = map[string]int{
	"JPY": 0,
	"KWD": 3,
	"BHD": 3,
}

// Amount is an immutable monetary value. The zero Amount has no currency and
// is only usable after construction through New or Zero.
type Amount struct {
	minor    int64
	currency string
}

// New builds an Amount from minor units, e.g. New(1050, "INR") is ₹10.50.
func New(minor int64, currency string) (Amount, error) {
	c, err := normalizeCurrency(currency)
	if err != nil {
		return Amount{}, err
	}
	return Amount{minor: minor, currency: c}, nil
}

// Zero returns a zero Amount in the given currency.
func Zero(currency string) (Amount, error) {
	return New(0, currency)
}

func normalizeCurrency(currency string) (string, error) {
	c := strings.ToUpper(strings.TrimSpace(currency))
	if len(c) != 3 {
		return "", customerrors.BAD_REQUEST_ERROR("currency must be a 3-letter ISO-4217 code, got %q", currency)
	}
	for _, r := range c {
		if r < 'A' || r > 'Z' {
			return "", customerrors.BAD_REQUEST_ERROR("currency must be a 3-letter ISO-4217 code, got %q", currency)
		}
	}
	return c, nil
}

func (a Amount) Minor() int64     { return a.minor }
func (a Amount) Currency() string { return a.currency }
func (a Amount) IsZero() bool     { return a.minor == 0 }
func (a Amount) IsNegative() bool { return a.minor < 0 }
func (a Amount) IsPositive() bool { return a.minor > 0 }

// sameCurrency guards every binary operation. Mixing currencies is a bug, not
// something to resolve with an implicit conversion (see plan.md D11).
func (a Amount) sameCurrency(b Amount) error {
	if a.currency != b.currency {
		return customerrors.BAD_REQUEST_ERROR("currency mismatch: %s vs %s", a.currency, b.currency)
	}
	return nil
}

func (a Amount) Add(b Amount) (Amount, error) {
	if err := a.sameCurrency(b); err != nil {
		return Amount{}, err
	}
	return Amount{minor: a.minor + b.minor, currency: a.currency}, nil
}

func (a Amount) Sub(b Amount) (Amount, error) {
	if err := a.sameCurrency(b); err != nil {
		return Amount{}, err
	}
	return Amount{minor: a.minor - b.minor, currency: a.currency}, nil
}

// Mul scales an amount by a whole number, e.g. unit price by quantity.
func (a Amount) Mul(n int64) Amount {
	return Amount{minor: a.minor * n, currency: a.currency}
}

func (a Amount) Neg() Amount {
	return Amount{minor: -a.minor, currency: a.currency}
}

// Cmp returns -1, 0 or 1 comparing a to b.
func (a Amount) Cmp(b Amount) (int, error) {
	if err := a.sameCurrency(b); err != nil {
		return 0, err
	}
	switch {
	case a.minor < b.minor:
		return -1, nil
	case a.minor > b.minor:
		return 1, nil
	default:
		return 0, nil
	}
}

func (a Amount) Equal(b Amount) bool {
	return a.currency == b.currency && a.minor == b.minor
}

// Sum adds amounts, requiring at least one so the currency is known.
func Sum(amounts ...Amount) (Amount, error) {
	if len(amounts) == 0 {
		return Amount{}, customerrors.BAD_REQUEST_ERROR("cannot sum zero amounts: currency would be unknown")
	}
	total := amounts[0]
	for _, a := range amounts[1:] {
		var err error
		if total, err = total.Add(a); err != nil {
			return Amount{}, err
		}
	}
	return total, nil
}

// Allocate splits an amount across ratios so the parts add back up to the
// whole. Integer division alone loses minor units — splitting ₹10.00 three
// ways gives 333+333+333 = 999, a paisa short. The remainder is distributed one
// minor unit at a time so nothing is created or destroyed.
func (a Amount) Allocate(ratios ...int64) ([]Amount, error) {
	if len(ratios) == 0 {
		return nil, customerrors.BAD_REQUEST_ERROR("allocate needs at least one ratio")
	}
	var total int64
	for _, r := range ratios {
		if r < 0 {
			return nil, customerrors.BAD_REQUEST_ERROR("allocate ratios must not be negative, got %d", r)
		}
		total += r
	}
	if total == 0 {
		return nil, customerrors.BAD_REQUEST_ERROR("allocate ratios must not all be zero")
	}

	shares := make([]Amount, len(ratios))
	remainder := a.minor
	for i, r := range ratios {
		share := a.minor * r / total
		shares[i] = Amount{minor: share, currency: a.currency}
		remainder -= share
	}

	for i := 0; remainder != 0; i = (i + 1) % len(shares) {
		if remainder > 0 {
			shares[i].minor++
			remainder--
		} else {
			shares[i].minor--
			remainder++
		}
	}
	return shares, nil
}

func exponent(currency string) int {
	if e, ok := exponents[currency]; ok {
		return e
	}
	return defaultExponent
}

// Decimal renders the value without the currency, e.g. "1234.56".
func (a Amount) Decimal() string {
	e := exponent(a.currency)
	if e == 0 {
		return fmt.Sprintf("%d", a.minor)
	}

	minor := a.minor
	sign := ""
	if minor < 0 {
		sign = "-"
		minor = -minor
	}

	div := int64(1)
	for range e {
		div *= 10
	}
	return fmt.Sprintf("%s%d.%0*d", sign, minor/div, e, minor%div)
}

// String renders currency and value, e.g. "INR 1234.56". Locale-free by design:
// this is for logs and APIs, not for display.
func (a Amount) String() string {
	if a.currency == "" {
		return "<uninitialized amount>"
	}
	return a.currency + " " + a.Decimal()
}
