package postgresql

import (
	"context"

	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// ListProviderControls returns every operator override, read on each routing
// decision so a kill-switch takes effect in every process at once.
func (r *Repository) ListProviderControls(ctx context.Context) (map[string]provider.Control, error) {
	rows, err := r.executor(ctx).QueryContext(ctx, `SELECT provider, disabled, forced, reason FROM provider_controls`)
	if err != nil {
		return nil, apperrors.Wrap(err, apperrors.Internal, "failed to read provider controls")
	}
	defer rows.Close()
	out := map[string]provider.Control{}
	for rows.Next() {
		var name string
		var c provider.Control
		if err := rows.Scan(&name, &c.Disabled, &c.Forced, &c.Reason); err != nil {
			return nil, apperrors.Wrap(err, apperrors.Internal, "failed to scan provider control")
		}
		out[name] = c
	}
	return out, rows.Err()
}

// SetProviderControl sets one provider's override. Forcing a provider clears
// any other's force first, in the same transaction.
func (r *Repository) SetProviderControl(ctx context.Context, name string, c provider.Control, by string) error {
	return r.WithTx(ctx, func(ctx context.Context) error {
		if c.Forced {
			if _, err := r.executor(ctx).ExecContext(ctx,
				`UPDATE provider_controls SET forced = FALSE WHERE forced AND provider <> $1`, name); err != nil {
				return apperrors.Wrap(err, apperrors.Internal, "failed to clear forced provider")
			}
		}
		_, err := r.executor(ctx).ExecContext(ctx, `
			INSERT INTO provider_controls (provider, disabled, forced, reason, updated_by, updated_at)
			VALUES ($1, $2, $3, $4, $5, NOW())
			ON CONFLICT (provider) DO UPDATE
			SET disabled = $2, forced = $3, reason = $4, updated_by = $5, updated_at = NOW()`,
			name, c.Disabled, c.Forced, c.Reason, by)
		if err != nil {
			return apperrors.Wrap(err, apperrors.Internal, "failed to set provider control")
		}
		return nil
	})
}
