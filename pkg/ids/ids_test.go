package ids

import (
	"sort"
	"strings"
	"testing"
)

func TestNewFormat(t *testing.T) {
	id := New(Payment)
	prefix, body, found := strings.Cut(id, separator)
	if !found {
		t.Fatalf("New(Payment) = %q, want a separator", id)
	}
	if prefix != string(Payment) {
		t.Errorf("prefix = %q, want %q", prefix, Payment)
	}
	if len(body) != 32 {
		t.Errorf("body = %q (len %d), want a 32-char dashless uuid", body, len(body))
	}
	if strings.Contains(body, "-") {
		t.Errorf("body %q should not contain dashes", body)
	}
}

func TestNewIsUnique(t *testing.T) {
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		id := New(Wallet)
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id generated: %s", id)
		}
		seen[id] = struct{}{}
	}
}

// UUIDv7 embeds a timestamp, so ids generated in order must sort in order.
// This is what keeps index inserts appending rather than scattering.
func TestNewIsTimeSortable(t *testing.T) {
	const n = 50
	generated := make([]string, n)
	for i := range generated {
		generated[i] = New(Payment)
	}

	sorted := make([]string, n)
	copy(sorted, generated)
	sort.Strings(sorted)

	for i := range generated {
		if generated[i] != sorted[i] {
			t.Fatalf("ids are not lexicographically time-ordered at index %d", i)
		}
	}
}

func TestParse(t *testing.T) {
	id := New(Payment)

	body, err := Parse(id, Payment)
	if err != nil {
		t.Fatalf("Parse(%q, Payment): %v", id, err)
	}
	if body != strings.TrimPrefix(id, string(Payment)+separator) {
		t.Errorf("Parse returned body %q, not the id's body", body)
	}

	// The point of prefixes: a wallet id must not pass as a payment id.
	if _, err := Parse(New(Wallet), Payment); err == nil {
		t.Error("Parse of a wallet id as a payment id: expected error")
	}

	for _, bad := range []string{"", "pay", "pay_", "_abc", "payabc"} {
		if _, err := Parse(bad, Payment); err == nil {
			t.Errorf("Parse(%q): expected error", bad)
		}
	}
}

func TestIs(t *testing.T) {
	if !Is(New(Wallet), Wallet) {
		t.Error("Is(wallet id, Wallet) = false, want true")
	}
	if Is(New(Wallet), Payment) {
		t.Error("Is(wallet id, Payment) = true, want false")
	}
	if Is("nonsense", Wallet) {
		t.Error("Is on a malformed id = true, want false")
	}
}

func TestPrefixesAreDistinct(t *testing.T) {
	all := []Prefix{
		Customer, Product, ServiceCredential, WalletType, Wallet,
		LedgerAccount, LedgerJournal, LedgerHold, Payment, PaymentAttempt,
		Order, Refund, Dispute, Settlement, Payout, Idempotency, OutboxEvent,
		Request,
	}
	seen := make(map[Prefix]struct{}, len(all))
	for _, p := range all {
		if _, dup := seen[p]; dup {
			t.Errorf("duplicate prefix %q — ids would be ambiguous", p)
		}
		seen[p] = struct{}{}
	}
}
