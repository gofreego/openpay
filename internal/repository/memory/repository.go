package memory

import (
	"context"

	"github.com/gofreego/goutils/logger"
)

type Config struct {
}

type MemoryRepository struct {
	cfg *Config
}

func NewRepository(ctx context.Context, cfg *Config) (*MemoryRepository, error) {
	return &MemoryRepository{
		cfg: cfg,
	}, nil
}

func (r *MemoryRepository) Ping(ctx context.Context) error {
	logger.Debug(ctx, "MemoryRepository.Ping called")
	return nil
}
