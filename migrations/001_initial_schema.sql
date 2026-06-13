-- Generated for profile: prod
-- Generated at: 2026-06-13T03:02:04Z
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
-- All nine types below are profile-invariant (identical values in demo and
-- production) and are declared first in the migration, satisfying the DM §9
-- ordering rule: types precede tables.
-- [REF: DM §4, DM §9]

-- provider_status — lifecycle states for a storage provider.
-- PENDING_ONBOARDING : registered, Razorpay cooling period not yet elapsed
-- VETTING            : first heartbeat received; accumulating audit passes
-- ACTIVE             : 80 consecutive passes achieved; full assignment eligibility
-- DEPARTED           : silent (>=72h) or announced departure; never physically deleted
-- [REF: DM §4.2]
CREATE TYPE provider_status AS ENUM (
    'PENDING_ONBOARDING',
    'VETTING',
    'ACTIVE',
    'DEPARTED'
);

-- file_status — lifecycle states for an uploaded file.
-- [REF: DM §4.3, DM §9 three-value checklist]
CREATE TYPE file_status AS ENUM (
    'ACTIVE',
    'DELETION_PENDING',
    'DELETED'
);

-- assignment_status — lifecycle states for a single shard assignment.
-- [REF: DM §4.5]
CREATE TYPE assignment_status AS ENUM (
    'ACTIVE',           -- provider holds this shard; audit challenges issued daily
    'REPAIRING',        -- shard is being replaced; old holder still being challenged
    'PENDING_DELETION', -- owner deleted file (or ACTIVE transition GC in progress);
                        -- provider notified to GC its vLog; no further challenges issued
    'DELETED'           -- provider confirmed deletion; no further challenge issued
);

-- audit_result_type — terminal outcomes of an audit challenge.
-- PASS / FAIL / TIMEOUT are the three terminal states. The column is nullable
-- (no NOT NULL) to represent the in-flight PENDING state during the two-phase
-- write (ADR-015). Defining this as an ENUM, rather than TEXT with a CHECK, is
-- consistent with all other status columns and rejects invalid values at the
-- wire-protocol level before any constraint fires.
-- [REF: DM §4.7]
CREATE TYPE audit_result_type AS ENUM ('PASS', 'FAIL', 'TIMEOUT');

-- escrow_event_type — provider-side escrow ledger event kinds.
-- [REF: DM §4.8; REVERSAL required per DM §9 checklist]
CREATE TYPE escrow_event_type AS ENUM (
    'DEPOSIT',   -- data owner funds escrow; triggers on Razorpay webhook
    'RELEASE',   -- monthly payment released to provider after multiplier applied
    'SEIZURE',   -- all held earnings seized on silent departure (ADR-024)
    'REVERSAL'   -- correction of a previously recorded DEPOSIT/RELEASE/SEIZURE entry
);

-- owner_escrow_event_type — data-owner-side prepaid balance event kinds.
-- [REF: DM §4.9]
CREATE TYPE owner_escrow_event_type AS ENUM (
    'DEPOSIT',      -- data owner funds escrow via UPI Smart Collect 2.0
    'CHARGE',       -- monthly storage deduction per active file (per-audit-pass credits)
    'WITHDRAWAL',   -- owner withdraws available balance to their bank account
    'REFUND'        -- file deleted early; unused prepaid storage refunded
);

-- repair_trigger_type — events that enqueue a repair job.
-- [REF: DM §4.10]
CREATE TYPE repair_trigger_type AS ENUM (
    'SILENT_DEPARTURE',     -- provider absent >=72h; fragments definitely lost
    'ANNOUNCED_DEPARTURE',  -- provider explicitly notified of departure
    'THRESHOLD_WARNING',    -- fragment count dropped to s+r0=24 (lazy threshold)
    'EMERGENCY_FLOOR'       -- fragment count at s=16 (reconstruction floor); immediate
);

-- repair_priority — drain order for the repair job queue.
-- ENUM order = priority order for ORDER BY ASC
-- [REF: DM §4.10, ADR-004]
CREATE TYPE repair_priority AS ENUM (
    'EMERGENCY',            -- EMERGENCY_FLOOR: s=16, immediate, front of queue
    'PERMANENT_DEPARTURE',  -- SILENT or ANNOUNCED departures drain first (ADR-004)
    'PRE_WARNING'           -- THRESHOLD_WARNING jobs wait behind the above
);

-- repair_job_status — lifecycle states for a queued repair job.
-- [REF: DM §4.10]
CREATE TYPE repair_job_status AS ENUM (
    'QUEUED',
    'IN_PROGRESS',
    'COMPLETED',
    'FAILED'
);

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
