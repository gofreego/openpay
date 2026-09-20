package configs

import (
	"context"
	"fmt"
	"time"

	"github.com/gofreego/openpay/internal/outbox"
	repo "github.com/gofreego/openpay/internal/repository"
	"github.com/gofreego/openpay/internal/service"

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
	Debug        debug.Config       `yaml:"Debug"`
}

type Server struct {
	GRPCPort int `yaml:"GRPCPort"`
	HTTPPort int `yaml:"HTTPPort"`
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
}

func (w *Worker) WithDefaults() {
	if w.IdempotencySweepInterval <= 0 {
		w.IdempotencySweepInterval = time.Hour
	}
	if w.IdempotencySweepBatch <= 0 {
		w.IdempotencySweepBatch = 1000
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
