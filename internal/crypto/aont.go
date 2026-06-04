// Package crypto is declared in doc.go.
// This file implements the All-or-Nothing Transform (AONT) per IC §5.1 and ADR-022.
//
// Algorithm source: ARCH §10 Stage 1 / Stage 4.
//
// Package layout produced by AONTEncodeSegment:
//
//	[ (numDataWords+1) × 16-byte encrypted words ]  [ 32-byte key block ]
//	  └── numDataWords encrypted data words           └── K ⊕ SHA-256(ciphertext)
//	  └── 1 encrypted canary word (last ciphertext word)
//
// The canary is appended to the plaintext before encryption (ARCH §10 step 1),
// so any single-byte corruption anywhere in the package changes either the hash
// input (ciphertext) or the key block, making correct K recovery impossible and
// causing the canary check to fail — the all-or-nothing property.
//
// Cipher selection:
//   - aesNIAvailable == false  →  ChaCha20-256, zero 12-byte nonce
//   - aesNIAvailable == true   →  AES-256-CTR, counter starting at 1
//
// The two paths produce incompatible packages; cross-cipher decode yields
// ErrCanaryMismatch (see TestAONTCrossCipherIncompatible).
//
// INVARIANT: K is generated fresh via crypto/rand for every AONTEncodeSegment
// call. Reusing K across segments violates the zero-knowledge property (IC §11).
//
// [REF: IC §5.1, ADR-022, ADR-019, ARCH §10 Stage 1 / Stage 4,
//
//	build.md Phase 2.4 Sessions 2.4.2 and 2.4.3]
package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20"
)

// ── Package-level constants ───────────────────────────────────────────────────

const (
	// aontWordSize is the AONT word size in bytes — equal to the AES block size
	// and the unit of both AONT encryption and Reed-Solomon sharding.
	aontWordSize = 16

	// aontKeySize is the AONT symmetric key size in bytes (256-bit K).
	// Equal to sha256.Size; defined separately for readability at call sites.
	aontKeySize = sha256.Size

	// aontKeyBlockSize is the size of the key-embedding block appended to every
	// AONT package: K ⊕ SHA-256(all ciphertext words). Always 32 bytes.
	aontKeyBlockSize = sha256.Size

	// aontNonceSize is the ChaCha20 nonce size in bytes per RFC 8439 §2.3.
	aontNonceSize = 12

	// aontMinCiphertextWords is the minimum number of encrypted words a valid
	// AONT package must contain: at least one data word plus the canary word.
	aontMinCiphertextWords = 2
)

// ── Internal helpers ─────────────────────────────────────────────────────────

// xor32 XORs two 32-byte slices and returns the result as a fixed-size array.
// Both a and b must be at least 32 bytes; only the first 32 bytes are used.
// Used to compute and recover the AONT key block (K ⊕ h).
func xor32(a, b []byte) [aontKeyBlockSize]byte {
	var out [aontKeyBlockSize]byte
	for i := range out {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// ── Exported functions ────────────────────────────────────────────────────────

// AONTEncodeSegment applies the All-or-Nothing Transform to a plaintext segment.
// A fresh random 256-bit key K is generated per call (IC §11: K must never be
// reused across segments or files).
//
// Algorithm (ARCH §10 Stage 1):
//  1. Append the fixed 16-byte canary to the segment.
//  2. Generate K = 32 random bytes.
//  3. Encrypt (segment ‖ canary) word-by-word using ChaCha20 or AES-256-CTR.
//  4. Compute h = SHA-256(all ciphertext words, including encrypted canary).
//  5. Append key block = K ⊕ h.
//
// Output layout: [(numDataWords+1) × 16-byte encrypted words] [32-byte key block].
// Total output length: (numDataWords+3) × 16 bytes (always a multiple of 16).
//
// Pre-conditions (return error on violation):
//   - len(segment) > 0
//   - len(segment) % 16 == 0  (caller pads to minimum 4 MB before calling)
//
// Error semantics: returns error only if crypto/rand fails; treat as fatal.
// Goroutine-safe: yes (pure function, no shared mutable state).
//
// [REF: IC §5.1, ADR-022, ARCH §10 Stage 1, build.md Phase 2.4 Session 2.4.2]
func AONTEncodeSegment(segment []byte, aesNIAvailable bool) ([]byte, error) {
	if len(segment) == 0 {
		return nil, fmt.Errorf("crypto.AONTEncodeSegment: segment must not be empty")
	}
	if len(segment)%aontWordSize != 0 {
		return nil, fmt.Errorf("crypto.AONTEncodeSegment: segment length %d is not a multiple of %d",
			len(segment), aontWordSize)
	}

	// Generate fresh K — never reuse across calls (IC §11).
	K := make([]byte, aontKeySize)
	if _, err := io.ReadFull(rand.Reader, K); err != nil {
		return nil, fmt.Errorf("crypto.AONTEncodeSegment: crypto/rand failure: %w", err)
	}

	numDataWords := len(segment) / aontWordSize

	// plaintext = segment data ‖ canary word (ARCH §10 step 1).
	plaintext := make([]byte, (numDataWords+1)*aontWordSize)
	copy(plaintext[:numDataWords*aontWordSize], segment)
	copy(plaintext[numDataWords*aontWordSize:], aontCanary[:])

	// output = [(numDataWords+1) encrypted words] [32-byte key block].
	output := make([]byte, (numDataWords+1)*aontWordSize+aontKeyBlockSize)
	ciphertext := output[:(numDataWords+1)*aontWordSize]

	if err := aontEncrypt(plaintext, ciphertext, K, aesNIAvailable); err != nil {
		return nil, fmt.Errorf("crypto.AONTEncodeSegment: encryption failed: %w", err)
	}

	// Key block = K ⊕ SHA-256(all ciphertext words including encrypted canary).
	// This ties K to the entire ciphertext, enforcing all-or-nothing.
	h := sha256.Sum256(ciphertext)
	keyBlock := xor32(K, h[:])
	copy(output[(numDataWords+1)*aontWordSize:], keyBlock[:])

	return output, nil
}

// AONTDecodePackage recovers the plaintext segment from an AONT package.
// Verifies the canary word after decryption.
//
// Algorithm (ARCH §10 Stage 4):
//  1. h = SHA-256(all ciphertext words).
//  2. Recover K = key_block ⊕ h.
//  3. Decrypt all ciphertext words.
//  4. Verify last decrypted word == aontCanary; zero buffer and return
//     ErrCanaryMismatch on mismatch.
//  5. Return decrypted segment without the canary word.
//
// CRITICAL: on ErrCanaryMismatch the caller MUST NOT return any plaintext to
// the data owner. The output buffer is zeroed before this error is returned.
//
// Pre-conditions (return error on violation):
//   - len(aontPackage) >= 64  (at least 1 data word + canary word + key block)
//   - (len(aontPackage) - 32) % 16 == 0
//
// Error semantics:
//   - ErrCanaryMismatch: corrupt package or wrong cipher path; buffer is zeroed.
//   - Other errors: cipher construction failure; treat as fatal.
//
// Goroutine-safe: yes (pure function, no shared mutable state).
//
// [REF: IC §5.1, ADR-022, ARCH §10 Stage 4, build.md Phase 2.4 Session 2.4.3]
func AONTDecodePackage(aontPackage []byte, aesNIAvailable bool) ([]byte, error) {
	minPkgSize := aontMinCiphertextWords*aontWordSize + aontKeyBlockSize
	if len(aontPackage) < minPkgSize {
		return nil, fmt.Errorf(
			"crypto.AONTDecodePackage: package too short: %d bytes (minimum %d)",
			len(aontPackage), minPkgSize)
	}
	if (len(aontPackage)-aontKeyBlockSize)%aontWordSize != 0 {
		return nil, fmt.Errorf(
			"crypto.AONTDecodePackage: ciphertext portion is not a multiple of %d bytes",
			aontWordSize)
	}

	ciphertextLen := len(aontPackage) - aontKeyBlockSize
	ciphertext := aontPackage[:ciphertextLen]
	keyBlock := aontPackage[ciphertextLen:]

	// Recover K = key_block ⊕ SHA-256(ciphertext).
	h := sha256.Sum256(ciphertext)
	K := xor32(keyBlock, h[:])

	// Decrypt all ciphertext words (data + encrypted canary).
	decrypted := make([]byte, ciphertextLen)
	if err := aontEncrypt(ciphertext, decrypted, K[:], aesNIAvailable); err != nil {
		return nil, fmt.Errorf("crypto.AONTDecodePackage: decryption failed: %w", err)
	}

	// Verify canary: last aontWordSize bytes of decrypted output must equal aontCanary.
	canaryOffset := ciphertextLen - aontWordSize
	if !bytes.Equal(decrypted[canaryOffset:], aontCanary[:]) {
		// Zero the decryption buffer before returning — callers must not use any
		// returned bytes when ErrCanaryMismatch is signalled (IC §5.1).
		for i := range decrypted {
			decrypted[i] = 0
		}
		return nil, ErrCanaryMismatch
	}

	// Return decrypted segment without the canary word.
	return decrypted[:canaryOffset], nil
}

// ── Internal cipher helpers ───────────────────────────────────────────────────

// aontEncrypt applies the selected stream cipher to src, writing the result into
// dst. dst and src must have the same length. Both ChaCha20 and AES-256-CTR are
// self-inverse (XOR-based), so the same function is used for both encode and
// decode.
//
// AES-256-CTR path: counter initialised to 1 (big-endian uint128) so that
// ciphertext word i = plaintext word i ⊕ AES-256-ECB(K, counter=i+1),
// matching the ARCH §10 Stage 1 specification.
//
// ChaCha20-256 path: zero 12-byte nonce, counter starts at 0 per RFC 8439.
func aontEncrypt(src, dst, K []byte, aesNIAvailable bool) error {
	if aesNIAvailable {
		block, err := aes.NewCipher(K)
		if err != nil {
			return fmt.Errorf("AES cipher init: %w", err)
		}
		// Counter starts at 1: initCTR is a big-endian 128-bit integer = 1.
		// Only the last byte is set; the upper 15 bytes remain zero.
		var initCTR [aontWordSize]byte
		initCTR[aontWordSize-1] = 1
		stream := cipher.NewCTR(block, initCTR[:])
		stream.XORKeyStream(dst, src)
	} else {
		nonce := make([]byte, aontNonceSize)
		stream, err := chacha20.NewUnauthenticatedCipher(K, nonce)
		if err != nil {
			return fmt.Errorf("ChaCha20 init: %w", err)
		}
		stream.XORKeyStream(dst, src)
	}
	return nil
}
