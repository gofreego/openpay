package dao

import "time"

// OutboxEvent is a domain event recorded in the same transaction as the change
// that caused it, then published asynchronously.
type OutboxEvent struct {
	ID            int64
	EventID       string
	Topic         string
	AggregateType string
	AggregateID   string
	Payload       []byte
	CreatedAt     time.Time
	PublishedAt   *time.Time
	Attempts      int
	LastError     string
}
