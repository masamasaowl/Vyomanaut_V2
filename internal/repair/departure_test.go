// Package repair is declared in doc.go.
// Unit and live-database integration tests for the departure detector.
//
// Tests:
//   - TestDepartureDetectorCatchesActiveProviders
//   - TestDepartureDetectorCatchesVettingProviders
//   - TestDepartureDetectorIgnoresRecentHeartbeats
//   - TestDepartureDetectorNeverPhysicallyDeletesRow
//   - TestDepartureDetectorCallsPenaliseWithSeizureIdempotencyKey
//
// [REF: IC §3.1, IC §6, DM §3 Invariant 3, FR-035, FR-065, ARCH §12,
// build.md Phase 9.3 Session 9.3.1]

package repair

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/masamasaowl/Vyomanaut_V2/internal/config"
)

// penaliseCall records one invocation of a recordingPenalise mock.
type penaliseCall struct {
	providerID     uuid.UUID
	amountPaise    int64
	idempotencyKey string
}

// recordingPenalise returns a PenaliseFunc that appends every call to calls
// and always succeeds — DetectOnce in these tests runs candidates
// sequentially (no concurrency), so no locking is needed around the slice.
func recordingPenalise(calls *[]penaliseCall) PenaliseFunc {
	return func(ctx context.Context, providerID uuid.UUID, amountPaise int64, idempotencyKey string) error {
		*calls = append(*calls, penaliseCall{providerID, amountPaise, idempotencyKey})
		return nil
	}
}

func staleHeartbeat(profile config.NetworkProfile) *time.Time {
	t := time.Now().UTC().Add(-2 * profile.DepartureThreshold)
	return &t
}

func freshHeartbeat() *time.Time {
	t := time.Now().UTC()
	return &t
}

func TestDepartureDetectorCatchesActiveProviders(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	profile := config.DemoProfile

	providerID := insertTestProvider(t, db, testProviderSpec{status: "ACTIVE", lastHeartbeatTs: staleHeartbeat(profile)})
	segmentID := insertTestSegmentChain(t, db)
	shardIndex := 0
	chunkID := randChunkID()
	insertTestChunkAssignment(t, db, testChunkAssignmentSpec{
		chunkID:    chunkID,
		segmentID:  &segmentID,
		shardIndex: &shardIndex,
		providerID: providerID,
		status:     "ACTIVE",
	})

	var calls []penaliseCall
	detector := NewDepartureDetector(db, profile, recordingPenalise(&calls))
	if err := detector.DetectOnce(context.Background()); err != nil {
		t.Fatalf("DetectOnce: %v", err)
	}

	var status string
	var frozen bool
	var departedAt sql.NullTime
	if err := verify.QueryRow(`SELECT status, frozen, departed_at FROM providers WHERE provider_id = $1`, providerID).
		Scan(&status, &frozen, &departedAt); err != nil {
		t.Fatalf("query provider: %v", err)
	}
	if status != "DEPARTED" {
		t.Errorf("status = %q, want DEPARTED", status)
	}
	if !frozen {
		t.Error("frozen = false, want true")
	}
	if !departedAt.Valid {
		t.Error("departed_at is NULL, want set")
	}

	var jobCount int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM repair_jobs WHERE chunk_id = $1 AND trigger_type = 'SILENT_DEPARTURE'`,
		chunkID[:]).Scan(&jobCount); err != nil {
		t.Fatalf("count repair_jobs: %v", err)
	}
	if jobCount != 1 {
		t.Errorf("repair_jobs rows for this chunk with trigger SILENT_DEPARTURE = %d, want 1", jobCount)
	}

	found := false
	for _, c := range calls {
		if c.providerID == providerID {
			found = true
		}
	}
	if !found {
		t.Error("penalise was not called for the departed ACTIVE provider")
	}
}

func TestDepartureDetectorCatchesVettingProviders(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	profile := config.DemoProfile

	providerID := insertTestProvider(t, db, testProviderSpec{status: "VETTING", lastHeartbeatTs: staleHeartbeat(profile)})
	chunkID := randChunkID()
	insertTestChunkAssignment(t, db, testChunkAssignmentSpec{
		chunkID:        chunkID,
		isVettingChunk: true,
		providerID:     providerID,
		status:         "ACTIVE",
	})

	var calls []penaliseCall
	detector := NewDepartureDetector(db, profile, recordingPenalise(&calls))
	if err := detector.DetectOnce(context.Background()); err != nil {
		t.Fatalf("DetectOnce: %v", err)
	}

	var status string
	if err := verify.QueryRow(`SELECT status FROM providers WHERE provider_id = $1`, providerID).Scan(&status); err != nil {
		t.Fatalf("query provider: %v", err)
	}
	if status != "DEPARTED" {
		t.Errorf("provider status = %q, want DEPARTED", status)
	}

	var assignmentStatus string
	var deletedAt sql.NullTime
	if err := verify.QueryRow(`SELECT status, deleted_at FROM chunk_assignments WHERE chunk_id = $1 AND provider_id = $2`,
		chunkID[:], providerID).Scan(&assignmentStatus, &deletedAt); err != nil {
		t.Fatalf("query chunk_assignments: %v", err)
	}
	if assignmentStatus != "DELETED" {
		t.Errorf("chunk_assignments.status = %q, want DELETED", assignmentStatus)
	}
	if !deletedAt.Valid {
		t.Error("deleted_at is NULL, want set")
	}

	var jobCount int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM repair_jobs WHERE chunk_id = $1`, chunkID[:]).Scan(&jobCount); err != nil {
		t.Fatalf("count repair_jobs: %v", err)
	}
	if jobCount != 0 {
		t.Errorf("repair_jobs rows for this vetting chunk = %d, want 0 (FR-065: zero repair jobs for a vetting departure)", jobCount)
	}

	found := false
	for _, c := range calls {
		if c.providerID == providerID {
			found = true
		}
	}
	if !found {
		t.Error("penalise was not called for the departed VETTING provider (escrow seizure still applies)")
	}
}

func TestDepartureDetectorIgnoresRecentHeartbeats(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	profile := config.DemoProfile

	providerID := insertTestProvider(t, db, testProviderSpec{status: "ACTIVE", lastHeartbeatTs: freshHeartbeat()})

	var calls []penaliseCall
	detector := NewDepartureDetector(db, profile, recordingPenalise(&calls))
	if err := detector.DetectOnce(context.Background()); err != nil {
		t.Fatalf("DetectOnce: %v", err)
	}

	var status string
	if err := verify.QueryRow(`SELECT status FROM providers WHERE provider_id = $1`, providerID).Scan(&status); err != nil {
		t.Fatalf("query provider: %v", err)
	}
	if status != "ACTIVE" {
		t.Errorf("status = %q, want unchanged ACTIVE (heartbeat is recent)", status)
	}

	for _, c := range calls {
		if c.providerID == providerID {
			t.Error("penalise was called for a provider with a recent heartbeat")
		}
	}
}

func TestDepartureDetectorNeverPhysicallyDeletesRow(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	profile := config.DemoProfile

	insertTestProvider(t, db, testProviderSpec{status: "ACTIVE", lastHeartbeatTs: staleHeartbeat(profile)})
	insertTestProvider(t, db, testProviderSpec{status: "VETTING", lastHeartbeatTs: staleHeartbeat(profile)})

	var before int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM providers`).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}

	var calls []penaliseCall
	detector := NewDepartureDetector(db, profile, recordingPenalise(&calls))
	if err := detector.DetectOnce(context.Background()); err != nil {
		t.Fatalf("DetectOnce: %v", err)
	}

	var after int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM providers`).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != before {
		t.Errorf("providers row count changed from %d to %d, want unchanged (DM §3 Invariant 3: never physically deleted)",
			before, after)
	}
}

func TestDepartureDetectorCallsPenaliseWithSeizureIdempotencyKey(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	profile := config.DemoProfile

	providerID := insertTestProvider(t, db, testProviderSpec{status: "ACTIVE", lastHeartbeatTs: staleHeartbeat(profile)})

	const depositPaise = 50000
	if _, err := verify.Exec(`
		INSERT INTO escrow_events (provider_id, event_type, amount_paise, idempotency_key)
		VALUES ($1, 'DEPOSIT', $2, $3)`,
		providerID, depositPaise, uuid.New().String()); err != nil {
		t.Fatalf("seed escrow_events: %v", err)
	}

	var calls []penaliseCall
	detector := NewDepartureDetector(db, profile, recordingPenalise(&calls))
	if err := detector.DetectOnce(context.Background()); err != nil {
		t.Fatalf("DetectOnce: %v", err)
	}

	var call *penaliseCall
	for i := range calls {
		if calls[i].providerID == providerID {
			call = &calls[i]
		}
	}
	if call == nil {
		t.Fatal("penalise was never called for this provider")
	}
	if call.amountPaise != depositPaise {
		t.Errorf("amountPaise = %d, want %d (the provider's full current escrow balance)", call.amountPaise, depositPaise)
	}
	if len(call.idempotencyKey) != 64 {
		t.Errorf("idempotencyKey length = %d, want 64 (SHA-256 as hex)", len(call.idempotencyKey))
	}
}
