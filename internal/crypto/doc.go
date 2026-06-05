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

Design notes after Phase 2.4 — AONT Cipher:
 1. Package layout follows ARCH §10 Stage 1
 2. The two cipher paths (ChaCha20-256 / AES-256-CTR) share the internal aontEncrypt helper; self-inverse XOR means encode == decode.
 3. AES-256-CTR counter starts at 1 (big-endian uint128) matching ARCH §10 Stage 1; cipher.NewCTR is used in place of the manual word loop.
 4. Canary value = first 16 bytes of SHA-256("vyomanaut-aont-canary-v1"): 0x16 0x14 0x38 0x2e 0x7a 0x0b 0x48 0xc4 0xe2 0xc7 0x42 0x13 0x03 0x5f 0xbc 0x64

Design notes after Phase 2.5 — Pointer File AEAD:
 1. NFR-019 constant-time guarantee: chacha20poly1305.Open uses crypto/subtle internally; the 5 grep hits are from comments in chacha20poly1305.go that explicitly document this for auditors.
 2. ErrInvalidMnemonic pre-declared for Phase 2.6 (BIP-39), consistent with the existing errors.go in the repo.

Ref: ADR-019 (ChaCha20-256 / AES-256-CTR), ADR-020 (HKDF key hierarchy)
*/
package crypto
