// Package payment collects money through PSPs and applies it: the state
// machine, the provider calls, webhook ingestion and the jobs that recover
// from missed webhooks.
package payment

import (
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/provider"
)

// transitions is the payment state machine (plan.md D6), enforced in one
// place. A move not listed is ignored rather than written: an out-of-order
// webhook saying "pending" about a captured payment is stale news, not an
// instruction.
//
// The failure states may still move to captured. Payments do succeed late —
// a UPI payment reported failed that the bank later settles — and when the
// provider says money was captured, refusing to record it would lose it.
var transitions = map[dao.PaymentStatus][]dao.PaymentStatus{
	dao.PaymentCreated:    {dao.PaymentPending, dao.PaymentAuthorized, dao.PaymentCaptured, dao.PaymentFailed, dao.PaymentExpired, dao.PaymentCancelled},
	dao.PaymentPending:    {dao.PaymentAuthorized, dao.PaymentCaptured, dao.PaymentFailed, dao.PaymentExpired, dao.PaymentCancelled},
	dao.PaymentAuthorized: {dao.PaymentCaptured, dao.PaymentFailed, dao.PaymentExpired, dao.PaymentCancelled},
	dao.PaymentCaptured:   {dao.PaymentSettled},
	dao.PaymentFailed:     {dao.PaymentCaptured},
	dao.PaymentExpired:    {dao.PaymentCaptured},
	dao.PaymentCancelled:  {dao.PaymentCaptured},
}

func canTransition(from, to dao.PaymentStatus) bool {
	for _, allowed := range transitions[from] {
		if allowed == to {
			return true
		}
	}
	return false
}

// fromProvider maps a provider's canonical status onto a payment status.
func fromProvider(s provider.Status) dao.PaymentStatus {
	switch s {
	case provider.StatusPending:
		return dao.PaymentPending
	case provider.StatusAuthorized:
		return dao.PaymentAuthorized
	case provider.StatusCaptured:
		return dao.PaymentCaptured
	case provider.StatusFailed:
		return dao.PaymentFailed
	case provider.StatusCancelled:
		return dao.PaymentCancelled
	default:
		return dao.PaymentCreated
	}
}
