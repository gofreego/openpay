package payment

import (
	"sync"

	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/internal/provider/mock"
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
		registry = provider.NewRegistry(cfg.Providers, all...)
	})
	return registry, mockProvider
}
