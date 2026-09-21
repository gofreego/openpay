package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"

	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/pkg/apperrors"

	"github.com/gofreego/goutils/logger"
)

// forwardedHeaders are passed through to gRPC metadata. The gateway drops
// unknown headers by default, so identity injected by opengate would silently
// vanish without this.
var forwardedHeaders = []string{
	appcontext.HeaderUserID,
	appcontext.HeaderUserPerms,
	appcontext.HeaderRequestID,
	appcontext.HeaderIdempotencyKey,
}

// IncomingHeaderMatcher forwards OpenPay's headers and otherwise defers to the
// gateway's default behaviour.
func IncomingHeaderMatcher(key string) (string, bool) {
	lower := strings.ToLower(key)
	for _, h := range forwardedHeaders {
		if lower == h {
			return lower, true
		}
	}
	return runtime.DefaultHeaderMatcher(key)
}

// CallerMiddleware populates the caller for requests arriving over HTTP.
//
// It reads the raw headers rather than gRPC metadata because gateway
// middlewares run before the request is annotated, and it echoes the request id
// back so a caller can quote it in a support ticket.
func CallerMiddleware() runtime.Middleware {
	return func(next runtime.HandlerFunc) runtime.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request, pathParams map[string]string) {
			caller := appcontext.CallerFromValues(r.Header.Get)
			w.Header().Set(appcontext.HeaderRequestID, caller.RequestID)

			ctx := appcontext.WithCaller(r.Context(), caller)
			annotateSpan(ctx, caller)
			next(w, r.WithContext(ctx), pathParams)
		}
	}
}

// errorBody is the JSON shape of every failed HTTP response.
type errorBody struct {
	// Code is the stable machine-readable identifier callers branch on.
	Code apperrors.Code `json:"code"`
	// Message is human-readable and may change; do not parse it.
	Message string `json:"message"`
	// RequestID ties the failure to the server logs.
	RequestID string `json:"request_id,omitempty"`
}

// ErrorHandler renders errors as {code, message, request_id} instead of the
// gateway's default {code: <number>, message, details}. A numeric gRPC code is
// not a useful contract for a client; a stable string is.
func ErrorHandler(ctx context.Context, mux *runtime.ServeMux, marshaler runtime.Marshaler, w http.ResponseWriter, r *http.Request, err error) {
	appErr := apperrors.From(err)

	if appErr.Code() == apperrors.Internal {
		logger.Error(ctx, "%s %s failed: %v", r.Method, r.URL.Path, appErr.Error())
	} else {
		logger.Warn(ctx, "%s %s rejected [%s]: %v", r.Method, r.URL.Path, appErr.Code(), appErr.Error())
	}

	status := runtime.HTTPStatusFromCode(appErr.GRPCStatus().Code())
	body := errorBody{
		Code:      appErr.Code(),
		Message:   appErr.Message(),
		RequestID: appcontext.RequestID(ctx),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		logger.Error(ctx, "failed to write error response: %v", err)
	}
}
