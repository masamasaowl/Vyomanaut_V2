// Package storage is declared in doc.go.
// This file defines the five sentinel errors exported by the storage package.
// Callers must compare using errors.Is; never construct these values inline.
//
// [REF: IC §5.3, ADR-023, ARCH §16, build.md Phase 5.1 Session 5.1.1]

package storage

import "errors"

var (
	// ErrChunkNotFound is returned by LookupChunk when the chunk is absent from
	// the RocksDB index (or eliminated by the Bloom filter before the index is
	// consulted). No vLog disk I/O is incurred on this path — the Bloom filter
	// exits fast in memory.
	//
	// This is the expected response for an audit challenge on a chunk that is not
	// assigned to this provider. Callers must return audit status 0x01 (FAIL_NOT_FOUND).
	//
	// [REF: IC §5.3, ARCH §16 §Audit lookup path, ARCH §27.1]
	ErrChunkNotFound = errors.New("storage: chunk not found")

	// ErrContentHashMismatch is returned by LookupChunk when the chunk data is
	// present in the vLog but SHA-256(chunk_data) does not equal the stored
	// content_hash. This indicates silent disk corruption.
	//
	// Callers must return audit_result = FAIL with status byte 0x02
	// (FAIL_CORRUPTION) per IC §4.2. Repair is triggered by the microservice on
	// receipt of a FAIL with this status.
	//
	// [REF: IC §5.3, IC §4.2, ARCH §16]
	ErrContentHashMismatch = errors.New("storage: content hash mismatch (disk corruption)")

	// ErrVLogFsync is returned by AppendChunk when the fsync() call fails after
	// the vLog write. The vLog may be in an inconsistent state.
	//
	// The daemon MUST halt and restart immediately. RecoverFromCrash will perform
	// a tail-scan on the next startup and re-insert any index entries for chunks
	// that were written to the vLog but not yet flushed to RocksDB.
	//
	// [REF: IC §5.3, ARCH §16 §Crash recovery, ADR-023]
	ErrVLogFsync = errors.New("storage: vLog fsync failed")

	// ErrVLogRead is returned by LookupChunk or RecoverFromCrash on an I/O error
	// reading the vLog. Treat as fatal — the daemon must halt and alert.
	//
	// [REF: IC §5.3, ARCH §16]
	ErrVLogRead = errors.New("storage: vLog read failed")

	// ErrRocksDBInsert is returned by AppendChunk when the RocksDB index INSERT
	// fails after a successful vLog write. The vLog entry is durable; the index
	// entry is missing.
	//
	// RecoverFromCrash will re-insert the missing index entry on the next daemon
	// startup by scanning forward from the last known vLog head pointer.
	//
	// [REF: IC §5.3, ARCH §16 §Crash recovery, ADR-023]
	ErrRocksDBInsert = errors.New("storage: RocksDB index insert failed")
)
