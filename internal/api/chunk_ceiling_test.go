// Package api is declared in doc.go.
// Tests for the per-provider chunk storage ceiling (NFR-044) added to
// upload.go, readiness.go, and provider.go: build.md Milestone 11
// Phase 11.11, Session 11.11.1.
//
// Tests:
//   - TestChunkCeilingAt180DaysMatchesArchitectureDoc
//   - TestChunkCeilingAt300DaysMatchesArchitectureDoc
//   - TestChunkCeilingExcludesProvidersOverLimit
//   - TestChunkCeilingReturns503WhenPoolTooSmall
//
// The last two exercise providersAtOrOverChunkCount/enforceProviderCapacity
// directly with a small, explicit maxChunks rather than going through
// HandleAssign with architecture.md's real ~267,000-chunk (70 GB)
// threshold — see upload.go's own doc comments on providersAtOrOverChunkCount
// and enforceProviderCapacity for why maxChunks is an explicit parameter on
// both. This is the same "test the right level, not the least effort at the
// most global level" reasoning already used for admin_test.go's
// TestAdminProvidersRequiresAdminAPIKey.
//
// [REF: NFR-044, architecture.md §27.3, ADR-004, ADR-010, build.md
// Phase 11.11 Session 11.11.1]

package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestChunkCeilingAt180DaysMatchesArchitectureDoc(t *testing.T) {
	got := storageCeilingForMTTFDays(mttfTier180Days)
	if got != storageCeiling180DaysGB {
		t.Errorf("storageCeilingForMTTFDays(180) = %d, want %d (architecture.md §27.3 — not the 78GB a naive mttf_days/300*130 formula gives)", got, storageCeiling180DaysGB)
	}
}

func TestChunkCeilingAt300DaysMatchesArchitectureDoc(t *testing.T) {
	got := storageCeilingForMTTFDays(mttfTier300Days)
	if got != storageCeiling300DaysGB {
		t.Errorf("storageCeilingForMTTFDays(300) = %d, want %d (architecture.md §27.3)", got, storageCeiling300DaysGB)
	}
}

func TestChunkCeilingExcludesProvidersOverLimit(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	const maxChunks = 3

	ownerID := insertTestOwnerDirect(t, db)
	fileID := insertTestFileDirect(t, db, ownerID)

	pubOver, _, _ := ed25519.GenerateKey(nil)
	overID := insertTestProviderDirect(t, db, pubOver, "ACTIVE")
	overSegment := insertTestSegmentDirect(t, db, fileID, 0)
	for i := 0; i < maxChunks; i++ { // exactly at maxChunks -> "at or over"
		idx := i
		insertChunkAssignmentDirect(t, db, overID, &overSegment, &idx, "ACTIVE")
	}

	pubUnder, _, _ := ed25519.GenerateKey(nil)
	underID := insertTestProviderDirect(t, db, pubUnder, "ACTIVE")
	underSegment := insertTestSegmentDirect(t, db, fileID, 1)
	idx0 := 0
	insertChunkAssignmentDirect(t, db, underID, &underSegment, &idx0, "ACTIVE") // 1 < maxChunks

	overCeiling, err := providersAtOrOverChunkCount(ctx, db, maxChunks)
	if err != nil {
		t.Fatalf("providersAtOrOverChunkCount: %v", err)
	}
	sawOver, sawUnder := false, false
	for _, id := range overCeiling {
		if id == overID {
			sawOver = true
		}
		if id == underID {
			sawUnder = true
		}
	}
	if !sawOver {
		t.Errorf("expected provider %s (%d chunks, at ceiling) in over-ceiling set", overID, maxChunks)
	}
	if sawUnder {
		t.Errorf("did not expect provider %s (1 chunk, under ceiling) in over-ceiling set", underID)
	}
}

func TestChunkCeilingReturns503WhenPoolTooSmall(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// A maxChunks this large means nothing this shared test database could
	// possibly accumulate counts as "over ceiling" — eligibleActiveProvider-
	// CountAtOrUnder degenerates to "every ACTIVE provider that exists right
	// now." Requiring one more than that baseline guarantees the capacity
	// check fails deterministically, regardless of how many ACTIVE
	// providers other tests have already seeded in this run (the same
	// baseline-delta technique admin_test.go's TestRepairQueueFiltersByStatus
	// and TestVettingStatusAggregatesAcrossProviders already use for this
	// same shared-database-accumulation reason).
	const maxChunks = 1_000_000_000
	baselineEligible, err := eligibleActiveProviderCountAtOrUnder(ctx, db, maxChunks)
	if err != nil {
		t.Fatalf("baseline eligibleActiveProviderCountAtOrUnder: %v", err)
	}

	w := httptest.NewRecorder()
	overCeiling, ok := enforceProviderCapacity(ctx, w, db, maxChunks, baselineEligible+1)
	if ok {
		t.Fatal("enforceProviderCapacity returned ok=true, want false (eligible pool one short of required)")
	}
	if overCeiling != nil {
		t.Errorf("overCeiling = %v, want nil on the failure path", overCeiling)
	}
	if w.Code != 503 {
		t.Fatalf("status = %d, want 503, body = %s", w.Code, w.Body.String())
	}
	var body errorBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.ErrorCode != ErrInsufficientProviderCapacity {
		t.Errorf("error_code = %q, want %q", body.ErrorCode, ErrInsufficientProviderCapacity)
	}
	if body.RetryAfter == nil || *body.RetryAfter != insufficientProviderCapacityRetryAfterSeconds {
		t.Errorf("retry_after = %v, want %d", body.RetryAfter, insufficientProviderCapacityRetryAfterSeconds)
	}
}
