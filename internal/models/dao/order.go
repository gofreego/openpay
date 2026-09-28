package dao

import "time"

type Item struct {
	ID             int64
	PublicID       string
	ProductID      int64
	Code           string
	Name           string
	ReferencePrice int64
	Currency       string
	TaxClass       string
	Status         string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type OrderStatus string

const (
	OrderPendingPayment OrderStatus = "pending_payment"
	OrderPaid           OrderStatus = "paid"
	OrderFailed         OrderStatus = "failed"
	OrderCancelled      OrderStatus = "cancelled"
)

// Order is what was bought. Its amounts are the product's, recorded and
// checked for arithmetic, never computed here (plan.md D13).
type Order struct {
	ID          int64
	PublicID    string
	ProductID   int64
	CustomerID  *int64
	ExternalRef string
	InvoiceRef  string

	Currency string
	Subtotal int64
	Discount int64
	Tax      int64
	Total    int64
	TaxRate  string
	// TaxBreakdownProvided is false when the product sent a bare total.
	TaxBreakdownProvided bool

	Status        OrderStatus
	FailureReason *string
	GatewayAmount int64

	ExpiresAt time.Time
	PaidAt    *time.Time

	ProductPublicID  string
	CustomerPublicID *string

	CreatedAt time.Time
	UpdatedAt time.Time
}

type OrderLine struct {
	ID          int64
	OrderID     int64
	ItemID      *int64
	Description string
	Quantity    int
	UnitAmount  int64
	Amount      int64

	ItemPublicID *string
}

type TenderKind string

const (
	TenderWallet  TenderKind = "wallet"
	TenderGateway TenderKind = "gateway"
)

type TenderStatus string

const (
	TenderHeld     TenderStatus = "held"
	TenderCaptured TenderStatus = "captured"
	TenderReleased TenderStatus = "released"
)

// OrderTender is one way an order is paid: a wallet share or the card share.
type OrderTender struct {
	ID       int64
	OrderID  int64
	Kind     TenderKind
	WalletID *int64
	Amount   int64
	Status   TenderStatus

	WalletPublicID *string
}
