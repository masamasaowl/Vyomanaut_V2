-- Generated for profile: prod
-- Generated at: 2026-06-12T15:53:25Z
-- ShardSize: 262144 (compile-time constant; NOT profile-variable)
-- DataShards: 16
-- TotalShards: 56

-- ── Extensions ─────────────────────────────────────────────────────────────────
-- btree_gist: required by audit_periods EXCLUDE USING gist (tsrange WITH &&).
-- pgcrypto:   provides gen_random_uuid() for UUID primary-key column defaults.
-- [REF: DM §9, deployments/dev/init-db.sql, CI check-07]
CREATE EXTENSION IF NOT EXISTS btree_gist;
CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- ── ENUMs ──────────────────────────────────────────────────────────────────────
-- TODO(4.2.1): provider_status, shard_state, repair_priority, audit_outcome,
--              escrow_event_type, vetting_state ENUMs from DM §2.

-- ── providers ──────────────────────────────────────────────────────────────────
-- TODO(4.2.2): providers table from DM §3 (declared_storage_gb, asn, region,
--              heartbeat_ts, departure_ts, status, ed25519_pubkey).

-- ── files / pointer_files ──────────────────────────────────────────────────────
-- TODO(4.3.1): files and pointer_files tables from DM §4 (owner_id, size_bytes,
--              pointer_enc_ciphertext, pointer_nonce, file_key_ciphertext).

-- ── chunk_assignments ───────────────────────────────────────────────────────────
-- PROFILE-VARIABLE: shard_index upper bound = TotalShards-1 = 55 for this profile.
-- [REF: DM §9 Profile rule, MVP §5.5, DM §8.23, ADR-031]
-- TODO(4.3.2): full chunk_assignments schema from DM §4 (file_id, provider_id,
--              shard_state, assigned_at, confirmed_at, vlog_offset).
CREATE TABLE IF NOT EXISTS chunk_assignments (
    id          UUID    NOT NULL DEFAULT gen_random_uuid(),
    shard_index INTEGER,
    CONSTRAINT chunk_assignments_pkey
        PRIMARY KEY (id),
    CONSTRAINT chunk_assignments_shard_index_range
        CHECK (shard_index BETWEEN 0 AND 55 OR shard_index IS NULL)
);

-- ── audit_periods / audit_receipts ─────────────────────────────────────────────
-- TODO(4.4.1): audit_periods table from DM §5 (EXCLUDE USING gist for
--              non-overlapping tsrange — requires btree_gist extension).
--              audit_receipts table from DM §5 (challenge_nonce BYTEA(33),
--              NOT BYTEA(32) per IC §5.1 INV-5 / CI check-08).

-- ── repair_jobs ─────────────────────────────────────────────────────────────────
-- PROFILE-VARIABLE: available_shard_count range = [16, 56] for this profile.
-- [REF: DM §9 Profile rule, MVP §5.5, DM §8.23, ADR-031]
-- TODO(4.5.1): full repair_jobs schema from DM §6 (file_id, trigger_type,
--              priority, missing_shards, state, created_at, promoted_at).
CREATE TABLE IF NOT EXISTS repair_jobs (
    id                    UUID    NOT NULL DEFAULT gen_random_uuid(),
    available_shard_count INTEGER NOT NULL DEFAULT 0,
    CONSTRAINT repair_jobs_pkey
        PRIMARY KEY (id),
    CONSTRAINT repair_jobs_shard_count_range
        CHECK (available_shard_count BETWEEN 16 AND 56)
);

-- ── escrow / payment ledger ─────────────────────────────────────────────────────
-- TODO(4.6.1): escrow_events and ledger tables from DM §7.
--              INVARIANT: all monetary amounts are INTEGER (int64 paise). No
--              FLOAT, DECIMAL, or NUMERIC types permitted (IC §5.1 INV-4).

-- ── vetting_chunks ─────────────────────────────────────────────────────────────
-- TODO(4.7.1): vetting_chunks table from DM §8 (synthetic chunk lifecycle:
--              generation, assignment, GC delivery, departure cleanup — ADR-030).

-- ── Row-level security policies ────────────────────────────────────────────────
-- TODO(4.7.2): RSPs from IC §6 (per-provider isolation on chunk_assignments;
--              microservice-role full access; client-role file-owner reads).

-- ── Indexes ────────────────────────────────────────────────────────────────────
-- TODO(4.7.3): B-tree and GiST indexes from DM §9 (covering indexes for
--              audit scheduling, repair queue polling, scoring window queries).

-- ── Triggers ───────────────────────────────────────────────────────────────────
-- TODO(4.7.4): updated_at maintenance triggers from DM §9.

-- ── Views ──────────────────────────────────────────────────────────────────────
-- IMPORTANT: mv_provider_scores is NOT here. It is dropped and recreated at
-- microservice startup from profile.ScoreWindow{Short,Medium,Long} values.
-- Hard-coding scoring windows in a migration violates DM §9 Profile rule.
-- [REF: DM §9, MVP §5.5, build.md Phase 4.1 Session 4.1.1]
-- TODO(4.7.5): any other views defined in DM §9.
