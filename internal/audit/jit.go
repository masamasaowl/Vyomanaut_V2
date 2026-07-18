// Package audit is declared in doc.go.
// This file implements JIT (just-in-time retrieval) anomaly detection per
// ARCH §20 §Outsourcing and ARCH §14's audit-receipt-schema jit_flag row.
// EvaluateJIT is pure (no shared mutable state) and goroutine-safe.
//
// UNITS CORRECTION — read before touching the constants below. ARCH §20 and
// ARCH §14 both state the floor as a bare "× 0.3": jit_flag fires when
// response_latency_ms < (chunk_size / p95_throughput_kbps) × 0.3. Taken
// completely literally, with p95_throughput_kbps in KB/s (confirmed by
// DM §4.2's own column comment) and chunk_size = 256 (KB), the quotient
// chunk_size/p95_throughput is in SECONDS, so "× 0.3" alone yields a
// threshold on the order of tenths of a second — but the left-hand side,
// response_latency_ms, is in MILLISECONDS. Compared directly, that
// threshold is under one millisecond for any realistic throughput, so
// EVERY response would satisfy responseLatencyMs < floor and jit_flag
// would fire on essentially every PASS — the feature would misfire
// constantly rather than detect anything anomalous.
//
// DM §4.2's own audit-deadline formula for the exact same quotient uses
// deadline_ms = (chunk_size_kb / p95_throughput_kbps) × 1500, not the bare
// safety factor ARCH §20 separately documents for the deadline on its own —
// 1500 already folds that safety factor together with the ×1000
// seconds-to-milliseconds conversion the quotient needs before it can be
// compared against a milliseconds value. This file applies the identical
// ×1000 conversion to the JIT floor's own 0.3 fraction, keeping the two
// factors (0.3 and 1000) separate constants rather than silently folding
// them into a single combined constant the way the deadline formula folds
// its own two factors into "1500" — so the 0.3 ARCH §20 actually specifies
// stays visible on its own, rather than disappearing into an unexplained
// constant. Flagged here rather than silently worked around; ARCH §20/§14
// should be corrected to state the floor the same explicit way DM §4.2
// states the deadline.
//
//	jit_flag = responseLatencyMs < (chunkSizeKB / p95ThroughputKbps) * 0.3 * 1000
//
// [REF: ARCH §20 §Outsourcing, ARCH §14, DM §4.2, ADR-014 Defence 3]

package audit

// ── Constants ──────────────────────────────────────────────────────────────

const (
	// chunkSizeKB is the fixed shard size in kilobytes. ShardSize = 262,144
	// bytes = 256 KB is a compile-time constant in both demo and production
	// (DM §3 Invariant 7); JIT detection always uses this fixed value, never
	// a per-request size.
	chunkSizeKB = 256

	// jitFloorFraction is the ARCH §20 / ARCH §14 anomaly fraction: a
	// response arriving in under 0.3× the time a genuine local-disk read at
	// the provider's own measured p95 throughput would take is flagged as
	// anomalously fast (possible JIT retrieval from a co-located source).
	// See the UNITS CORRECTION note above for why this is not compared
	// against responseLatencyMs on its own.
	jitFloorFraction = 0.3

	// msPerSecond converts the chunkSizeKB/p95ThroughputKbps quotient
	// (seconds, since p95ThroughputKbps is KB/s per DM §4.2) into
	// milliseconds, matching responseLatencyMs's unit. See the UNITS
	// CORRECTION note above.
	msPerSecond = 1000
)

// EvaluateJIT computes whether an audit response is anomalously fast — a
// signal of just-in-time retrieval from a co-located source rather than a
// genuine local-disk read (ARCH §20 §Outsourcing, ARCH §14 jit_flag row).
//
// chunkSizeKB is always 256, since ShardSize = 262,144 bytes = 256 KB is a
// compile-time constant in both demo and production modes (DM §3
// Invariant 7).
//
// p95ThroughputKbps may be nil for a new provider (DM §4.2: NULL until
// vetting audits accumulate samples). When nil, this function returns false
// (no flag) — deliberately, and asymmetrically from how the audit deadline
// handles the same NULL case (DM §4.2: deadline computation substitutes the
// pool median). JIT detection is a best-effort anti-fraud heuristic, not a
// required gate; flagging a new, fast, honest provider before its real
// throughput is known would be a false positive with real consequences
// (score/escrow impact), so this function prefers a false negative over
// that risk. A zero (rather than nil) p95ThroughputKbps is treated the same
// way — DM §4.2 documents a prior schema revision that defaulted this
// column to 0 instead of NULL specifically because an unguarded division by
// it produces exactly this kind of bug; a value at that CHECK constraint's
// floor is treated as "unestablished," not "infinitely fast," for the same
// reason DM §4.2 gives.
//
// NOTE: this threshold is distinct from the per-provider RTO / audit
// deadline (IC §4.2, DM §4.2's deadline_ms formula) — that is a maximum
// wait time before TIMEOUT; this is a minimum plausible time below which a
// PASS is flagged as suspicious. Do not conflate the two multipliers.
//
// Goroutine-safe: yes (pure function, no shared mutable state).
//
// [REF: ARCH §20 §Outsourcing, ARCH §14, DM §4.2, ADR-014 Defence 3]
func EvaluateJIT(responseLatencyMs int, p95ThroughputKbps *float64) bool {
	if p95ThroughputKbps == nil || *p95ThroughputKbps <= 0 {
		return false
	}

	floorMs := (chunkSizeKB / *p95ThroughputKbps) * jitFloorFraction * msPerSecond
	return float64(responseLatencyMs) < floorMs
}
