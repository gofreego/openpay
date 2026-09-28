package payment

import (
	"context"

	"github.com/gofreego/openpay/internal/models/dao"
)

// MarkSettled records that the provider paid a payment out to our bank, in
// the caller's transaction. A captured payment becomes settled (plan.md D6);
// one already refunded or disputed keeps that status, since it says more,
// but still records when it settled.
func (e *Engine) MarkSettled(ctx context.Context, paymentID int64, settlementRef string) (*dao.Payment, error) {
	var p *dao.Payment
	err := e.repo.WithTx(ctx, func(ctx context.Context) error {
		var err error
		if p, err = e.repo.LockPayment(ctx, paymentID); err != nil {
			return err
		}
		if p.SettledAt != nil {
			return nil
		}
		now := e.now()
		p.SettledAt = &now
		if p.Status == dao.PaymentCaptured {
			return e.transition(ctx, p, dao.PaymentSettled, "settlement", settlementRef, "")
		}
		return e.repo.UpdatePayment(ctx, p)
	})
	return p, err
}
