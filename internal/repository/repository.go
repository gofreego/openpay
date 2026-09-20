package repository

import (
	"context"
	"sync"

	"github.com/gofreego/openpay/internal/repository/memory"
	"github.com/gofreego/openpay/internal/service"
)

type Config struct {
	Name   string        `yaml:"Name"`
	Memory memory.Config `yaml:"Memory"`
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
			repo, err := memory.NewRepository(ctx, &cfg.Memory)
			if err != nil {
				panic("failed to create repository: " + err.Error())
			}
			instance = repo
		}
	})

	return instance
}
