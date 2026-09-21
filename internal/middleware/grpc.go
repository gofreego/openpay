// Package middleware populates request context and normalizes errors at the
// two edges OpenPay serves: the gRPC server and the grpc-gateway HTTP mux.
//
// Both edges exist because the HTTP path registers the service in-process
// (RegisterOpenPayHandlerServer), which bypasses gRPC interceptors entirely.
// Anything that must apply to every request therefore needs an implementation
// on each side; keeping them in one package is what stops them drifting.
package middleware

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/pkg/apperrors"

	"github.com/gofreego/goutils/logger"
)

// CallerUnaryInterceptor populates the caller on context for gRPC requests.
func CallerUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		md, _ := metadata.FromIncomingContext(ctx)
		caller := appcontext.CallerFromValues(func(key string) string {
			if values := md.Get(key); len(values) > 0 {
				return values[0]
			}
			return ""
		})
		ctx = appcontext.WithCaller(ctx, caller)
		annotateSpan(ctx, caller)
		return handler(ctx, req)
	}
}

// ErrorUnaryInterceptor logs the full detail of a failure and returns the
// caller-safe form. Internal errors keep their SQL and driver text in the logs
// and send a generic message on the wire (apperrors.Error.Message).
func ErrorUnaryInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		resp, err := handler(ctx, req)
		if err == nil {
			return resp, nil
		}

		appErr := apperrors.From(err)
		if appErr.Code() == apperrors.Internal {
			logger.Error(ctx, "%s failed: %v", info.FullMethod, appErr.Error())
		} else {
			logger.Warn(ctx, "%s rejected [%s]: %v", info.FullMethod, appErr.Code(), appErr.Error())
		}
		return resp, appErr
	}
}
