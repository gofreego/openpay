package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/models/dao"
	"github.com/gofreego/openpay/pkg/apperrors"

	"github.com/gofreego/goutils/logger"
)

// idempotencyRetention is how long a key stays replayable. It also bounds how
// long a key abandoned by a crashed process stays claimed.
const idempotencyRetention = 24 * time.Hour

// idempotent makes a mutating operation safe to retry (plan.md D4).
//
// It lives at the service layer, not in an interceptor, for the same reason as
// authorization: the gateway registers the service in-process and never runs
// gRPC interceptors, so the service method is the only point both transports
// pass through. A bonus is that the recorded response is identical whichever
// transport produced it.
//
// The sequence is deliberate:
//
//	claim                    committed on its own, so concurrent duplicates see it
//	WithTx( work; complete ) response recorded in the same commit as the work
//	release on failure       so an immediate retry re-runs
//
// Recording the response inside the work's transaction is what makes a stored
// response mean "the work definitely happened".
func idempotent[Resp proto.Message](
	ctx context.Context,
	repo Repository,
	operation string,
	req proto.Message,
	fn func(ctx context.Context) (Resp, error),
) (Resp, error) {
	var zero Resp

	key := appcontext.IdempotencyKey(ctx)
	if key == "" {
		// Required rather than optional: without it a network timeout leaves
		// the caller unable to retry safely, which for money movement is the
		// difference between one charge and two.
		return zero, apperrors.New(apperrors.InvalidArgument,
			"the Idempotency-Key header is required for this request")
	}

	fingerprint, err := fingerprintRequest(operation, req)
	if err != nil {
		return zero, err
	}
	scope := idempotencyScope(ctx)

	existing, claimed, err := repo.ClaimIdempotencyKey(ctx, &dao.IdempotencyKey{
		Scope:              scope,
		Key:                key,
		RequestFingerprint: fingerprint,
		ExpiresAt:          time.Now().Add(idempotencyRetention),
	})
	if err != nil {
		return zero, err
	}

	if !claimed {
		return replay[Resp](ctx, existing, key, fingerprint)
	}

	var resp Resp
	err = repo.WithTx(ctx, func(ctx context.Context) error {
		resp, err = fn(ctx)
		if err != nil {
			return err
		}
		body, err := protojson.Marshal(resp)
		if err != nil {
			return apperrors.Wrap(err, apperrors.Internal, "failed to encode response for replay")
		}
		return repo.CompleteIdempotencyKey(ctx, scope, key, body)
	})
	if err != nil {
		// The work rolled back, so the claim must go too — otherwise a retry
		// would be told a request is in progress that nothing is working on.
		// Best effort: the claim expires anyway, and reporting this failure
		// instead of the real one would hide why the request failed.
		if releaseErr := repo.ReleaseIdempotencyKey(ctx, scope, key); releaseErr != nil {
			logger.Error(ctx, "failed to release idempotency key %q after a failed operation: %v", key, releaseErr)
		}
		return zero, err
	}

	return resp, nil
}

// replay answers a repeated request from what the first one recorded.
func replay[Resp proto.Message](ctx context.Context, existing *dao.IdempotencyKey, key, fingerprint string) (Resp, error) {
	var zero Resp

	// Same key, different request. Serving the stored response would answer a
	// question the caller did not ask, so this is always an error.
	if existing.RequestFingerprint != fingerprint {
		return zero, apperrors.New(apperrors.IdempotencyKeyConflict,
			"idempotency key %q was already used for a different request", key)
	}

	if existing.Status == dao.IdempotencyInProgress {
		// The first attempt is still running. Retrying now would duplicate the
		// work, so the caller is told to wait rather than served a guess.
		return zero, apperrors.New(apperrors.IdempotencyInProgress,
			"a request with idempotency key %q is already in progress, retry shortly", key)
	}

	resp := zero.ProtoReflect().New().Interface().(Resp)
	if err := protojson.Unmarshal(existing.ResponseBody, resp); err != nil {
		return zero, apperrors.Wrap(err, apperrors.Internal, "failed to decode the recorded response")
	}
	logger.Info(ctx, "replayed idempotency key %q", key)
	return resp, nil
}

// fingerprintRequest identifies the request a key was used for.
//
// The operation name is part of it, so one key reused across two endpoints
// conflicts rather than replaying an unrelated response.
//
// Marshalling is deterministic binary proto, not protojson: protojson
// intentionally varies its whitespace between runs, which would make the same
// request fingerprint differently and turn every retry into a false conflict.
func fingerprintRequest(operation string, req proto.Message) (string, error) {
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(req)
	if err != nil {
		return "", apperrors.Wrap(err, apperrors.Internal, "failed to encode request for fingerprinting")
	}

	sum := sha256.New()
	sum.Write([]byte(operation))
	sum.Write([]byte{0})
	sum.Write(encoded)
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// idempotencyScope namespaces keys so two callers can independently use
// "order-42" without colliding.
func idempotencyScope(ctx context.Context) string {
	caller, ok := appcontext.CallerFrom(ctx)
	if !ok {
		return "global"
	}
	switch caller.Kind {
	case appcontext.KindService:
		return "product:" + strconv.FormatInt(caller.ProductID, 10)
	case appcontext.KindOperator:
		return "operator:" + caller.UserID
	default:
		return "global"
	}
}
