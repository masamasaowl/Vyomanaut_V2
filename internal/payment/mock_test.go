// Package payment is declared in doc.go.
// Unit and live-database integration tests for MockProvider.
//
// Tests:
//   - TestMockProviderInitiateEscrowCreditsLedgerImmediately
//   - TestMockProviderReleaseEscrowWritesRealRow
//   - TestMockProviderIdempotencyEnforcedByDBConstraint
//   - TestMockProviderGetBalanceMatchesRealView
//
// [REF: MVP §6.1 CR-10, MVP §7.7, build.md Phase 10.1 Session 10.1.2]

package payment

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestMockProviderInitiateEscrowCreditsLedgerImmediately(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	provider := NewMockProvider(db)

	ownerID := insertTestOwner(t, db, "")
	contractID := uuid.New()

	vpa, qrURL, err := provider.InitiateEscrow(context.Background(), ownerID, 75000, contractID)
	if err != nil {
		t.Fatalf("InitiateEscrow: %v", err)
	}
	if vpa == "" || qrURL == "" {
		t.Errorf("InitiateEscrow returned empty vpa/qrURL: %q, %q", vpa, qrURL)
	}

	// No webhook to wait for in demo mode — the deposit ledger row must
	// exist right after the call returns.
	var rows int
	var amount int64
	if err := verify.QueryRow(`SELECT COUNT(*), COALESCE(MAX(amount_paise), 0) FROM owner_escrow_events WHERE owner_id = $1 AND event_type = 'DEPOSIT'`,
		ownerID).Scan(&rows, &amount); err != nil {
		t.Fatalf("query owner_escrow_events: %v", err)
	}
	if rows != 1 {
		t.Errorf("owner_escrow_events DEPOSIT rows = %d, want 1 (credited synchronously, no webhook needed)", rows)
	}
	if amount != 75000 {
		t.Errorf("amount_paise = %d, want 75000", amount)
	}
}

func TestMockProviderReleaseEscrowWritesRealRow(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	provider := NewMockProvider(db)

	providerID := insertTestProvider(t, db, testProviderSpec{})
	auditPeriodID := insertTestAuditPeriod(t, db, providerID)
	idempotencyKey := releaseIdempotencyKeyForTest(providerID, auditPeriodID)

	if err := provider.ReleaseEscrow(context.Background(), providerID, 40000, auditPeriodID, idempotencyKey); err != nil {
		t.Fatalf("ReleaseEscrow: %v", err)
	}

	var rows int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM escrow_events WHERE provider_id = $1 AND event_type = 'RELEASE' AND idempotency_key = $2`,
		providerID, idempotencyKey).Scan(&rows); err != nil {
		t.Fatalf("query escrow_events: %v", err)
	}
	if rows != 1 {
		t.Errorf("escrow_events RELEASE rows = %d, want 1 (a real row, not an in-memory stand-in)", rows)
	}
}

func TestMockProviderIdempotencyEnforcedByDBConstraint(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	provider := NewMockProvider(db)

	providerID := insertTestProvider(t, db, testProviderSpec{})
	auditPeriodID := insertTestAuditPeriod(t, db, providerID)
	idempotencyKey := releaseIdempotencyKeyForTest(providerID, auditPeriodID)

	if err := provider.ReleaseEscrow(context.Background(), providerID, 20000, auditPeriodID, idempotencyKey); err != nil {
		t.Fatalf("ReleaseEscrow (first call): %v", err)
	}
	// Second call, same idempotency key, different amount — must be a no-op,
	// not a second row, and must not error (idempotent retry semantics).
	if err := provider.ReleaseEscrow(context.Background(), providerID, 99999, auditPeriodID, idempotencyKey); err != nil {
		t.Fatalf("ReleaseEscrow (duplicate key): %v", err)
	}

	var rows int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM escrow_events WHERE idempotency_key = $1`, idempotencyKey).Scan(&rows); err != nil {
		t.Fatalf("query: %v", err)
	}
	if rows != 1 {
		t.Errorf("escrow_events rows for this idempotency key = %d, want exactly 1 (DB UNIQUE constraint enforces "+
			"this — MVP §7.7 — not any mock-side logic)", rows)
	}
}

func TestMockProviderGetBalanceMatchesRealView(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	provider := NewMockProvider(db)

	providerID := insertTestProvider(t, db, testProviderSpec{})
	auditPeriodID := insertTestAuditPeriod(t, db, providerID)
	idempotencyKey := releaseIdempotencyKeyForTest(providerID, auditPeriodID)

	// Seed a SEIZURE directly (bypassing the provider under test) so there
	// is a nonzero, independently-known balance to compare against.
	seedKey := reversalIdempotencyKey("seed-" + idempotencyKey) // reuse this file's SHA-256 helper to stay within VARCHAR(64)
	if err := InsertEscrowEvent(context.Background(), db, providerID, EscrowSeizure, 12345, seedKey, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := verify.Exec(`REFRESH MATERIALIZED VIEW mv_provider_escrow_balance`); err != nil {
		t.Fatalf("refresh view: %v", err)
	}

	got, err := provider.GetBalance(context.Background(), providerID)
	if err != nil {
		t.Fatalf("GetBalance: %v", err)
	}

	var want int64
	if err := verify.QueryRow(`SELECT balance_paise FROM mv_provider_escrow_balance WHERE provider_id = $1`, providerID).
		Scan(&want); err != nil {
		t.Fatalf("query view directly: %v", err)
	}
	if want < 0 {
		want = 0
	}
	if got != want {
		t.Errorf("GetBalance = %d, want %d (must match a direct query against mv_provider_escrow_balance exactly)", got, want)
	}
}
