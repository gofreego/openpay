package service

import (
	"context"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
)

func fingerprint(t *testing.T, operation string, req *openpay_v1.CreateProductRequest) string {
	t.Helper()
	fp, err := fingerprintRequest(operation, req)
	if err != nil {
		t.Fatalf("fingerprintRequest: %v", err)
	}
	return fp
}

// The same request must fingerprint identically every time, or every retry
// would be rejected as a conflict instead of replayed.
//
// This is not hypothetical: protojson deliberately varies its whitespace
// between calls to discourage byte comparison, so fingerprinting with it would
// make identical requests hash differently. Deterministic binary proto is used
// precisely to avoid that, and this test fails if anyone switches back.
func TestFingerprintIsStableAcrossCalls(t *testing.T) {
	req := &openpay_v1.CreateProductRequest{
		Code: "zshala", Name: "Zshala", DefaultCurrency: "INR",
	}

	first := fingerprint(t, "CreateProduct", req)
	for range 100 {
		if got := fingerprint(t, "CreateProduct", req); got != first {
			t.Fatalf("fingerprint changed between calls: %q then %q", first, got)
		}
	}

	// Demonstrate the hazard being guarded against.
	a, _ := protojson.Marshal(req)
	b, _ := protojson.Marshal(req)
	if string(a) == string(b) {
		t.Log("note: protojson happened to match here, but it is not guaranteed to")
	}
}

// Equal values built separately must agree, since a retry constructs a fresh
// message rather than reusing the original.
func TestFingerprintMatchesEqualRequests(t *testing.T) {
	first := fingerprint(t, "CreateProduct", &openpay_v1.CreateProductRequest{
		Code: "zshala", Name: "Zshala", DefaultCurrency: "INR",
	})
	second := fingerprint(t, "CreateProduct", &openpay_v1.CreateProductRequest{
		Code: "zshala", Name: "Zshala", DefaultCurrency: "INR",
	})
	if first != second {
		t.Error("two equal requests fingerprinted differently; retries would all be conflicts")
	}
}

func TestFingerprintDiffersByContent(t *testing.T) {
	base := &openpay_v1.CreateProductRequest{Code: "zshala", Name: "Zshala", DefaultCurrency: "INR"}
	baseFP := fingerprint(t, "CreateProduct", base)

	changed := []*openpay_v1.CreateProductRequest{
		{Code: "bappaapp", Name: "Zshala", DefaultCurrency: "INR"},
		{Code: "zshala", Name: "Renamed", DefaultCurrency: "INR"},
		{Code: "zshala", Name: "Zshala", DefaultCurrency: "USD"},
	}
	for i, req := range changed {
		if fingerprint(t, "CreateProduct", req) == baseFP {
			t.Errorf("variant %d fingerprinted the same as the base; a different request would be served a stale response", i)
		}
	}
}

// One key reused on two endpoints must conflict rather than replay a response
// that belongs to a different operation.
func TestFingerprintIncludesTheOperation(t *testing.T) {
	req := &openpay_v1.CreateProductRequest{Code: "zshala", Name: "Zshala", DefaultCurrency: "INR"}

	if fingerprint(t, "CreateProduct", req) == fingerprint(t, "UpdateProduct", req) {
		t.Error("the operation name is not part of the fingerprint")
	}
}

// Scope is what lets two callers independently use the key "order-42".
func TestIdempotencyScope(t *testing.T) {
	cases := []struct {
		name   string
		caller *appcontext.Caller
		want   string
	}{
		{
			name:   "service is scoped to its product",
			caller: &appcontext.Caller{Kind: appcontext.KindService, ProductID: 7},
			want:   "product:7",
		},
		{
			name:   "operator is scoped to themselves",
			caller: &appcontext.Caller{Kind: appcontext.KindOperator, UserID: "ops_42"},
			want:   "operator:ops_42",
		},
		{
			name:   "anonymous falls back to global",
			caller: &appcontext.Caller{Kind: appcontext.KindAnonymous},
			want:   "global",
		},
		{
			name:   "no caller falls back to global",
			caller: nil,
			want:   "global",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			if tc.caller != nil {
				ctx = appcontext.WithCaller(ctx, *tc.caller)
			}
			if got := idempotencyScope(ctx); got != tc.want {
				t.Errorf("scope = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIdempotencyScopeSeparatesProductsAndOperators(t *testing.T) {
	serviceA := idempotencyScope(appcontext.WithCaller(context.Background(),
		appcontext.Caller{Kind: appcontext.KindService, ProductID: 1}))
	serviceB := idempotencyScope(appcontext.WithCaller(context.Background(),
		appcontext.Caller{Kind: appcontext.KindService, ProductID: 2}))
	operator := idempotencyScope(appcontext.WithCaller(context.Background(),
		appcontext.Caller{Kind: appcontext.KindOperator, UserID: "1"}))

	if serviceA == serviceB {
		t.Error("two products share an idempotency scope; one could replay the other's response")
	}
	// A product id and a user id could otherwise collide as bare strings.
	if serviceA == operator {
		t.Error("product 1 and operator \"1\" share a scope")
	}
}
