package dao

import "time"

type ProductStatus string

const (
	ProductActive ProductStatus = "active"
	// ProductSuspended stops new activity without deleting anything: money has
	// already moved against the product and the ledger must stay interpretable.
	ProductSuspended ProductStatus = "suspended"
)

// Product is one of our own apps. It is the scoping dimension for wallets,
// orders and revenue.
type Product struct {
	ID       int64
	PublicID string

	// Code is the stable handle that appears in ledger account codes such as
	// income:zshala:product_sales, so it cannot change once set.
	Code string

	Name            string
	Status          ProductStatus
	DefaultCurrency string

	CreatedAt time.Time
	UpdatedAt time.Time
}

type CredentialStatus string

const (
	CredentialActive  CredentialStatus = "active"
	CredentialRevoked CredentialStatus = "revoked"
)

// ServiceCredential is how a product's backend authenticates to OpenPay.
type ServiceCredential struct {
	ID        int64
	PublicID  string
	ProductID int64
	Name      string

	// KeyID is the public half, used to find the row.
	KeyID string
	// SecretHash is the algorithm-tagged digest. The secret itself is shown
	// once at creation and never stored.
	SecretHash string

	Status     CredentialStatus
	LastUsedAt *time.Time
	RevokedAt  *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

func (c *ServiceCredential) IsActive() bool {
	return c.Status == CredentialActive
}
