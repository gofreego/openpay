package service

import (
	"strings"

	"github.com/gofreego/openpay/pkg/apperrors"
)

// validatable is what protoc-gen-validate generates on every request message.
type validatable interface {
	ValidateAll() error
}

// validate runs the rules declared in the proto.
//
// It is called at the top of each service method rather than from an
// interceptor because the HTTP and gRPC paths converge only here: the gateway
// registers the service in-process and never runs gRPC interceptors, so an
// interceptor would silently validate one path and not the other.
//
// ValidateAll reports every broken rule rather than stopping at the first, so a
// caller fixing a request does not have to discover the problems one at a time.
func validate(req any) error {
	v, ok := req.(validatable)
	if !ok {
		return nil
	}
	if err := v.ValidateAll(); err != nil {
		return apperrors.Wrap(err, apperrors.InvalidArgument, "%s", summarizeValidation(err))
	}
	return nil
}

// summarizeValidation flattens the generated multierror into one line. The full
// error is preserved as the wrapped cause for the logs.
func summarizeValidation(err error) string {
	msg := err.Error()
	// The generated text is already readable; collapse newlines so the message
	// survives JSON and log formatting intact.
	return strings.Join(strings.Fields(msg), " ")
}
