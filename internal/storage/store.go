// Package storage is declared in doc.go.
// This file defines the ChunkStore interface and the two layout constants derived
// from ARCH §16 and ARCH §27.1.
//
// IMPORT CONSTRAINT (IC §9): this package must NOT import internal/payment,
// internal/scoring, or internal/repair. The storage engine is unaware of
// network economics or topology. Only stdlib and the grocksdb binding are
// permitted. This file imports only "context".
//
// INVARIANT (DM §3 Invariant 6, ADR-030): The daemon cannot distinguish
// between synthetic vetting chunks and real file shards. Both use identical
// AppendChunk / LookupChunk / DeleteChunk code paths. The is_vetting_chunk
// flag exists only in the microservice's chunk_assignments table.
//
// [REF: IC §5.3, ARCH §16, ARCH §27.1, ADR-023, build.md Phase 5.1 Session 5.1.1]

package storage

import "context"

// vLogEntrySize is the fixed byte size of every vLog entry (ARCH §16, ARCH §27.1).
//
//	Layout: chunk_id(32) + chunk_size(4) + chunk_data(262144) + content_hash(32)
//	      = 262212 bytes.
//
// Every read and write uses this exact size; no variable-length entries exist in V2.
// This constant is intentionally larger than the raw 256 KB chunk data (262144 bytes)
// to account for the 68-byte per-entry header and trailer fields.
const vLogEntrySize = 262212

// indexValueSize is the byte size of the RocksDB value per chunk index entry (ARCH §27.1).
//
//	Layout: vlog_offset(uint64=8) + chunk_size(uint32=4) = 12 bytes.
//
// The RocksDB key is the 32-byte chunk_id, so the total on-disk entry is
// approximately 44 bytes (32 key + 12 value). At 50 GB declared storage the
// entire index fits in ~8.8 MB of RocksDB block cache (ARCH §27.1).
const indexValueSize = 12

// ChunkStore is the WiscKey key-value separated chunk storage engine (ARCH §16, ADR-023).
// The RocksDB index holds small 44-byte entries; the append-only vLog holds all
// 262 KB chunk data. Write amplification ≈ 1.0 at 256 KB values (ARCH §4.1, ARCH §27.1).
//
// Create with NewChunkStore (implemented in a later session).
//
// CONCURRENCY: Only AppendChunk and RecoverFromCrash are NOT goroutine-safe. All
// other methods are goroutine-safe. See each method's comment for details.
type ChunkStore interface {
	// AppendChunk writes a 256 KB chunk to the vLog and inserts the RocksDB index entry.
	//
	// *** SINGLE WRITER ONLY — NOT goroutine-safe ***
	//
	// All concurrent callers MUST submit write requests via a buffered channel to the
	// designated single writer goroutine. POSIX O_APPEND atomicity does not hold for
	// writes above ~4 KB; for 262212-byte vLog entries, goroutine serialisation is
	// mandatory to prevent interleaved writes and vLog corruption.
	// (ARCH §16 §Single writer goroutine, IC §11)
	//
	// Pre-conditions (panic in debug builds if violated):
	//   - len(chunkID)   == 32      (SHA-256 content address; fixed-size array enforces this)
	//   - len(chunkData) == 262144  (exactly one 256 KB shard; caller must pad if short)
	//   - SHA-256(chunkData) == chunkID  (caller MUST verify before calling;
	//     AppendChunk does NOT re-verify to avoid double SHA-256 overhead)
	//   - The vLog file handle is open and positioned at EOF
	//
	// Post-conditions (on nil error):
	//   - chunkData and its SHA-256 content_hash are durably fsync'd to the vLog.
	//   - RocksDB index entry (chunkID → vlog_offset, chunk_size) is inserted.
	//   - The returned vlogOffset is the byte offset where the 262212-byte entry
	//     begins in the vLog.
	//
	// Error semantics:
	//   - ErrVLogFsync    — fsync failed; daemon MUST halt; RecoverFromCrash repairs
	//                       the tail on next restart (ARCH §16 §Crash recovery).
	//   - ErrRocksDBInsert — index INSERT failed after a successful vLog write;
	//                        RecoverFromCrash re-inserts the missing entry on restart.
	//
	// Goroutine-safe: NO — single designated writer goroutine only.
	AppendChunk(chunkID [32]byte, chunkData []byte) (vlogOffset uint64, err error)

	// LookupChunk retrieves a chunk from the vLog by content address.
	// Internal path: Bloom filter → RocksDB block cache → vLog pread → SHA-256 verify.
	// (ARCH §16 §Audit lookup path)
	//
	// For an absent chunk the Bloom filter exits in memory — no disk I/O occurs.
	// For a present chunk: typically one SSD read (~1 ms) or HDD read (~12–15 ms)
	// for the 262212-byte vLog entry; the 44-byte RocksDB index entry is usually
	// in block cache (ARCH §27.1).
	//
	// Pre-conditions:
	//   - chunkID is a 32-byte SHA-256 content address (fixed-size array enforces this)
	//
	// Post-conditions (on nil error):
	//   - Returned slice is exactly 262144 bytes.
	//   - SHA-256(returned data) == chunkID — verified internally; caller need not re-check.
	//
	// Error semantics:
	//   - ErrChunkNotFound       — Bloom filter / RocksDB has no entry; return audit
	//                              status 0x01 (FAIL_NOT_FOUND). Expected for challenges
	//                              on chunks not assigned to this provider.
	//   - ErrContentHashMismatch — data present, hash wrong; silent disk corruption;
	//                              return audit status 0x02 (FAIL_CORRUPTION), IC §4.2.
	//   - ErrVLogRead            — fatal I/O error; treat as daemon halt.
	//
	// Goroutine-safe: yes (read-only via pread; concurrent with the writer goroutine).
	LookupChunk(chunkID [32]byte) ([]byte, error)

	// DeleteChunk removes the RocksDB index entry for chunkID. The corresponding
	// vLog entry remains on disk until the next GC cycle reclaims it (RunGC).
	//
	// Subsequent LookupChunk calls for this chunkID will return ErrChunkNotFound.
	//
	// VETTING GC PATH (ADR-030, IC §4.5): DeleteChunk is also called by the vetting
	// GC handler to retire synthetic vetting chunks on the ACTIVE provider transition.
	// The call semantics are identical for synthetic and real chunks — the daemon has
	// no visibility into which type is being deleted (DM §3 Invariant 6).
	//
	// Goroutine-safe: yes.
	DeleteChunk(chunkID [32]byte) error

	// RecoverFromCrash scans the vLog tail for entries that are present on disk but
	// missing from the RocksDB index, and re-inserts them. (ARCH §16 §Crash recovery,
	// ADR-023, NFR-024)
	//
	// MUST be called exactly once at daemon startup, BEFORE the writer goroutine is
	// started and BEFORE any AppendChunk call is made.
	//
	// Maximum scan cost: one RocksDB memtable flush interval of chunks (typically a
	// few hundred entries). Bounded recovery time is a design constraint of ADR-023.
	//
	// Pre-conditions:
	//   - No AppendChunk has been called since the store was opened.
	//   - No writer goroutine is running.
	//
	// Post-conditions (on nil error):
	//   - All vLog entries up to current EOF are reflected in the RocksDB index.
	//
	// Goroutine-safe: NO — must complete before the writer goroutine is launched.
	RecoverFromCrash() error

	// RunGC reclaims vLog disk space from entries whose RocksDB index entry has been
	// deleted (by DeleteChunk). Uses fallocate(FALLOC_FL_PUNCH_HOLE) on Linux to
	// release disk blocks without compacting the vLog file. (ADR-023 §Garbage collection)
	//
	// Runs in a background goroutine; ctx cancellation stops it cleanly without data loss.
	// GC uses a separate read file handle from the writer goroutine — no coordination
	// with AppendChunk is required.
	//
	// Goroutine-safe: yes.
	RunGC(ctx context.Context) error

	// Close flushes pending RocksDB writes and closes both the RocksDB instance and
	// the vLog file handle. After Close returns, all method calls on this ChunkStore
	// produce undefined behaviour and must not be made.
	//
	// Goroutine-safe: yes (safe to call concurrently with other methods, but only once).
	Close() error
}
