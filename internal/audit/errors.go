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

	// ErrReplayDetected is returned by WriteReceiptPhase1 when
	// challenge_nonce has already been used in a prior audit_receipts row —
	// the audit_receipt_nonces PRIMARY KEY (DM §4.7, ADR-033) rejecting a
	// replayed nonce. Unlike ErrReceiptAlreadyFinal, this is NOT a legitimate
	// idempotent retry: a replayed nonce means a provider (or an attacker)
	// is resubmitting proof against a challenge that was already answered
	// once, under a different server_challenge_ts. Callers must treat this
	// as a hard rejection of the dispatch attempt.
	ErrReplayDetected = errors.New("audit: challenge nonce already used (replay detected)")

	// ErrResponseAlreadyRecorded is returned by WriteReceiptRecordResponse
	// when the target row's response-derived columns are already populated
	// (response_hash IS NOT NULL) — the idempotent-retry counterpart to
	// ErrReceiptAlreadyFinal, one step earlier in the three-phase pipeline
	// (IC §5.5 Option B): a provider's signed response was already recorded
	// for this receipt, whether or not WriteReceiptPhase2 has adjudicated it
	// yet. Also returned if the row is already abandoned or already final,
	// for the same reason WriteReceiptPhase2's own WHERE clause cannot
	// distinguish those cases from each other.
	ErrResponseAlreadyRecorded = errors.New("audit: response already recorded for this receipt")

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
