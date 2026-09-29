package provider_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/internal/provider/mock"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// flaky counts calls and fails the first n of them as an outage would.
type flaky struct {
	*mock.Provider
	calls, failFirst atomic.Int64
}

func (f *flaky) FetchPayment(ctx context.Context, id string) (*provider.Payment, error) {
	if f.calls.Add(1) <= f.failFirst.Load() {
		return nil, provider.ErrUnavailable("flaky: 503")
	}
	return &provider.Payment{ProviderPaymentID: id, Status: provider.StatusCaptured}, nil
}

func newFlaky(failFirst int64) *flaky {
	f := &flaky{Provider: mock.New("s", "u")}
	f.failFirst.Store(failFirst)
	return f
}

func TestRetriesRideOutABlip(t *testing.T) {
	f := newFlaky(2)
	r := provider.NewResilient(f, provider.ResilienceConfig{Retries: 2, Backoff: time.Millisecond, FailureThreshold: 10})
	if _, err := r.FetchPayment(context.Background(), "p1"); err != nil {
		t.Fatalf("two failures then success should succeed with two retries: %v", err)
	}
	if f.calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", f.calls.Load())
	}
}

// After enough consecutive outage failures the circuit opens and calls fail
// fast without reaching the provider; after the cooldown one probe goes through.
func TestCircuitOpensAndRecovers(t *testing.T) {
	f := newFlaky(1_000)
	r := provider.NewResilient(f, provider.ResilienceConfig{Retries: 0, FailureThreshold: 3, Cooldown: 50 * time.Millisecond})
	for range 3 {
		_, _ = r.FetchPayment(context.Background(), "p")
	}
	if r.Health().Healthy {
		t.Fatal("circuit still closed after 3 consecutive failures")
	}
	before := f.calls.Load()
	_, err := r.FetchPayment(context.Background(), "p")
	if !apperrors.Is(err, apperrors.Unavailable) || f.calls.Load() != before {
		t.Errorf("open circuit: err %v, provider called %d more times; want a fast Unavailable", err, f.calls.Load()-before)
	}

	time.Sleep(60 * time.Millisecond)
	f.failFirst.Store(0)
	if _, err := r.FetchPayment(context.Background(), "p"); err != nil {
		t.Fatalf("probe after cooldown: %v", err)
	}
	if !r.Health().Healthy || r.Health().ConsecutiveFailures != 0 {
		t.Errorf("circuit did not close after a successful probe: %+v", r.Health())
	}
}

// A decline is the provider working: it must not count against the circuit.
func TestDeclinesDoNotTripTheCircuit(t *testing.T) {
	m := mock.New("s", "u")
	r := provider.NewResilient(m, provider.ResilienceConfig{FailureThreshold: 2})
	for range 5 {
		_, _ = r.FetchPayment(context.Background(), "does-not-exist") // NotFound
	}
	if !r.Health().Healthy {
		t.Error("not-found answers opened the circuit")
	}
}

// A hung provider is cut off at the timeout and reported as unavailable.
type slow struct{ *mock.Provider }

func (s slow) FetchPayment(ctx context.Context, id string) (*provider.Payment, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestTimeout(t *testing.T) {
	r := provider.NewResilient(slow{mock.New("s", "u")}, provider.ResilienceConfig{Timeout: 20 * time.Millisecond})
	start := time.Now()
	_, err := r.FetchPayment(context.Background(), "p")
	if !apperrors.Is(err, apperrors.Unavailable) || time.Since(start) > time.Second {
		t.Errorf("err %v after %s; want Unavailable within the timeout", err, time.Since(start))
	}
}

func TestChooseRoutesAroundOutagesAndOverrides(t *testing.T) {
	primary := provider.NewResilient(mock.NewNamed("alpha", "s", "u"), provider.ResilienceConfig{FailureThreshold: 1, Cooldown: time.Minute})
	secondary := provider.NewResilient(mock.NewNamed("beta", "s", "u"), provider.ResilienceConfig{})
	reg := provider.NewRegistry([]string{"alpha", "beta"}, primary, secondary)

	if p, _, _ := reg.Choose(nil); p.Name() != "alpha" {
		t.Errorf("healthy routing chose %s, want alpha", p.Name())
	}
	if p, reason, _ := reg.Choose(map[string]provider.Control{"alpha": {Disabled: true, Reason: "degraded"}}); p.Name() != "beta" || reason == "" {
		t.Errorf("with alpha disabled chose %s (%q), want beta", p.Name(), reason)
	}
	if p, _, _ := reg.Choose(map[string]provider.Control{"beta": {Forced: true, Reason: "drill"}}); p.Name() != "beta" {
		t.Errorf("with beta forced chose %s, want beta", p.Name())
	}

	primary.Provider.(*mock.Provider).SetUnavailable(true)
	_, _ = primary.FetchPayment(context.Background(), "p") // trips the circuit
	if p, reason, _ := reg.Choose(nil); p.Name() != "beta" {
		t.Errorf("with alpha's circuit open chose %s (%q), want beta", p.Name(), reason)
	}
	_, _, err := reg.Choose(map[string]provider.Control{"beta": {Disabled: true, Reason: "x"}})
	if !apperrors.Is(err, apperrors.Unavailable) {
		t.Errorf("with nothing available: error code = %q", apperrors.CodeOf(err))
	}
}
