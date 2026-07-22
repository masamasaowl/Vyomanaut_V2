// Package payment is declared in doc.go.
// Unit and live-database integration tests for the monthly release
// computation.
//
// Tests:
//   - TestComputeMonthlyReleaseFullMultiplierAboveThreshold
//   - TestComputeMonthlyReleasePartialMultiplierTiers
//   - TestComputeMonthlyReleaseDualWindowUsesLowerMultiplier
//   - TestComputeMonthlyReleaseSkipsStaleScore
//   - TestComputeMonthlyReleaseIdempotentOnRerun
//
// [REF: FR-048, FR-049, FR-050, DM §7, build.md Phase 10.4 Session 10.4.1]

package payment

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/masamasaowl/Vyomanaut_V2/internal/config"
)

// ── Pure multiplier-table tests (no database needed) ──────────────────────────

func TestComputeMonthlyReleaseFullMultiplierAboveThreshold(t *testing.T) {
	const score30dBP = 9700 // 0.97
	if got := releaseMultiplierBasisPoints(score30dBP); got != multiplierFullBP {
		t.Errorf("releaseMultiplierBasisPoints(%d) = %d, want %d (full release)", score30dBP, got, multiplierFullBP)
	}
}

func TestComputeMonthlyReleasePartialMultiplierTiers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scoreBP int64
		want    int64
	}{
		{"full, at the boundary", 9500, multiplierFullBP},
		{"full, above the boundary", 9900, multiplierFullBP},
		{"partial-hi, at the boundary", 8000, multiplierPartialHiBP},
		{"partial-hi, mid-band", 8700, multiplierPartialHiBP},
		{"partial-lo, at the boundary", 6500, multiplierPartialLoBP},
		{"partial-lo, mid-band", 7200, multiplierPartialLoBP},
		{"none, just below the floor", 6499, multiplierNoneBP},
		{"none, zero", 0, multiplierNoneBP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := releaseMultiplierBasisPoints(tc.scoreBP); got != tc.want {
				t.Errorf("releaseMultiplierBasisPoints(%d) = %d, want %d", tc.scoreBP, got, tc.want)
			}
		})
	}
}

// TestComputeMonthlyReleaseSkipsStaleScore verifies the exact 60-minute
// staleness boundary (DM §7). See isScoreStale's own comment (release.go)
// for why this is a direct unit test of the boundary function rather than a
// full end-to-end run: mv_provider_scores.scores_as_of is tied to genuine
// wall-clock NOW() at refresh time and cannot be faked via seed data — a
// true integration test of this exact path would require actually waiting
// 61 real minutes.
func TestComputeMonthlyReleaseSkipsStaleScore(t *testing.T) {
	fresh := time.Now().UTC()
	if isScoreStale(fresh) {
		t.Error("isScoreStale(now) = true, want false")
	}

	justUnder := time.Now().UTC().Add(-59 * time.Minute)
	if isScoreStale(justUnder) {
		t.Error("isScoreStale(59 minutes old) = true, want false")
	}

	stale := time.Now().UTC().Add(-61 * time.Minute)
	if !isScoreStale(stale) {
		t.Error("isScoreStale(61 minutes old) = false, want true")
	}
}

// ── Live-database fixtures shared by the remaining tests ──────────────────────

func insertTestAuditReceiptForRelease(t *testing.T, verify *sql.DB, providerID uuid.UUID, challengeTS time.Time, result string) {
	t.Helper()
	var chunkID [32]byte
	_, _ = rand.Read(chunkID[:])
	var nonce [33]byte
	_, _ = rand.Read(nonce[:])

	var responseHash, providerSig interface{} // SQL NULL unless PASS/FAIL
	if result == "PASS" || result == "FAIL" {
		var h [32]byte
		_, _ = rand.Read(h[:])
		var s [64]byte
		_, _ = rand.Read(s[:])
		responseHash = h[:]
		providerSig = s[:]
	}

	_, err := verify.Exec(`
		INSERT INTO audit_receipts (
			chunk_id, provider_id, challenge_nonce, server_challenge_ts,
			audit_result, response_hash, provider_sig
		) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		chunkID[:], providerID, nonce[:], challengeTS, result, responseHash, providerSig,
	)
	if err != nil {
		t.Fatalf("insertTestAuditReceiptForRelease: %v", err)
	}
}

func refreshProviderScoresForRelease(t *testing.T, verify *sql.DB) {
	t.Helper()
	if _, err := verify.Exec(`REFRESH MATERIALIZED VIEW CONCURRENTLY mv_provider_scores`); err != nil {
		t.Fatalf("refreshProviderScoresForRelease: %v", err)
	}
}

// testEscrowSeedKey generates a fresh, valid-length idempotency key for
// seeding an escrow_events row with no relationship to the actual function
// under test's own key derivation.
func testEscrowSeedKey() string {
	return reversalIdempotencyKey(uuid.New().String())
}

// drainPendingReleaseCandidates marks every currently-pending
// (release_computed = FALSE) audit_periods row as computed, WITHOUT
// actually processing them. audit_periods accumulates such rows across this
// whole shared test database — from this package's own other fixtures
// (mock_test.go, razorpay_test.go each create one via insertTestAuditPeriod
// without ever setting release_computed) and from repeated test runs in
// this same persistent database across the build session — and
// ComputeMonthlyRelease has no way to scope itself to just one test's own
// rows. Call this before seeding a test's own pending audit period so
// ComputeMonthlyRelease only ever sees what that test cares about,
// mirroring internal/repair's drainQueue pattern (Milestone 9) for the
// exact same class of shared-table accumulation.
func drainPendingReleaseCandidates(t *testing.T, verify *sql.DB) {
	t.Helper()
	if _, err := verify.Exec(`UPDATE audit_periods SET release_computed = TRUE WHERE release_computed = FALSE`); err != nil {
		t.Fatalf("drainPendingReleaseCandidates: %v", err)
	}
}

// ── Live-database tests ─────────────────────────────────────────────────────────

// TestComputeMonthlyReleaseDualWindowUsesLowerMultiplier seeds a provider
// with a strong 30-day history (8 PASS, 8-20 days ago, outside the 7-day
// window) but a recently-degraded 7-day window (2 FAIL, 1-2 days ago):
// score_30d = 8/10 = 0.8 (partial-hi tier, 7500bp), score_7d = 0/2 = 0.0
// (none tier, 0bp), triggering DualWindowFlag (0.8 - 0.0 > 0.20). The
// applied multiplier must be the LOWER of the two tiers (0bp), not the
// higher 30-day tier.
func TestComputeMonthlyReleaseDualWindowUsesLowerMultiplier(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	drainPendingReleaseCandidates(t, verify)
	providerID := insertTestProvider(t, db, testProviderSpec{})
	auditPeriodID := insertTestAuditPeriod(t, db, providerID)

	now := time.Now().UTC()
	for _, daysAgo := range []int{8, 10, 12, 14, 16, 18, 19, 20} {
		insertTestAuditReceiptForRelease(t, verify, providerID, now.Add(-time.Duration(daysAgo)*24*time.Hour), "PASS")
	}
	for _, daysAgo := range []int{1, 2} {
		insertTestAuditReceiptForRelease(t, verify, providerID, now.Add(-time.Duration(daysAgo)*24*time.Hour), "FAIL")
	}
	refreshProviderScoresForRelease(t, verify)

	if err := InsertEscrowEvent(context.Background(), db, providerID, EscrowDeposit, 100000, testEscrowSeedKey(), nil); err != nil {
		t.Fatalf("seed balance: %v", err)
	}
	if _, err := verify.Exec(`REFRESH MATERIALIZED VIEW mv_provider_escrow_balance`); err != nil {
		t.Fatalf("refresh balance view: %v", err)
	}

	var released []int64
	mock := &recordingPaymentProvider{
		onRelease: func(providerID uuid.UUID, amountPaise int64, auditPeriodID uuid.UUID, idempotencyKey string) error {
			released = append(released, amountPaise)
			return nil
		},
	}

	if err := ComputeMonthlyRelease(context.Background(), db, db, testDemoProfileForRelease(), mock); err != nil {
		t.Fatalf("ComputeMonthlyRelease: %v", err)
	}

	// score_7d = 0.0 -> multiplierNoneBP (0) -> releaseAmountPaise = 0 ->
	// ReleaseEscrow is never called at all (see computeReleaseForProvider's
	// own "if releaseAmountPaise > 0" gate).
	if len(released) != 0 {
		t.Errorf("ReleaseEscrow was called with amounts %v, want it never called (dual-window multiplier is 0)", released)
	}

	var computed bool
	if err := verify.QueryRow(`SELECT release_computed FROM audit_periods WHERE id = $1`, auditPeriodID).Scan(&computed); err != nil {
		t.Fatalf("query audit_periods: %v", err)
	}
	if !computed {
		t.Error("release_computed = false, want true (the period is still fully processed even at a zero multiplier)")
	}
}

// TestComputeMonthlyReleaseIdempotentOnRerun runs ComputeMonthlyRelease
// twice for the same pending audit period and confirms exactly one RELEASE
// event exists afterward.
func TestComputeMonthlyReleaseIdempotentOnRerun(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	drainPendingReleaseCandidates(t, verify)
	providerID := insertTestProvider(t, db, testProviderSpec{})
	auditPeriodID := insertTestAuditPeriod(t, db, providerID)

	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		insertTestAuditReceiptForRelease(t, verify, providerID, now.Add(-time.Duration(i)*time.Hour), "PASS")
	}
	refreshProviderScoresForRelease(t, verify)

	if err := InsertEscrowEvent(context.Background(), db, providerID, EscrowDeposit, 50000, testEscrowSeedKey(), nil); err != nil {
		t.Fatalf("seed balance: %v", err)
	}
	if _, err := verify.Exec(`REFRESH MATERIALIZED VIEW mv_provider_escrow_balance`); err != nil {
		t.Fatalf("refresh balance view: %v", err)
	}

	mock := &recordingPaymentProvider{
		onRelease: func(providerID uuid.UUID, amountPaise int64, auditPeriodID uuid.UUID, idempotencyKey string) error {
			// Simulate the real ledger write, exactly like RazorpayProvider/
			// MockProvider's own ReleaseEscrow would — including treating a
			// duplicate idempotency key as an idempotent success, not an
			// error, so a second call with the SAME key genuinely exercises
			// the DB constraint the same way the real implementations do.
			err := InsertEscrowEvent(context.Background(), db, providerID, EscrowRelease, amountPaise, idempotencyKey, &auditPeriodID)
			if errors.Is(err, ErrDuplicateIdempotencyKey) {
				return nil
			}
			return err
		},
	}

	profile := testDemoProfileForRelease()
	if err := ComputeMonthlyRelease(context.Background(), db, db, profile, mock); err != nil {
		t.Fatalf("ComputeMonthlyRelease (first run): %v", err)
	}

	// Reset release_computed so the second run actually reconsiders this
	// audit period (otherwise pendingReleaseCandidates would simply not
	// select it a second time, which wouldn't test idempotency at all).
	if _, err := verify.Exec(`UPDATE audit_periods SET release_computed = FALSE WHERE id = $1`, auditPeriodID); err != nil {
		t.Fatalf("reset release_computed: %v", err)
	}

	if err := ComputeMonthlyRelease(context.Background(), db, db, profile, mock); err != nil {
		t.Fatalf("ComputeMonthlyRelease (second run): %v", err)
	}

	var rows int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM escrow_events WHERE provider_id = $1 AND event_type = 'RELEASE'`,
		providerID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("RELEASE rows after two runs for the same audit period = %d, want exactly 1", rows)
	}
}

// recordingPaymentProvider is a minimal PaymentProvider stand-in for
// release_test.go — only ReleaseEscrow is exercised by ComputeMonthlyRelease,
// so the other methods panic if ever called (they should never be).
type recordingPaymentProvider struct {
	onRelease func(providerID uuid.UUID, amountPaise int64, auditPeriodID uuid.UUID, idempotencyKey string) error
}

var _ PaymentProvider = (*recordingPaymentProvider)(nil)

func (r *recordingPaymentProvider) InitiateEscrow(context.Context, uuid.UUID, int64, uuid.UUID) (string, string, error) {
	panic("recordingPaymentProvider.InitiateEscrow: unexpectedly called")
}

func (r *recordingPaymentProvider) ReleaseEscrow(_ context.Context, providerID uuid.UUID, amountPaise int64, auditPeriodID uuid.UUID, idempotencyKey string) error {
	return r.onRelease(providerID, amountPaise, auditPeriodID, idempotencyKey)
}

func (r *recordingPaymentProvider) Penalise(context.Context, uuid.UUID, int64, string) error {
	panic("recordingPaymentProvider.Penalise: unexpectedly called")
}

func (r *recordingPaymentProvider) GetBalance(context.Context, uuid.UUID) (int64, error) {
	panic("recordingPaymentProvider.GetBalance: unexpectedly called")
}

func (r *recordingPaymentProvider) WithdrawOwnerEscrow(context.Context, uuid.UUID, int64, string) (string, error) {
	panic("recordingPaymentProvider.WithdrawOwnerEscrow: unexpectedly called")
}

// testDemoProfileForRelease returns a NetworkProfile suitable for these
// tests — only ReleaseComputationInterval matters to ComputeMonthlyRelease
// itself (it does not branch on it directly; RunReleaseComputationLoop
// does), so config.DemoProfile is used as-is.
func testDemoProfileForRelease() config.NetworkProfile {
	return config.DemoProfile
}
