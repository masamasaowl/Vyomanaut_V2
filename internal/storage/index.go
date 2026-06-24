// Package storage is declared in doc.go.
// This file implements rocksDBIndex, the RocksDB wrapper used as the WiscKey
// chunk index. It owns two column families:
//
//	"default"  — data index: chunk_id(32) → vlog_offset(uint64)+chunk_size(uint32) = 12 bytes
//	"dht-keys" — DHT key cache: chunk_id(32) → dht_key(32) (IC §12.2, ARCH §13)
//
// Configuration:
//   - Bloom filter: 10 bits/key, ~1% false-positive rate (ARCH §16, ARCH §27.1)
//   - Block cache: 64 MB LRU so the index stays warm after startup (ARCH §16)
//
// IMPORT CONSTRAINT (IC §9): no other internal/ package may be imported here.
// DHT key derivation belongs in internal/crypto; this package only stores the
// pre-computed value supplied by the caller.
//
// [REF: IC §5.3, IC §12.2, ARCH §13, ARCH §16, ARCH §27.1, ADR-023,
//
//	build.md Phase 5.1 Session 5.1.3]
package storage

import (
	"encoding/binary"
	"fmt"

	"github.com/linxGnu/grocksdb"
)

// rocksDBIndex wraps the RocksDB instance used as the WiscKey chunk index.
//
// Column families:
//
//	"default"  — chunk_id (32-byte key) → vlog_offset(uint64) + chunk_size(uint32) = 12-byte value
//	             Total on-disk entry ≈ 44 bytes (ARCH §27.1: key=32, value=12).
//	"dht-keys" — chunk_id (32-byte key) → dht_key (32-byte value)
//	             Pre-computed DHT lookup keys stored at upload time (IC §12.2, ARCH §13).
//	             The heartbeat goroutine reads these for DHT republication without
//	             re-deriving from file_owner_key.
type rocksDBIndex struct {
	db       *grocksdb.DB
	cfData   *grocksdb.ColumnFamilyHandle // "default" CF — data index
	cfDHT    *grocksdb.ColumnFamilyHandle // "dht-keys" CF — cached DHT keys
	writeOps *grocksdb.WriteOptions
	readOps  *grocksdb.ReadOptions
}

// openRocksDBIndex opens (or creates) the RocksDB index at dbPath with two
// column families, a Bloom filter, and a 64 MB LRU block cache.
//
// Bloom filter: NewBloomFilter(10) — 10 bits/key ≈ 1% false-positive rate.
// An audit challenge on a chunk absent from this provider hits only the Bloom
// filter in memory; no disk I/O occurs (ARCH §16, ARCH §27.1).
//
// Block cache: 64 MB LRU. At 50 GB declared storage the full index fits in
// ~8.8 MB — the block cache keeps it warm so index lookups during audit
// challenges require no disk I/O (ARCH §16, ARCH §27.1).
func openRocksDBIndex(dbPath string) (*rocksDBIndex, error) {
	// Column-family options: shared Bloom filter + block cache for both CFs.
	bbto := grocksdb.NewDefaultBlockBasedTableOptions()
	bbto.SetFilterPolicy(grocksdb.NewBloomFilter(10)) // 10 bits/key ≈ 1% FP
	bbto.SetBlockCache(grocksdb.NewLRUCache(64 * 1024 * 1024))

	cfOpts := grocksdb.NewDefaultOptions()
	cfOpts.SetBlockBasedTableFactory(bbto)
	cfOpts.SetCreateIfMissing(true)

	// DB-level options: create the DB and any missing CFs if this is a first open.
	dbOpts := grocksdb.NewDefaultOptions()
	dbOpts.SetCreateIfMissing(true)
	dbOpts.SetCreateIfMissingColumnFamilies(true) // exact method name from options.go:2163

	cfNames := []string{"default", "dht-keys"}
	cfOptsList := []*grocksdb.Options{cfOpts, cfOpts}

	db, handles, err := grocksdb.OpenDbColumnFamilies(dbOpts, dbPath, cfNames, cfOptsList)
	if err != nil {
		return nil, fmt.Errorf("storage: openRocksDBIndex: %w", err)
	}
	return &rocksDBIndex{
		db:       db,
		cfData:   handles[0],
		cfDHT:    handles[1],
		writeOps: grocksdb.NewDefaultWriteOptions(),
		readOps:  grocksdb.NewDefaultReadOptions(),
	}, nil
}

// put inserts or updates the data index entry for chunkID in the "default" CF.
// Value layout: vlog_offset(uint64 big-endian, 8 bytes) + chunk_size(uint32 big-endian, 4 bytes)
// = indexValueSize (12) bytes total (ARCH §27.1).
func (idx *rocksDBIndex) put(chunkID [32]byte, vlogOffset uint64, chunkSize uint32) error {
	var val [indexValueSize]byte // indexValueSize = 12 (store.go)
	binary.BigEndian.PutUint64(val[0:8], vlogOffset)
	binary.BigEndian.PutUint32(val[8:12], chunkSize)
	if err := idx.db.PutCF(idx.writeOps, idx.cfData, chunkID[:], val[:]); err != nil {
		return fmt.Errorf("%w: chunkID %x: %v", ErrRocksDBInsert, chunkID, err)
	}
	return nil
}

// get retrieves vlog_offset and chunk_size for chunkID from the "default" CF.
// Returns ErrChunkNotFound when the key is absent.
//
// On the happy path, the result comes from the in-memory block cache — no
// disk I/O. On the first read after startup the SST file is read once, then
// cached. The Bloom filter screens absent keys before the block cache is
// consulted (ARCH §16 §Audit lookup path, step 2–3).
func (idx *rocksDBIndex) get(chunkID [32]byte) (vlogOffset uint64, chunkSize uint32, err error) {
	sl, err := idx.db.GetCF(idx.readOps, idx.cfData, chunkID[:])
	if err != nil {
		return 0, 0, fmt.Errorf("%w: RocksDB GetCF: %v", ErrVLogRead, err)
	}
	defer sl.Free()
	if !sl.Exists() {
		return 0, 0, ErrChunkNotFound
	}
	d := sl.Data()
	return binary.BigEndian.Uint64(d[0:8]), binary.BigEndian.Uint32(d[8:12]), nil
}

// del removes the data index entry for chunkID from the "default" CF.
// The vLog entry remains on disk until RunGC reclaims it.
func (idx *rocksDBIndex) del(chunkID [32]byte) error {
	return idx.db.DeleteCF(idx.writeOps, idx.cfData, chunkID[:])
}

// putDHTKey stores the pre-computed DHT lookup key for a chunk in the
// "dht-keys" CF. Called once per chunk at upload time by the upload
// orchestrator (Session 15.2.1).
//
// IMPORTANT: the dhtKey must be derived by internal/crypto.DeriveDHTKey before
// calling this function. This package never computes DHT keys (IC §9, IC §12.2).
func (idx *rocksDBIndex) putDHTKey(chunkID [32]byte, dhtKey [32]byte) error {
	return idx.db.PutCF(idx.writeOps, idx.cfDHT, chunkID[:], dhtKey[:])
}

// dhtKeyFor retrieves the cached DHT lookup key for chunkID from the "dht-keys" CF.
// Returns ok=false when no DHT key was stored for this chunk (e.g. the chunk
// was added before DHT integration was enabled, or the DB was freshly created).
//
// The heartbeat goroutine calls this during DHT republication (IC §12.2, ARCH §13).
// It must NEVER recompute dhtKey from file_owner_key — that derivation belongs
// exclusively in internal/crypto.
func (idx *rocksDBIndex) dhtKeyFor(chunkID [32]byte) (dhtKey [32]byte, ok bool) {
	sl, err := idx.db.GetCF(idx.readOps, idx.cfDHT, chunkID[:])
	if err != nil || !sl.Exists() {
		if sl != nil {
			sl.Free()
		}
		return dhtKey, false
	}
	defer sl.Free()
	copy(dhtKey[:], sl.Data())
	return dhtKey, true
}

// allChunkIDs returns every chunk_id present in the data index ("default" CF).
//
// Used by the DHT republication loop in the heartbeat goroutine to iterate
// over all locally stored chunks and republish their DHT entries (IC §12.2,
// Session 6.3.1).
//
// The iterator snapshot is consistent; concurrent writes do not affect it.
func (idx *rocksDBIndex) allChunkIDs() [][32]byte {
	it := idx.db.NewIteratorCF(idx.readOps, idx.cfData)
	defer it.Close()
	var ids [][32]byte
	for it.SeekToFirst(); it.Valid(); it.Next() {
		k := it.Key()
		var id [32]byte
		copy(id[:], k.Data())
		k.Free()
		ids = append(ids, id)
	}
	return ids
}

// close releases all RocksDB handles in the correct order:
// CF handles before WriteOptions/ReadOptions before the DB itself.
func (idx *rocksDBIndex) close() {
	idx.cfData.Destroy()
	idx.cfDHT.Destroy()
	idx.writeOps.Destroy()
	idx.readOps.Destroy()
	idx.db.Close()
}
