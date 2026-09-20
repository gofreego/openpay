package dao

import "time"

// IdempotencyStatus is the lifecycle of a claimed key.
type IdempotencyStatus string

const (
	// IdempotencyInProgress means the key is claimed and the work is either
	// running or was abandoned by a crashed process.
	IdempotencyInProgress IdempotencyStatus = "in_progress"
	// IdempotencyCompleted means ResponseBody holds the recorded result.
	IdempotencyCompleted IdempotencyStatus = "completed"
)

// IdempotencyKey is one caller-supplied key and what became of it.
type IdempotencyKey struct {
	ID                 int64
	Scope              string
	Key                string
	RequestFingerprint string
	Status             IdempotencyStatus
	ResponseBody       []byte
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ExpiresAt          time.Time
}
