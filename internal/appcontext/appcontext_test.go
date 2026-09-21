package appcontext

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/gofreego/goutils/logger"
)

func valuesFrom(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func TestCallerFromValues(t *testing.T) {
	c := CallerFromValues(valuesFrom(map[string]string{
		HeaderUserID:         "ops_42",
		HeaderUserPerms:      "payments:read,wallets:adjust",
		HeaderRequestID:      "req_abc",
		HeaderIdempotencyKey: "key-1",
	}))

	if c.UserID != "ops_42" {
		t.Errorf("UserID = %q, want ops_42", c.UserID)
	}
	if c.RequestID != "req_abc" {
		t.Errorf("RequestID = %q, want the supplied id", c.RequestID)
	}
	if c.IdempotencyKey != "key-1" {
		t.Errorf("IdempotencyKey = %q, want key-1", c.IdempotencyKey)
	}
	if want := []string{"payments:read", "wallets:adjust"}; !slices.Equal(c.Permissions, want) {
		t.Errorf("Permissions = %v, want %v", c.Permissions, want)
	}
}

// Every request must be traceable even when nothing upstream set an id.
func TestCallerFromValuesGeneratesRequestID(t *testing.T) {
	c := CallerFromValues(valuesFrom(nil))
	if c.RequestID == "" {
		t.Fatal("RequestID is empty; an untraceable request is a support dead end")
	}
	if !strings.HasPrefix(c.RequestID, "req_") {
		t.Errorf("RequestID = %q, want a req_ prefixed id", c.RequestID)
	}

	other := CallerFromValues(valuesFrom(nil))
	if c.RequestID == other.RequestID {
		t.Error("generated request ids must be unique")
	}
}

func TestPermissionParsingTolerance(t *testing.T) {
	cases := []struct {
		raw  string
		want []string
	}{
		{"a,b", []string{"a", "b"}},
		{" a , b ", []string{"a", "b"}},
		{"a,,b", []string{"a", "b"}},
		{"a", []string{"a"}},
		{"", nil},
		{"   ", nil},
		{",,,", nil},
	}
	for _, tc := range cases {
		got := parsePermissions(tc.raw)
		if !slices.Equal(got, tc.want) {
			t.Errorf("parsePermissions(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}

func TestWithCallerAndAccessors(t *testing.T) {
	ctx := WithCaller(context.Background(), Caller{
		UserID:         "ops_7",
		Permissions:    []string{"payments:read"},
		RequestID:      "req_1",
		IdempotencyKey: "idem",
	})

	if got, ok := CallerFrom(ctx); !ok || got.UserID != "ops_7" {
		t.Errorf("CallerFrom = %+v, ok=%v", got, ok)
	}
	if UserID(ctx) != "ops_7" {
		t.Errorf("UserID = %q", UserID(ctx))
	}
	if RequestID(ctx) != "req_1" {
		t.Errorf("RequestID = %q", RequestID(ctx))
	}
	if IdempotencyKey(ctx) != "idem" {
		t.Errorf("IdempotencyKey = %q", IdempotencyKey(ctx))
	}
	if !HasPermission(ctx, "payments:read") {
		t.Error("HasPermission should be true for a held permission")
	}
	if HasPermission(ctx, "wallets:adjust") {
		t.Error("HasPermission should be false for a permission not held")
	}
}

// A context that never passed through the edge holds no permissions, so
// permission checks must fail closed rather than panic or pass.
func TestAccessorsOnBareContextFailClosed(t *testing.T) {
	ctx := context.Background()

	if _, ok := CallerFrom(ctx); ok {
		t.Error("CallerFrom on a bare context should report false")
	}
	if HasPermission(ctx, "anything") {
		t.Error("HasPermission on a bare context must be false")
	}
	if UserID(ctx) != "" || RequestID(ctx) != "" || IdempotencyKey(ctx) != "" {
		t.Error("accessors on a bare context should return empty strings")
	}
}

// Identity is mirrored into the goutils logger context so every log line for
// the request carries it without callers threading anything through.
func TestWithCallerEnrichesLoggerContext(t *testing.T) {
	ctx := WithCaller(context.Background(), Caller{UserID: "ops_9", RequestID: "req_x"})

	rc, ok := ctx.Value(logger.RequestContextKey).(logger.RequestContext)
	if !ok {
		t.Fatal("logger.RequestContext was not set")
	}
	if rc.RequestID != "req_x" {
		t.Errorf("logger RequestID = %q, want req_x", rc.RequestID)
	}
	if rc.UserID != "ops_9" {
		t.Errorf("logger UserID = %q, want ops_9", rc.UserID)
	}
}

// The HTTP middleware runs after goutils' own request middleware, which already
// set fields like URI. Populating the caller must not wipe them.
func TestWithCallerPreservesExistingLoggerFields(t *testing.T) {
	base := context.WithValue(context.Background(), logger.RequestContextKey,
		logger.RequestContext{URI: "/openpay/v1/products", Method: "GET", IP: "10.0.0.1"})

	ctx := WithCaller(base, Caller{UserID: "ops_9", RequestID: "req_x"})

	rc := ctx.Value(logger.RequestContextKey).(logger.RequestContext)
	if rc.URI != "/openpay/v1/products" || rc.Method != "GET" || rc.IP != "10.0.0.1" {
		t.Errorf("existing logger fields were clobbered: %+v", rc)
	}
	if rc.UserID != "ops_9" {
		t.Errorf("logger UserID = %q, want ops_9", rc.UserID)
	}
}
