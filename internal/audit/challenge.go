// Package audit is declared in doc.go.
// This file implements challenge nonce generation per IC §5.5 and FR-038.
// ChallengeNonce is pure (no shared mutable state) and goroutine-safe.
// Pre-condition violations always panic — they represent programming errors,
// not recoverable runtime conditions; callers must supply a correct-length
// secret.
//
// [REF: IC §5.5, FR-038, DM §3 Invariant 5, ADR-017, ADR-027]

package audit

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// ── Pre-condition / layout sizes ──────────────────────────────────────────────

const (
	// serverSecretSize is the required byte length of a cluster server secret
	// (server_secret_vN) passed to ChallengeNonce.
	serverSecretSize = 32

	// chunkIDSize is the byte length of a chunk content address (SHA-256
	// output) — matches the fixed [32]byte parameter type below.
	chunkIDSize = 32

	// serverTsSize is the byte length of the big-endian int64 server
	// timestamp component of the HMAC signing input.
	serverTsSize = 8
)

// ChallengeNonce generates a 33-byte versioned challenge nonce (IC §5.5, FR-038).
//
//	nonce = version_byte || HMAC-SHA256(serverSecretVN, chunkID || serverTsMs)
//
// The version byte identifies which server_secret_vN produced the HMAC, so
// any microservice replica — even one that has since rotated to a newer
// secret version — can look up the correct historical secret to validate a
// nonce issued before a failover. That cross-replica validation property is
// the entire reason the nonce is 33 bytes, not 32 (DM §3 Invariant 5, IC §11,
// ADR-027).
//
// Signing input: chunkID (32 bytes) ‖ serverTsMs as a big-endian int64
// (8 bytes) = 40 bytes total.
//
// Pre-conditions (panic on violation):
//   - len(serverSecretVN) == 32
//   - len(chunkID) == 32 — enforced by the compiler via the fixed [32]byte
//     parameter type, so no runtime check is needed for this one.
//   - versionByte identifies the version of serverSecretVN. This is a caller
//     obligation and is NOT checked here: a raw secret byte slice carries no
//     embedded version tag, so there is nothing in serverSecretVN itself for
//     this function to validate versionByte against. The cluster secret
//     cache (Phase 7.4) is the sole source of truth for that pairing.
//
// Post-conditions:
//   - returns a 33-byte nonce; nonce[0] == versionByte
//   - deterministic: identical inputs always produce an identical nonce
//
// Providers must not be able to compute nonces in advance: serverTsMs is set
// by the microservice at challenge-dispatch time (caller passes a
// time.Now()-derived millisecond value), and serverSecretVN is never
// disclosed to providers, so a provider cannot predict a future nonce
// (FR-038).
//
// Goroutine-safe: yes (pure function, no shared mutable state).
//
// [REF: IC §5.5, FR-038, DM §3 Invariant 5, ADR-017, ADR-027]
func ChallengeNonce(serverSecretVN []byte, versionByte uint8, chunkID [32]byte, serverTsMs int64) [33]byte {
	if len(serverSecretVN) != serverSecretSize {
		panic(fmt.Sprintf(
			"audit.ChallengeNonce: serverSecretVN must be %d bytes, got %d",
			serverSecretSize, len(serverSecretVN)))
	}

	// signingInput = chunkID(32) || serverTsMs big-endian int64(8).
	var signingInput [chunkIDSize + serverTsSize]byte
	copy(signingInput[:chunkIDSize], chunkID[:])
	binary.BigEndian.PutUint64(signingInput[chunkIDSize:], uint64(serverTsMs))

	mac := hmac.New(sha256.New, serverSecretVN)
	_, _ = mac.Write(signingInput[:]) // hash.Hash.Write is guaranteed to return a nil error
	sum := mac.Sum(nil)

	var nonce [33]byte
	nonce[0] = versionByte
	copy(nonce[1:], sum)
	return nonce
}
