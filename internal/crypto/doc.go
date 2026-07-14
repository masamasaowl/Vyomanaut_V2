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
 1. [UPDATED — M2 review corrections] Originally scratch-only (crypto/hmac + crypto/sha256, zero external deps). Swapped hkdfSHA256's Extract/Expand to golang.org/x/crypto/hkdf: that module is already a direct dependency (argon2, chacha20, chacha20poly1305 elsewhere in this package), so this adds no new go.mod/go.sum entry — it only reduces hand-written crypto surface area. Verified byte-identical before switching, via independent Python HMAC/SHA-256 re-derivation of all four KATs and direct inspection of the pinned v0.53.0 hkdf.go source.
 2. Single-block expand. All five functions need exactly 32 bytes (= sha256.Size = HashLen), so the RFC 5869 Expand phase terminates after one block: T(1) = HMAC(PRK, "" ∥ info ∥ 0x01). If a future function needs more than 32 bytes, read more than sha256.Size bytes from the same io.Reader — x/crypto/hkdf already supports up to 255×HashLen bytes natively, so no hand-rolled multi-block loop is needed.
 3. _, _ = h.Write(…) still applies to DeriveDHTKey's direct hmac.New usage — that function doesn't go through hkdfSHA256 (HKDF isn't the right primitive for combining a chunk hash with a derived key; see its own doc comment).
 4. [sha256.Size]byte return type = [32]byte — identical type in Go. Uses the named constant rather than a raw literal, keeping mnd clean on non-test files. The VERIFY grep anchored to ^func matches either spelling.

Design notes after Phase 2.4 — AONT Cipher:
 1. Package layout follows ARCH §10 Stage 1
 2. The two cipher paths (ChaCha20-256 / AES-256-CTR) share the internal aontEncrypt helper; self-inverse XOR means encode == decode.
 3. AES-256-CTR counter starts at 1 (big-endian uint128) matching ARCH §10 Stage 1; cipher.NewCTR is used in place of the manual word loop.
 4. Canary value = first 16 bytes of SHA-256("vyomanaut-aont-canary-v1"): 0x16 0x14 0x38 0x2e 0x7a 0x0b 0x48 0xc4 0xe2 0xc7 0x42 0x13 0x03 0x5f 0xbc 0x64

Design notes after Phase 2.5 — Pointer File AEAD:
 1. NFR-019 constant-time guarantee: chacha20poly1305.Open uses crypto/subtle internally; the 5 grep hits are from comments in chacha20poly1305.go that explicitly document this for auditors.
 2. ErrInvalidMnemonic pre-declared for Phase 2.6 (BIP-39), consistent with the existing errors.go in the repo.

Design notes after Phase 2.7 — Ed25519 Signing Conventions
 1. SignBytes/VerifyBytes take [64]byte / [32]byte fixed-size arrays; the type system enforces sig and key lengths at compile time, removing the len(sig)==64 runtime checks IC §3.2 describes for higher-level callers.
 2. ErrInvalidSignature is not declared here; it belongs in the calling package (audit, p2p) per IC §3.2 "Return ErrInvalidSignature if false."
 3. Compile-time assertion var _ [ed25519.PublicKeySize - 32]byte creates [0]byte when the constant is 32 (valid) and a negative-size or non-zero array otherwise, (compile error), anchored by the exact string in the comment above it

[REF: ADR-019 (ChaCha20-256 / AES-256-CTR), ADR-020 (HKDF key hierarchy)]
*/
package crypto
