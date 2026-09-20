package service

import (
	"context"

	"github.com/gofreego/openpay/api/openpay_v1"
)

type Config struct {
}

type Repository interface {
	Ping(ctx context.Context) error
}

type Service struct {
	repo Repository
	openpay_v1.UnimplementedOpenPayServer
}

func NewService(ctx context.Context, cfg *Config, repo Repository) *Service {
	return &Service{
		repo: repo,
	}
}
