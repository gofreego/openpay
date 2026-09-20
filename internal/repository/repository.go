package repository

import (
	"context"
	"sync"

	"github.com/gofreego/openpay/internal/repository/postgresql"
	"github.com/gofreego/openpay/internal/service"

	"github.com/gofreego/goutils/databases/connections/sql"
	"github.com/gofreego/goutils/logger"
)

// Config configures the system of record. PostgreSQL is the only implementation
// by design: every balance OpenPay reports is derived from ledger postings, and
// that requires real transactions (see plan.md D2 and D5).
type Config struct {
	PostgreSQL sql.Config `yaml:"PostgreSQL"`
}

var (
	instance service.Repository
	once     sync.Once
	mu       sync.RWMutex
)

// GetInstance returns the singleton instance of the repository
func GetInstance(ctx context.Context, cfg *Config) service.Repository {
	mu.RLock()
	if instance != nil {
		defer mu.RUnlock()
		return instance
	}
	mu.RUnlock()

	once.Do(func() {
		mu.Lock()
		defer mu.Unlock()
		if instance == nil {
			if cfg.PostgreSQL.Name == "" {
				cfg.PostgreSQL.Name = sql.Postgres
			}
			repo, err := postgresql.NewRepository(ctx, &cfg.PostgreSQL)
			if err != nil {
				logger.Panic(ctx, "failed to create postgresql repository: %v", err)
			}
			logger.Info(ctx, "PostgreSQL repository initialized successfully")
			instance = repo
		}
	})

	return instance
}
