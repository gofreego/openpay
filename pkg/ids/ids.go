// Package ids generates the prefixed, time-sortable identifiers OpenPay exposes
// over its API. Internal primary keys stay sequential integers; these are the
// public handles, so an id never leaks how many of something exists.
package ids

import (
	"strings"

	"github.com/google/uuid"

	"github.com/gofreego/goutils/customerrors"
)

// Prefix names the kind of object an id refers to, so a wallet id pasted into a
// payment lookup fails immediately and visibly rather than finding nothing.
type Prefix string

const (
	Customer          Prefix = "cus"
	Product           Prefix = "prd"
	ServiceCredential Prefix = "scr"
	WalletType        Prefix = "wtp"
	Wallet            Prefix = "wlt"
	LedgerAccount     Prefix = "acc"
	LedgerJournal     Prefix = "jrn"
	LedgerHold        Prefix = "hld"
	Payment           Prefix = "pay"
	PaymentAttempt    Prefix = "pat"
	Order             Prefix = "ord"
	Refund            Prefix = "ref"
	Dispute           Prefix = "dsp"
	Settlement        Prefix = "stl"
	Payout            Prefix = "pot"
	Idempotency       Prefix = "idk"
	OutboxEvent       Prefix = "evt"
	Request           Prefix = "req"
)

const separator = "_"

// New returns an id such as "pay_0199c4f21a7d7c8e9f0a1b2c3d4e5f60".
//
// The body is a UUIDv7, which embeds a millisecond timestamp in its leading
// bits. That makes ids sort chronologically and keeps index inserts appending
// to the right of the b-tree rather than scattering across it.
func New(p Prefix) string {
	// uuid.NewV7 only errors if the system random source fails, which Go's
	// crypto/rand treats as fatal anyway.
	return string(p) + separator + strings.ReplaceAll(uuid.Must(uuid.NewV7()).String(), "-", "")
}

// Parse splits an id and verifies it carries the expected prefix.
func Parse(id string, want Prefix) (string, error) {
	prefix, body, found := strings.Cut(id, separator)
	if !found || prefix == "" || body == "" {
		return "", customerrors.BAD_REQUEST_ERROR("malformed id %q: expected <prefix>_<body>", id)
	}
	if Prefix(prefix) != want {
		return "", customerrors.BAD_REQUEST_ERROR("expected a %q id, got %q", want, prefix)
	}
	return body, nil
}

// Is reports whether id carries the given prefix, for validation that only
// needs a yes or no.
func Is(id string, p Prefix) bool {
	prefix, body, found := strings.Cut(id, separator)
	return found && body != "" && Prefix(prefix) == p
}
