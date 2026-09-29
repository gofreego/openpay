package ratelimit

import (
	"testing"

	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/pkg/apperrors"
)

func service(credential string) appcontext.Caller {
	return appcontext.Caller{Kind: appcontext.KindService, CredentialID: credential}
}

// allowed counts how many of n back-to-back requests get through. The rate is
// low enough that no token refills during the loop.
func allowed(l *Limiter, caller appcontext.Caller, n int) int {
	ok := 0
	for range n {
		if err := l.Allow(caller); err == nil {
			ok++
		} else if !apperrors.Is(err, apperrors.RateLimited) {
			panic(err)
		}
	}
	return ok
}

func TestBurstThenRefused(t *testing.T) {
	l := New(Config{Default: Limit{RequestsPerSecond: 0.01, Burst: 3}})
	if got := allowed(l, service("cred_a"), 10); got != 3 {
		t.Fatalf("allowed %d of 10, want the burst of 3", got)
	}
}

func TestCredentialsHaveSeparateBuckets(t *testing.T) {
	l := New(Config{Default: Limit{RequestsPerSecond: 0.01, Burst: 2}})
	allowed(l, service("cred_noisy"), 10)
	if got := allowed(l, service("cred_quiet"), 2); got != 2 {
		t.Fatalf("a noisy credential starved a quiet one: %d of 2 allowed", got)
	}
}

func TestOverride(t *testing.T) {
	l := New(Config{
		Default:   Limit{RequestsPerSecond: 0.01, Burst: 1},
		Overrides: map[string]Limit{"cred_big": {RequestsPerSecond: 0.01, Burst: 5}},
	})
	if got := allowed(l, service("cred_big"), 10); got != 5 {
		t.Errorf("override: allowed %d, want 5", got)
	}
	if got := allowed(l, service("cred_small"), 10); got != 1 {
		t.Errorf("default: allowed %d, want 1", got)
	}
}

func TestUnlimited(t *testing.T) {
	l := New(Config{Overrides: map[string]Limit{"cred_capped": {RequestsPerSecond: 0.01, Burst: 1}}})
	if got := allowed(l, service("cred_free"), 1000); got != 1000 {
		t.Errorf("zero rate should mean unlimited: %d of 1000", got)
	}
	if got := allowed(l, service("cred_capped"), 5); got != 1 {
		t.Errorf("an override applies even when the default is unlimited: %d", got)
	}
}

func TestOnlyServicesAreLimited(t *testing.T) {
	l := New(Config{Default: Limit{RequestsPerSecond: 0.01, Burst: 1}})
	operator := appcontext.Caller{Kind: appcontext.KindOperator, UserID: "ops_1"}
	if got := allowed(l, operator, 50); got != 50 {
		t.Errorf("operators are limited by opengate, not here: %d of 50", got)
	}
	var none *Limiter
	if err := none.Allow(service("cred_a")); err != nil {
		t.Errorf("a nil limiter must allow: %v", err)
	}
}
