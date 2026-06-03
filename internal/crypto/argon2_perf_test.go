//go:build !short

// Package crypto is declared in doc.go.
// Performance tests for DeriveMasterSecret, tagged !short so they are skipped
// in fast CI runs (go test -short ./...).
//
// TestArgon2idProduction verifies production parameters (t=3, m=65536 KiB, p=4)
// complete in >= 20ms. This floor is intentionally conservative: the raw cost
// ratio between production and demo parameters is ~192× (3×65536×4 vs 1×4096×1),
// so even on the fastest consumer hardware production cannot complete as quickly
// as demo. The IC §5.1 post-condition target of >= 200ms holds on reference
// hardware; the 20ms floor here catches accidental parameter substitution without
// being fragile on modern CPUs.
//
// TestArgon2idDemo verifies demo parameters (t=1, m=4096 KiB, p=1) complete
// in < 5000ms. The limit is generous: demo derives keys interactively during
// account registration, and the full upload→audit→repair cycle must stay under
// 30 minutes on a laptop.
//
// [REF: IC §5.1, MVP §3.5, build.md Phase 2.3 Session 2.3.2]

package crypto

import (
	"testing"
	"time"
)

// perfPassphrase is the passphrase used for both performance tests.
// Not a real secret — test fixture only.
var perfPassphrase = []byte("perf-test-passphrase-01")

// perfOwnerID is the 16-byte owner UUID used for both performance tests.
var perfOwnerID = [uuidSize]byte{
	0x50, 0x51, 0x52, 0x53, 0x54, 0x55, 0x56, 0x57,
	0x58, 0x59, 0x5a, 0x5b, 0x5c, 0x5d, 0x5e, 0x5f,
}

// TestArgon2idProduction verifies that production Argon2id parameters
// (t=3, m=65536 KiB, p=4) produce a 32-byte secret and complete in >= 20ms.
//
// The 20ms floor is a machine-agnostic lower bound: the production cost is
// ~192× the demo cost, so any machine completing demo in ~0.1ms would still
// take >= 20ms for production. A result below 20ms indicates the wrong
// parameters were passed (e.g. demo values substituted for production).
//
// The IC §5.1 post-condition "Production: >= 200ms" is the user-experience
// target on reference hardware; the floor here is the correctness gate.
//
// [REF: IC §5.1, MVP §3.5, build.md Phase 2.3 Session 2.3.2]
func TestArgon2idProduction(t *testing.T) {
	const (
		prodTime   uint32 = 3
		prodMemory uint32 = 65536 // 64 MiB in KiB — production value per MVP §3.5
		prodP      uint8  = 4
		minElapsed        = 20 * time.Millisecond
	)

	start := time.Now()
	out := DeriveMasterSecret(perfPassphrase, perfOwnerID[:], prodTime, prodMemory, prodP)
	elapsed := time.Since(start)

	var zero [32]byte
	if out == zero {
		t.Fatal("TestArgon2idProduction: DeriveMasterSecret returned all-zero output")
	}

	t.Logf("TestArgon2idProduction: elapsed=%v (floor=%v, IC §5.1 target >= 200ms on reference hw)",
		elapsed.Round(time.Millisecond), minElapsed)

	if elapsed < minElapsed {
		t.Errorf(
			"TestArgon2idProduction: elapsed=%v is below the %v floor — "+
				"production parameters (t=%d, m=%d KiB, p=%d) should never be this fast. "+
				"Verify profile.Argon2Time/Memory/Threads are being passed correctly (MVP §3.5).",
			elapsed, minElapsed, prodTime, prodMemory, prodP,
		)
	}
}

// TestArgon2idDemo verifies that demo Argon2id parameters
// (t=1, m=4096 KiB, p=1) produce a 32-byte secret and complete in < 5000ms.
//
// The 5000ms ceiling is deliberately generous: demo is meant to be fast
// (~20–50ms per IC §5.1) but the test should not flap on heavily loaded CI
// runners. If elapsed approaches this ceiling something is genuinely wrong with
// the demo parameters.
//
// [REF: IC §5.1, MVP §3.5, build.md Phase 2.3 Session 2.3.2]
func TestArgon2idDemo(t *testing.T) {
	const (
		demoTime   uint32 = 1
		demoMemory uint32 = 4096 // 4 MiB in KiB — demo value per MVP §3.5
		demoP      uint8  = 1
		maxElapsed        = 5000 * time.Millisecond
	)

	start := time.Now()
	out := DeriveMasterSecret(perfPassphrase, perfOwnerID[:], demoTime, demoMemory, demoP)
	elapsed := time.Since(start)

	var zero [32]byte
	if out == zero {
		t.Fatal("TestArgon2idDemo: DeriveMasterSecret returned all-zero output")
	}

	t.Logf("TestArgon2idDemo: elapsed=%v (ceiling=%v, IC §5.1 target ~20–50ms on reference hw)",
		elapsed.Round(time.Millisecond), maxElapsed)

	if elapsed >= maxElapsed {
		t.Errorf(
			"TestArgon2idDemo: elapsed=%v exceeds %v ceiling — "+
				"demo parameters (t=%d, m=%d KiB, p=%d) should be fast. "+
				"Verify profile.Argon2Time/Memory/Threads are being passed correctly (MVP §3.5).",
			elapsed, maxElapsed, demoTime, demoMemory, demoP,
		)
	}
}

// TestArgon2idProductionStrongerThanDemo verifies that production parameters
// produce a strictly different output than demo parameters given the same inputs.
// This is always true for a correct KDF (different cost params → different output),
// and serves as an additional guard against accidental parameter collisions.
//
// [REF: IC §5.1, MVP §3.5]
func TestArgon2idProductionStrongerThanDemo(t *testing.T) {
	const (
		prodTime   uint32 = 3
		prodMemory uint32 = 65536
		prodP      uint8  = 4
		demoTime   uint32 = 1
		demoMemory uint32 = 4096
		demoP      uint8  = 1
	)

	prod := DeriveMasterSecret(perfPassphrase, perfOwnerID[:], prodTime, prodMemory, prodP)
	demo := DeriveMasterSecret(perfPassphrase, perfOwnerID[:], demoTime, demoMemory, demoP)

	if prod == demo {
		t.Error("TestArgon2idProductionStrongerThanDemo: production and demo parameters " +
			"produced the same secret — KDF is not cost-parameter-sensitive")
	}
}
