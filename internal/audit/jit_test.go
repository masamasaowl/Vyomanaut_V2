// Package audit is declared in doc.go.
// Unit tests for EvaluateJIT.
//
// Tests:
//   - TestEvaluateJITFlagsAnomalouslyFastResponse latency well below the floor -> true
//   - TestEvaluateJITDoesNotFlagNormalResponse     latency well above the floor -> false
//   - TestEvaluateJITSkipsOnNilThroughput           p95ThroughputKbps == nil -> false, no panic
//   - TestEvaluateJITSkipsOnZeroThroughput          p95ThroughputKbps == 0 -> false, no panic (bonus — see jit.go)
//   - TestEvaluateJITBoundaryExactlyAtThreshold     latency == floor -> false; strict less-than
//
// [REF: ARCH §20 §Outsourcing, ARCH §14, build.md Phase 7.5 Session 7.5.1]

package audit

import "testing"

// TestEvaluateJITFlagsAnomalouslyFastResponse verifies a response arriving
// well under the floor is flagged. At 500 KB/s, the floor is
// (256/500) × 0.3 × 1000 = 153.6ms; 50ms is comfortably below that.
func TestEvaluateJITFlagsAnomalouslyFastResponse(t *testing.T) {
	throughput := 500.0
	if !EvaluateJIT(50, &throughput) {
		t.Error("EvaluateJIT(50ms, 500KB/s): expected true (anomalously fast), got false")
	}
}

// TestEvaluateJITDoesNotFlagNormalResponse verifies a response arriving
// well above the floor (153.6ms at 500 KB/s) — but still comfortably under
// the separate ~768ms audit deadline for the same throughput — is not
// flagged.
func TestEvaluateJITDoesNotFlagNormalResponse(t *testing.T) {
	throughput := 500.0
	if EvaluateJIT(400, &throughput) {
		t.Error("EvaluateJIT(400ms, 500KB/s): expected false (normal latency), got true")
	}
}

// TestEvaluateJITSkipsOnNilThroughput verifies a nil p95ThroughputKbps
// (unestablished for a new provider, DM §4.2) never flags and never panics.
func TestEvaluateJITSkipsOnNilThroughput(t *testing.T) {
	if EvaluateJIT(1, nil) {
		t.Error("EvaluateJIT(1ms, nil): expected false, got true")
	}
	if EvaluateJIT(0, nil) {
		t.Error("EvaluateJIT(0ms, nil): expected false, got true")
	}
}

// TestEvaluateJITSkipsOnZeroThroughput verifies a zero (as opposed to nil)
// p95ThroughputKbps is treated the same as unestablished, not as an
// infinitely-fast floor via division by zero. See jit.go's doc comment for
// why this guard exists alongside the nil check.
func TestEvaluateJITSkipsOnZeroThroughput(t *testing.T) {
	zero := 0.0
	if EvaluateJIT(1, &zero) {
		t.Error("EvaluateJIT(1ms, 0KB/s): expected false, got true")
	}
}

// TestEvaluateJITBoundaryExactlyAtThreshold verifies strict less-than
// semantics at the exact computed floor. The expected floor is derived here
// using the identical expression EvaluateJIT itself uses (rather than a
// hand-computed literal), so this test is not sensitive to float64
// rounding specifics of the 0.3 × 1000 multiplication — it verifies the
// boundary property against whatever value the implementation actually
// derives, not an independently-asserted number.
func TestEvaluateJITBoundaryExactlyAtThreshold(t *testing.T) {
	throughput := 256.0
	floorMs := (chunkSizeKB / throughput) * jitFloorFraction * msPerSecond
	atThreshold := int(floorMs)

	if EvaluateJIT(atThreshold, &throughput) {
		t.Errorf("EvaluateJIT(%d, ...): latency exactly at the floor (%v) must NOT be flagged (strict less-than)", atThreshold, floorMs)
	}
	if !EvaluateJIT(atThreshold-1, &throughput) {
		t.Errorf("EvaluateJIT(%d, ...): latency one ms below the floor (%v) must be flagged", atThreshold-1, floorMs)
	}
}
