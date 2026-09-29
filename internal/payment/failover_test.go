package payment_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/payment"
	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/internal/provider/mock"
	"github.com/gofreego/openpay/internal/wallet"
)

// Phase 5's exit criterion, provable without a real PSP: when the primary is
// down, new top-ups go to the secondary with no caller-visible difference;
// and an operator's kill-switch moves traffic without a deploy.
func TestFailoverAndKillSwitch(t *testing.T) {
	f := setup(t)
	alpha, beta := mock.NewNamed("alpha", "s", "u"), mock.NewNamed("beta", "s", "u")
	registry := provider.NewRegistry([]string{"alpha", "beta"},
		provider.NewResilient(alpha, provider.ResilienceConfig{Retries: 0, FailureThreshold: 1, Cooldown: time.Minute}),
		provider.NewResilient(beta, provider.ResilienceConfig{}))
	f.engine = payment.New(f.repo, registry, wallet.New(f.repo, wallet.Limits{}), payment.Config{})
	if _, err := ledger.EnsureChart(f.ctx, f.repo, ledger.ChartConfig{Providers: []string{"alpha", "beta"}}); err != nil {
		t.Fatalf("chart: %v", err)
	}

	attemptFor := func() *dao.PaymentAttempt {
		t.Helper()
		var a *dao.PaymentAttempt
		if err := f.repo.WithTx(f.ctx, func(ctx context.Context) error {
			var err error
			_, a, err = f.engine.CreateTopup(ctx, payment.TopupRequest{ProductID: f.product.ID, Customer: f.customer,
				Wallet: f.wallet, Amount: 1000, Currency: "INR"})
			return err
		}); err != nil {
			t.Fatalf("top-up: %v", err)
		}
		return a
	}

	if a := attemptFor(); a.Provider != "alpha" {
		t.Errorf("healthy: routed to %s, want alpha", a.Provider)
	}

	// alpha goes down: the next attempt fails there and trips its circuit;
	// every attempt after that goes to beta, and says why.
	alpha.SetUnavailable(true)
	attemptFor()
	a := attemptFor()
	if a.Provider != "beta" || !strings.Contains(a.RoutingReason, "failover") {
		t.Errorf("during the outage: routed to %s (%q), want beta by failover", a.Provider, a.RoutingReason)
	}

	// The kill-switch: force alpha back regardless of health, then disable it.
	alpha.SetUnavailable(false)
	if err := f.repo.SetProviderControl(f.ctx, "beta", provider.Control{Disabled: true, Reason: "fee dispute"}, "op_1"); err != nil {
		t.Fatalf("disable beta: %v", err)
	}
	if err := f.repo.SetProviderControl(f.ctx, "alpha", provider.Control{Forced: true, Reason: "drill"}, "op_1"); err != nil {
		t.Fatalf("force alpha: %v", err)
	}
	if a := attemptFor(); a.Provider != "alpha" || !strings.Contains(a.RoutingReason, "forced") {
		t.Errorf("forced: routed to %s (%q), want alpha", a.Provider, a.RoutingReason)
	}
}
