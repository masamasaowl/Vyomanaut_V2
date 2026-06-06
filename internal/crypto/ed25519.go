// Package crypto is declared in doc.go.
// This file documents and implements the Ed25519 signing conventions for Vyomanaut V2.
//
// All Ed25519 operations in this package follow the canonical signing procedure
// from IC §3.2:
//
//	provider_sig = Ed25519(private_key, SHA-256(input_bytes))
//
// CRITICAL: JSON serialisation MUST NOT be used for signing inputs — field
// ordering is not guaranteed across Go versions. All signing inputs must be
// constructed as a fixed-layout byte sequence per the relevant protocol section.
//
// This file does NOT wrap the standard library for key generation or storage —
// it provides only the two helpers that enforce the pre-hashing convention and
// a compile-time assertion on the public key size.
//
// [REF: IC §3.2, IC §5.1, build.md Phase 2.7 Session 2.7.1]

package crypto

import (
	"crypto/ed25519"
	"crypto/sha256"
)

// Compile-time assertion: ed25519.PublicKeySize == 32.
// The blank identifier below evaluates to [0]byte when the constant equals 32.
// Any other value produces a negative or non-zero array size, which fails compilation.
var _ [ed25519.PublicKeySize - 32]byte //nolint:mnd // 32 is the Ed25519 public key size per RFC 8032

// SignBytes computes Ed25519(private_key, SHA-256(inputBytes)) per IC §3.2.
// Pre-hashing with SHA-256 is mandatory — callers must never pass a pre-hashed
// digest; this function always applies SHA-256 internally.
//
// SIGNING_INPUT_RULE: use fixed-layout byte sequence, never JSON.
// JSON serialisation must not be used to construct inputBytes — field ordering
// is not guaranteed across Go versions. Use fixed-width fields in a defined
// byte order (e.g. big-endian uint64 for timestamps, raw UUIDs for identifiers).
//
// Pre-conditions:
//   - privateKey must be a valid ed25519.PrivateKey (64 bytes); standard library
//     panics on wrong length.
//
// Post-conditions:
//   - returned signature is exactly 64 bytes ([ed25519.SignatureSize]byte)
//   - VerifyBytes(publicKey, inputBytes, result) == true for the matching public key
//
// Goroutine-safe: yes (pure function, no shared mutable state).
//
// [REF: IC §3.2, build.md Phase 2.7 Session 2.7.1]
func SignBytes(privateKey ed25519.PrivateKey, inputBytes []byte) [64]byte {
	digest := sha256.Sum256(inputBytes)
	raw := ed25519.Sign(privateKey, digest[:])
	var sig [64]byte
	copy(sig[:], raw)
	return sig
}

// VerifyBytes verifies an Ed25519 signature produced by SignBytes.
// It replicates the verification procedure from IC §3.2:
//  1. Compute SHA-256(inputBytes) using the same fixed-layout as the signer.
//  2. Call ed25519.Verify(publicKey, sha256Digest, signature).
//  3. Return true iff the signature is valid; false on any mismatch.
//
// SIGNING_INPUT_RULE: use fixed-layout byte sequence, never JSON.
// The inputBytes passed here must be constructed with the identical layout
// used at signing time. Any divergence in field order or encoding will cause
// a SHA-256 mismatch and verification will return false.
//
// Pre-conditions:
//   - publicKey is a 32-byte Ed25519 public key (enforced by [32]byte type).
//   - sig is a 64-byte Ed25519 signature (enforced by [64]byte type).
//
// Post-conditions (on true return):
//   - inputBytes was signed by the holder of the private key corresponding to publicKey.
//   - The signing input was identical (including byte order and field layout).
//
// Callers that need an error return should wrap false as ErrInvalidSignature (IC §3.2).
// Goroutine-safe: yes (pure function, no shared mutable state).
//
// [REF: IC §3.2, build.md Phase 2.7 Session 2.7.1]
func VerifyBytes(publicKey [32]byte, inputBytes []byte, sig [64]byte) bool {
	digest := sha256.Sum256(inputBytes)
	return ed25519.Verify(ed25519.PublicKey(publicKey[:]), digest[:], sig[:])
}
