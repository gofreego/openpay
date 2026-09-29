package client_test

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"

	"github.com/gofreego/openpay/api/openpay_v1"
	"github.com/gofreego/openpay/internal/appcontext"
	"github.com/gofreego/openpay/internal/auth"
	"github.com/gofreego/openpay/internal/ledger"
	"github.com/gofreego/openpay/internal/middleware"
	"github.com/gofreego/openpay/internal/payment"
	"github.com/gofreego/openpay/internal/provider/mock"
	"github.com/gofreego/openpay/internal/service"
	"github.com/gofreego/openpay/internal/testsupport"
	"github.com/gofreego/openpay/pkg/apperrors"
	"github.com/gofreego/openpay/pkg/client"
	"github.com/gofreego/openpay/pkg/ids"
)

// server runs the real service behind a real gRPC server with the production
// interceptors, on an in-memory listener, and issues a credential for a
// fresh product.
func server(t *testing.T) (dial grpc.DialOption, keyID, secret string) {
	t.Helper()
	repo := testsupport.Repository(t)
	svc := service.NewService(context.Background(), &service.Config{
		Payments: payment.Config{Providers: []string{mock.Name},
			Mock: payment.MockConfig{Enabled: true, WebhookSecret: "test", CheckoutURL: "https://mock.test/checkout/"}},
		Encryption: testsupport.EncryptionConfig(),
	}, repo)

	ops := func() context.Context {
		return appcontext.WithCaller(context.Background(), appcontext.Caller{
			Kind: appcontext.KindOperator, UserID: "op_test", IdempotencyKey: ids.New(ids.Idempotency),
			Permissions: []string{auth.PermScopeAll, auth.PermProductsWrite, auth.PermCredentialsWrite},
		})
	}
	product, err := svc.CreateProduct(ops(), &openpay_v1.CreateProductRequest{Code: "zshala", Name: "Zshala", DefaultCurrency: "INR"})
	if err != nil {
		t.Fatalf("product: %v", err)
	}
	if _, err := ledger.EnsureChart(context.Background(), repo, ledger.ChartConfig{Providers: []string{mock.Name}}); err != nil {
		t.Fatalf("chart: %v", err)
	}
	cred, err := svc.CreateServiceCredential(ops(), &openpay_v1.CreateServiceCredentialRequest{
		ProductId: product.GetProduct().GetId(), Name: "client test"})
	if err != nil {
		t.Fatalf("credential: %v", err)
	}

	lis := bufconn.Listen(1 << 20)
	s := grpc.NewServer(grpc.ChainUnaryInterceptor(
		middleware.CallerUnaryInterceptor(auth.New(repo)),
		middleware.ErrorUnaryInterceptor(),
	))
	openpay_v1.RegisterOpenPayServer(s, svc)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)

	return grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		cred.GetCredential().GetKeyId(), cred.GetSecret()
}

func connect(t *testing.T, dial grpc.DialOption, keyID, secret string) *client.Client {
	t.Helper()
	c, err := client.New(client.Config{Target: "passthrough:///openpay", KeyID: keyID, Secret: secret, Insecure: true}, dial)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// A product backend's day, through the client: authenticate, register a
// customer, open a wallet, start a checkout — and a retried checkout with
// the same key is the same checkout.
func TestClientEndToEnd(t *testing.T) {
	dial, keyID, secret := server(t)
	c := connect(t, dial, keyID, secret)
	ctx := context.Background()

	customer, err := c.UpsertCustomer(ctx, &openpay_v1.UpsertCustomerRequest{ExternalRef: "openauth|7"})
	if err != nil {
		t.Fatalf("customer: %v", err)
	}
	wallet, err := c.OpenWallet(ctx, &openpay_v1.OpenWalletRequest{CustomerId: customer.GetCustomer().GetId(), WalletTypeCode: "MAIN"})
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}

	checkout := &openpay_v1.CreatePaymentRequest{
		Purpose: openpay_v1.PaymentPurpose_PAYMENT_PURPOSE_WALLET_TOPUP, WalletId: wallet.GetWallet().GetId(),
		Amount: 50000, Currency: "INR", Description: "top-up"}
	keyed := client.WithIdempotencyKey(ctx, "topup:order-1")
	first, err := c.CreatePayment(keyed, checkout)
	if err != nil {
		t.Fatalf("payment: %v", err)
	}
	var again *openpay_v1.CreatePaymentResponse
	if err := client.Retry(keyed, 3, func(ctx context.Context) error {
		again, err = c.CreatePayment(ctx, checkout)
		return err
	}); err != nil {
		t.Fatalf("retried payment: %v", err)
	}
	if again.GetPayment().GetId() != first.GetPayment().GetId() {
		t.Errorf("retry created %s, want the original %s", again.GetPayment().GetId(), first.GetPayment().GetId())
	}

	// The same key for a different request is a conflict, by its own code.
	checkout.Amount = 60000
	_, err = c.CreatePayment(keyed, checkout)
	if client.Code(err) != client.IdempotencyKeyConflict {
		t.Errorf("reused key: code %q (%v), want %q", client.Code(err), err, client.IdempotencyKeyConflict)
	}
}

// Domain codes arrive intact over gRPC: a gRPC caller can tell a wallet
// refusing an operation from any other precondition, as an HTTP caller can.
func TestClientSeesDomainCodes(t *testing.T) {
	dial, keyID, secret := server(t)
	c := connect(t, dial, keyID, secret)
	ctx := context.Background()

	customer, err := c.UpsertCustomer(ctx, &openpay_v1.UpsertCustomerRequest{ExternalRef: "openauth|8"})
	if err != nil {
		t.Fatalf("customer: %v", err)
	}
	main, err := c.OpenWallet(ctx, &openpay_v1.OpenWalletRequest{CustomerId: customer.GetCustomer().GetId(), WalletTypeCode: "MAIN"})
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}
	// MAIN holds purchased money: it is not grantable.
	_, err = c.GrantWallet(client.WithIdempotencyKey(ctx, "grant-1"), &openpay_v1.GrantWalletRequest{
		WalletId: main.GetWallet().GetId(), Amount: 100, ReasonCode: "promotion"})
	if client.Code(err) != client.WalletOperationDenied {
		t.Errorf("grant into MAIN: code %q (%v), want %q", client.Code(err), err, client.WalletOperationDenied)
	}
	if client.Retryable(err) {
		t.Error("a refused operation is not retryable")
	}
}

func TestClientWithWrongSecret(t *testing.T) {
	dial, keyID, _ := server(t)
	c := connect(t, dial, keyID, "not-the-secret")
	_, err := c.UpsertCustomer(context.Background(), &openpay_v1.UpsertCustomerRequest{ExternalRef: "x"})
	if client.Code(err) != client.Unauthenticated {
		t.Errorf("code %q, want %q", client.Code(err), client.Unauthenticated)
	}
}

func TestClientRefusesPlaintextCredentialsByDefault(t *testing.T) {
	if _, err := client.New(client.Config{Target: "openpay:8086"}); err == nil {
		t.Error("a client without a credential was built")
	}
	c, err := client.New(client.Config{Target: "passthrough:///openpay", KeyID: "k", Secret: "s"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer c.Close()
	// TLS by default: over an insecure connection the credential is withheld.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := c.GetProduct(ctx, &openpay_v1.GetProductRequest{Id: "x"}); err == nil {
		t.Error("call succeeded without a reachable TLS server")
	}
}

func TestRetryStopsOnPermanentErrors(t *testing.T) {
	calls := 0
	err := client.Retry(context.Background(), 5, func(context.Context) error {
		calls++
		return apperrors.New(apperrors.InvalidArgument, "bad")
	})
	if calls != 1 || client.Code(err) != client.InvalidArgument {
		t.Errorf("permanent error: %d calls, code %q", calls, client.Code(err))
	}

	calls = 0
	err = client.Retry(context.Background(), 3, func(context.Context) error {
		calls++
		if calls < 3 {
			return apperrors.New(apperrors.Unavailable, "blip")
		}
		return nil
	})
	if err != nil || calls != 3 {
		t.Errorf("transient error: %d calls, err %v; want success on the third", calls, err)
	}
}
