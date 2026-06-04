// Package crypto is declared in doc.go.
// Unit tests for DeriveMasterSecret — determinism, non-collision, and
// pre-condition panic enforcement.
//
// Performance tests are in argon2_perf_test.go (tagged //go:build !short).
//
// [REF: IC §5.1, build.md Phase 2.3 Session 2.3.1]

package crypto

import "testing"

// ── Fixed test inputs ─────────────────────────────────────────────────────────
//
// These inputs are distinct from the HKDF test vectors; argon2 tests use their
// own fixtures to keep each test file self-contained.

// katArgon2Passphrase is a minimal valid passphrase (>= 8 bytes) for unit tests.
var katArgon2Passphrase = []byte("test-passphrase-01")

// katArgon2PassphraseB is a different passphrase for non-collision tests.
var katArgon2PassphraseB = []byte("test-passphrase-02")

// katArgon2OwnerID is the 16-byte owner UUID salt for unit tests.
var katArgon2OwnerID = [uuidSize]byte{
	0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
	0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
}

// katArgon2OwnerIDB is a different 16-byte owner UUID for non-collision tests.
var katArgon2OwnerIDB = [uuidSize]byte{
	0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27,
	0x28, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, 0x2e, 0x2f,
}

// ── Fast cost parameters for unit tests ──────────────────────────────────────
//
// These are the demo profile parameters — fast enough for unit tests without
// the !short tag. Correctness tests use cost parameters consistent with the
// demo profile to keep test runtime under a few hundred milliseconds.
const (
	unitArgon2Time    = 1
	unitArgon2Memory  = 4096
	unitArgon2Threads = 1
)

// TestArgon2idDeterminism verifies that DeriveMasterSecret is deterministic:
// calling it twice with identical inputs must produce identical outputs.
//
// [REF: IC §5.1, build.md Phase 2.3 Session 2.3.1]
func TestArgon2idDeterminism(t *testing.T) {
	pass := katArgon2Passphrase
	owner := katArgon2OwnerID[:]

	a := DeriveMasterSecret(pass, owner, unitArgon2Time, unitArgon2Memory, unitArgon2Threads)
	b := DeriveMasterSecret(pass, owner, unitArgon2Time, unitArgon2Memory, unitArgon2Threads)

	if a != b {
		t.Errorf("DeriveMasterSecret: got different outputs for identical inputs\na=%x\nb=%x", a, b)
	}
}

// TestArgon2idNonCollision verifies that changing any single input changes the
// derived secret. This is a necessary (though not sufficient) property for a
// correct KDF.
//
// [REF: IC §5.1, build.md Phase 2.3 Session 2.3.1]
func TestArgon2idNonCollision(t *testing.T) {
	owner := katArgon2OwnerID[:]

	t.Run("different_passphrase_different_secret", func(t *testing.T) {
		keyA := DeriveMasterSecret(katArgon2Passphrase, owner, unitArgon2Time, unitArgon2Memory, unitArgon2Threads)
		keyB := DeriveMasterSecret(katArgon2PassphraseB, owner, unitArgon2Time, unitArgon2Memory, unitArgon2Threads)
		if keyA == keyB {
			t.Errorf("DeriveMasterSecret: different passphrases produced the same secret: %x", keyA)
		}
	})

	t.Run("different_ownerID_different_secret", func(t *testing.T) {
		keyA := DeriveMasterSecret(katArgon2Passphrase, katArgon2OwnerID[:], unitArgon2Time, unitArgon2Memory, unitArgon2Threads)
		keyB := DeriveMasterSecret(katArgon2Passphrase, katArgon2OwnerIDB[:], unitArgon2Time, unitArgon2Memory, unitArgon2Threads)
		if keyA == keyB {
			t.Errorf("DeriveMasterSecret: different ownerIDs produced the same secret: %x", keyA)
		}
	})
}

// TestArgon2idOutputLength verifies the output is always exactly 32 bytes.
// The [sha256.Size]byte return type enforces this at compile time, but an
// explicit length check makes the invariant visible in the test suite.
//
// [REF: IC §5.1]
func TestArgon2idOutputLength(t *testing.T) {
	out := DeriveMasterSecret(katArgon2Passphrase, katArgon2OwnerID[:], unitArgon2Time, unitArgon2Memory, unitArgon2Threads)
	// resolve staticcheck linting error by explicitly marking out as intentionally unused
	_ = out
	if len(out) != 32 {
		t.Errorf("DeriveMasterSecret: output length = %d, want 32", len(out))
	}
}

// TestArgon2idPanicOnShortPassphrase verifies that supplying a passphrase
// shorter than 8 bytes panics rather than silently producing a wrong key.
//
// [REF: IC §5.1 — "pre-condition violations panic", build.md Phase 2.3 Session 2.3.1]
func TestArgon2idPanicOnShortPassphrase(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("DeriveMasterSecret: expected panic on short passphrase, got none")
		}
	}()
	// 7 bytes — one below the minimum.
	DeriveMasterSecret([]byte("short!"), katArgon2OwnerID[:], unitArgon2Time, unitArgon2Memory, unitArgon2Threads)
}

// TestArgon2idPanicOnBadOwnerID verifies that supplying a wrong-length ownerID
// panics.
//
// [REF: IC §5.1 — "pre-condition violations panic"]
func TestArgon2idPanicOnBadOwnerID(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("DeriveMasterSecret: expected panic on wrong-length ownerID, got none")
		}
	}()
	DeriveMasterSecret(katArgon2Passphrase, []byte("short"), unitArgon2Time, unitArgon2Memory, unitArgon2Threads)
}

// TestArgon2idPanicOnZeroTime verifies that argon2Time == 0 panics.
//
// [REF: IC §5.1 — "argon2Time >= 1"]
func TestArgon2idPanicOnZeroTime(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("DeriveMasterSecret: expected panic on argon2Time=0, got none")
		}
	}()
	DeriveMasterSecret(katArgon2Passphrase, katArgon2OwnerID[:], 0, unitArgon2Memory, unitArgon2Threads)
}

// TestArgon2idPanicOnLowMemory verifies that argon2Memory < 4096 panics.
//
// [REF: IC §5.1 — "argon2Memory >= 4096"]
func TestArgon2idPanicOnLowMemory(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("DeriveMasterSecret: expected panic on argon2Memory < 4096, got none")
		}
	}()
	// 4095 KiB — one below the minimum.
	DeriveMasterSecret(katArgon2Passphrase, katArgon2OwnerID[:], unitArgon2Time, argon2MinMemory-1, unitArgon2Threads)
}

// TestArgon2idPanicOnZeroThreads verifies that argon2Threads == 0 panics.
//
// [REF: IC §5.1 — "argon2Threads >= 1"]
func TestArgon2idPanicOnZeroThreads(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("DeriveMasterSecret: expected panic on argon2Threads=0, got none")
		}
	}()
	DeriveMasterSecret(katArgon2Passphrase, katArgon2OwnerID[:], unitArgon2Time, unitArgon2Memory, 0)
}
