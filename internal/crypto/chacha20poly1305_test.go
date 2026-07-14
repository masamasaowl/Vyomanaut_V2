// Package crypto is declared in doc.go.
// Unit tests for EncryptPointerFile and DecryptPointerFile.
//
// Tests:
//   - TestPointerFileRoundTrip        encrypt→decrypt returns original plaintext
//   - TestPointerFileTagMismatch      tampered ciphertext → ErrTagMismatch
//   - TestPointerFileNilOnTagMismatch ErrTagMismatch returns nil plaintext
//   - TestPointerFileNonceUniqueness  different nonces → different ciphertexts
//
// [REF: IC §5.1, ADR-019, NFR-019, build.md Phase 2.5 Session 2.5.1]

package crypto

import (
	"bytes"
	"errors"
	"testing"
)

// ── Fixed test fixtures ───────────────────────────────────────────────────────

// ptKey is a 32-byte key for pointer file AEAD tests.
var ptKey = [32]byte{
	0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
	0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10,
	0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
	0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x20,
}

// ptNonceA is the primary 12-byte nonce used in round-trip and tamper tests.
var ptNonceA = [12]byte{
	0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5,
	0xa6, 0xa7, 0xa8, 0xa9, 0xaa, 0xab,
}

// ptNonceB is a second 12-byte nonce — different from ptNonceA — used in
// uniqueness tests to verify that two ciphertexts produced from the same
// plaintext but different nonces are distinct.
var ptNonceB = [12]byte{
	0xb0, 0xb1, 0xb2, 0xb3, 0xb4, 0xb5,
	0xb6, 0xb7, 0xb8, 0xb9, 0xba, 0xbb,
}

// ptAAD is the additional authenticated data used in tests.
// Follows the ownerID||fileID||schemaVersion structure from IC §5.1.
var ptAAD = []byte{
	// ownerID (16 bytes)
	0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
	0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
	// fileID (16 bytes)
	0x20, 0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27,
	0x28, 0x29, 0x2a, 0x2b, 0x2c, 0x2d, 0x2e, 0x2f,
	// schemaVersion (1 byte)
	0x01,
}

// ptPlaintext is the pointer file plaintext used in tests.
var ptPlaintext = []byte("test pointer file payload — shard location table v1")

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestPointerFileRoundTrip verifies that DecryptPointerFile(EncryptPointerFile(p))
// returns the original plaintext p with no error.
//
// [REF: build.md Phase 2.5 Session 2.5.1]
func TestPointerFileRoundTrip(t *testing.T) {
	ciphertext, err := EncryptPointerFile(ptKey, ptNonceA, ptAAD, ptPlaintext)
	if err != nil {
		t.Fatalf("EncryptPointerFile: %v", err)
	}

	// Output length must be plaintext + 16-byte Poly1305 tag.
	wantLen := len(ptPlaintext) + poly1305TagSize
	if len(ciphertext) != wantLen {
		t.Errorf("ciphertext length = %d, want %d (plaintext %d + tag %d)",
			len(ciphertext), wantLen, len(ptPlaintext), poly1305TagSize)
	}

	plaintext, err := DecryptPointerFile(ptKey, ptNonceA, ptAAD, ciphertext)
	if err != nil {
		t.Fatalf("DecryptPointerFile: %v", err)
	}

	if !bytes.Equal(plaintext, ptPlaintext) {
		t.Errorf("round-trip mismatch:\ngot  %q\nwant %q", plaintext, ptPlaintext)
	}
}

// TestPointerFileRoundTripEmptyPlaintext verifies that an empty plaintext
// round-trips correctly (ciphertext is exactly the 16-byte tag, plaintext
// recovered is empty).
func TestPointerFileRoundTripEmptyPlaintext(t *testing.T) {
	empty := []byte{}
	ciphertext, err := EncryptPointerFile(ptKey, ptNonceA, ptAAD, empty)
	if err != nil {
		t.Fatalf("EncryptPointerFile (empty plaintext): %v", err)
	}

	if len(ciphertext) != poly1305TagSize {
		t.Errorf("empty plaintext: ciphertext length = %d, want %d (tag only)",
			len(ciphertext), poly1305TagSize)
	}

	got, err := DecryptPointerFile(ptKey, ptNonceA, ptAAD, ciphertext)
	if err != nil {
		t.Fatalf("DecryptPointerFile (empty plaintext): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("empty plaintext round-trip: got %d bytes, want 0", len(got))
	}
}

// TestPointerFileTagMismatch verifies that tampering with any single byte of the
// ciphertext (including the Poly1305 tag) causes DecryptPointerFile to return
// ErrTagMismatch.
//
// [REF: NFR-019, build.md Phase 2.5 Session 2.5.1]
func TestPointerFileTagMismatch(t *testing.T) {
	ciphertext, err := EncryptPointerFile(ptKey, ptNonceA, ptAAD, ptPlaintext)
	if err != nil {
		t.Fatalf("EncryptPointerFile: %v", err)
	}

	for i := 0; i < len(ciphertext); i++ {
		corrupt := make([]byte, len(ciphertext))
		copy(corrupt, ciphertext)
		corrupt[i] ^= 0xFF // flip all 8 bits at position i

		_, decErr := DecryptPointerFile(ptKey, ptNonceA, ptAAD, corrupt)
		if decErr == nil {
			t.Errorf("corrupt[%d]: expected ErrTagMismatch, got nil", i)
			continue
		}
		if !errors.Is(decErr, ErrTagMismatch) {
			t.Errorf("corrupt[%d]: expected ErrTagMismatch, got %v", i, decErr)
		}
	}
}

// TestPointerFileNilOnTagMismatch verifies that when ErrTagMismatch is returned,
// the plaintext return value is nil — never a partial decryption.
//
// Callers must not use any returned bytes when ErrTagMismatch is signalled (IC §5.1).
//
// [REF: IC §5.1, NFR-019, build.md Phase 2.5 Session 2.5.1]
func TestPointerFileNilOnTagMismatch(t *testing.T) {
	ciphertext, err := EncryptPointerFile(ptKey, ptNonceA, ptAAD, ptPlaintext)
	if err != nil {
		t.Fatalf("EncryptPointerFile: %v", err)
	}

	// Corrupt the first byte.
	corrupt := make([]byte, len(ciphertext))
	copy(corrupt, ciphertext)
	corrupt[0] ^= 0xFF

	result, decErr := DecryptPointerFile(ptKey, ptNonceA, ptAAD, corrupt)
	if !errors.Is(decErr, ErrTagMismatch) {
		t.Fatalf("expected ErrTagMismatch, got %v", decErr)
	}
	if result != nil {
		t.Errorf("ErrTagMismatch must return nil plaintext, got %d-byte slice", len(result))
	}
}

// TestPointerFileNonceUniqueness verifies that encrypting the same plaintext
// with two different nonces produces two different ciphertexts.
//
// This documents the required nonce-uniqueness discipline for callers: the
// nonce must be advanced before each EncryptPointerFile call.
//
// [REF: IC §5.1, build.md Phase 2.5 Session 2.5.1]
func TestPointerFileNonceUniqueness(t *testing.T) {
	ct1, err := EncryptPointerFile(ptKey, ptNonceA, ptAAD, ptPlaintext)
	if err != nil {
		t.Fatalf("EncryptPointerFile (nonceA): %v", err)
	}
	ct2, err := EncryptPointerFile(ptKey, ptNonceB, ptAAD, ptPlaintext)
	if err != nil {
		t.Fatalf("EncryptPointerFile (nonceB): %v", err)
	}

	if bytes.Equal(ct1, ct2) {
		t.Error("different nonces produced identical ciphertexts — nonce uniqueness violated")
	}

	// Both must still decrypt correctly with their respective nonces.
	pt1, err := DecryptPointerFile(ptKey, ptNonceA, ptAAD, ct1)
	if err != nil {
		t.Fatalf("DecryptPointerFile (nonceA): %v", err)
	}
	pt2, err := DecryptPointerFile(ptKey, ptNonceB, ptAAD, ct2)
	if err != nil {
		t.Fatalf("DecryptPointerFile (nonceB): %v", err)
	}
	if !bytes.Equal(pt1, ptPlaintext) || !bytes.Equal(pt2, ptPlaintext) {
		t.Error("nonce-unique ciphertexts did not round-trip to the original plaintext")
	}
}

// TestPointerFileWrongNonceTagMismatch verifies that decrypting with the wrong
// nonce returns ErrTagMismatch, not garbled plaintext.
func TestPointerFileWrongNonceTagMismatch(t *testing.T) {
	ciphertext, err := EncryptPointerFile(ptKey, ptNonceA, ptAAD, ptPlaintext)
	if err != nil {
		t.Fatalf("EncryptPointerFile: %v", err)
	}

	// Decrypt with ptNonceB — wrong nonce must cause tag verification failure.
	result, decErr := DecryptPointerFile(ptKey, ptNonceB, ptAAD, ciphertext)
	if !errors.Is(decErr, ErrTagMismatch) {
		t.Errorf("wrong nonce: expected ErrTagMismatch, got %v", decErr)
	}
	if result != nil {
		t.Errorf("wrong nonce: expected nil plaintext, got %d bytes", len(result))
	}
}

// TestPointerFileWrongAADTagMismatch verifies that decrypting with different
// AAD returns ErrTagMismatch — the AAD is bound to the ciphertext.
func TestPointerFileWrongAADTagMismatch(t *testing.T) {
	ciphertext, err := EncryptPointerFile(ptKey, ptNonceA, ptAAD, ptPlaintext)
	if err != nil {
		t.Fatalf("EncryptPointerFile: %v", err)
	}

	wrongAAD := append([]byte(nil), ptAAD...)
	wrongAAD[0] ^= 0x01 // flip one bit in ownerID

	result, decErr := DecryptPointerFile(ptKey, ptNonceA, wrongAAD, ciphertext)
	if !errors.Is(decErr, ErrTagMismatch) {
		t.Errorf("wrong AAD: expected ErrTagMismatch, got %v", decErr)
	}
	if result != nil {
		t.Errorf("wrong AAD: expected nil plaintext, got %d bytes", len(result))
	}
}

// TestPointerFileTooShortCiphertext verifies that ciphertext shorter than 16 bytes
// (the minimum for a Poly1305 tag) returns an error.
func TestPointerFileTooShortCiphertext(t *testing.T) {
	_, err := DecryptPointerFile(ptKey, ptNonceA, ptAAD, make([]byte, poly1305TagSize-1))
	if err == nil {
		t.Error("DecryptPointerFile: expected error for too-short ciphertext, got nil")
	}
}

// TestPointerFileEncryptPanicOnEmptyAAD verifies that EncryptPointerFile panics
// when aad is empty (IC §5.1 pre-condition).
func TestPointerFileEncryptPanicOnEmptyAAD(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("EncryptPointerFile: expected panic on empty aad, got none")
		}
	}()
	_, _ = EncryptPointerFile(ptKey, ptNonceA, []byte{}, ptPlaintext)
}

// TestEncryptAEADAllowsEmptyAAD verifies that, unlike EncryptPointerFile,
// EncryptAEAD/DecryptAEAD accept an empty AAD — the non-empty-AAD
// requirement is EncryptPointerFile's own precondition, not the generic
// primitive's.
func TestEncryptAEADAllowsEmptyAAD(t *testing.T) {
	ciphertext, err := EncryptAEAD(ptKey, ptNonceA, []byte{}, ptPlaintext)
	if err != nil {
		t.Fatalf("EncryptAEAD with empty aad: %v", err)
	}
	plaintext, err := DecryptAEAD(ptKey, ptNonceA, []byte{}, ciphertext)
	if err != nil {
		t.Fatalf("DecryptAEAD with empty aad: %v", err)
	}
	if !bytes.Equal(plaintext, ptPlaintext) {
		t.Errorf("round-trip with empty aad mismatch:\ngot  %q\nwant %q", plaintext, ptPlaintext)
	}
}

// TestEncryptPointerFileMatchesEncryptAEAD verifies EncryptPointerFile and
// DecryptPointerFile are genuinely thin wrappers — byte-identical output to
// calling EncryptAEAD/DecryptAEAD directly with the same inputs. This is the
// regression test for the M2 review's §3 finding: a caller that needs a
// differently-named AEAD wrapper (e.g. internal/p2p/identity.go encrypting a
// daemon identity key) can rely on EncryptAEAD directly producing identical,
// independently-verified output, instead of repurposing the pointer-file name.
func TestEncryptPointerFileMatchesEncryptAEAD(t *testing.T) {
	viaWrapper, err := EncryptPointerFile(ptKey, ptNonceA, ptAAD, ptPlaintext)
	if err != nil {
		t.Fatalf("EncryptPointerFile: %v", err)
	}
	viaGeneric, err := EncryptAEAD(ptKey, ptNonceA, ptAAD, ptPlaintext)
	if err != nil {
		t.Fatalf("EncryptAEAD: %v", err)
	}
	if !bytes.Equal(viaWrapper, viaGeneric) {
		t.Errorf("EncryptPointerFile and EncryptAEAD diverged for identical inputs:\n"+
			"wrapper=%x\ngeneric=%x", viaWrapper, viaGeneric)
	}
}