// Package client is how a product backend talks to OpenPay over gRPC.
//
// It wraps the generated client with the parts every integration otherwise
// re-implements, usually slightly wrong: the service credential on every
// call, idempotency keys, a default deadline, the stable error codes, and
// retrying only what is safe to retry.
//
//	c, err := client.New(client.Config{Target: "openpay:8086", KeyID: id, Secret: secret})
//	...
//	ctx = client.WithIdempotencyKey(ctx, "checkout:"+orderID)
//	resp, err := c.CreatePayment(ctx, req)
//	switch client.Code(err) {
//	case client.InsufficientBalance: ...
//	}
//
// Every RPC of openpay_v1.OpenPayClient is available on Client directly.
package client

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/pkg/apperrors"
)

// Config says where OpenPay is and who is calling.
type Config struct {
	// Target is OpenPay's gRPC address, e.g. "openpay.internal:8086".
	Target string
	// KeyID and Secret are the product's service credential, as issued by
	// CreateServiceCredential. The secret is shown once; keep it in a secret
	// store.
	KeyID  string
	Secret string
	// Insecure sends the credential over plaintext. Local development only:
	// anywhere else it hands the credential to anyone on the network.
	Insecure bool
	// Timeout is the deadline given to a call that has none. Default 10s.
	Timeout time.Duration
}

// Client is an OpenPay connection. It is safe for concurrent use; create one
// and share it.
type Client struct {
	openpay_v1.OpenPayClient
	conn *grpc.ClientConn
}

// New connects to OpenPay. Extra dial options are appended, e.g. for a
// custom dialer in tests.
func New(cfg Config, opts ...grpc.DialOption) (*Client, error) {
	if cfg.Target == "" || cfg.KeyID == "" || cfg.Secret == "" {
		return nil, errors.New("openpay client: Target, KeyID and Secret are required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}

	transport := credentials.NewTLS(nil)
	if cfg.Insecure {
		transport = insecure.NewCredentials()
	}
	dial := append([]grpc.DialOption{
		grpc.WithTransportCredentials(transport),
		grpc.WithPerRPCCredentials(bearer{token: cfg.KeyID + "." + cfg.Secret, insecure: cfg.Insecure}),
		grpc.WithChainUnaryInterceptor(defaultDeadline(cfg.Timeout)),
	}, opts...)

	conn, err := grpc.NewClient(cfg.Target, dial...)
	if err != nil {
		return nil, err
	}
	return &Client{OpenPayClient: openpay_v1.NewOpenPayClient(conn), conn: conn}, nil
}

// Close releases the connection.
func (c *Client) Close() error { return c.conn.Close() }

// WithIdempotencyKey attaches the key OpenPay uses to recognise a retry of
// the same action. Every call that moves money requires one.
//
// Derive it from the action, not the attempt — "checkout:<order id>", not a
// fresh random value per try — so a retry after a timeout is recognised and
// answered with the original result instead of acting twice. Reusing a key
// for a different request is refused (IdempotencyKeyConflict).
func WithIdempotencyKey(ctx context.Context, key string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "idempotency-key", key)
}

// Stable error codes a caller may branch on. The message is for people and
// may change; the code will not.
const (
	InvalidArgument        = apperrors.InvalidArgument
	NotFound               = apperrors.NotFound
	AlreadyExists          = apperrors.AlreadyExists
	PermissionDenied       = apperrors.PermissionDenied
	Unauthenticated        = apperrors.Unauthenticated
	FailedPrecondition     = apperrors.FailedPrecondition
	Unavailable            = apperrors.Unavailable
	RateLimited            = apperrors.RateLimited
	Internal               = apperrors.Internal
	InsufficientBalance    = apperrors.InsufficientBalance
	IdempotencyKeyConflict = apperrors.IdempotencyKeyConflict
	IdempotencyInProgress  = apperrors.IdempotencyInProgress
	CurrencyMismatch       = apperrors.CurrencyMismatch
	WalletOperationDenied  = apperrors.WalletOperationDenied
)

// Code is the stable code of an error returned by any Client call, "" for
// nil.
func Code(err error) apperrors.Code {
	if err == nil {
		return ""
	}
	return apperrors.CodeOf(err)
}

// Retryable reports whether trying the same call again, with the same
// idempotency key, may succeed: OpenPay or a provider was briefly
// unavailable, the caller was rate limited, or the first attempt is still
// running. Anything else will fail the same way again.
func Retryable(err error) bool {
	switch Code(err) {
	case Unavailable, RateLimited, IdempotencyInProgress:
		return true
	}
	return false
}

// Retry runs call until it succeeds, fails with an error that is not
// Retryable, has run attempts times, or ctx ends — waiting 200ms, 400ms,
// 800ms … (capped at 5s) in between. Put the idempotency key on ctx before
// calling, so every attempt is the same action.
func Retry(ctx context.Context, attempts int, call func(ctx context.Context) error) error {
	wait := 200 * time.Millisecond
	var err error
	for i := 0; i < max(attempts, 1); i++ {
		if err = call(ctx); err == nil || !Retryable(err) {
			return err
		}
		if i == attempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait = min(wait*2, 5*time.Second)
	}
	return err
}

// bearer presents the service credential on every call.
type bearer struct {
	token    string
	insecure bool
}

func (b bearer) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"authorization": "Bearer " + b.token}, nil
}

func (b bearer) RequireTransportSecurity() bool { return !b.insecure }

// defaultDeadline bounds calls the caller left unbounded, so a hung
// connection never hangs a checkout.
func defaultDeadline(timeout time.Duration) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		if _, ok := ctx.Deadline(); !ok {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}
