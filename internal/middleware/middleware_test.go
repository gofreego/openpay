package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// headerAuthenticator resolves a caller from headers alone, with no credential
// lookup. Service-credential authentication is covered in the auth package.
type headerAuthenticator struct{ err error }

func (h headerAuthenticator) Authenticate(_ context.Context, get func(string) string) (appcontext.Caller, error) {
	caller := appcontext.CallerFromValues(get)
	return caller, h.err
}

func TestCallerUnaryInterceptorPopulatesContext(t *testing.T) {
	ctx := metadata.NewIncomingContext(context.Background(), metadata.New(map[string]string{
		appcontext.HeaderUserID:         "ops_42",
		appcontext.HeaderUserPerms:      "payments:read,wallets:adjust",
		appcontext.HeaderRequestID:      "req_abc",
		appcontext.HeaderIdempotencyKey: "key-1",
	}))

	var seen appcontext.Caller
	handler := func(ctx context.Context, req any) (any, error) {
		seen, _ = appcontext.CallerFrom(ctx)
		return "ok", nil
	}

	if _, err := CallerUnaryInterceptor(headerAuthenticator{})(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/v1.OpenPay/GetProduct"}, handler); err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}

	if seen.UserID != "ops_42" {
		t.Errorf("UserID = %q, want ops_42", seen.UserID)
	}
	if seen.RequestID != "req_abc" {
		t.Errorf("RequestID = %q, want req_abc", seen.RequestID)
	}
	if seen.IdempotencyKey != "key-1" {
		t.Errorf("IdempotencyKey = %q, want key-1", seen.IdempotencyKey)
	}
	if !seen.HasPermission("wallets:adjust") {
		t.Errorf("permissions not parsed: %v", seen.Permissions)
	}
}

func TestCallerUnaryInterceptorWithoutMetadata(t *testing.T) {
	var seen appcontext.Caller
	handler := func(ctx context.Context, req any) (any, error) {
		seen, _ = appcontext.CallerFrom(ctx)
		return nil, nil
	}

	if _, err := CallerUnaryInterceptor(headerAuthenticator{})(context.Background(), nil, &grpc.UnaryServerInfo{}, handler); err != nil {
		t.Fatalf("interceptor returned error: %v", err)
	}
	if seen.RequestID == "" {
		t.Error("a request with no metadata should still get a generated request id")
	}
}

func TestErrorUnaryInterceptorNormalizesErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode codes.Code
		wantMsg  string
	}{
		{
			name:     "app error keeps its mapped code",
			err:      apperrors.New(apperrors.InsufficientBalance, "short by 500"),
			wantCode: codes.FailedPrecondition,
			wantMsg:  "short by 500",
		},
		{
			name:     "unclassified error becomes internal and is not leaked",
			err:      errors.New(`pq: relation "wallets" does not exist`),
			wantCode: codes.Internal,
			wantMsg:  "internal error",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := func(ctx context.Context, req any) (any, error) { return nil, tc.err }

			_, err := ErrorUnaryInterceptor()(context.Background(), nil,
				&grpc.UnaryServerInfo{FullMethod: "/v1.OpenPay/GetProduct"}, handler)
			if err == nil {
				t.Fatal("expected an error")
			}

			s, ok := status.FromError(err)
			if !ok {
				t.Fatal("returned error does not carry a gRPC status")
			}
			if s.Code() != tc.wantCode {
				t.Errorf("code = %v, want %v", s.Code(), tc.wantCode)
			}
			if s.Message() != tc.wantMsg {
				t.Errorf("message = %q, want %q", s.Message(), tc.wantMsg)
			}
		})
	}
}

func TestErrorUnaryInterceptorPassesSuccessThrough(t *testing.T) {
	handler := func(ctx context.Context, req any) (any, error) { return "value", nil }
	resp, err := ErrorUnaryInterceptor()(context.Background(), nil, &grpc.UnaryServerInfo{}, handler)
	if err != nil || resp != "value" {
		t.Errorf("resp=%v err=%v, want value/nil", resp, err)
	}
}

// Without this matcher the gateway drops x-user-id and identity never reaches
// the service.
func TestIncomingHeaderMatcherForwardsIdentityHeaders(t *testing.T) {
	for _, h := range forwardedHeaders {
		key, ok := IncomingHeaderMatcher(h)
		if !ok {
			t.Errorf("header %q is not forwarded", h)
		}
		if key != h {
			t.Errorf("header %q mapped to %q, want it unchanged", h, key)
		}
	}

	// Case from a real client varies; matching must not depend on it.
	if key, ok := IncomingHeaderMatcher("X-User-Id"); !ok || key != appcontext.HeaderUserID {
		t.Errorf("X-User-Id mapped to (%q, %v), want (%q, true)", key, ok, appcontext.HeaderUserID)
	}
}

func TestCallerMiddlewarePopulatesContextAndEchoesRequestID(t *testing.T) {
	var seen appcontext.Caller
	next := func(w http.ResponseWriter, r *http.Request, pathParams map[string]string) {
		seen, _ = appcontext.CallerFrom(r.Context())
	}

	req := httptest.NewRequest(http.MethodGet, "/openpay/v1/products", nil)
	req.Header.Set(appcontext.HeaderUserID, "ops_42")
	req.Header.Set(appcontext.HeaderUserPerms, "payments:read")
	rec := httptest.NewRecorder()

	CallerMiddleware(headerAuthenticator{})(next)(rec, req, nil)

	if seen.UserID != "ops_42" {
		t.Errorf("UserID = %q, want ops_42", seen.UserID)
	}
	if !seen.HasPermission("payments:read") {
		t.Errorf("permissions = %v, want payments:read", seen.Permissions)
	}
	if got := rec.Header().Get(appcontext.HeaderRequestID); got != seen.RequestID {
		t.Errorf("response request id header = %q, want %q echoed back", got, seen.RequestID)
	}
}

func TestErrorHandlerRendersStableCode(t *testing.T) {
	ctx := appcontext.WithCaller(context.Background(), appcontext.Caller{RequestID: "req_1"})
	req := httptest.NewRequest(http.MethodGet, "/openpay/v1/products", nil)
	rec := httptest.NewRecorder()

	ErrorHandler(ctx, runtime.NewServeMux(), &runtime.JSONPb{}, rec, req,
		apperrors.New(apperrors.InsufficientBalance, "wallet wlt_1 is short by 500"))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d (FailedPrecondition)", rec.Code, http.StatusBadRequest)
	}

	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v (%s)", err, rec.Body.String())
	}
	if body.Code != apperrors.InsufficientBalance {
		t.Errorf("code = %q, want %q", body.Code, apperrors.InsufficientBalance)
	}
	if body.Message != "wallet wlt_1 is short by 500" {
		t.Errorf("message = %q", body.Message)
	}
	if body.RequestID != "req_1" {
		t.Errorf("request_id = %q, want req_1", body.RequestID)
	}
}

func TestErrorHandlerHidesInternalDetail(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/openpay/v1/products", nil)
	rec := httptest.NewRecorder()

	ErrorHandler(context.Background(), runtime.NewServeMux(), &runtime.JSONPb{}, rec, req,
		errors.New(`pq: password authentication failed for user "openpay"`))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}

	var body errorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("response is not valid JSON: %v", err)
	}
	if body.Code != apperrors.Internal {
		t.Errorf("code = %q, want %q", body.Code, apperrors.Internal)
	}
	if body.Message != "internal error" {
		t.Errorf("message = %q leaked internal detail", body.Message)
	}
}
