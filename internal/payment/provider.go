// Package payment is declared in doc.go.
// This file declares PaymentProvider, the abstraction over Razorpay Route +
// Smart Collect 2.0 + RazorpayX (IC §5.8, ADR-011).
//
// [Flagged, build.md Phase 10.1] IC §5.8's own INVARIANT block describes
// passing the forbidden numeric type as something that "panics in debug
// builds." In Go this needs no runtime check at all: every amountPaise
// parameter below is declared int64, and Go's static type system already
// refuses — at compile time — to pass any other numeric type to an int64
// parameter without an explicit, visible conversion. A compile-time
// guarantee is strictly stronger than a runtime panic would be, so no
// separate debug-build check is implemented; the type signature itself is
// the enforcement.
//
// [REF: IC §5.8, ADR-011, ADR-012, ADR-024, FR-059, MVP §6.1 CR-10,
// build.md Phase 10.1 Session 10.1.1]

package payment

import (
	"context"

	"github.com/google/uuid"
)

// PaymentProvider abstracts over Razorpay Route + Smart Collect 2.0 +
// RazorpayX (IC §5.8, ADR-011). Every amountPaise parameter and return
// value is int64 paise (₹1 = 100 paise) — see this file's header comment
// for why that alone is sufficient enforcement of DM §3 Invariant 4 and
// IC §11's absolute ban on any non-integer numeric type in this package.
type PaymentProvider interface {
	// InitiateEscrow creates a virtual UPI address for a data owner and
	// records the expected deposit amount. contractID is an opaque
	// caller-supplied correlation ID (e.g. a file_id or deposit-request
	// ID) — no document in scope defines the term "contract" for this
	// package beyond IC §5.8's own use of the parameter name, so it is
	// implemented as an unattached correlation value this package never
	// interprets, rather than guessed at a more specific meaning.
	//
	// Pre-conditions:
	//   - amountPaise > 0
	// Goroutine-safe: yes.
	InitiateEscrow(ctx context.Context, ownerID uuid.UUID, amountPaise int64, contractID uuid.UUID) (vpa string, qrURL string, err error)

	// ReleaseEscrow initiates a monthly payout to a provider's Razorpay
	// Linked Account. idempotencyKey = SHA-256(providerID || auditPeriodID),
	// hex-encoded, mandatory per Razorpay's X-Payout-Idempotency requirement
	// (ADR-012, FR-047).
	//
	// Pre-conditions:
	//   - amountPaise > 0
	//   - idempotencyKey is a 64-character hex string
	// Post-conditions (on nil error):
	//   - a payout transfer is initiated to the provider's Razorpay Linked Account
	//   - the transfer's on_hold_until is set to the last working day of the
	//     current month (FR-048)
	// Goroutine-safe: yes.
	ReleaseEscrow(ctx context.Context, providerID uuid.UUID, amountPaise int64, auditPeriodID uuid.UUID, idempotencyKey string) error

	// Penalise seizes a departed provider's rolling escrow window (ADR-024 §5).
	// idempotencyKey = SHA-256(providerID || "seizure" || departedAt).
	//
	// Pre-conditions:
	//   - amountPaise > 0
	// Goroutine-safe: yes.
	Penalise(ctx context.Context, providerID uuid.UUID, amountPaise int64, idempotencyKey string) error

	// GetBalance returns a provider's current balance, always
	// SUM(DEPOSIT + REVERSAL) - SUM(RELEASE + SEIZURE); never negative.
	//
	// Goroutine-safe: yes.
	GetBalance(ctx context.Context, providerID uuid.UUID) (int64, error)

	// WithdrawOwnerEscrow pays out a data owner's available balance to their
	// UPI-linked bank account (FR-059). idempotencyKey =
	// SHA-256(ownerID || withdrawalRequestID), hex-encoded.
	//
	// [Flagged, build.md Phase 10.1] No owner-withdrawal method exists
	// anywhere in the originally-scoped interface contract, despite FR-059
	// requiring it and Milestone 11 Session 11.5.6 assuming it already
	// exists to call. Added here, structured to parallel ReleaseEscrow as
	// closely as possible.
	//
	// Must be rejected by the caller (Milestone 11, Session 11.5.6) while any
	// upload is in-flight for this owner — this method itself does not check
	// that, nor does it check the requested amount against the owner's
	// available balance (total minus the next-30-days reserve, FR-059); both
	// are the caller's responsibility before this is ever invoked.
	//
	// Goroutine-safe: yes.
	WithdrawOwnerEscrow(ctx context.Context, ownerID uuid.UUID, amountPaise int64, idempotencyKey string) (payoutID string, err error)
}
