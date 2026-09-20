// Package apperrors defines OpenPay's error taxonomy.
//
// Every failure a caller might reasonably branch on gets a stable string code.
// Those strings are part of the API contract: they outlive message wording,
// survive translation, and are what an integrating service should switch on.
// HTTP status and gRPC code are derived from the code, never chosen by hand at
// the call site.
package apperrors

import (
	"errors"
	"fmt"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Code is a stable, machine-readable error identifier.
type Code string

const (
	// Transport-shaped codes, for failures with no more specific meaning.
	Internal           Code = "internal"
	InvalidArgument    Code = "invalid_argument"
	NotFound           Code = "not_found"
	AlreadyExists      Code = "already_exists"
	PermissionDenied   Code = "permission_denied"
	Unauthenticated    Code = "unauthenticated"
	FailedPrecondition Code = "failed_precondition"
	Unavailable        Code = "unavailable"

	// Domain codes. Add one whenever a caller would sensibly handle a failure
	// differently, rather than overloading a generic code with prose.
	InsufficientBalance    Code = "insufficient_balance"
	IdempotencyKeyConflict Code = "idempotency_key_conflict"
	IdempotencyInProgress  Code = "idempotency_in_progress"
	LedgerImbalance        Code = "ledger_imbalance"
	CurrencyMismatch       Code = "currency_mismatch"
	WalletOperationDenied  Code = "wallet_operation_denied"
)

// grpcCodes maps each code onto the wire. grpc-gateway turns the gRPC code into
// an HTTP status, so this table is the single place transport semantics live.
var grpcCodes = map[Code]codes.Code{
	Internal:           codes.Internal,
	InvalidArgument:    codes.InvalidArgument,
	NotFound:           codes.NotFound,
	AlreadyExists:      codes.AlreadyExists,
	PermissionDenied:   codes.PermissionDenied,
	Unauthenticated:    codes.Unauthenticated,
	FailedPrecondition: codes.FailedPrecondition,
	Unavailable:        codes.Unavailable,

	InsufficientBalance:    codes.FailedPrecondition,
	IdempotencyKeyConflict: codes.AlreadyExists,
	IdempotencyInProgress:  codes.Aborted,
	LedgerImbalance:        codes.Internal,
	CurrencyMismatch:       codes.InvalidArgument,
	WalletOperationDenied:  codes.FailedPrecondition,
}

// publicInternalMessage replaces the real message for internal failures, which
// routinely quote SQL, hostnames and driver text. The detail stays on the error
// for logging; it just never crosses the wire.
const publicInternalMessage = "internal error"

// Error is an application error carrying a stable Code.
type Error struct {
	code  Code
	msg   string
	cause error
}

func New(code Code, format string, args ...any) *Error {
	return &Error{code: code, msg: fmt.Sprintf(format, args...)}
}

// Wrap attaches a code and context to an existing error, preserving the cause
// for errors.Is and errors.As.
func Wrap(err error, code Code, format string, args ...any) *Error {
	return &Error{code: code, msg: fmt.Sprintf(format, args...), cause: err}
}

func (e *Error) Error() string {
	if e.cause != nil {
		return e.msg + ": " + e.cause.Error()
	}
	return e.msg
}

func (e *Error) Unwrap() error { return e.cause }
func (e *Error) Code() Code    { return e.code }

// Message is the text safe to return to a caller.
//
// Deliberately only this error's own message, never the wrapped cause: the
// cause is the internal detail that belongs in logs, and appending it produces
// both a leak and unreadable chains like
// "Not Found: rpc error: code = NotFound desc = Not Found".
func (e *Error) Message() string {
	if e.code == Internal {
		return publicInternalMessage
	}
	return e.msg
}

// GRPCStatus lets the gRPC runtime derive the right status automatically:
// status.FromError checks for this interface, so a handler can simply return
// the error and get the correct code without an interceptor translating it.
func (e *Error) GRPCStatus() *status.Status {
	c, ok := grpcCodes[e.code]
	if !ok {
		c = codes.Internal
	}
	return status.New(c, e.Message())
}

// GRPCCode reports the gRPC code an error maps to.
func GRPCCode(err error) codes.Code {
	return From(err).GRPCStatus().Code()
}

// CodeOf returns the stable code for any error, classifying unknown ones as
// Internal. Handy for logging and metrics without unwrapping by hand.
func CodeOf(err error) Code {
	return From(err).Code()
}

// Is reports whether err carries the given code anywhere in its chain.
func Is(err error, code Code) bool {
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr.code == code
	}
	return false
}

// From normalizes any error into an *Error. Errors that already carry a code
// pass through unchanged; anything else becomes Internal, on the principle that
// an unclassified failure is not something a caller should branch on.
func From(err error) *Error {
	if err == nil {
		return nil
	}
	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr
	}
	// Respect a status already set by gRPC or another library.
	if s, ok := status.FromError(err); ok && s.Code() != codes.Unknown {
		return &Error{code: codeFromGRPC(s.Code()), msg: s.Message(), cause: err}
	}
	return &Error{code: Internal, msg: err.Error(), cause: err}
}

func codeFromGRPC(c codes.Code) Code {
	switch c {
	case codes.InvalidArgument, codes.OutOfRange:
		return InvalidArgument
	case codes.NotFound:
		return NotFound
	case codes.AlreadyExists:
		return AlreadyExists
	case codes.PermissionDenied:
		return PermissionDenied
	case codes.Unauthenticated:
		return Unauthenticated
	case codes.FailedPrecondition:
		return FailedPrecondition
	case codes.Unavailable, codes.DeadlineExceeded:
		return Unavailable
	default:
		return Internal
	}
}
