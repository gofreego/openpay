package dao

import "time"

type ActorType string

const (
	// ActorOperator is a person acting through the admin console.
	ActorOperator ActorType = "operator"
	// ActorService is a product's backend acting on behalf of its users.
	ActorService ActorType = "service"
	// ActorSystem is a background worker with no caller behind it.
	ActorSystem ActorType = "system"
)

// AuditEntry records one change. The table is append-only: an audit trail that
// can be edited is not one.
type AuditEntry struct {
	ID int64

	ActorType ActorType
	ActorID   string

	// Action is dotted past tense, e.g. product.created.
	Action       string
	ResourceType string
	ResourceID   string

	ProductID *int64
	RequestID string

	// Before and After are JSON. Keeping both means an entry answers what
	// changed, not merely that something did. Before is nil on creation.
	Before []byte
	After  []byte

	CreatedAt time.Time
}
