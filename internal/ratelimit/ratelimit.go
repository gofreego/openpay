// Package ratelimit caps how fast each product backend may call OpenPay.
//
// The limit is per service credential, so one misbehaving integration — a
// retry loop, a runaway batch job — cannot starve the others of database
// connections. Operators are not limited here: they arrive through opengate,
// which owns rate limiting for people.
//
// Buckets live in memory, one set per process. With N instances behind a load
// balancer a credential can therefore reach N times its configured rate. That
// is accepted at this scale: the goal is to stop runaway callers, not to meter
// traffic precisely, and a shared store would put a network hop in front of
// every request.
package ratelimit

import (
	"sync"

	"golang.org/x/time/rate"

	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// Limit is a sustained rate with room for a short burst above it.
type Limit struct {
	// RequestsPerSecond is the sustained rate. 0 means unlimited.
	RequestsPerSecond float64 `yaml:"RequestsPerSecond"`
	// Burst is how many requests may arrive at once before the rate applies.
	// Defaults to the rate rounded up, at least 1.
	Burst int `yaml:"Burst"`
}

type Config struct {
	// Default applies to every service credential without an override.
	Default Limit `yaml:"Default"`
	// Overrides by credential public id, for a product that genuinely needs
	// more (or should get less).
	Overrides map[string]Limit `yaml:"Overrides"`
}

// Limiter hands out one token bucket per credential.
type Limiter struct {
	cfg Config

	mu      sync.Mutex
	buckets map[string]*rate.Limiter
}

func New(cfg Config) *Limiter {
	return &Limiter{cfg: cfg, buckets: map[string]*rate.Limiter{}}
}

// Allow reports whether the caller may proceed, and returns RateLimited when
// it may not. Callers other than services always pass.
//
// Allow does not wait: a request over the limit is refused at once, so a
// flood never piles up goroutines holding connections.
func (l *Limiter) Allow(caller appcontext.Caller) error {
	if l == nil || caller.Kind != appcontext.KindService || caller.CredentialID == "" {
		return nil
	}
	bucket := l.bucket(caller.CredentialID)
	if bucket == nil || bucket.Allow() {
		return nil
	}
	return apperrors.New(apperrors.RateLimited,
		"rate limit exceeded for credential %s; retry shortly", caller.CredentialID)
}

// bucket returns the credential's bucket, or nil when it is unlimited. The
// map only ever holds one entry per credential, and credentials are few.
func (l *Limiter) bucket(credentialID string) *rate.Limiter {
	l.mu.Lock()
	defer l.mu.Unlock()

	if b, ok := l.buckets[credentialID]; ok {
		return b
	}
	limit := l.cfg.Default
	if o, ok := l.cfg.Overrides[credentialID]; ok {
		limit = o
	}
	var b *rate.Limiter
	if limit.RequestsPerSecond > 0 {
		burst := limit.Burst
		if burst <= 0 {
			burst = max(1, int(limit.RequestsPerSecond+0.999))
		}
		b = rate.NewLimiter(rate.Limit(limit.RequestsPerSecond), burst)
	}
	l.buckets[credentialID] = b
	return b
}
