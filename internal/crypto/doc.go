/*
Package crypto implements all cryptographic primitives for Vyomanaut V2.

All functions are pure (no shared
mutable state) and goroutine-safe by design — they take all inputs as arguments and
return all outputs as values.

INVARIANT: No function in this package accepts a float64 or float32 parameter or returns one. All monetary calculations are delegated to internal/payment.

Included components:

  - AONT encryption
  - HKDF key derivation
  - Argon2id password hardening
  - Pointer file encryption
  - BIP-39 mnemonic support
  - Ed25519 helpers

Design notes after HKDF key derivation (Phase 2.2)
 1. HKDF is scratch-only, zero external deps using Used crypto/hmac + crypto/sha256. No golang.org/x/crypto/hkdf
 2. Single-block expand. All five functions need exactly 32 bytes (= sha256.Size = HashLen), so the RFC 5869 Expand phase terminates after one block: T(1) = HMAC(PRK, info ∥ 0x01). If a future function needs more than 32 bytes, hkdfSHA256 must be extended to a multi-block loop
 3. _, _ = h.Write(…) everywhere. hash.Hash.Write is contractually infallible, but errcheck still flags a bare h.Write(data). The explicit double-blank avoids both the lint failure and any //nolint that would need a BUILD citation
 4. [sha256.Size]byte return type = [32]byte — identical type in Go. Uses the named constant rather than a raw literal, keeping mnd clean on non-test files. The VERIFY grep anchored to ^func matches either spelling.

Ref: ADR-019 (ChaCha20-256 / AES-256-CTR), ADR-020 (HKDF key hierarchy)
*/
package crypto
