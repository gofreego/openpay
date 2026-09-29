package configs

import (
	"context"
	"fmt"
	"time"

	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/outbox"
	"github.com/gofreego/openpay/internal/ratelimit"
	repo "github.com/gofreego/openpay/internal/repository"
	"github.com/gofreego/openpay/internal/service"
	"github.com/gofreego/openpay/internal/telemetry"

	"github.com/gofreego/goutils/api/debug"
	"github.com/gofreego/goutils/configutils"
	"github.com/gofreego/goutils/logger"
)

type Configuration struct {
	LogConfig    bool               `yaml:"LogConfig"`
	Logger       logger.Config      `yaml:"Logger"`
	ConfigReader configutils.Config `yaml:"ConfigReader"`
	AppNames     []string           `yaml:"AppNames"`
	Server       Server             `yaml:"Server" `
	Repository   repo.Config        `yaml:"Repository"`
	Service      service.Config     `yaml:"Service"`
	Worker       Worker             `yaml:"Worker"`
	Ledger       ledger.ChartConfig `yaml:"Ledger"`
	Telemetry    telemetry.Config   `yaml:"Telemetry"`
	Debug        debug.Config       `yaml:"Debug"`
}

type Server struct {
	GRPCPort int `yaml:"GRPCPort"`
	HTTPPort int `yaml:"HTTPPort"`
	// RateLimit caps each service credential's request rate, per instance.
	RateLimit ratelimit.Config `yaml:"RateLimit"`
}

// Worker configures the background job runner. It lives here rather than in
// cmd/worker because cmd packages import configs, not the other way round.
type Worker struct {
	Outbox outbox.Config `yaml:"Outbox"`

	// IdempotencySweepInterval controls how often expired idempotency keys are
	// pruned. This is also what frees keys abandoned by a crashed process.
	IdempotencySweepInterval time.Duration `yaml:"IdempotencySweepInterval"`

	// IdempotencySweepBatch caps rows deleted per sweep, so pruning a backlog
	// never becomes one enormous delete.
	IdempotencySweepBatch int `yaml:"IdempotencySweepBatch"`

	// LedgerCheckInterval controls how often the ledger invariant checks run.
	// They also run once at startup, so a deploy is followed by a verdict.
	LedgerCheckInterval time.Duration `yaml:"LedgerCheckInterval"`

	// HoldSweepInterval controls how often expired holds are released. Short,
	// because an expired hold is customer money locked for nothing.
	HoldSweepInterval time.Duration `yaml:"HoldSweepInterval"`

	// WalletExpiryInterval controls how often dormant rolling-expiry wallets
	// are lapsed. Expiry is measured in days, so hourly is plenty.
	WalletExpiryInterval time.Duration `yaml:"WalletExpiryInterval"`

	// PaymentEventInterval is how often stored webhooks are processed when
	// the queue is idle. A backlog is drained without waiting for it.
	PaymentEventInterval time.Duration `yaml:"PaymentEventInterval"`

	// PaymentPollInterval is how often open payments are checked with their
	// provider — the safety net for webhooks that never arrive.
	PaymentPollInterval time.Duration `yaml:"PaymentPollInterval"`

	// ReconInterval is how often settlements are ingested and reconciled.
	ReconInterval time.Duration `yaml:"ReconInterval"`
}

func (w *Worker) WithDefaults() {
	if w.IdempotencySweepInterval <= 0 {
		w.IdempotencySweepInterval = time.Hour
	}
	if w.IdempotencySweepBatch <= 0 {
		w.IdempotencySweepBatch = 1000
	}
	if w.LedgerCheckInterval <= 0 {
		w.LedgerCheckInterval = 24 * time.Hour
	}
	if w.HoldSweepInterval <= 0 {
		w.HoldSweepInterval = time.Minute
	}
	if w.WalletExpiryInterval <= 0 {
		w.WalletExpiryInterval = time.Hour
	}
	if w.PaymentEventInterval <= 0 {
		w.PaymentEventInterval = time.Second
	}
	if w.PaymentPollInterval <= 0 {
		w.PaymentPollInterval = time.Minute
	}
	if w.ReconInterval <= 0 {
		w.ReconInterval = time.Hour
	}
}

func LoadConfig(ctx context.Context, path string, env string) *Configuration {
	filePath := fmt.Sprintf("%s/%s.yaml", path, env)
	var conf Configuration
	err := configutils.ReadConfig(ctx, filePath, &conf)
	if err != nil {
		logger.Panic(ctx, "failed to read configs : %v", err)
	}
	// logging config for debug
	if conf.LogConfig {
		configutils.LogConfig(ctx, conf)
	}
	return &conf
}
