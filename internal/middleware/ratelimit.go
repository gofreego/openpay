package middleware

import (
	"context"
	"net/http"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"

	"github.com/gofreego/openpay/internal/appcontext"
)

// RateLimiter decides whether an authenticated caller may proceed.
type RateLimiter interface {
	Allow(caller appcontext.Caller) error
}

// RateLimitUnaryInterceptor refuses gRPC requests over the caller's limit. It
// must run after CallerUnaryInterceptor: the limit is per credential, so it
// needs to know whose request this is.
func RateLimitUnaryInterceptor(limiter RateLimiter) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		caller, _ := appcontext.CallerFrom(ctx)
		if err := limiter.Allow(caller); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// RateLimitMiddleware is the HTTP twin of RateLimitUnaryInterceptor, and must
// likewise come after CallerMiddleware.
func RateLimitMiddleware(limiter RateLimiter) runtime.Middleware {
	return func(next runtime.HandlerFunc) runtime.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request, pathParams map[string]string) {
			caller, _ := appcontext.CallerFrom(r.Context())
			if err := limiter.Allow(caller); err != nil {
				ErrorHandler(r.Context(), nil, nil, w, r, err)
				return
			}
			next(w, r, pathParams)
		}
	}
}
