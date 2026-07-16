// Package audit is declared in doc.go.
// This file defines all sentinel errors exported by the audit package.
// Callers must compare using errors.Is; never construct these values inline.
//
// This is the single accumulating home for every sentinel error the audit
// package exports across Milestone 7 (mirrors how internal/crypto/errors.go
// accumulates across Milestone 2) — later sessions (7.4.1, 7.6.1) append
// further sentinels here (e.g. ErrSecretExpired); they are never declared in
// a separate errors file.
//
// [REF: IC §5.5, IC §8]

package audit

import "errors"

var (
	// ErrInvalidSignature is returned by ValidateResponse when the provider's
	// Ed25519 signature does not verify (IC §5.5, IC §3.2).
	ErrInvalidSignature = errors.New("audit: invalid Ed25519 signature")

	// ErrNonceLength is returned by ValidateResponse when challengeNonce is not
	// exactly 33 bytes (DM §3 Invariant 5, FR-038).
	ErrNonceLength = errors.New("audit: challenge nonce must be exactly 33 bytes")

	// ErrReceiptAlreadyFinal is returned by WriteReceiptPhase2 when the target
	// row already has a non-NULL audit_result — idempotent retry (IC §5.5,
	// ADR-015).
	ErrReceiptAlreadyFinal = errors.New("audit: receipt already has a terminal result")

	// ErrSecretNotFound mirrors IC §8: the requested secrets-manager path
	// does not exist.
	ErrSecretNotFound = errors.New("audit: secret path not found")

	// ErrSecretManagerUnavailable mirrors IC §8: the secrets manager is
	// unreachable.
	ErrSecretManagerUnavailable = errors.New("audit: secrets manager unreachable")

	// ErrSecretExpired is returned once the 5-minute cached-secret TTL has
	// elapsed and the secrets manager remains unreachable (IC §8). The
	// caller must back off and must not issue further challenges while this
	// is returned.
	ErrSecretExpired = errors.New("audit: cached secret TTL expired and manager unavailable")
)
