// Package crypto is declared in doc.go.
// This file implements HKDF-SHA256 key derivation per IC §5.1 and ADR-020.
// All exported functions are pure (no shared mutable state) and goroutine-safe.
// Pre-condition violations always panic — they represent programming errors, not
// recoverable runtime conditions; callers must supply correct-length slices.
//
// [REF: IC §5.1, ADR-020, RFC 5869]

package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// ── Pre-condition sizes ───────────────────────────────────────────────────────

const (
	// masterSecretSize is the required byte length of a Vyomanaut master secret.
	// Equal to sha256.Size; both represent a 256-bit value.
	masterSecretSize = sha256.Size

	// uuidSize is the required byte length of a UUID argument (ownerID, fileID).
	// 16 bytes encodes a 128-bit UUID v4.
	uuidSize = 16
)

// ── Pre-condition guard ───────────────────────────────────────────────────────

// mustLen panics when b does not have exactly want bytes.
// fn is the exported function name; param is the parameter name.
// All input-length guards in this file go through this single helper.
func mustLen(b []byte, want int, fn, param string) {
	if len(b) != want {
		panic(fmt.Sprintf("crypto.%s: %s must be %d bytes, got %d", fn, param, want, len(b)))
	}
}

// ── Internal HKDF-SHA256 (RFC 5869) ──────────────────────────────────────────

// hkdfSHA256 derives exactly sha256.Size bytes of output key material using
// HKDF-SHA256 (RFC 5869) with the given IKM, salt, and info.
//
// Delegates to golang.org/x/crypto/hkdf (already vendored for argon2,
// chacha20, and chacha20poly1305 elsewhere in this package — this adds no
// new dependency). Verified byte-for-byte equivalent to this package's
// previous hand-rolled Extract/Expand implementation against all four KAT
// vectors in hkdf_test.go, and against the pinned v0.53.0 source directly,
// before this change was made (M2 review corrections).
//
// Since every caller in this package needs exactly sha256.Size output bytes
// (L = HashLen = 32), this always reads a single Expand block:
//
//	Step 1 — Extract: PRK = HMAC-SHA256(key=salt, msg=ikm)
//	Step 2 — Expand:  T(1) = HMAC-SHA256(key=PRK, msg="" ∥ info ∥ 0x01)
//	Output: T(1)
//
// x/crypto/hkdf's Reader supports reading up to 255×HashLen = 8,160 bytes
// before its "entropy limit reached" error fires; a 32-byte read never
// reaches that, so the error path below is unreachable in practice and is
// treated as an invariant violation, not a recoverable condition.
//
// Not exported. Goroutine-safe: yes (hkdf.New allocates fresh state per
// call; no shared mutable state).
func hkdfSHA256(ikm, salt, info []byte) [sha256.Size]byte {
	r := hkdf.New(sha256.New, ikm, salt, info)
	var out [sha256.Size]byte
	if _, err := io.ReadFull(r, out[:]); err != nil {
		panic(fmt.Sprintf("crypto.hkdfSHA256: unexpected hkdf.New Read failure: %v", err))
	}
	return out
}

// ── Exported key derivation functions ────────────────────────────────────────

// DeriveFileKey derives a 32-byte file encryption key using HKDF-SHA256.
//
//	file_key = HKDF-SHA256(ikm=masterSecret, salt=ownerID, info="vyomanaut-file-v1"||fileID)
//
// Pre-conditions: len(masterSecret)==32, len(ownerID)==16, len(fileID)==16.
// Pre-condition violations always panic.
// Goroutine-safe: yes.
//
// [REF: IC §5.1, ADR-020]
func DeriveFileKey(masterSecret, ownerID, fileID []byte) [sha256.Size]byte {
	mustLen(masterSecret, masterSecretSize, "DeriveFileKey", "masterSecret")
	mustLen(ownerID, uuidSize, "DeriveFileKey", "ownerID")
	mustLen(fileID, uuidSize, "DeriveFileKey", "fileID")
	const prefix = "vyomanaut-file-v1"
	info := make([]byte, 0, len(prefix)+uuidSize)
	info = append(info, prefix...)
	info = append(info, fileID...)
	return hkdfSHA256(masterSecret, ownerID, info)
}

// DerivePointerEncKey derives the 32-byte key used to encrypt a pointer file.
//
//	key = HKDF-SHA256(ikm=masterSecret, salt=ownerID, info="vyomanaut-pointer-v1"||fileID)
//
// Pre-conditions and goroutine safety are identical to DeriveFileKey.
//
// [REF: IC §5.1, ADR-020]
func DerivePointerEncKey(masterSecret, ownerID, fileID []byte) [sha256.Size]byte {
	mustLen(masterSecret, masterSecretSize, "DerivePointerEncKey", "masterSecret")
	mustLen(ownerID, uuidSize, "DerivePointerEncKey", "ownerID")
	mustLen(fileID, uuidSize, "DerivePointerEncKey", "fileID")
	const prefix = "vyomanaut-pointer-v1"
	info := make([]byte, 0, len(prefix)+uuidSize)
	info = append(info, prefix...)
	info = append(info, fileID...)
	return hkdfSHA256(masterSecret, ownerID, info)
}

// DeriveKeystoreEncKey derives the 32-byte key used to encrypt the daemon keystore.
//
//	key = HKDF-SHA256(ikm=masterSecret, salt=ownerID, info="vyomanaut-keystore-v1")
//
// Pre-conditions and goroutine safety are identical to DeriveFileKey.
//
// [REF: IC §5.1, ADR-020]
func DeriveKeystoreEncKey(masterSecret, ownerID []byte) [sha256.Size]byte {
	mustLen(masterSecret, masterSecretSize, "DeriveKeystoreEncKey", "masterSecret")
	mustLen(ownerID, uuidSize, "DeriveKeystoreEncKey", "ownerID")
	return hkdfSHA256(masterSecret, ownerID, []byte("vyomanaut-keystore-v1"))
}

// DeriveDHTOwnerKey derives the 32-byte per-file DHT owner-key component.
//
//	file_owner_key = HKDF-SHA256(ikm=masterSecret, salt=ownerID, info="vyomanaut-dht-v1"||fileID)
//
// The returned key must be passed to DeriveDHTKey to produce the actual DHT
// lookup key for a specific chunk.
// Pre-conditions and goroutine safety are identical to DeriveFileKey.
//
// [REF: IC §5.1, IC §12, ADR-020]
func DeriveDHTOwnerKey(masterSecret, ownerID, fileID []byte) [sha256.Size]byte {
	mustLen(masterSecret, masterSecretSize, "DeriveDHTOwnerKey", "masterSecret")
	mustLen(ownerID, uuidSize, "DeriveDHTOwnerKey", "ownerID")
	mustLen(fileID, uuidSize, "DeriveDHTOwnerKey", "fileID")
	const prefix = "vyomanaut-dht-v1"
	info := make([]byte, 0, len(prefix)+uuidSize)
	info = append(info, prefix...)
	info = append(info, fileID...)
	return hkdfSHA256(masterSecret, ownerID, info)
}

// DeriveDHTKey produces the 32-byte DHT lookup key for a specific chunk.
//
//	dht_key = HMAC-SHA256(key=chunkHash, msg=fileOwnerKey)
//
// Both inputs are fixed-size arrays; the type system enforces their lengths so
// no explicit pre-condition check is required.
// Goroutine-safe: yes.
//
// [REF: IC §5.1, IC §12, ADR-020]
func DeriveDHTKey(chunkHash, fileOwnerKey [sha256.Size]byte) [sha256.Size]byte {
	h := hmac.New(sha256.New, chunkHash[:])
	_, _ = h.Write(fileOwnerKey[:]) // hash.Hash.Write is guaranteed to return a nil error
	var out [sha256.Size]byte
	copy(out[:], h.Sum(nil))
	return out
}
