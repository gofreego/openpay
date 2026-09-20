package postgresql

import (
	"context"

	sqlutils "github.com/gofreego/goutils/databases/connections/sql"
)

// Repository is the PostgreSQL implementation of service.Repository.
// PostgreSQL is the system of record for every balance in OpenPay.
type Repository struct {
	connManager sqlutils.DBManager
}

func NewRepository(ctx context.Context, cfg *sqlutils.Config) (*Repository, error) {
	connManager, err := sqlutils.NewDBManager(cfg)
	if err != nil {
		return nil, err
	}
	return &Repository{connManager: connManager}, nil
}

func (r *Repository) Ping(ctx context.Context) error {
	return r.connManager.Primary().PingContext(ctx)
}
