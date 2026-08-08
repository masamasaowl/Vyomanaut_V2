// Package storage is declared in doc.go.
// This file implements the corrected DHT-cache RAM requirement formula
// (build.md Session 13.6.1, A1). NOT in Session 13.6.1's literal FILES:
// list (which names only main.go and the four memcheck_*.go platform
// files) — added because the session's own VERIFY block requires
// `go test -v -run TestDHTCacheRAMFormula ./internal/storage/` to exist and
// pass, which is only possible if the formula lives in a file this package
// compiles unconditionally (every memcheck_*.go file is build-tag-gated to
// exactly one platform, so none of them can host a formula every platform
// needs). This file carries no build tag deliberately.
//
// [REF: NFR-045, ARCH §27.5, build.md Session 13.6.1 (A1)]
package storage

import "math"

// DHT-cache RAM requirement constants (A1 fix — the previous formula's
// erroneous ×400 factor is gone; see build.md's CORRECTED_CONSTANT_NOT_400
// VERIFY check).
const (
	bytesPerGiB = 1 << 30
	bytesPerMiB = 1 << 20
	// ChunksPerGB is the number of ChunkDataSize-sized chunks in one GiB of
	// declared storage: (1<<30) / 262144 = 4096.
	ChunksPerGB = bytesPerGiB / ChunkDataSize

	// DHTRecordSizeBytes is the estimated per-chunk DHT provider-record
	// cache footprint.
	DHTRecordSizeBytes = 200
)

// RequiredDHTCacheRAMMB computes the required free RAM, in MB, to hold the
// DHT provider-record cache for declaredStorageGB of declared storage:
//
//	required_mb = ceil(declared_storage_gb × ChunksPerGB × DHTRecordSizeBytes / (1<<20))
//
// NUMERIC NOTE (flagged, recomputed independently — see this session's
// build report): build.md's own sanity-check prose for this formula states
// "50 GB → 40 MB, 200 GB → 160 MB, 500 GB → ~400 MB". Recomputing the
// formula exactly as given: 50GB → 40MB (matches), 200GB → 157MB (not
// 160MB — off by ~2%), 500GB → 391MB (close to the "~400MB" the prose
// itself already hedges as approximate). This function implements the
// formula precisely as specified rather than forcing its output to match
// the prose's slightly-off 160MB example; TestDHTCacheRAMFormula asserts
// the true computed values.
func RequiredDHTCacheRAMMB(declaredStorageGB uint64) uint64 {
	exact := float64(declaredStorageGB) * float64(ChunksPerGB) * float64(DHTRecordSizeBytes) / float64(bytesPerMiB)
	return uint64(math.Ceil(exact))
}
