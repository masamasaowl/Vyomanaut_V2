// Package scoring is declared in doc.go.
// This file defines all sentinel errors exported by the scoring package.
// Callers must compare using errors.Is; never construct these values inline.
//
// This is the single accumulating home for every sentinel error the scoring
// package exports across Milestone 8 (mirrors how internal/audit/errors.go
// accumulates across Milestone 7) — later sessions append further sentinels
// here; they are never declared in a separate errors file.
//
// [REF: IC §5.6]

package scoring

import "errors"

var (
	// ErrProviderNotFound is returned by GetScore when the provider has no rows
	// in mv_provider_scores yet (no audit history) (IC §5.6).
	ErrProviderNotFound = errors.New("scoring: provider not found in score view")

	// ErrProviderNotVetting is returned by IncrementConsecutivePasses when the
	// provider is not in VETTING status (IC §5.6).
	ErrProviderNotVetting = errors.New("scoring: provider is not in VETTING status")
)
