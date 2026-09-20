package service

import (
	"context"

	"github.com/gofreego/openpay/api/openpay_v1"
)

type Config struct {
}

type Repository interface {
	Ping(ctx context.Context) error

	// WithTx runs fn inside a single database transaction, committing when fn
	// returns nil and rolling back on error or panic. The transaction travels on
	// the context, so every repository call made with that ctx joins it.
	//
	// Nested calls join the outer transaction rather than opening a new one, so
	// a service method that calls another still forms one unit of work.
	//
	// Anything that moves money runs inside this. A ledger journal and its
	// postings and balance updates commit together or not at all.
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
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
