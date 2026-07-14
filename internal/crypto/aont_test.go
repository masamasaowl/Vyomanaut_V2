// Package crypto is declared in doc.go.
// Unit tests for AONTEncodeSegment and AONTDecodePackage.
//
// Tests:
//   - TestAONTRoundTrip               encode→decode returns original plaintext
//   - TestAONTKeyFreshness            two encodes of same input produce different packages
//   - TestAONTCorruptionDetection     any single-byte flip causes ErrCanaryMismatch
//   - TestAONTCrossCipherIncompatible AES encode + ChaCha20 decode → ErrCanaryMismatch
//
// [REF: IC §5.1, ADR-022, build.md Phase 2.4 Session 2.4.4]

package crypto

import (
	"bytes"
	"errors"
	"testing"
)

// testSegment builds a deterministic test segment of the given word count.
// Each byte is its position modulo 256, so the content is non-trivial.
func testSegment(words int) []byte {
	s := make([]byte, words*16)
	for i := range s {
		s[i] = byte(i)
	}
	return s
}

// TestAONTRoundTrip verifies that AONTDecodePackage(AONTEncodeSegment(data, path), path)
// returns the original segment for both cipher paths.
//
// [REF: build.md Phase 2.4 Session 2.4.4]
func TestAONTRoundTrip(t *testing.T) {
	segment := testSegment(16) // 16 words = 256 bytes; representative non-trivial input

	paths := []struct {
		name  string
		aesNI bool
	}{
		{"chacha20_path", false},
		{"aesni_path", true},
	}

	for _, p := range paths {
		p := p
		t.Run(p.name, func(t *testing.T) {
			pkg, err := AONTEncodeSegment(segment, p.aesNI)
			if err != nil {
				t.Fatalf("AONTEncodeSegment: %v", err)
			}

			got, err := AONTDecodePackage(pkg, p.aesNI)
			if err != nil {
				t.Fatalf("AONTDecodePackage: %v", err)
			}

			if !bytes.Equal(got, segment) {
				t.Errorf("round-trip mismatch:\ngot  %x\nwant %x", got, segment)
			}
		})
	}
}

// TestAONTRoundTripSingleWord exercises the minimum-size encode/decode path:
// a 1-word (16-byte) segment. This ensures the canary word does not collide
// with the data at the boundary.
func TestAONTRoundTripSingleWord(t *testing.T) {
	segment := testSegment(1) // 1 word = 16 bytes

	for _, aesNI := range []bool{false, true} {
		pkg, err := AONTEncodeSegment(segment, aesNI)
		if err != nil {
			t.Fatalf("aesNI=%v AONTEncodeSegment: %v", aesNI, err)
		}
		got, err := AONTDecodePackage(pkg, aesNI)
		if err != nil {
			t.Fatalf("aesNI=%v AONTDecodePackage: %v", aesNI, err)
		}
		if !bytes.Equal(got, segment) {
			t.Errorf("aesNI=%v single-word round-trip mismatch", aesNI)
		}
	}
}

// TestAONTKeyFreshness verifies that each call to AONTEncodeSegment produces a
// different ciphertext even for identical input — proving K is fresh per call
// (IC §11: K reuse is a correctness violation).
//
// [REF: IC §11, build.md Phase 2.4 Session 2.4.4]
func TestAONTKeyFreshness(t *testing.T) {
	segment := testSegment(16)

	pkg1, err := AONTEncodeSegment(segment, false)
	if err != nil {
		t.Fatalf("first AONTEncodeSegment: %v", err)
	}
	pkg2, err := AONTEncodeSegment(segment, false)
	if err != nil {
		t.Fatalf("second AONTEncodeSegment: %v", err)
	}

	if bytes.Equal(pkg1, pkg2) {
		t.Error("two calls with identical input produced the same package — K is not fresh (IC §11 violation)")
	}

	// Both must still decode correctly, confirming freshness does not break round-trip.
	got1, err := AONTDecodePackage(pkg1, false)
	if err != nil {
		t.Fatalf("decode of pkg1: %v", err)
	}
	got2, err := AONTDecodePackage(pkg2, false)
	if err != nil {
		t.Fatalf("decode of pkg2: %v", err)
	}
	if !bytes.Equal(got1, segment) || !bytes.Equal(got2, segment) {
		t.Error("fresh-key packages did not round-trip correctly")
	}
}

// TestAONTCorruptionDetection verifies the all-or-nothing property: flipping any
// single byte in the AONT package must cause ErrCanaryMismatch.
//
// Rationale by region:
//   - Ciphertext bytes: changes SHA-256(ciphertext), so K recovery gives wrong K,
//     decryption produces garbage, canary position does not match.
//   - Key-block bytes: changes recovered K directly, same outcome.
//
// A 4-word (64-byte) segment produces a 112-byte package; all 112 positions are
// exercised, keeping the test fast while covering every byte.
//
// [REF: IC §5.1, ADR-022, build.md Phase 2.4 Session 2.4.4]
func TestAONTCorruptionDetection(t *testing.T) {
	segment := bytes.Repeat([]byte{0xAB}, 4*16) // 4 words = 64 bytes

	pkg, err := AONTEncodeSegment(segment, false)
	if err != nil {
		t.Fatalf("AONTEncodeSegment: %v", err)
	}

	for i := 0; i < len(pkg); i++ {
		corrupt := make([]byte, len(pkg))
		copy(corrupt, pkg)
		corrupt[i] ^= 0xFF // flip all 8 bits at this position

		_, decErr := AONTDecodePackage(corrupt, false)
		if decErr == nil {
			t.Errorf("corrupt[%d]: expected ErrCanaryMismatch, got nil", i)
			continue
		}
		if !errors.Is(decErr, ErrCanaryMismatch) {
			t.Errorf("corrupt[%d]: expected ErrCanaryMismatch, got %v", i, decErr)
		}
	}
}

// TestAONTCrossCipherIncompatible documents and verifies that encoding with one
// cipher path and decoding with the other always produces ErrCanaryMismatch.
// This is the expected, correct behaviour: the two paths are intentionally
// incompatible. Both sides correctly recover K (the hash input is the same
// ciphertext bytes), but the wrong cipher produces garbage plaintext whose
// last word does not equal aontCanary.
//
// [REF: IC §5.1, ADR-019, build.md Phase 2.4 Session 2.4.4]
func TestAONTCrossCipherIncompatible(t *testing.T) {
	segment := testSegment(16)

	t.Run("aes_encode_chacha_decode", func(t *testing.T) {
		pkg, err := AONTEncodeSegment(segment, true) // AES-256-CTR
		if err != nil {
			t.Fatalf("AONTEncodeSegment (AES): %v", err)
		}
		_, decErr := AONTDecodePackage(pkg, false) // ChaCha20 — wrong path
		if !errors.Is(decErr, ErrCanaryMismatch) {
			t.Errorf("expected ErrCanaryMismatch for AES→ChaCha cross-decode, got %v", decErr)
		}
	})

	t.Run("chacha_encode_aes_decode", func(t *testing.T) {
		pkg, err := AONTEncodeSegment(segment, false) // ChaCha20
		if err != nil {
			t.Fatalf("AONTEncodeSegment (ChaCha20): %v", err)
		}
		_, decErr := AONTDecodePackage(pkg, true) // AES-256-CTR — wrong path
		if !errors.Is(decErr, ErrCanaryMismatch) {
			t.Errorf("expected ErrCanaryMismatch for ChaCha→AES cross-decode, got %v", decErr)
		}
	})
}

// TestAONTPreconditionEmptySegment verifies that an empty segment returns an error
// rather than panicking or producing a garbage package.
func TestAONTPreconditionEmptySegment(t *testing.T) {
	_, err := AONTEncodeSegment([]byte{}, false)
	if err == nil {
		t.Error("AONTEncodeSegment: expected error for empty segment, got nil")
	}
}

// TestAONTPreconditionUnalignedSegment verifies that a segment whose length is not
// a multiple of 16 returns an error.
func TestAONTPreconditionUnalignedSegment(t *testing.T) {
	_, err := AONTEncodeSegment(make([]byte, 17), false) // 17 is not a multiple of 16
	if err == nil {
		t.Error("AONTEncodeSegment: expected error for unaligned segment, got nil")
	}
}

// TestAONTDecodeTooShort verifies that a package shorter than the minimum valid
// size returns an error from AONTDecodePackage.
func TestAONTDecodeTooShort(t *testing.T) {
	_, err := AONTDecodePackage(make([]byte, 32), false) // 32 < minimum 64
	if err == nil {
		t.Error("AONTDecodePackage: expected error for too-short package, got nil")
	}
}

// TestAONTOutputSize verifies that the encoded package length equals
// (numDataWords+3) × 16 bytes: numDataWords encrypted data words, 1 encrypted
// canary word, and 2 words (32 bytes) for the key block.
func TestAONTOutputSize(t *testing.T) {
	for _, numWords := range []int{1, 4, 16, 256} {
		segment := testSegment(numWords)
		pkg, err := AONTEncodeSegment(segment, false)
		if err != nil {
			t.Fatalf("words=%d AONTEncodeSegment: %v", numWords, err)
		}
		wantLen := (numWords+1)*16 + 32 // (data+canary)*wordSize + keyBlockSize
		if len(pkg) != wantLen {
			t.Errorf("words=%d: got package len %d, want %d", numWords, len(pkg), wantLen)
		}
	}
}

// TestAONTDecryptedBufferZeroedOnMismatch verifies that when ErrCanaryMismatch
// is returned, the caller receives nil (not a partially-decrypted buffer), confirming
// the output buffer was zeroed before return.
func TestAONTDecryptedBufferZeroedOnMismatch(t *testing.T) {
	pkg, err := AONTEncodeSegment(testSegment(4), false)
	if err != nil {
		t.Fatalf("AONTEncodeSegment: %v", err)
	}
	// Corrupt the first byte of the package.
	pkg[0] ^= 0xFF

	result, decErr := AONTDecodePackage(pkg, false)
	if !errors.Is(decErr, ErrCanaryMismatch) {
		t.Fatalf("expected ErrCanaryMismatch, got %v", decErr)
	}
	if result != nil {
		t.Errorf("expected nil result on ErrCanaryMismatch, got %d-byte slice", len(result))
	}
}

// TestAONTCanaryIsEncryptedNotRaw pins a structural property that
// TestAONTCorruptionDetection's byte-sweep doesn't name directly: the canary
// word AS STORED in an AONT package must be its ciphertext, never the
// plaintext aontCanary bytes copied in raw.
//
// This exists because build.md's own Session 2.4.2 pseudocode at one point
// described the inverse (and insecure) construction: append the canary in
// the clear AFTER encryption, then hash only the preceding data words,
// excluding the canary from the commitment hash. Under that construction the
// canary check degenerates into a bare comparison against the public
// aontCanary constant — completely decoupled from whether K or the
// surrounding ciphertext was correctly recovered — which defeats FR-018 and
// the entire point of AONT. The shipped code correctly does the opposite
// (ARCH §10 Stage 1: canary appended to plaintext BEFORE encryption, hash
// covers the full ciphertext including the encrypted canary word); this test
// names that property explicitly so a future refactor toward the flawed
// pseudocode fails here with a message that says exactly what went wrong,
// rather than surfacing only as one anonymous iteration inside
// TestAONTCorruptionDetection's byte sweep.
//
// [REF: ARCH §10 Stage 1, ADR-022, FR-018]
func TestAONTCanaryIsEncryptedNotRaw(t *testing.T) {
	segment := testSegment(4) // 4 words = 64 bytes; representative non-trivial input

	for _, p := range []struct {
		name  string
		aesNI bool
	}{
		{"chacha20_path", false},
		{"aesni_path", true},
	} {
		pkg, err := AONTEncodeSegment(segment, p.aesNI)
		if err != nil {
			t.Fatalf("%s: AONTEncodeSegment: %v", p.name, err)
		}

		numDataWords := len(segment) / aontWordSize
		canaryWordOffset := numDataWords * aontWordSize
		storedCanaryWord := pkg[canaryWordOffset : canaryWordOffset+aontWordSize]

		if bytes.Equal(storedCanaryWord, aontCanary[:]) {
			t.Errorf("%s: canary word in the package equals the plaintext aontCanary "+
				"constant — it was copied in raw instead of encrypted "+
				"(this is build.md Session 2.4.2's flawed construction, not ARCH §10 Stage 1)",
				p.name)
		}
	}
}
