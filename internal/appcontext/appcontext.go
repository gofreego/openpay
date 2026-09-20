// Package appcontext carries the identity of whoever made the current request.
//
// It is populated once at the edge — a gRPC interceptor or a gateway middleware
// — and read everywhere else. Nothing below the edge parses headers.
package appcontext

import (
	"context"
	"slices"
	"strings"

	"github.com/gofreego/openpay/pkg/ids"

	"github.com/gofreego/goutils/logger"
)

// Header names. x-user-id and x-user-perms are injected by opengate after
// OpenAuth has validated the session; OpenPay trusts them and never sees a
// password or token of its own (see plan.md U-D2).
const (
	HeaderUserID         = "x-user-id"
	HeaderUserPerms      = "x-user-perms"
	HeaderRequestID      = "x-request-id"
	HeaderIdempotencyKey = "idempotency-key"
)

type contextKey int

const callerKey contextKey = iota

// Caller is the authenticated identity behind a request.
type Caller struct {
	// UserID is the operator acting, from x-user-id. Empty for calls made by a
	// product's backend with a service credential rather than by a person.
	UserID string

	// Permissions comes from x-user-perms. Gating the UI on these is cosmetic;
	// the server checking them is the actual boundary (plan.md U-D6).
	Permissions []string

	// RequestID ties logs, audit rows and support tickets to one request.
	RequestID string

	// IdempotencyKey is the caller's key for retry-safe mutations (plan.md D4).
	IdempotencyKey string
}

// HasPermission reports whether the caller holds a permission.
func (c Caller) HasPermission(p string) bool {
	return slices.Contains(c.Permissions, p)
}

// CallerFromValues builds a Caller from a lookup function, so the gRPC
// interceptor and the HTTP gateway middleware parse identity identically
// despite reading from metadata and headers respectively.
//
// A request id is generated when the caller did not supply one, so every
// request is traceable whether or not the edge set a header.
func CallerFromValues(get func(key string) string) Caller {
	requestID := strings.TrimSpace(get(HeaderRequestID))
	if requestID == "" {
		requestID = ids.New(ids.Request)
	}
	return Caller{
		UserID:         strings.TrimSpace(get(HeaderUserID)),
		Permissions:    parsePermissions(get(HeaderUserPerms)),
		RequestID:      requestID,
		IdempotencyKey: strings.TrimSpace(get(HeaderIdempotencyKey)),
	}
}

// parsePermissions splits the comma-separated x-user-perms header, tolerating
// stray whitespace and empty entries.
func parsePermissions(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	perms := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			perms = append(perms, p)
		}
	}
	if len(perms) == 0 {
		return nil
	}
	return perms
}

// WithCaller stores the caller, and mirrors the identity into the goutils
// logger context so every log line for this request carries it without callers
// having to pass anything around.
func WithCaller(ctx context.Context, c Caller) context.Context {
	ctx = context.WithValue(ctx, callerKey, c)

	rc, _ := ctx.Value(logger.RequestContextKey).(logger.RequestContext)
	rc.RequestID = c.RequestID
	rc.UserID = c.UserID
	return context.WithValue(ctx, logger.RequestContextKey, rc)
}

// CallerFrom returns the caller, and false when the context never passed
// through the edge — which means a background worker, or a bug.
func CallerFrom(ctx context.Context) (Caller, bool) {
	c, ok := ctx.Value(callerKey).(Caller)
	return c, ok
}

// UserID returns the acting operator, or "" when there is none.
func UserID(ctx context.Context) string {
	c, _ := CallerFrom(ctx)
	return c.UserID
}

// RequestID returns the request id, or "" outside a request.
func RequestID(ctx context.Context) string {
	c, _ := CallerFrom(ctx)
	return c.RequestID
}

// IdempotencyKey returns the caller-supplied key, or "" when absent.
func IdempotencyKey(ctx context.Context) string {
	c, _ := CallerFrom(ctx)
	return c.IdempotencyKey
}

// HasPermission reports whether the request's caller holds a permission.
// A context with no caller holds nothing, so this fails closed.
func HasPermission(ctx context.Context, p string) bool {
	c, ok := CallerFrom(ctx)
	return ok && c.HasPermission(p)
}
