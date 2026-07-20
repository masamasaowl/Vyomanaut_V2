// Package repair is declared in doc.go.
// This file defines all sentinel errors exported by the repair package.
// Callers must compare using errors.Is; never construct these values inline.
// This is the single accumulating home for every sentinel error the repair
// package exports across Milestone 9 (mirrors internal/audit/errors.go and
// internal/scoring/errors.go's accumulating pattern) — later sessions append
// further sentinels here.
//
// [REF: IC §5.7]

package repair

import "errors"

var (
	// ErrShardCountOutOfRange is returned by EnqueueJob when availableShardCount
	// falls outside [profile.DataShards, profile.TotalShards] (DM §4.10).
	ErrShardCountOutOfRange = errors.New("repair: availableShardCount outside [DataShards, TotalShards] for active profile")

	// ErrJobQueueEmpty is returned by DequeueNextJob when no QUEUED job exists.
	// (Distinguishable from a genuine database error — callers should treat
	// this as "nothing to do right now", not retry with backoff.)
	ErrJobQueueEmpty = errors.New("repair: no queued repair job available")

	// ErrNoEligibleReplacement is returned by SelectReplacementProvider when
	// every candidate drawn within the bounded retry budget would violate the
	// 20% ASN cap (FR-045, ADR-014).
	ErrNoEligibleReplacement = errors.New("repair: no ASN-cap-eligible replacement provider found after bounded retries")
)
