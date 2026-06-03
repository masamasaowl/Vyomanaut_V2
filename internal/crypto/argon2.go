// Package crypto is declared in doc.go.
// This file implements Argon2id master secret derivation per IC §5.1 and ADR-020.
// All exported functions are pure (no shared mutable state) and goroutine-safe.
// Pre-condition violations always panic — they represent programming errors, not
// recoverable runtime conditions; callers must supply correct-length slices and
// valid cost parameters.
//
// [REF: IC §5.1, ADR-020, MVP §3.5, MVP §5.4, build.md Phase 2.3]

package crypto

import (
	"crypto/sha256"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// ── Pre-condition constants ───────────────────────────────────────────────────

const (
	// argon2MinPassphraseLen is the minimum acceptable passphrase byte length.
	// Pre-condition for DeriveMasterSecret.
	argon2MinPassphraseLen = 8

	// argon2MinMemory is the minimum acceptable Argon2id memory parameter in KiB.
	// The demo profile uses exactly this minimum; production uses 64 MiB.
	// Pre-condition for DeriveMasterSecret.
	argon2MinMemory = 4096
)

// DeriveMasterSecret derives the 32-byte master secret from the owner's passphrase
// using Argon2id. The cost parameters are supplied by the caller from the active
// NetworkProfile — they must never be hardcoded at call sites.
//
// The ownerID (UUID bytes) is used directly as the Argon2id salt.
//
// Pre-conditions (panic on violation):
//   - len(passphrase) >= 8
//   - len(ownerID) == 16   (UUID bytes; used as Argon2id salt)
//   - argon2Time >= 1      (minimum one iteration)
//   - argon2Memory >= 4096 (minimum 4 MiB in KiB)
//   - argon2Threads >= 1
//
// Post-conditions:
//   - Returns a 32-byte master secret, deterministic for the given inputs.
//   - Execution time is governed by the supplied Argon2id parameters.
//     Production: >= 200ms. Demo: ~20–50ms.
//
// Caller responsibility: always pass profile.Argon2Time, profile.Argon2Memory, and
// profile.Argon2Threads from the active NetworkProfile. Never hardcode Argon2id
// parameters inline. (IC §5.1, ADR-031, MVP §5.4)
//
// Error semantics: no errors returned; pre-condition violations panic.
// Goroutine-safe: yes (pure function).
//
// [REF: IC §5.1, ADR-020, MVP §3.5, MVP §5.4, build.md Phase 2.3 Session 2.3.1]
func DeriveMasterSecret(passphrase, ownerID []byte, argon2Time uint32, argon2Memory uint32, argon2Threads uint8) [sha256.Size]byte {
	if len(passphrase) < argon2MinPassphraseLen {
		panic(fmt.Sprintf(
			"crypto.DeriveMasterSecret: passphrase must be >= %d bytes, got %d",
			argon2MinPassphraseLen, len(passphrase),
		))
	}
	mustLen(ownerID, uuidSize, "DeriveMasterSecret", "ownerID")
	if argon2Time < 1 {
		panic("crypto.DeriveMasterSecret: argon2Time must be >= 1")
	}
	if argon2Memory < argon2MinMemory {
		panic(fmt.Sprintf(
			"crypto.DeriveMasterSecret: argon2Memory must be >= %d KiB, got %d",
			argon2MinMemory, argon2Memory,
		))
	}
	if argon2Threads < 1 {
		panic("crypto.DeriveMasterSecret: argon2Threads must be >= 1")
	}

	raw := argon2.IDKey(passphrase, ownerID, argon2Time, argon2Memory, argon2Threads, sha256.Size)
	var out [sha256.Size]byte
	copy(out[:], raw)
	return out
}
