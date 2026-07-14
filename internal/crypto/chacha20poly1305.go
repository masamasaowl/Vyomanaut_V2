// Package crypto is declared in doc.go.
// This file implements AEAD encryption and decryption using ChaCha20-Poly1305
// (RFC 8439) per IC §5.1 and ADR-019.
//
// EncryptAEAD/DecryptAEAD are the package's general-purpose AEAD primitive.
// EncryptPointerFile/DecryptPointerFile are a thin wrapper kept for the one
// artifact they were originally documented for (pointer files) — the wrapper
// exists because IC §5.1 documents a pointer-file-specific precondition
// (non-empty AAD encoding ownerID||fileID||schemaVersion) that doesn't belong
// on the generic primitive. Callers encrypting anything that isn't a pointer
// file (e.g. internal/p2p/identity.go's daemon identity key) call EncryptAEAD/
// DecryptAEAD directly instead of repurposing the pointer-file-named
// functions with an unrelated AAD string. (M2 review §3)
//
// NFR-019: Poly1305 tag comparison uses constant-time comparison via
// crypto/subtle internally inside golang.org/x/crypto/chacha20poly1305.Open.
// No timing oracle on tag verification is introduced, in either the generic
// primitive or the pointer-file wrapper.
//
// [REF: IC §5.1, ADR-019, NFR-019, REQ §5.4 NFR-019, build.md Phase 2.5 Session 2.5.1]

package crypto

import (
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

// poly1305TagSize is the byte length of the Poly1305 authentication tag appended
// to every ciphertext produced by EncryptPointerFile.
// golang.org/x/crypto/chacha20poly1305.Overhead == 16.
const poly1305TagSize = 16

// EncryptAEAD encrypts plaintext using AEAD_CHACHA20_POLY1305 (RFC 8439,
// ADR-019). This is the package's general-purpose AEAD primitive; callers
// name their own artifact-specific wrapper (see EncryptPointerFile below) or
// call this directly when no existing wrapper fits.
//
// The nonce must be advanced by the caller before each call (IC §5.1). aad
// may be empty; callers that need domain separation should pass a
// non-empty, artifact-specific aad and document why (EncryptPointerFile's
// stricter precondition is the example).
//
// Post-conditions:
//   - returned ciphertext is len(plaintext)+16 bytes (plaintext + 16-byte Poly1305 tag)
//   - tag is computed over ciphertext with the supplied AAD
//
// Error semantics: returns error only if cipher construction fails (treat as fatal).
// Goroutine-safe: yes (pure function, no shared mutable state).
func EncryptAEAD(key [32]byte, nonce [12]byte, aad, plaintext []byte) ([]byte, error) {
	// NFR-019: chacha20poly1305 uses crypto/subtle.ConstantTimeCompare for
	// constant-time tag operations (both Seal and Open paths).
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, fmt.Errorf("crypto.EncryptAEAD: cipher construction failed: %w", err)
	}
	// Seal appends the 16-byte Poly1305 tag to the ciphertext.
	// dst=nil allocates a fresh slice of length len(plaintext)+16.
	return aead.Seal(nil, nonce[:], plaintext, aad), nil
}

// EncryptPointerFile encrypts the serialised pointer file plaintext using
// AEAD_CHACHA20_POLY1305 (RFC 8439, ADR-019). A thin wrapper around
// EncryptAEAD that enforces the pointer-file-specific AAD contract (IC §5.1).
//
// The AAD must include ownerID ‖ fileID ‖ schemaVersion to bind the
// ciphertext to its identity context. Callers encrypting something other
// than a pointer file should call EncryptAEAD directly instead of reusing
// this name with an unrelated AAD.
//
// Pre-conditions (panic on violation):
//   - len(aad) > 0  (aad must include ownerID||fileID||schemaVersion)
//
// Post-conditions, error semantics, and goroutine safety: identical to
// EncryptAEAD.
//
// [REF: IC §5.1, ADR-019, build.md Phase 2.5 Session 2.5.1]
func EncryptPointerFile(key [32]byte, nonce [12]byte, aad, plaintext []byte) ([]byte, error) {
	if len(aad) == 0 {
		panic("crypto.EncryptPointerFile: aad must not be empty (must include ownerID||fileID||schemaVersion)")
	}
	return EncryptAEAD(key, nonce, aad, plaintext)
}

// DecryptAEAD decrypts and verifies a ciphertext produced by EncryptAEAD.
//
// CRITICAL (NFR-019): The Poly1305 tag is verified with constant-time comparison
// via crypto/subtle inside chacha20poly1305.Open before any plaintext is returned.
// Do not replace aead.Open with a manual tag comparison.
//
// Pre-conditions (return error on violation):
//   - len(ciphertext) >= 16  (must include the 16-byte Poly1305 tag)
//
// Error semantics:
//   - ErrTagMismatch: tag verification failed; caller MUST NOT use any returned bytes.
//     nil plaintext is always returned alongside ErrTagMismatch.
//   - Other errors: pre-condition violation or internal cipher failure; treat as fatal.
//
// Goroutine-safe: yes (pure function, no shared mutable state).
func DecryptAEAD(key [32]byte, nonce [12]byte, aad, ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < poly1305TagSize {
		return nil, fmt.Errorf(
			"crypto.DecryptAEAD: ciphertext too short: %d bytes (minimum %d for Poly1305 tag)",
			len(ciphertext), poly1305TagSize)
	}

	// NFR-019: chacha20poly1305.New returns an AEAD whose Open method uses
	// crypto/subtle.ConstantTimeCompare for tag verification internally.
	aead, err := chacha20poly1305.New(key[:])
	if err != nil {
		return nil, fmt.Errorf("crypto.DecryptAEAD: cipher construction failed: %w", err)
	}

	// Open verifies the Poly1305 tag with constant-time comparison (NFR-019),
	// then decrypts. On tag mismatch it returns an error and no plaintext bytes.
	plaintext, err := aead.Open(nil, nonce[:], ciphertext, aad)
	if err != nil {
		// Return nil plaintext — never return partial plaintext on tag mismatch.
		// ErrTagMismatch is the sentinel error; callers must use errors.Is.
		return nil, ErrTagMismatch
	}
	return plaintext, nil
}

// DecryptPointerFile decrypts and verifies a pointer file ciphertext produced
// by EncryptPointerFile. A thin wrapper around DecryptAEAD — see its doc
// comment for the constant-time tag-verification guarantee (NFR-019), which
// applies identically here, and for the full pre-condition/error/goroutine-
// safety contract.
//
// [REF: IC §5.1, ADR-019, NFR-019, REQ §5.4 NFR-019, build.md Phase 2.5 Session 2.5.1]
func DecryptPointerFile(key [32]byte, nonce [12]byte, aad, ciphertext []byte) ([]byte, error) {
	return DecryptAEAD(key, nonce, aad, ciphertext)
}
