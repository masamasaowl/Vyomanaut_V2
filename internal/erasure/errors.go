// Package erasure is declared in doc.go.
// This file defines all sentinel errors exported by the erasure package.
// Callers must compare using errors.Is; never construct these values inline.
//
// [REF: IC §5.2, ADR-003, build.md Phase 3.1 Session 3.1.2]

package erasure

import "errors"

var (
	// ErrDataShardsZero is returned by NewEngine when NetworkProfile.DataShards < 1.
	ErrDataShardsZero = errors.New("erasure: DataShards must be >= 1")

	// ErrTotalShardsMismatch is returned by NewEngine when
	// TotalShards != DataShards + ParityShards.
	ErrTotalShardsMismatch = errors.New("erasure: TotalShards must equal DataShards + ParityShards")

	// ErrShardSizeMismatch is returned by NewEngine when NetworkProfile.ShardSize
	// does not equal the compile-time ShardSize constant.
	// This indicates a misconfigured profile, not a runtime data error.
	ErrShardSizeMismatch = errors.New("erasure: profile ShardSize must equal compile-time ShardSize=262144")

	// ErrTooFewShards is returned by DecodeSegment when fewer than DataShards
	// non-nil shards are provided — reconstruction is impossible.
	ErrTooFewShards = errors.New("erasure: fewer than DataShards non-nil shards provided")

	// ErrShardSize is returned by DecodeSegment when a non-nil shard has a
	// length other than ShardSize bytes.
	ErrShardSize = errors.New("erasure: shard has incorrect length; expected ShardSize bytes")
)
