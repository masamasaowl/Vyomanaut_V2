// Package scoring is declared in doc.go.
// This file implements IncrementConsecutivePasses (the VETTING→ACTIVE
// transition) and ResetConsecutivePasses.
//
// [REF: IC §5.6, DM §4.2, DM §4.7, FR-026, MVP §3.4, MVP §5.4, ADR-005,
// build.md Phase 8.2 Session 8.2.1]

package scoring

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/masamasaowl/Vyomanaut_V2/internal/config"
)

// IncrementConsecutivePasses atomically increments consecutive_audit_passes
// for a provider and, if both VETTING→ACTIVE conditions are now satisfied,
// transitions status to 'ACTIVE' in the same transaction (FR-026, ADR-005).
//
// The threshold is profile.VettingMinPasses (80 in production, 5 in demo) —
// NEVER a hardcoded 80; see MVP §5.4, which names this exact function as the
// enforcement point for that field. The duration condition is also checked:
// first_chunk_assignment_at + profile.VettingMinDuration <= NOW() (FR-026,
// MVP §5.4).
//
// Pre-conditions:
//   - providerID identifies a provider with status = 'VETTING'
//
// Post-conditions (on nil error):
//   - consecutive_audit_passes is incremented by 1
//   - if the new value >= profile.VettingMinPasses AND the duration condition
//     holds, status is set to 'ACTIVE' in the same transaction
//
// Error semantics:
//   - ErrProviderNotVetting: provider is not in VETTING status; no-op, no error
//     side effects on consecutive_audit_passes
//
// Goroutine-safe: yes — uses SELECT ... FOR UPDATE within a transaction so two
// concurrent audit-PASS events for the same provider cannot both observe the
// pre-increment count and both trigger the transition redundantly.
func IncrementConsecutivePasses(ctx context.Context, db *sql.DB, providerID uuid.UUID, profile config.NetworkProfile) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("scoring.IncrementConsecutivePasses: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	const selectForUpdate = `
SELECT status, consecutive_audit_passes, first_chunk_assignment_at
FROM providers
WHERE provider_id = $1
FOR UPDATE`

	var (
		status                 string
		consecutivePasses      int
		firstChunkAssignmentAt sql.NullTime
	)
	err = tx.QueryRowContext(ctx, selectForUpdate, providerID).
		Scan(&status, &consecutivePasses, &firstChunkAssignmentAt)
	if err != nil {
		return fmt.Errorf("scoring.IncrementConsecutivePasses: select for update: %w", err)
	}

	if status != "VETTING" {
		// No-op per this function's documented error semantics: no write to
		// consecutive_audit_passes happens on this path. The deferred
		// Rollback above discards the SELECT ... FOR UPDATE's row lock
		// without having modified anything.
		return ErrProviderNotVetting
	}

	newPasses := consecutivePasses + 1

	// Duration condition (FR-026, MVP §5.4): first_chunk_assignment_at is
	// NULL until the assignment service assigns this provider's first chunk
	// (DM §4.2) — a provider that has never been assigned a chunk cannot
	// satisfy an elapsed-time-since-assignment condition, so NULL is treated
	// as "not yet satisfied" rather than an error.
	durationSatisfied := firstChunkAssignmentAt.Valid &&
		!time.Now().UTC().Before(firstChunkAssignmentAt.Time.Add(profile.VettingMinDuration))

	transitionsToActive := newPasses >= profile.VettingMinPasses && durationSatisfied

	if transitionsToActive {
		const updateAndActivate = `
UPDATE providers
SET consecutive_audit_passes = $1, status = 'ACTIVE'
WHERE provider_id = $2`
		if _, err := tx.ExecContext(ctx, updateAndActivate, newPasses, providerID); err != nil {
			return fmt.Errorf("scoring.IncrementConsecutivePasses: update (ACTIVE transition): %w", err)
		}
	} else {
		const updateCountOnly = `
UPDATE providers
SET consecutive_audit_passes = $1
WHERE provider_id = $2`
		if _, err := tx.ExecContext(ctx, updateCountOnly, newPasses, providerID); err != nil {
			return fmt.Errorf("scoring.IncrementConsecutivePasses: update (count only): %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("scoring.IncrementConsecutivePasses: commit: %w", err)
	}
	return nil
}

// ResetConsecutivePasses resets consecutive_audit_passes to 0 on any non-PASS
// audit result (ADR-005).
//
// CALLER PRE-CONDITION (enforced by the caller, not inside this function — see
// note below): the caller MUST NOT invoke this when the just-recorded audit
// row has audit_result = 'TIMEOUT' AND address_was_stale = TRUE (DM §4.7). A
// TIMEOUT against a known-stale heartbeat address is evidence the DHT
// fallback didn't work, not evidence the provider failed, and must not reset
// vetting progress. This function's signature carries no addressWasStale
// parameter (matching IC §5.6 as declared), so the gate lives in the audit
// dispatch loop (Milestone 12, Session 12.1.2) which already has the receipt
// row in hand when deciding whether to call this function at all.
//
// Goroutine-safe: yes.
func ResetConsecutivePasses(ctx context.Context, db *sql.DB, providerID uuid.UUID) error {
	const resetCount = `
UPDATE providers
SET consecutive_audit_passes = 0
WHERE provider_id = $1`
	if _, err := db.ExecContext(ctx, resetCount, providerID); err != nil {
		return fmt.Errorf("scoring.ResetConsecutivePasses: update: %w", err)
	}
	return nil
}
