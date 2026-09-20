package apperrors

import (
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestEveryCodeMapsToAGRPCCode(t *testing.T) {
	all := []Code{
		Internal, InvalidArgument, NotFound, AlreadyExists, PermissionDenied,
		Unauthenticated, FailedPrecondition, Unavailable,
		InsufficientBalance, IdempotencyKeyConflict, IdempotencyInProgress,
		LedgerImbalance, CurrencyMismatch, WalletOperationDenied,
	}
	for _, c := range all {
		if _, ok := grpcCodes[c]; !ok {
			t.Errorf("code %q has no gRPC mapping — it would silently become Internal", c)
		}
	}
	if len(grpcCodes) != len(all) {
		t.Errorf("grpcCodes has %d entries but the test knows %d codes; keep them in step", len(grpcCodes), len(all))
	}
}

func TestGRPCStatusUsesMappedCode(t *testing.T) {
	err := New(InsufficientBalance, "wallet %s is short", "wlt_1")

	s, ok := status.FromError(err)
	if !ok {
		t.Fatal("status.FromError did not recognise *Error; GRPCStatus is not being picked up")
	}
	if s.Code() != codes.FailedPrecondition {
		t.Errorf("status code = %v, want FailedPrecondition", s.Code())
	}
	if s.Message() != "wallet wlt_1 is short" {
		t.Errorf("status message = %q, want the formatted message", s.Message())
	}
}

// Internal errors quote SQL, hostnames and driver text. That must not reach a caller.
func TestInternalMessageIsNotLeaked(t *testing.T) {
	cause := errors.New(`pq: relation "ledger_postings" does not exist`)
	err := Wrap(cause, Internal, "failed to post journal")

	if got := err.Message(); got != publicInternalMessage {
		t.Errorf("Message() = %q, want %q — internal detail leaked to the caller", got, publicInternalMessage)
	}
	s, _ := status.FromError(err)
	if s.Message() != publicInternalMessage {
		t.Errorf("gRPC status message = %q, want %q", s.Message(), publicInternalMessage)
	}

	// The detail must still be available for logs.
	if got := err.Error(); got == publicInternalMessage {
		t.Error("Error() should retain the full detail for logging")
	}
	if !errors.Is(err, cause) {
		t.Error("Wrap should preserve the cause for errors.Is")
	}
}

func TestNonInternalMessagesAreReturned(t *testing.T) {
	err := New(NotFound, "wallet %s not found", "wlt_9")
	if got := err.Message(); got != "wallet wlt_9 not found" {
		t.Errorf("Message() = %q, want the real message", got)
	}
}

// The public message must not drag the cause chain along with it: that both
// leaks internals and produces unreadable doubled text.
func TestMessageExcludesCause(t *testing.T) {
	cause := status.Error(codes.NotFound, "Not Found")
	err := Wrap(cause, NotFound, "Not Found")

	if got := err.Message(); got != "Not Found" {
		t.Errorf("Message() = %q, want just %q", got, "Not Found")
	}
	if got := err.Error(); got == err.Message() {
		t.Error("Error() should still include the cause for logging")
	}

	s, _ := status.FromError(err)
	if s.Message() != "Not Found" {
		t.Errorf("gRPC status message = %q, want the clean message", s.Message())
	}
}

func TestFromClassifiesUnknownErrorsAsInternal(t *testing.T) {
	got := From(errors.New("something odd"))
	if got.Code() != Internal {
		t.Errorf("From(plain error).Code() = %q, want %q", got.Code(), Internal)
	}
	if From(nil) != nil {
		t.Error("From(nil) should be nil")
	}
}

func TestFromPassesThroughAppErrors(t *testing.T) {
	original := New(NotFound, "gone")
	if got := From(original); got != original {
		t.Error("From should return an existing *Error unchanged")
	}

	// Also when buried behind a wrap.
	wrapped := Wrap(original, NotFound, "looking up wallet")
	if got := From(wrapped); got.Code() != NotFound {
		t.Errorf("From(wrapped).Code() = %q, want %q", got.Code(), NotFound)
	}
}

func TestFromRespectsExistingGRPCStatus(t *testing.T) {
	err := status.Error(codes.PermissionDenied, "nope")
	if got := From(err).Code(); got != PermissionDenied {
		t.Errorf("From(grpc status).Code() = %q, want %q", got, PermissionDenied)
	}
}

func TestIs(t *testing.T) {
	err := Wrap(New(InsufficientBalance, "short"), InsufficientBalance, "spending")
	if !Is(err, InsufficientBalance) {
		t.Error("Is should find the code on the outer error")
	}
	if Is(err, NotFound) {
		t.Error("Is matched the wrong code")
	}
	if Is(errors.New("plain"), NotFound) {
		t.Error("Is should be false for errors with no code")
	}
}

func TestCodeOfAndGRPCCode(t *testing.T) {
	if got := CodeOf(New(CurrencyMismatch, "x")); got != CurrencyMismatch {
		t.Errorf("CodeOf = %q, want %q", got, CurrencyMismatch)
	}
	if got := GRPCCode(New(CurrencyMismatch, "x")); got != codes.InvalidArgument {
		t.Errorf("GRPCCode = %v, want InvalidArgument", got)
	}
	if got := GRPCCode(errors.New("plain")); got != codes.Internal {
		t.Errorf("GRPCCode(plain) = %v, want Internal", got)
	}
}
