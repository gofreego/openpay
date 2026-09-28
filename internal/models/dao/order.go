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
	OrderPendingPayment    OrderStatus = "pending_payment"
	OrderPaid              OrderStatus = "paid"
	OrderPartiallyRefunded OrderStatus = "partially_refunded"
	OrderRefunded          OrderStatus = "refunded"
	OrderFailed            OrderStatus = "failed"
	OrderCancelled         OrderStatus = "cancelled"
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

	// Refunded so far, per component. The database refuses any beyond the
	// order's own.
	RefundedSubtotal int64
	RefundedDiscount int64
	RefundedTax      int64

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
	// RefundedAmount is what refunds have returned of this tender so far.
	RefundedAmount int64

	WalletPublicID *string
}

// OrderRefund returns part of an order, with its own tax breakdown (D13).
type OrderRefund struct {
	ID        int64
	PublicID  string
	OrderID   int64
	ProductID int64

	Amount, Subtotal, Discount, Tax int64
	// TaxBreakdownProvided is false when the split was allocated
	// proportionally because the product did not send one.
	TaxBreakdownProvided bool

	Destination string
	ReasonCode  string
	Memo        string
	RequestedBy string
	CreatedAt   time.Time

	OrderPublicID string
}

// OrderRefundPart is the share of a refund returned for one tender.
type OrderRefundPart struct {
	ID            int64
	OrderRefundID int64
	TenderID      int64
	Amount        int64
	// Exactly one of these: a wallet credit, or a card refund.
	WalletID *int64
	RefundID *int64

	WalletPublicID *string
	RefundPublicID *string
}
