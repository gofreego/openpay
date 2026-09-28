package payment

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/internal/provider"
	"github.com/gofreego/openpay/pkg/apperrors"

	"github.com/gofreego/goutils/logger"
)

// WebhookRepository is what webhook ingestion needs: somewhere to store the
// event, nothing else. The handler does no payment work at all.
type WebhookRepository interface {
	SaveProviderEvent(ctx context.Context, e *dao.ProviderEvent) (inserted bool, err error)
}

// maxWebhookBody bounds what an unauthenticated caller can make us read
// before the signature has been checked.
const maxWebhookBody = 1 << 20

// WebhookHandler serves POST {prefix}{provider}.
//
// It verifies the signature, stores the event verbatim, and answers 200 —
// nothing more. Payment work happens in the event processor, so a slow
// database or a processing bug never makes the provider retry, and never
// makes it give up on an event we have not handled (plan.md phase 4).
//
// It sits outside the API gateway on purpose: the signature check needs the
// raw body, and a provider is not an authenticated OpenPay caller.
func WebhookHandler(prefix string, registry *provider.Registry, repo WebhookRepository) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, prefix)
		p, err := registry.Get(name)
		if err != nil {
			http.Error(w, "unknown provider", http.StatusNotFound)
			return
		}

		body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody+1))
		if err != nil || len(body) > maxWebhookBody {
			http.Error(w, "unreadable body", http.StatusBadRequest)
			return
		}

		event, err := p.VerifyWebhook(r.Header, body)
		if err != nil {
			// Nothing from an unverified request is stored or acted on.
			logger.Warn(r.Context(), "rejected %s webhook: %v", name, err)
			status := http.StatusBadRequest
			if apperrors.Is(err, apperrors.Unauthenticated) {
				status = http.StatusUnauthorized
			}
			http.Error(w, "invalid webhook", status)
			return
		}

		stored := &dao.ProviderEvent{
			Provider: name, EventID: event.EventID, EventType: event.Type,
			ObjectKind: string(event.ObjectKind), Payload: body,
		}
		if stored.ObjectKind == "" {
			stored.ObjectKind = string(provider.ObjectPayment)
		}
		if event.ObjectID != "" {
			stored.ObjectID = &event.ObjectID
		}
		if _, err := repo.SaveProviderEvent(r.Context(), stored); err != nil {
			// A 5xx makes the provider retry, which is what we want when we
			// could not store it.
			logger.Error(r.Context(), "failed to store %s webhook %s: %v", name, event.EventID, err)
			http.Error(w, "try again", http.StatusServiceUnavailable)
			return
		}
		// A duplicate is acknowledged like a first delivery: we have it.
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"received":true}`))
	})
}

// EventRepository is what the event processor needs beyond the engine's own.
type EventRepository interface {
	WithTx(ctx context.Context, fn func(ctx context.Context) error) error
	WithSavepoint(ctx context.Context, fn func(ctx context.Context) error) error
	ClaimProviderEvent(ctx context.Context, now time.Time) (*dao.ProviderEvent, error)
	MarkProviderEventProcessed(ctx context.Context, id int64) error
	MarkProviderEventFailed(ctx context.Context, id int64, cause string, retryAt time.Time) error
}

// ProcessNextEvent handles one stored webhook, reporting whether there was
// one. Each event gets its own transaction; a failure is recorded on the
// event with a backoff rather than lost or retried in a tight loop.
func (e *Engine) ProcessNextEvent(ctx context.Context, events EventRepository) (bool, error) {
	found := false
	err := events.WithTx(ctx, func(ctx context.Context) error {
		event, err := events.ClaimProviderEvent(ctx, e.now())
		if err != nil || event == nil {
			return err
		}
		found = true

		processErr := events.WithSavepoint(ctx, func(ctx context.Context) error {
			return e.processEvent(ctx, event)
		})
		if processErr == nil {
			return events.MarkProviderEventProcessed(ctx, event.ID)
		}
		logger.Warn(ctx, "provider event %s/%s failed (attempt %d): %v",
			event.Provider, event.EventID, event.Attempts+1, processErr)
		return events.MarkProviderEventFailed(ctx, event.ID, processErr.Error(), e.now().Add(backoff(event.Attempts)))
	})
	return found, err
}

// backoff spaces retries of a failing event: 5s, 10s, 20s … capped at 10
// minutes, so a provider outage does not become a hot loop.
func backoff(attempts int) time.Duration {
	d := 5 * time.Second << min(attempts, 7)
	return min(d, 10*time.Minute)
}

// processEvent treats the webhook as a hint (plan.md D7): it identifies the
// payment, then asks the provider what is actually true and syncs to that.
// Because the event's own claim is never trusted, duplicates and
// out-of-order deliveries converge on the same answer.
func (e *Engine) processEvent(ctx context.Context, event *dao.ProviderEvent) error {
	if event.ObjectID == nil {
		return nil // not about anything we track
	}
	reference := event.Provider + ":" + event.EventID
	switch provider.ObjectKind(event.ObjectKind) {
	case provider.ObjectRefund:
		return e.processRefundEvent(ctx, event.Provider, *event.ObjectID, reference)
	case provider.ObjectDispute:
		return e.processDisputeEvent(ctx, event.Provider, *event.ObjectID)
	case provider.ObjectPayment:
		attempt, err := e.attemptFor(ctx, event.Provider, *event.ObjectID)
		if err != nil {
			return err
		}
		_, err = e.syncFromProvider(ctx, attempt, "webhook", reference)
		return err
	default:
		return nil
	}
}

func (e *Engine) attemptFor(ctx context.Context, providerName, providerPaymentID string) (*dao.PaymentAttempt, error) {
	attempt, err := e.repoAttempt(ctx, providerName, providerPaymentID)
	if apperrors.Is(err, apperrors.NotFound) {
		// Usually a webhook that beat the commit of the payment it is about.
		// Retrying later finds it.
		return nil, apperrors.New(apperrors.Unavailable,
			"no attempt yet for %s payment %s", providerName, providerPaymentID)
	}
	return attempt, err
}

func (e *Engine) repoAttempt(ctx context.Context, providerName, providerPaymentID string) (*dao.PaymentAttempt, error) {
	return e.repo.GetAttemptByProviderRef(ctx, providerName, providerPaymentID)
}

// syncFromProvider fetches the attempt's authoritative state and syncs the
// payment to it, capturing first if the provider stopped at authorized.
func (e *Engine) syncFromProvider(ctx context.Context, attempt *dao.PaymentAttempt, source, reference string) (*dao.Payment, error) {
	p, err := e.provider(attempt.Provider)
	if err != nil {
		return nil, err
	}
	pp, err := p.FetchPayment(ctx, *attempt.ProviderPaymentID)
	if err != nil {
		return nil, err
	}

	// Top-ups capture immediately. Capture is idempotent at the provider, so
	// two sources seeing "authorized" at once do no harm.
	if pp.Status == provider.StatusAuthorized {
		if err := p.Capture(ctx, pp.ProviderPaymentID, pp.Amount); err != nil {
			return nil, err
		}
		if pp, err = p.FetchPayment(ctx, pp.ProviderPaymentID); err != nil {
			return nil, err
		}
	}
	return e.Sync(ctx, attempt, pp, source, reference)
}

// latestAttempt is the attempt that currently speaks for a payment.
func (e *Engine) latestAttempt(ctx context.Context, payment *dao.Payment) (*dao.PaymentAttempt, error) {
	attempts, err := e.repo.ListPaymentAttempts(ctx, payment.ID)
	if err != nil {
		return nil, err
	}
	if len(attempts) == 0 {
		return nil, nil
	}
	return attempts[len(attempts)-1], nil
}

// Poll asks the provider about a payment directly. It is the safety net for
// webhooks, which will be missed: a payment the provider captured is
// credited whether or not we ever hear about it. Also serves an operator's
// manual sync.
func (e *Engine) Poll(ctx context.Context, payment *dao.Payment, source string) (*dao.Payment, error) {
	attempt, err := e.latestAttempt(ctx, payment)
	if err != nil {
		return nil, err
	}
	if attempt == nil || attempt.ProviderPaymentID == nil {
		return payment, nil // the provider never heard of it; nothing to ask
	}
	return e.syncFromProvider(ctx, attempt, source, "")
}

// Expire closes a payment the customer did not complete in time.
//
// It asks the provider first, because "past its expiry here" and "unpaid
// there" are different facts: a customer who paid in the last second must
// be credited, not expired. Only a payment the provider confirms as unpaid
// is cancelled there and expired here.
func (e *Engine) Expire(ctx context.Context, payment *dao.Payment) (*dao.Payment, error) {
	attempt, err := e.latestAttempt(ctx, payment)
	if err != nil {
		return nil, err
	}

	if attempt != nil && attempt.ProviderPaymentID != nil {
		synced, err := e.syncFromProvider(ctx, attempt, "sweeper", "expiry check")
		if err != nil {
			return nil, err
		}
		if !synced.Status.IsOpen() {
			return synced, nil // it resolved on its own — paid, failed or cancelled
		}
		p, err := e.provider(attempt.Provider)
		if err != nil {
			return nil, err
		}
		if err := p.Cancel(ctx, *attempt.ProviderPaymentID); err != nil {
			// The customer may be mid-payment. Leave it open; the next sweep
			// or the poller will see how it ended.
			return nil, err
		}
	}

	var expired *dao.Payment
	err = e.repo.WithTx(ctx, func(ctx context.Context) error {
		locked, err := e.repo.LockPayment(ctx, payment.ID)
		if err != nil {
			return err
		}
		expired = locked
		if !locked.Status.IsOpen() {
			return nil
		}
		if attempt != nil {
			attempt.Status = string(provider.StatusCancelled)
			if err := e.repo.UpdatePaymentAttempt(ctx, attempt); err != nil {
				return err
			}
		}
		return e.transition(ctx, locked, dao.PaymentExpired, "sweeper", "", "not paid before "+locked.ExpiresAt.Format(time.RFC3339))
	})
	return expired, err
}
