package payment

import (
	"context"
	"sync"

	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/internal/provider/mock"
	"github.com/gofreego/openpay/internal/provider/razorpay"

	"github.com/gofreego/goutils/logger"
)

var (
	providersOnce sync.Once
	registry      *provider.Registry
	mockProvider  *mock.Provider
)

// Providers builds the process's provider registry once. The HTTP server,
// the gRPC server and the worker share it, which matters for the mock: its
// state is in memory, so a payment it opened for the API must be the one the
// worker later fetches.
//
// The mock is returned too (nil unless enabled) so the development checkout
// page can drive it.
func Providers(cfg Config) (*provider.Registry, *mock.Provider) {
	providersOnce.Do(func() {
		var all []provider.Provider
		if cfg.Mock.Enabled {
			mockProvider = mock.New(cfg.Mock.WebhookSecret, cfg.Mock.CheckoutURL)
			// Wrapped like any real provider: the mock is first-class.
			all = append(all, provider.NewResilient(mockProvider, cfg.Resilience))
		}
		if cfg.Razorpay.Enabled {
			rp, err := razorpay.New(cfg.Razorpay)
			if err != nil {
				// Misconfigured: left out, so payments fail over or report no
				// provider rather than going to a provider that can't work.
				logger.Error(context.Background(), "razorpay provider not started: %v", err)
			} else {
				all = append(all, provider.NewResilient(rp, cfg.Resilience))
			}
		}
		registry = provider.NewRegistry(cfg.Providers, all...)
	})
	return registry, mockProvider
}
