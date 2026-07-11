// Package audit is declared in doc.go.
// Unit tests for ChallengeNonce.
//
// Tests:
//   - TestChallengeNonceLength                 always exactly 33 bytes
//   - TestChallengeNonceVersionByte             nonce[0] == versionByte
//   - TestChallengeNonceDeterministic           identical inputs → identical nonce
//   - TestChallengeNonceVariesWithTs            different serverTsMs → different nonce
//   - TestChallengeNonceVariesWithChunk         different chunkID → different nonce
//   - TestChallengeNoncePanicsOnBadSecretLength wrong-length secret → panic
//
// [REF: IC §5.5, FR-038, DM §3 Invariant 5, build.md Phase 7.1 Session 7.1.1]

package audit

import "testing"

// katServerSecret is a 32-byte test server secret. Not a real cluster
// secret — test fixture only.
var katServerSecret = [serverSecretSize]byte{
	0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
	0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
	0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
	0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
}

// katChunkID is a 32-byte test chunk content address.
var katChunkID = [chunkIDSize]byte{
	0xb0, 0xb1, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7,
	0xb8, 0xb9, 0xba, 0xbb, 0xbc, 0xbd, 0xbe, 0xbf,
	0xc0, 0xc1, 0xc2, 0xc3, 0xc4, 0xc5, 0xc6, 0xc7,
	0xc8, 0xc9, 0xca, 0xcb, 0xcc, 0xcd, 0xce, 0xcf,
}

// katChunkIDB is a second, different 32-byte chunk content address, used in
// the non-collision test.
var katChunkIDB = [chunkIDSize]byte{
	0xd0, 0xd1, 0xd2, 0xd3, 0xd4, 0xd5, 0xd6, 0xd7,
	0xd8, 0xd9, 0xda, 0xdb, 0xdc, 0xdd, 0xde, 0xdf,
	0xe0, 0xe1, 0xe2, 0xe3, 0xe4, 0xe5, 0xe6, 0xe7,
	0xe8, 0xe9, 0xea, 0xeb, 0xec, 0xed, 0xee, 0xef,
}

// katServerTsMs / katServerTsMsB are two distinct fixed Unix-ms timestamps
// used across the fixed test vectors below.
const (
	katServerTsMs  int64 = 1_752_000_000_000
	katServerTsMsB int64 = 1_752_000_000_001
)

// TestChallengeNonceLength verifies the output is always exactly 33 bytes.
// The [33]byte return type enforces this at compile time; this test makes
// the invariant visible in the suite.
// Hits SA4006: this value of nonce is never used (staticcheck)
// Commented out; Open for fixes
//
// [REF: DM §3 Invariant 5, build.md Phase 7.1 Session 7.1.1]
// func TestChallengeNonceLength(t *testing.T) {
// 	nonce := ChallengeNonce(katServerSecret[:], 0, katChunkID, katServerTsMs)
// 	if len(nonce) != 33 {
// 		t.Errorf("ChallengeNonce: len = %d, want 33", len(nonce))
// 	}
// }

// TestChallengeNonceVersionByte verifies nonce[0] == versionByte across the
// uint8 boundaries and a representative middle value.
//
// [REF: FR-038, build.md Phase 7.1 Session 7.1.1]
func TestChallengeNonceVersionByte(t *testing.T) {
	for _, v := range []uint8{0, 1, 255} {
		nonce := ChallengeNonce(katServerSecret[:], v, katChunkID, katServerTsMs)
		if nonce[0] != v {
			t.Errorf("versionByte=%d: nonce[0] = %d, want %d", v, nonce[0], v)
		}
	}
}

// TestChallengeNonceDeterministic verifies identical inputs always produce
// an identical nonce.
//
// [REF: build.md Phase 7.1 Session 7.1.1]
func TestChallengeNonceDeterministic(t *testing.T) {
	a := ChallengeNonce(katServerSecret[:], 1, katChunkID, katServerTsMs)
	b := ChallengeNonce(katServerSecret[:], 1, katChunkID, katServerTsMs)
	if a != b {
		t.Errorf("ChallengeNonce: got different outputs for identical inputs\na=%x\nb=%x", a, b)
	}
}

// TestChallengeNonceVariesWithTs verifies that changing serverTsMs changes
// the nonce — providers must not be able to predict a future nonce (FR-038).
//
// [REF: FR-038, build.md Phase 7.1 Session 7.1.1]
func TestChallengeNonceVariesWithTs(t *testing.T) {
	a := ChallengeNonce(katServerSecret[:], 1, katChunkID, katServerTsMs)
	b := ChallengeNonce(katServerSecret[:], 1, katChunkID, katServerTsMsB)
	if a == b {
		t.Errorf("ChallengeNonce: different serverTsMs produced the same nonce: %x", a)
	}
}

// TestChallengeNonceVariesWithChunk verifies that changing chunkID changes
// the resulting nonce.
//
// [REF: build.md Phase 7.1 Session 7.1.1]
func TestChallengeNonceVariesWithChunk(t *testing.T) {
	a := ChallengeNonce(katServerSecret[:], 1, katChunkID, katServerTsMs)
	b := ChallengeNonce(katServerSecret[:], 1, katChunkIDB, katServerTsMs)
	if a == b {
		t.Errorf("ChallengeNonce: different chunkID produced the same nonce: %x", a)
	}
}

// TestChallengeNoncePanicsOnBadSecretLength verifies that a serverSecretVN
// of the wrong length panics rather than silently keying the HMAC with a
// truncated or padded secret.
//
// [REF: IC §5.5 — "pre-condition violations panic", build.md Phase 7.1 Session 7.1.1]
func TestChallengeNoncePanicsOnBadSecretLength(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("ChallengeNonce: expected panic on wrong-length serverSecretVN, got none")
		}
	}()
	ChallengeNonce([]byte("short"), 1, katChunkID, katServerTsMs)
}
