// Package scoring is declared in doc.go.
// Unit and live-database integration tests for UpdateRTO and PoolMedianRTO
// (Session 8.3.1). Reuses the DB fixture plumbing declared in score_test.go.
//
// TestPoolMedianRTOIsTrueMedian and TestPoolMedianRTOExcludesLowSampleProviders
// call the unexported queryPoolMedianRTO directly rather than the exported
// PoolMedianRTO, deliberately bypassing the 5-minute cache: PoolMedianRTO
// takes no parameters to seed with per-test data, so a package-level,
// non-resettable cache would otherwise make a SECOND correctness test within
// the same 5-minute window observe the FIRST test's cached value instead of
// its own freshly-seeded rows, regardless of whether the underlying query is
// actually correct. Splitting query execution (queryPoolMedianRTO) from
// caching (PoolMedianRTO) keeps each concern independently testable — an
// Engineering Review decision (build skill §2 Step 3), not a spec deviation:
// PoolMedianRTO's own declared signature and behaviour are unchanged.
// TestPoolMedianRTOCachedFor5Minutes is the one test that exercises the
// caching wrapper itself, and deliberately does not assert a specific value —
// only that two calls within the TTL agree despite the underlying data
// changing in between.
//
// Because providers rows accumulate across this whole test binary (nothing
// here deletes rows), the two correctness tests assert their result lands in
// a wide tolerance band around their intended cluster rather than an exact
// value — robust to whatever qualifying rows an earlier test in this file
// left behind, while still clearly distinguishing "median" from "mean" and
// "excluded" from "included".
//
// Tests:
//   - TestUpdateRTOFirstSampleInitialises       NULL avg_rtt_ms/var_rtt_ms -> avg=sample, var=0, not 0/2000
//   - TestUpdateRTOSubsequentSampleBlends        second call blends via EWMA, does not overwrite
//   - TestUpdateRTOIncrementsSampleCount         rto_sample_count += 1 each call
//   - TestUpdateRTOThroughputNeverDefaultsToZero first sample's throughput is never silently zeroed
//   - TestPoolMedianRTOIsTrueMedian               skewing outlier -> result tracks the median, not the mean
//   - TestPoolMedianRTOExcludesLowSampleProviders low-sample outlier excluded from the pool
//   - TestPoolMedianRTOCachedFor5Minutes           two calls within the TTL agree despite changed data
//
// [REF: IC §4.2, DM §4.2, DM §9, FR-040, ADR-006, build.md Phase 8.3 Session 8.3.1]

package scoring

import (
	"context"
	"database/sql"
	"testing"

	"github.com/google/uuid"
)

// ── UpdateRTO ──────────────────────────────────────────────────────────────────

// TestUpdateRTOFirstSampleInitialises verifies that a provider with NULL
// avg_rtt_ms/p95_throughput_kbps is initialised directly to the sample value
// on its first UpdateRTO call, with var_rtt_ms set to 0 — never blended
// against a NULL, and never defaulted to a hardcoded 0/2000 placeholder
// (DM §9 checklist).
func TestUpdateRTOFirstSampleInitialises(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := insertTestProvider(t, db, testProviderSpec{})

	if err := UpdateRTO(context.Background(), db, providerID, 100, 500.0); err != nil {
		t.Fatalf("UpdateRTO: %v", err)
	}

	var avgRTT, varRTT, p95Thpt float64
	var sampleCount int
	if err := verify.QueryRow(`
		SELECT avg_rtt_ms, var_rtt_ms, p95_throughput_kbps, rto_sample_count
		FROM providers WHERE provider_id = $1`, providerID).
		Scan(&avgRTT, &varRTT, &p95Thpt, &sampleCount); err != nil {
		t.Fatalf("query final state: %v", err)
	}
	if !floatsClose(avgRTT, 100.0) {
		t.Errorf("avg_rtt_ms = %v, want 100.0 (direct initialisation, not blended)", avgRTT)
	}
	if !floatsClose(varRTT, 0.0) {
		t.Errorf("var_rtt_ms = %v, want 0.0 on first sample", varRTT)
	}
	if !floatsClose(p95Thpt, 500.0) {
		t.Errorf("p95_throughput_kbps = %v, want 500.0 (direct initialisation, not blended)", p95Thpt)
	}
	if sampleCount != 1 {
		t.Errorf("rto_sample_count = %d, want 1", sampleCount)
	}
}

// TestUpdateRTOSubsequentSampleBlends verifies a second call blends the new
// sample into the existing EWMA state via alpha=1/8, beta=1/4, rather than
// overwriting it with the raw new sample.
func TestUpdateRTOSubsequentSampleBlends(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := insertTestProvider(t, db, testProviderSpec{})

	if err := UpdateRTO(context.Background(), db, providerID, 100, 500.0); err != nil {
		t.Fatalf("first UpdateRTO: %v", err)
	}
	if err := UpdateRTO(context.Background(), db, providerID, 200, 600.0); err != nil {
		t.Fatalf("second UpdateRTO: %v", err)
	}

	const wantAvg = 100.0 + 0.125*(200.0-100.0) // 112.5
	const wantVar = 0.0 + 0.25*(100.0-0.0)      // 25.0 (|200-100| - 0)
	const wantP95 = 500.0 + 0.125*(600.0-500.0) // 512.5

	var avgRTT, varRTT, p95Thpt float64
	var sampleCount int
	if err := verify.QueryRow(`
		SELECT avg_rtt_ms, var_rtt_ms, p95_throughput_kbps, rto_sample_count
		FROM providers WHERE provider_id = $1`, providerID).
		Scan(&avgRTT, &varRTT, &p95Thpt, &sampleCount); err != nil {
		t.Fatalf("query final state: %v", err)
	}
	if !floatsClose(avgRTT, wantAvg) {
		t.Errorf("avg_rtt_ms = %v, want %v (EWMA blend, not overwrite with raw 200)", avgRTT, wantAvg)
	}
	if !floatsClose(varRTT, wantVar) {
		t.Errorf("var_rtt_ms = %v, want %v", varRTT, wantVar)
	}
	if !floatsClose(p95Thpt, wantP95) {
		t.Errorf("p95_throughput_kbps = %v, want %v (EWMA blend, not overwrite with raw 600)", p95Thpt, wantP95)
	}
	if sampleCount != 2 {
		t.Errorf("rto_sample_count = %d, want 2", sampleCount)
	}
}

// TestUpdateRTOIncrementsSampleCount verifies rto_sample_count increments by
// exactly 1 on every call.
func TestUpdateRTOIncrementsSampleCount(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := insertTestProvider(t, db, testProviderSpec{})

	const calls = 3
	for i := 0; i < calls; i++ {
		if err := UpdateRTO(context.Background(), db, providerID, 100+i*10, 500.0); err != nil {
			t.Fatalf("UpdateRTO call %d: %v", i+1, err)
		}
	}

	var sampleCount int
	if err := verify.QueryRow(`SELECT rto_sample_count FROM providers WHERE provider_id = $1`, providerID).
		Scan(&sampleCount); err != nil {
		t.Fatalf("query: %v", err)
	}
	if sampleCount != calls {
		t.Errorf("rto_sample_count = %d, want %d after %d calls", sampleCount, calls, calls)
	}
}

// TestUpdateRTOThroughputNeverDefaultsToZero verifies a clearly-nonzero first
// throughput sample is never silently zeroed — guarding against exactly the
// kind of hardcoded-placeholder bug the DM §9 checklist warns about for
// p95_throughput_kbps.
func TestUpdateRTOThroughputNeverDefaultsToZero(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := insertTestProvider(t, db, testProviderSpec{})

	if err := UpdateRTO(context.Background(), db, providerID, 150, 750.0); err != nil {
		t.Fatalf("UpdateRTO: %v", err)
	}

	var p95Thpt float64
	if err := verify.QueryRow(`SELECT p95_throughput_kbps FROM providers WHERE provider_id = $1`, providerID).
		Scan(&p95Thpt); err != nil {
		t.Fatalf("query: %v", err)
	}
	if floatsClose(p95Thpt, 0.0) {
		t.Errorf("p95_throughput_kbps = %v, want 750.0 — must never silently default to 0", p95Thpt)
	}
	if !floatsClose(p95Thpt, 750.0) {
		t.Errorf("p95_throughput_kbps = %v, want 750.0", p95Thpt)
	}
}

// ── PoolMedianRTO ──────────────────────────────────────────────────────────────

// insertQualifyingRTOProvider inserts an ACTIVE provider with rto_sample_count
// >= 5 (qualifying for the pool-median aggregate) and var_rtt_ms = 0, so its
// contribution to avg_rtt_ms + 4*var_rtt_ms equals avgRTT exactly.
func insertQualifyingRTOProvider(t *testing.T, db *sql.DB, avgRTT float64) {
	t.Helper()
	v := avgRTT
	insertTestProvider(t, db, testProviderSpec{
		status:         "ACTIVE",
		avgRTTMs:       &v,
		varRTTMs:       0,
		rtoSampleCount: 5,
	})
}

// TestPoolMedianRTOIsTrueMedian seeds four providers clustered around 100-130
// plus one extreme outlier (90000). A true median lands within the cluster;
// an arithmetic mean would be dragged into the tens of thousands by the
// single outlier. This test asserts the result stays in the cluster band —
// not an exact value, since providers accumulate across this file's other
// PoolMedianRTO tests (see file header) — while still clearly distinguishing
// median from mean.
func TestPoolMedianRTOIsTrueMedian(t *testing.T) {
	db := openTestDB(t)
	for _, avgRTT := range []float64{100, 110, 120, 130, 90000} {
		insertQualifyingRTOProvider(t, db, avgRTT)
	}

	median, ok, err := queryPoolMedianRTO(context.Background(), db)
	if err != nil {
		t.Fatalf("queryPoolMedianRTO: %v", err)
	}
	if !ok {
		t.Fatal("queryPoolMedianRTO: ok = false, want true (qualifying providers were seeded)")
	}
	if median < 90 || median > 200 {
		t.Errorf("median = %v, want within [90, 200] (the clustered values) — got dragged toward the mean (~18092) or the outlier (90000) instead of the true median", median)
	}
}

// TestPoolMedianRTOExcludesLowSampleProviders seeds a cluster of qualifying
// (rto_sample_count=5) providers around 100-140, plus one NON-qualifying
// provider (rto_sample_count=2, an extreme 500000 avg_rtt_ms) that must be
// excluded by the WHERE clause. If the exclusion were broken, the extreme
// value would dominate the result; asserting the result stays in the
// clustered band proves it was excluded.
func TestPoolMedianRTOExcludesLowSampleProviders(t *testing.T) {
	db := openTestDB(t)
	for _, avgRTT := range []float64{100, 110, 120, 130, 140} {
		insertQualifyingRTOProvider(t, db, avgRTT)
	}
	extreme := 500000.0
	insertTestProvider(t, db, testProviderSpec{
		status:         "ACTIVE",
		avgRTTMs:       &extreme,
		varRTTMs:       0,
		rtoSampleCount: 2, // below the rto_sample_count >= 5 threshold
	})

	median, ok, err := queryPoolMedianRTO(context.Background(), db)
	if err != nil {
		t.Fatalf("queryPoolMedianRTO: %v", err)
	}
	if !ok {
		t.Fatal("queryPoolMedianRTO: ok = false, want true")
	}
	if median < 90 || median > 200 {
		t.Errorf("median = %v, want within [90, 200] — the rto_sample_count=2 provider's extreme value (500000) leaked into the pool", median)
	}
}

// TestPoolMedianRTOCachedFor5Minutes verifies that a second call to the
// exported PoolMedianRTO, made immediately after the first, returns the SAME
// value even though the underlying data is deliberately changed in between —
// proving the 5-minute in-process cache is in effect. The specific value
// returned is not asserted (see file header): only that the two calls agree.
func TestPoolMedianRTOCachedFor5Minutes(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)

	ids := make([]uuid.UUID, 0, 5)
	for _, avgRTT := range []float64{100, 110, 120, 130, 140} {
		v := avgRTT
		id := insertTestProvider(t, db, testProviderSpec{
			status:         "ACTIVE",
			avgRTTMs:       &v,
			varRTTMs:       0,
			rtoSampleCount: 5,
		})
		ids = append(ids, id)
	}

	first, err := PoolMedianRTO(context.Background(), db)
	if err != nil {
		t.Fatalf("PoolMedianRTO (first call): %v", err)
	}

	// Drastically mutate the very providers just seeded. A fresh (uncached)
	// query would return something near 999999; a cached one will not.
	for _, id := range ids {
		if _, err := verify.Exec(`UPDATE providers SET avg_rtt_ms = 999999 WHERE provider_id = $1`, id); err != nil {
			t.Fatalf("mutate provider for cache test: %v", err)
		}
	}

	second, err := PoolMedianRTO(context.Background(), db)
	if err != nil {
		t.Fatalf("PoolMedianRTO (second call): %v", err)
	}

	if !floatsClose(first, second) {
		t.Errorf("PoolMedianRTO changed from %v to %v within the cache TTL window despite underlying data changing — caching does not appear to be in effect", first, second)
	}
}