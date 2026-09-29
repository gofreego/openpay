package provider

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/gofreego/openpay/pkg/apperrors"
)

// ResilienceConfig bounds every outbound call to a provider.
type ResilienceConfig struct {
	// Timeout caps one call. A hung provider must not hang a checkout.
	Timeout time.Duration `yaml:"Timeout"`
	// Retries is how many more times an Unavailable call is tried. Safe for
	// every call: reads are reads, and every mutating call carries our
	// idempotency id, so the provider returns the first result, never a second.
	Retries int `yaml:"Retries"`
	// Backoff is the wait before the first retry; it doubles after.
	Backoff time.Duration `yaml:"Backoff"`
	// FailureThreshold consecutive Unavailable failures open the circuit.
	FailureThreshold int `yaml:"FailureThreshold"`
	// Cooldown is how long an open circuit refuses calls before one probe.
	Cooldown time.Duration `yaml:"Cooldown"`
}

func (c *ResilienceConfig) WithDefaults() {
	if c.Timeout <= 0 {
		c.Timeout = 10 * time.Second
	}
	if c.Retries < 0 {
		c.Retries = 0
	}
	if c.Backoff <= 0 {
		c.Backoff = 200 * time.Millisecond
	}
	if c.FailureThreshold <= 0 {
		c.FailureThreshold = 5
	}
	if c.Cooldown <= 0 {
		c.Cooldown = 30 * time.Second
	}
}

// Health is a provider's circuit state, for routing and for the ops panel.
type Health struct {
	Healthy             bool
	ConsecutiveFailures int
	OpenUntil           time.Time
	LastError           string
}

// Resilient wraps a provider with a timeout, bounded retries and a circuit
// breaker. Only Unavailable errors — timeouts, 5xx, connection failures —
// count against it: a decline is the provider working, not failing.
type Resilient struct {
	Provider
	cfg ResilienceConfig

	mu        sync.Mutex
	failures  int
	openUntil time.Time
	lastError string
	now       func() time.Time
}

func NewResilient(p Provider, cfg ResilienceConfig) *Resilient {
	cfg.WithDefaults()
	return &Resilient{Provider: p, cfg: cfg, now: time.Now}
}

// Health reports the circuit. An open circuit whose cooldown has passed is
// reported healthy: the next call is the probe.
func (r *Resilient) Health() Health {
	r.mu.Lock()
	defer r.mu.Unlock()
	return Health{
		Healthy:             r.now().After(r.openUntil),
		ConsecutiveFailures: r.failures,
		OpenUntil:           r.openUntil,
		LastError:           r.lastError,
	}
}

func (r *Resilient) record(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err == nil || !apperrors.Is(err, apperrors.Unavailable) {
		r.failures, r.lastError = 0, ""
		return
	}
	r.failures++
	r.lastError = err.Error()
	if r.failures >= r.cfg.FailureThreshold {
		r.openUntil = r.now().Add(r.cfg.Cooldown)
	}
}

// do runs one call under the circuit, with its timeout and retries.
func do[T any](ctx context.Context, r *Resilient, call func(ctx context.Context) (T, error)) (T, error) {
	var zero T
	if h := r.Health(); !h.Healthy {
		return zero, ErrUnavailable("%s circuit is open until %s after %d failures (last: %s)",
			r.Name(), h.OpenUntil.Format(time.RFC3339), h.ConsecutiveFailures, h.LastError)
	}
	backoff := r.cfg.Backoff
	var err error
	for attempt := 0; attempt <= r.cfg.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return zero, ErrUnavailable("%s: gave up retrying: %v", r.Name(), ctx.Err())
			case <-time.After(backoff):
			}
			backoff *= 2
		}
		callCtx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
		var out T
		out, err = call(callCtx)
		cancel()
		if err != nil && (errors.Is(err, context.DeadlineExceeded) || callCtx.Err() == context.DeadlineExceeded) {
			err = ErrUnavailable("%s timed out after %s", r.Name(), r.cfg.Timeout)
		}
		r.record(err)
		if err == nil || !apperrors.Is(err, apperrors.Unavailable) {
			return out, err
		}
		if !r.Health().Healthy {
			break // the circuit just opened; retrying would only add load
		}
	}
	return zero, err
}

func (r *Resilient) CreatePayment(ctx context.Context, req CreateRequest) (*CreateResult, error) {
	return do(ctx, r, func(ctx context.Context) (*CreateResult, error) { return r.Provider.CreatePayment(ctx, req) })
}

func (r *Resilient) FetchPayment(ctx context.Context, id string) (*Payment, error) {
	return do(ctx, r, func(ctx context.Context) (*Payment, error) { return r.Provider.FetchPayment(ctx, id) })
}

func (r *Resilient) Capture(ctx context.Context, id string, amount int64) error {
	_, err := do(ctx, r, func(ctx context.Context) (struct{}, error) { return struct{}{}, r.Provider.Capture(ctx, id, amount) })
	return err
}

func (r *Resilient) Cancel(ctx context.Context, id string) error {
	_, err := do(ctx, r, func(ctx context.Context) (struct{}, error) { return struct{}{}, r.Provider.Cancel(ctx, id) })
	return err
}

func (r *Resilient) Refund(ctx context.Context, req RefundRequest) (*Refund, error) {
	return do(ctx, r, func(ctx context.Context) (*Refund, error) { return r.Provider.Refund(ctx, req) })
}

func (r *Resilient) FetchRefund(ctx context.Context, id string) (*Refund, error) {
	return do(ctx, r, func(ctx context.Context) (*Refund, error) { return r.Provider.FetchRefund(ctx, id) })
}

func (r *Resilient) FetchDispute(ctx context.Context, id string) (*Dispute, error) {
	return do(ctx, r, func(ctx context.Context) (*Dispute, error) { return r.Provider.FetchDispute(ctx, id) })
}

func (r *Resilient) SubmitDisputeEvidence(ctx context.Context, id, evidence string) error {
	_, err := do(ctx, r, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, r.Provider.SubmitDisputeEvidence(ctx, id, evidence)
	})
	return err
}

func (r *Resilient) FetchSettlements(ctx context.Context, since time.Time) ([]*Settlement, error) {
	return do(ctx, r, func(ctx context.Context) ([]*Settlement, error) { return r.Provider.FetchSettlements(ctx, since) })
}

func (r *Resilient) VerifyDestination(ctx context.Context, d Destination) (*Verification, error) {
	return do(ctx, r, func(ctx context.Context) (*Verification, error) { return r.Provider.VerifyDestination(ctx, d) })
}

func (r *Resilient) CreatePayout(ctx context.Context, req PayoutRequest) (*Payout, error) {
	return do(ctx, r, func(ctx context.Context) (*Payout, error) { return r.Provider.CreatePayout(ctx, req) })
}

func (r *Resilient) FetchPayout(ctx context.Context, id string) (*Payout, error) {
	return do(ctx, r, func(ctx context.Context) (*Payout, error) { return r.Provider.FetchPayout(ctx, id) })
}

// VerifyWebhook is local signature checking, not a call out.
func (r *Resilient) VerifyWebhook(headers http.Header, body []byte) (*Event, error) {
	return r.Provider.VerifyWebhook(headers, body)
}
