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

Ref: ADR-019 (ChaCha20-256 / AES-256-CTR), ADR-020 (HKDF key hierarchy)
*/
package crypto;