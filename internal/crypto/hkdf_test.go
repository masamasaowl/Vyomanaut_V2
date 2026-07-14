package crypto

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

// ── Fixed test inputs ─────────────────────────────────────────────────────────
//
// All vectors below were computed offline using a Python HKDF-SHA256 reference
// implementation following RFC 5869 §2.2–§2.3 before this Go code existed.
// The Python reference is reproduced in the session log for auditability.
//
// [REF: build.md Phase 2.2 Session 2.2.2, RFC 5869]

// katMasterSecret is the 32-byte master secret used in all known-answer tests.
var katMasterSecret = [masterSecretSize]byte{
	0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
	0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
	0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
	0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
}

// katOwnerID is the 16-byte owner UUID used in all known-answer tests.
var katOwnerID = [uuidSize]byte{
	0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7,
	0xa8, 0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf,
}

// katFileIDA is the first 16-byte file UUID (used in all single-fileID tests).
var katFileIDA = [uuidSize]byte{
	0xb0, 0xb1, 0xb2, 0xb3, 0xb4, 0xb5, 0xb6, 0xb7,
	0xb8, 0xb9, 0xba, 0xbb, 0xbc, 0xbd, 0xbe, 0xbf,
}

// katFileIDB is the second 16-byte file UUID — different from katFileIDA —
// used in the non-collision test.
var katFileIDB = [uuidSize]byte{
	0xc0, 0xc1, 0xc2, 0xc3, 0xc4, 0xc5, 0xc6, 0xc7,
	0xc8, 0xc9, 0xca, 0xcb, 0xcc, 0xcd, 0xce, 0xcf,
}

// katChunkHash is the 32-byte chunk hash used in the DeriveDHTKey known-answer test.
var katChunkHash = [sha256.Size]byte{
	0xd0, 0xd1, 0xd2, 0xd3, 0xd4, 0xd5, 0xd6, 0xd7,
	0xd8, 0xd9, 0xda, 0xdb, 0xdc, 0xdd, 0xde, 0xdf,
	0xe0, 0xe1, 0xe2, 0xe3, 0xe4, 0xe5, 0xe6, 0xe7,
	0xe8, 0xe9, 0xea, 0xeb, 0xec, 0xed, 0xee, 0xef,
}

// ── Expected outputs (offline-computed) ───────────────────────────────────────

// katFileKey is the expected DeriveFileKey(katMasterSecret, katOwnerID, katFileIDA) output.
var katFileKey = [sha256.Size]byte{
	0xd4, 0x4f, 0x51, 0x1e, 0xee, 0x6a, 0xdb, 0x16,
	0x41, 0x2b, 0xe3, 0xa7, 0x51, 0x69, 0xa9, 0x8d,
	0xf6, 0xe5, 0x67, 0x9c, 0x5d, 0x52, 0xeb, 0x3c,
	0xf6, 0xcf, 0xb6, 0x54, 0x6b, 0xa4, 0x02, 0x76,
}

// katPointerKey is the expected DerivePointerEncKey(katMasterSecret, katOwnerID, katFileIDA) output.
var katPointerKey = [sha256.Size]byte{
	0xd4, 0x39, 0x3b, 0x93, 0x9e, 0x91, 0xb3, 0x01,
	0x04, 0x7e, 0xe9, 0x3f, 0xd2, 0xb6, 0xa4, 0x38,
	0xd5, 0x6f, 0xf6, 0x01, 0xdf, 0x62, 0xa8, 0xa0,
	0x57, 0xbb, 0x17, 0xd7, 0x1a, 0x3b, 0x4c, 0xb2,
}

// katKeystoreKey is the expected DeriveKeystoreEncKey(katMasterSecret, katOwnerID) output.
var katKeystoreKey = [sha256.Size]byte{
	0x63, 0x99, 0xba, 0xe9, 0x8a, 0xdb, 0xf9, 0x8c,
	0xe0, 0xee, 0x3c, 0x0b, 0x12, 0x87, 0x07, 0x98,
	0x51, 0xe5, 0x8a, 0x01, 0xac, 0x9e, 0x76, 0xf0,
	0x18, 0x4b, 0x0e, 0x3b, 0xfd, 0x1a, 0x2b, 0xf8,
}

// katDHTOwnerKey is the expected DeriveDHTOwnerKey(katMasterSecret, katOwnerID, katFileIDA) output.
var katDHTOwnerKey = [sha256.Size]byte{
	0x63, 0x6b, 0xa7, 0x1b, 0x6b, 0x61, 0x8c, 0x60,
	0xb5, 0xcb, 0xf4, 0xbd, 0x54, 0x92, 0xcd, 0x1e,
	0x46, 0x3b, 0x3b, 0x08, 0xb3, 0x85, 0x37, 0x54,
	0x90, 0x78, 0x68, 0x53, 0x18, 0xd3, 0x8d, 0x78,
}

// katDHTKey is the expected DeriveDHTKey(katChunkHash, katDHTOwnerKey) output.
var katDHTKey = [sha256.Size]byte{
	0x0c, 0x8e, 0xed, 0x1f, 0xea, 0x95, 0x77, 0xff,
	0x17, 0xc4, 0x43, 0x4b, 0xef, 0x4b, 0xc2, 0xb1,
	0x47, 0x34, 0x15, 0x5a, 0xea, 0x2a, 0x0d, 0x81,
	0xa4, 0x68, 0x0a, 0x91, 0x06, 0x5d, 0x03, 0x8b,
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestHKDFDeterminism verifies that every derivation function is deterministic:
// calling it twice with identical inputs must produce identical outputs.
//
// [REF: build.md Phase 2.2 Session 2.2.2]
func TestHKDFDeterminism(t *testing.T) {
	ms := katMasterSecret[:]
	owner := katOwnerID[:]
	fileA := katFileIDA[:]

	t.Run("DeriveFileKey", func(t *testing.T) {
		a := DeriveFileKey(ms, owner, fileA)
		b := DeriveFileKey(ms, owner, fileA)
		if a != b {
			t.Errorf("DeriveFileKey: got different outputs for identical inputs\na=%x\nb=%x", a, b)
		}
	})

	t.Run("DerivePointerEncKey", func(t *testing.T) {
		a := DerivePointerEncKey(ms, owner, fileA)
		b := DerivePointerEncKey(ms, owner, fileA)
		if a != b {
			t.Errorf("DerivePointerEncKey: got different outputs for identical inputs\na=%x\nb=%x", a, b)
		}
	})

	t.Run("DeriveKeystoreEncKey", func(t *testing.T) {
		a := DeriveKeystoreEncKey(ms, owner)
		b := DeriveKeystoreEncKey(ms, owner)
		if a != b {
			t.Errorf("DeriveKeystoreEncKey: got different outputs for identical inputs\na=%x\nb=%x", a, b)
		}
	})

	t.Run("DeriveDHTOwnerKey", func(t *testing.T) {
		a := DeriveDHTOwnerKey(ms, owner, fileA)
		b := DeriveDHTOwnerKey(ms, owner, fileA)
		if a != b {
			t.Errorf("DeriveDHTOwnerKey: got different outputs for identical inputs\na=%x\nb=%x", a, b)
		}
	})

	t.Run("DeriveDHTKey", func(t *testing.T) {
		ownerKey := DeriveDHTOwnerKey(ms, owner, fileA)
		a := DeriveDHTKey(katChunkHash, ownerKey)
		b := DeriveDHTKey(katChunkHash, ownerKey)
		if a != b {
			t.Errorf("DeriveDHTKey: got different outputs for identical inputs\na=%x\nb=%x", a, b)
		}
	})
}

// TestHKDFNonCollision verifies that changing the fileID changes the derived key
// for every HKDF derivation function that accepts a fileID.
// The keystore function is omitted because it takes no fileID parameter.
//
// [REF: build.md Phase 2.2 Session 2.2.2]
func TestHKDFNonCollision(t *testing.T) {
	ms := katMasterSecret[:]
	owner := katOwnerID[:]
	fileA := katFileIDA[:]
	fileB := katFileIDB[:]

	t.Run("DeriveFileKey_different_fileID", func(t *testing.T) {
		keyA := DeriveFileKey(ms, owner, fileA)
		keyB := DeriveFileKey(ms, owner, fileB)
		if keyA == keyB {
			t.Errorf("DeriveFileKey: fileID_A and fileID_B produced the same key: %x", keyA)
		}
	})

	t.Run("DerivePointerEncKey_different_fileID", func(t *testing.T) {
		keyA := DerivePointerEncKey(ms, owner, fileA)
		keyB := DerivePointerEncKey(ms, owner, fileB)
		if keyA == keyB {
			t.Errorf("DerivePointerEncKey: fileID_A and fileID_B produced the same key: %x", keyA)
		}
	})

	t.Run("DeriveDHTOwnerKey_different_fileID", func(t *testing.T) {
		keyA := DeriveDHTOwnerKey(ms, owner, fileA)
		keyB := DeriveDHTOwnerKey(ms, owner, fileB)
		if keyA == keyB {
			t.Errorf("DeriveDHTOwnerKey: fileID_A and fileID_B produced the same key: %x", keyA)
		}
	})

	t.Run("DeriveFileKey_differs_from_DerivePointerEncKey", func(t *testing.T) {
		// Different info prefixes must produce different keys even for the same inputs.
		fileKey := DeriveFileKey(ms, owner, fileA)
		ptrKey := DerivePointerEncKey(ms, owner, fileA)
		if fileKey == ptrKey {
			t.Errorf("DeriveFileKey and DerivePointerEncKey collide for the same inputs: %x", fileKey)
		}
	})
}

// TestHKDFKnownAnswer checks every derivation function against vectors computed
// offline by a Python HKDF-SHA256 reference (RFC 5869) before this code existed.
// A failure here means the in-package implementation diverges from the spec.
//
// [REF: build.md Phase 2.2 Session 2.2.2, IC §5.1, RFC 5869]
func TestHKDFKnownAnswer(t *testing.T) {
	ms := katMasterSecret[:]
	owner := katOwnerID[:]
	fileA := katFileIDA[:]

	t.Run("DeriveFileKey", func(t *testing.T) {
		got := DeriveFileKey(ms, owner, fileA)
		if !bytes.Equal(got[:], katFileKey[:]) {
			t.Errorf("DeriveFileKey KAT failed\ngot : %x\nwant: %x", got, katFileKey)
		}
	})

	t.Run("DerivePointerEncKey", func(t *testing.T) {
		got := DerivePointerEncKey(ms, owner, fileA)
		if !bytes.Equal(got[:], katPointerKey[:]) {
			t.Errorf("DerivePointerEncKey KAT failed\ngot : %x\nwant: %x", got, katPointerKey)
		}
	})

	t.Run("DeriveKeystoreEncKey", func(t *testing.T) {
		got := DeriveKeystoreEncKey(ms, owner)
		if !bytes.Equal(got[:], katKeystoreKey[:]) {
			t.Errorf("DeriveKeystoreEncKey KAT failed\ngot : %x\nwant: %x", got, katKeystoreKey)
		}
	})

	t.Run("DeriveDHTOwnerKey", func(t *testing.T) {
		got := DeriveDHTOwnerKey(ms, owner, fileA)
		if !bytes.Equal(got[:], katDHTOwnerKey[:]) {
			t.Errorf("DeriveDHTOwnerKey KAT failed\ngot : %x\nwant: %x", got, katDHTOwnerKey)
		}
	})

	t.Run("DeriveDHTKey", func(t *testing.T) {
		// fileOwnerKey is itself verified by the DeriveDHTOwnerKey sub-test above,
		// so using the live derivation here tests the full composition.
		fileOwnerKey := DeriveDHTOwnerKey(ms, owner, fileA)
		got := DeriveDHTKey(katChunkHash, fileOwnerKey)
		if !bytes.Equal(got[:], katDHTKey[:]) {
			t.Errorf("DeriveDHTKey KAT failed\ngot : %x\nwant: %x", got, katDHTKey)
		}
	})
}

// TestHKDFPanicOnBadMasterSecret verifies that supplying a wrong-length
// masterSecret panics rather than silently producing a wrong key.
//
// [REF: IC §5.1 — "pre-condition violations panic", build.md Phase 2.2 Session 2.2.1]
func TestHKDFPanicOnBadMasterSecret(t *testing.T) {
	owner := katOwnerID[:]
	fileA := katFileIDA[:]

	funcs := []struct {
		name string
		fn   func()
	}{
		{"DeriveFileKey", func() { DeriveFileKey([]byte("short"), owner, fileA) }},
		{"DerivePointerEncKey", func() { DerivePointerEncKey([]byte("short"), owner, fileA) }},
		{"DeriveKeystoreEncKey", func() { DeriveKeystoreEncKey([]byte("short"), owner) }},
		{"DeriveDHTOwnerKey", func() { DeriveDHTOwnerKey([]byte("short"), owner, fileA) }},
	}

	for _, tc := range funcs {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("%s: expected panic on short masterSecret, got none", tc.name)
				}
			}()
			tc.fn()
		})
	}
}

// TestHKDFPanicOnBadOwnerID verifies that supplying a wrong-length ownerID
// panics, for every function that accepts one. TestHKDFPanicOnBadMasterSecret
// only covers the masterSecret parameter; mustLen is called on ownerID (and
// fileID, see TestHKDFPanicOnBadFileID) in the same functions and this was
// previously untested.
//
// [REF: IC §5.1 — "pre-condition violations panic"]
func TestHKDFPanicOnBadOwnerID(t *testing.T) {
	ms := katMasterSecret[:]
	fileA := katFileIDA[:]
	badOwner := []byte("short")

	funcs := []struct {
		name string
		fn   func()
	}{
		{"DeriveFileKey", func() { DeriveFileKey(ms, badOwner, fileA) }},
		{"DerivePointerEncKey", func() { DerivePointerEncKey(ms, badOwner, fileA) }},
		{"DeriveKeystoreEncKey", func() { DeriveKeystoreEncKey(ms, badOwner) }},
		{"DeriveDHTOwnerKey", func() { DeriveDHTOwnerKey(ms, badOwner, fileA) }},
	}

	for _, tc := range funcs {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("%s: expected panic on short ownerID, got none", tc.name)
				}
			}()
			tc.fn()
		})
	}
}

// TestHKDFPanicOnBadFileID verifies that supplying a wrong-length fileID
// panics, for every function that accepts one. DeriveKeystoreEncKey is
// omitted — it takes no fileID parameter.
//
// [REF: IC §5.1 — "pre-condition violations panic"]
func TestHKDFPanicOnBadFileID(t *testing.T) {
	ms := katMasterSecret[:]
	owner := katOwnerID[:]
	badFile := []byte("short")

	funcs := []struct {
		name string
		fn   func()
	}{
		{"DeriveFileKey", func() { DeriveFileKey(ms, owner, badFile) }},
		{"DerivePointerEncKey", func() { DerivePointerEncKey(ms, owner, badFile) }},
		{"DeriveDHTOwnerKey", func() { DeriveDHTOwnerKey(ms, owner, badFile) }},
	}

	for _, tc := range funcs {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("%s: expected panic on short fileID, got none", tc.name)
				}
			}()
			tc.fn()
		})
	}
}
