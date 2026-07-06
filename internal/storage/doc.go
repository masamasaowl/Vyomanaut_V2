/*
Package storage implements the WiscKey key-value separated chunk storage engine. The vLog (value log) is an append-only file; the index is RocksDB.

CONCURRENCY CONTRACT: AppendChunk is NOT goroutine-safe. It must only be called from the single designated writer goroutine.

Versions:
rocksdb_version="10.10.1"
grocksDB = v1.10.8

All other exported functions are goroutine-safe (read-only paths).
Ref: ADR-023, NFR-023
*/
package storage
