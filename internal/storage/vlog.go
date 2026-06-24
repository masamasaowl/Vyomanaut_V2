// Package storage is declared in doc.go.
// This file defines the wiskeyStore concrete struct that implements ChunkStore,
// and the two core vLog I/O helpers: appendToVLog and readFromVLog.
//
// The RocksDB index and its operations are in index.go (Session 5.1.3).
// NewChunkStore, AppendChunk, LookupChunk, DeleteChunk, RecoverFromCrash,
// RunGC, and Close are implemented in subsequent sessions.
//
// IMPORT CONSTRAINT (IC §9): no other internal/ package may be imported here.
//
// [REF: IC §5.3, ARCH §16, ARCH §27.1, ADR-023, build.md Phase 5.1 Session 5.1.2]

package storage

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
)

// vLog entry field byte offsets. Derived from vLogEntrySize = 262212 (ARCH §16, ARCH §27.1).
//
// On-disk layout per entry:
//
//	[chunk_id:32][chunk_size:4][chunk_data:262144][content_hash:32]
//	 ↑0           ↑32           ↑36                ↑262180
const (
	vlogOffChunkID     = 0
	vlogOffChunkSize   = 32
	vlogOffChunkData   = 36
	vlogOffContentHash = 36 + 262144 // = 262180
)

// wiskeyStore is the concrete, unexported implementation of ChunkStore.
// Callers always interact through the ChunkStore interface returned by
// NewChunkStore (implemented in a later session).
//
// Field access rules:
//   - index: goroutine-safe after construction (RocksDB operations are thread-safe).
//   - vlog, vlogHead: NOT goroutine-safe. The single designated writer goroutine
//     owns vlog for writes; all other goroutines use ReadAt (pread) which is
//     safe to call concurrently on the same *os.File on POSIX systems.
//   - isRotational: read-only after construction; goroutine-safe.
type wiskeyStore struct {
	// index is the RocksDB chunk index (defined in index.go).
	index *rocksDBIndex

	// vlog is the append-only value log file.
	// The single writer goroutine holds exclusive write access.
	// All goroutines may call ReadAt concurrently (POSIX pread semantics).
	vlog     *os.File
	vlogPath string

	// vlogHead is the byte offset at which the next AppendChunk write will
	// begin. Protected by the single-writer invariant — only the writer
	// goroutine reads or modifies this field.
	vlogHead uint64

	// isRotational is true for HDD-backed storage, false for SSD.
	// Affects monitoring metrics and GC I/O scheduling only; all core
	// vLog read/write paths are storage-medium-agnostic (ARCH §16).
	isRotational bool
}

// appendToVLog writes one complete 262212-byte vLog entry and fsyncs to disk.
//
// MUST be called only from the single designated writer goroutine (IC §5.3).
//
// Algorithm (ARCH §16):
//  1. Compute content_hash = SHA-256(chunkData).
//  2. Build a 262212-byte buffer: [chunk_id][chunk_size][chunk_data][content_hash].
//  3. Write the complete buffer via os.File.Write.
//  4. Call Sync() — return ErrVLogFsync on either failure; daemon must halt.
//  5. Advance ws.vlogHead by vLogEntrySize.
//
// The chunk_size field is always 262144 (fixed in V2; the field is present for
// forward-compatibility with a hypothetical variable-size V3 extension).
//
// Returns the byte offset where the entry begins (= the pre-write vlogHead).
func (ws *wiskeyStore) appendToVLog(chunkID [32]byte, chunkData []byte) (offset uint64, err error) {
	contentHash := sha256.Sum256(chunkData)

	var buf [vLogEntrySize]byte
	copy(buf[vlogOffChunkID:], chunkID[:])
	binary.BigEndian.PutUint32(buf[vlogOffChunkSize:], 262144)
	copy(buf[vlogOffChunkData:], chunkData)
	copy(buf[vlogOffContentHash:], contentHash[:])

	offset = ws.vlogHead
	if _, werr := ws.vlog.Write(buf[:]); werr != nil {
		return 0, fmt.Errorf("%w: write at offset %d: %v", ErrVLogFsync, offset, werr)
	}
	if serr := ws.vlog.Sync(); serr != nil {
		return 0, fmt.Errorf("%w: sync at offset %d: %v", ErrVLogFsync, offset, serr)
	}
	ws.vlogHead += vLogEntrySize
	return offset, nil
}

// readFromVLog reads the 262212-byte entry at byteOffset and verifies integrity.
//
// Goroutine-safe: uses ReadAt (POSIX pread semantics). Multiple goroutines
// may call readFromVLog concurrently; concurrent AppendChunk calls in the
// writer goroutine are also safe because pread does not move the file position.
//
// Algorithm (ARCH §16 §Audit lookup path, steps 4–5):
//  1. ReadAt vLogEntrySize bytes from byteOffset into a fresh buffer.
//  2. Extract content_hash from buf[vlogOffContentHash : vlogOffContentHash+32].
//  3. Compute SHA-256(buf[vlogOffChunkData : vlogOffChunkData+262144]).
//  4. Compare byte-by-byte — return nil, ErrContentHashMismatch on any mismatch
//     (silent disk corruption; caller must set audit status 0x02, IC §4.2).
//  5. Return a fresh 262144-byte slice copied from the data region.
//
// The returned slice is always freshly allocated so callers may retain it
// after this function returns without aliasing the I/O buffer.
func (ws *wiskeyStore) readFromVLog(byteOffset uint64) ([]byte, error) {
	var buf [vLogEntrySize]byte
	if _, err := ws.vlog.ReadAt(buf[:], int64(byteOffset)); err != nil {
		return nil, fmt.Errorf("%w: ReadAt offset %d: %v", ErrVLogRead, byteOffset, err)
	}

	storedHash := buf[vlogOffContentHash : vlogOffContentHash+32]
	computed := sha256.Sum256(buf[vlogOffChunkData : vlogOffChunkData+262144])
	for i := range computed {
		if computed[i] != storedHash[i] {
			return nil, ErrContentHashMismatch
		}
	}

	data := make([]byte, 262144)
	copy(data, buf[vlogOffChunkData:vlogOffChunkData+262144])
	return data, nil
}
