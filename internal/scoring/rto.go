// Package scoring is declared in doc.go.
// This file implements UpdateRTO (per-provider EWMA response-time and
// throughput tracking) and PoolMedianRTO (network-wide fallback for
// providers with too few samples of their own).
//
// [REF: IC §4.2, DM §4.2, DM §9, FR-040, ADR-006, Paper 28,
// build.md Phase 8.3 Session 8.3.1]

package scoring

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/google/uuid"
)

// EWMA smoothing constants for the per-provider TCP-style RTO estimate
// (Jacobson/Karels), consistent with FR-040's "TCP-style RTO" framing
// (ADR-006, Paper 28). Named constants, not inline literals, per this
// codebase's "no magic numbers" standard.
const (
	rtoEWMAAlpha = 0.125 // 1/8 — smoothing constant for the mean (avg_rtt_ms, p95_throughput_kbps)
	rtoEWMABeta  = 0.25  // 1/4 — smoothing constant for the mean deviation (var_rtt_ms)
)

// poolMedianCacheTTL is an implementation optimisation (not a documented
// requirement) to avoid recomputing the network-wide PERCENTILE_CONT
// aggregate on every audit dispatch. Revisit the duration if measured DB load
// warrants it.
const poolMedianCacheTTL = 5 * time.Minute

// poolMedianCache holds the last computed pool-median RTO, keyed by the
// *sql.DB handle it was computed against.
//
// Milestone 8 corrections session: this used to be a single unkeyed value,
// shared across every caller regardless of which *sql.DB they passed in. In
// production there is genuinely only one pool-median RTO for the whole
// network at a given moment, as the original comment here said — but that
// argument is about there being one CORRECT ANSWER, not about there being
// only ever one *sql.DB handle asking for it. A process that (like
// GetScore/GetScoreFromPrimary, see score.go) distinguishes a
// pooled/replica-routed handle from a forced-primary one, or that runs
// parallel test suites each against their own database, would have every
// caller silently share one cache entry computed against whichever handle
// happened to populate it first — stale or simply wrong for a different
// handle, and a source of cross-test interference in exactly the kind of
// parallel Postgres-backed test suite this package's own tests use. Keying
// by *sql.DB matches GetScore/GetScoreFromPrimary's own stated philosophy:
// which handle to use is entirely the caller's concern; this package just
// serves whichever one it's given. The map is not unbounded in practice —
// *sql.DB handles are long-lived singletons created once at process startup
// (or once per test), not created per-request.
var poolMedianCache = struct {
	mu      sync.RWMutex
	entries map[*sql.DB]poolMedianCacheEntry
}{entries: make(map[*sql.DB]poolMedianCacheEntry)}

type poolMedianCacheEntry struct {
	value      float64
	computedAt time.Time
}

// UpdateRTO updates a provider's EWMA response-time and throughput statistics
// after one audit response is recorded, and increments rto_sample_count.
// Must be called once per PASS or FAIL response (a TIMEOUT has no
// response_latency_ms to sample, per DM §4.7 §8.10, and must not call this).
//
// EWMA constants follow the conventional TCP RTO estimation (Jacobson/Karels),
// consistent with FR-040's "TCP-style RTO" framing (ADR-006, Paper 28):
//
//	avg_rtt_ms' = avg_rtt_ms + alpha * (sample_ms - avg_rtt_ms),        alpha = 1/8
//	var_rtt_ms' = var_rtt_ms + beta  * (|sample_ms - avg_rtt_ms| - var_rtt_ms), beta = 1/4
//
// On the FIRST sample for a provider (avg_rtt_ms is NULL — DM §4.2), initialise
// avg_rtt_ms = sample_ms and var_rtt_ms = 0 directly, rather than blending
// against a NULL. NEVER write 0 or 2000 as a placeholder default outside of
// this first-sample initialisation (DM §9 checklist).
//
// p95_throughput_kbps is updated the same way, from the measured upload
// throughput of this response.
//
// Goroutine-safe: yes — uses SELECT ... FOR UPDATE within a transaction, since
// IC §4.2 allows the microservice to hold up to 32 concurrent challenge
// streams to a single provider, and this is a read-modify-write over the
// prior EWMA state (not spelled out as a locking requirement in this
// function's own interface description, but required by the package's
// blanket "Goroutine-safe" contract — see doc.go — given concurrent audit
// responses for one provider are an expected, not a hypothetical, case).
func UpdateRTO(ctx context.Context, db *sql.DB, providerID uuid.UUID, responseLatencyMs int, throughputKbps float64) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("scoring.UpdateRTO: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	const selectForUpdate = `
SELECT avg_rtt_ms, var_rtt_ms, p95_throughput_kbps
FROM providers
WHERE provider_id = $1
FOR UPDATE`

	var (
		avgRTT   sql.NullFloat64
		varRTT   float64 // NOT NULL DEFAULT 0 in the schema; never NULL
		p95Thpt  sql.NullFloat64
		sampleMs = float64(responseLatencyMs)
	)
	if err := tx.QueryRowContext(ctx, selectForUpdate, providerID).
		Scan(&avgRTT, &varRTT, &p95Thpt); err != nil {
		return fmt.Errorf("scoring.UpdateRTO: select for update: %w", err)
	}

	var newAvgRTT, newVarRTT, newP95Thpt float64
	if avgRTT.Valid {
		// Blend against the existing EWMA state.
		newAvgRTT = avgRTT.Float64 + rtoEWMAAlpha*(sampleMs-avgRTT.Float64)
		newVarRTT = varRTT + rtoEWMABeta*(math.Abs(sampleMs-avgRTT.Float64)-varRTT)
	} else {
		// First sample for this provider (DM §4.2, DM §9 checklist):
		// initialise directly rather than blending against a NULL baseline.
		newAvgRTT = sampleMs
		newVarRTT = 0
	}
	if p95Thpt.Valid {
		newP95Thpt = p95Thpt.Float64 + rtoEWMAAlpha*(throughputKbps-p95Thpt.Float64)
	} else {
		newP95Thpt = throughputKbps
	}

	const updateRTO = `
UPDATE providers
SET avg_rtt_ms = $1, var_rtt_ms = $2, p95_throughput_kbps = $3,
    rto_sample_count = rto_sample_count + 1
WHERE provider_id = $4`
	if _, err := tx.ExecContext(ctx, updateRTO, newAvgRTT, newVarRTT, newP95Thpt, providerID); err != nil {
		return fmt.Errorf("scoring.UpdateRTO: update: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("scoring.UpdateRTO: commit: %w", err)
	}
	return nil
}

// PoolMedianRTO returns the network-wide median RTO across ACTIVE providers,
// for use by any provider with rto_sample_count < 5 (IC §4.2, FR-040). This is
// a TRUE median (PERCENTILE_CONT(0.5)), matching the "pool-median" name used
// throughout DM §4.2 / IC §4.2 / FR-040 — NOT an AVG()/mean, which several
// providers with unusually slow or fast connections would skew.
//
//	SELECT PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY avg_rtt_ms + 4 * var_rtt_ms)
//	FROM providers WHERE status = 'ACTIVE' AND rto_sample_count >= 5
//
// The result is cached in-process for 5 minutes to avoid recomputing this
// aggregate on every audit dispatch (an implementation optimisation, not a
// documented requirement — revisit the cache duration if measured DB load
// warrants it).
//
// Error semantics:
//   - ErrNoPoolMedianAvailable: no ACTIVE provider has rto_sample_count >= 5
//     yet (PERCENTILE_CONT has no rows to aggregate over — expected only
//     very early in the network's life). Milestone 8 corrections session:
//     this used to be a plain wrapped error with no sentinel, indistinguishable
//     from any other database failure by a caller using errors.Is.
//   - other database errors: returned to caller.
//
// Goroutine-safe: yes (the shared cache is guarded by a RWMutex).
func PoolMedianRTO(ctx context.Context, db *sql.DB) (float64, error) {
	poolMedianCache.mu.RLock()
	entry, found := poolMedianCache.entries[db]
	poolMedianCache.mu.RUnlock()
	if found && time.Since(entry.computedAt) < poolMedianCacheTTL {
		return entry.value, nil
	}

	median, ok, err := queryPoolMedianRTO(ctx, db)
	if err != nil {
		return 0, fmt.Errorf("scoring.PoolMedianRTO: %w", err)
	}
	if !ok {
		return 0, fmt.Errorf("scoring.PoolMedianRTO: %w", ErrNoPoolMedianAvailable)
	}

	poolMedianCache.mu.Lock()
	poolMedianCache.entries[db] = poolMedianCacheEntry{value: median, computedAt: time.Now()}
	poolMedianCache.mu.Unlock()

	return median, nil
}

// queryPoolMedianRTO runs the PERCENTILE_CONT aggregate directly, with no
// caching — split out from PoolMedianRTO so this query's own correctness
// (true median, low-sample exclusion) can be exercised fresh on every call in
// tests, independent of the 5-minute process-wide cache above. ok is false
// when the aggregate has no qualifying rows (PERCENTILE_CONT returns SQL
// NULL, not an error, in that case).
func queryPoolMedianRTO(ctx context.Context, db *sql.DB) (median float64, ok bool, err error) {
	const query = `
SELECT PERCENTILE_CONT(0.5) WITHIN GROUP (ORDER BY avg_rtt_ms + 4 * var_rtt_ms)
FROM providers
WHERE status = 'ACTIVE' AND rto_sample_count >= 5`

	var result sql.NullFloat64
	if err := db.QueryRowContext(ctx, query).Scan(&result); err != nil {
		return 0, false, fmt.Errorf("query pool-median RTO: %w", err)
	}
	if !result.Valid {
		return 0, false, nil
	}
	return result.Float64, true, nil
}
