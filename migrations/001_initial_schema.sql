-- Generated for profile: prod
-- Generated at: 2026-06-13T06:29:08Z
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

-- ── owners ─────────────────────────────────────────────────────────────────────
-- [REF: DM §4.1, DM §8.1]
CREATE TABLE owners (
    -- ── Identity ─────────────────────────────────────────────────────────────
    owner_id            UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    -- UUIDv7 preferred at application layer for time-ordered PKs (ADR-013).

    phone_number        VARCHAR(15)     NOT NULL UNIQUE,
    -- E.164 format (e.g. +919876543210). OTP-verified at registration (FR-001).
    -- UNIQUE: one identity per phone number prevents trivial Sybil registration.

    ed25519_public_key  BYTEA           NOT NULL CHECK (octet_length(ed25519_public_key) = 32),
    -- 32-byte compressed Ed25519 public key (ADR-020). Never the private key.

    -- ── Payment ──────────────────────────────────────────────────────────────
    smart_collect_vpa   VARCHAR(255)    NULL,
    -- Razorpay Smart Collect 2.0 virtual UPI payment address.
    -- NULL until Razorpay completes VPA provisioning (DM §8.1).

    -- ── Timestamps ───────────────────────────────────────────────────────────
    created_at          TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE owners IS 'Registered data owners. One row per verified phone number.';
COMMENT ON COLUMN owners.smart_collect_vpa IS
    'Razorpay UPI VPA for escrow deposits. NULL until provisioned by Razorpay webhook.';

-- ── providers ──────────────────────────────────────────────────────────────────
-- [REF: DM §4.2, DM §8.2–§8.6]
CREATE TABLE providers (
    -- ── Identity ─────────────────────────────────────────────────────────────
    provider_id             UUID            PRIMARY KEY DEFAULT gen_random_uuid(),

    phone_number            VARCHAR(15)     NOT NULL UNIQUE,
    -- OTP-verified at registration. UNIQUE prevents Sybil attacks (ADR-005).

    ed25519_public_key      BYTEA           NOT NULL CHECK (octet_length(ed25519_public_key) = 32),
    -- libp2p peer key. Authenticates every heartbeat and audit receipt (ADR-021).

    -- ── Lifecycle ────────────────────────────────────────────────────────────
    status                  provider_status NOT NULL DEFAULT 'PENDING_ONBOARDING',

    -- ── Hardware declaration ─────────────────────────────────────────────────
    declared_storage_gb     INT             NOT NULL CHECK (declared_storage_gb BETWEEN 10 AND 100000),
    -- Minimum 10 GB, maximum 100 TB. Verified indirectly by vetting audits (ADR-030).

    city                    VARCHAR(100)    NOT NULL,

    region                  VARCHAR(100)    NOT NULL,
    -- Readiness gate: >=3 distinct metro regions required (ADR-029).

    asn                     VARCHAR(32)     NOT NULL,
    -- e.g. 'AS24560' (Airtel); 'SIM-AS1'...'SIM-AS5' in simulation mode.
    -- 20% ASN cap: no single ASN holds >20% of any file's shards (ADR-014).

    -- ── Payment rails ────────────────────────────────────────────────────────
    razorpay_linked_account_id  VARCHAR(255),
    -- NULL until account.created webhook fires. Assignments blocked until set (DM §8.2).

    razorpay_cooling_until  TIMESTAMPTZ,
    -- NULL until account created; set to NOW() + 24h on webhook receipt (DM §8.3).

    -- ── Network addresses (ADR-028) ──────────────────────────────────────────
    last_known_multiaddrs   JSONB           NOT NULL DEFAULT '[]',
    -- Ordered JSON array of libp2p multiaddrs from the most recent heartbeat.

    last_heartbeat_ts       TIMESTAMPTZ,
    -- NULL during PENDING_ONBOARDING before first heartbeat (DM §8.4).

    multiaddr_stale         BOOLEAN         NOT NULL DEFAULT FALSE,
    -- TRUE when 2+ consecutive heartbeats missed; triggers DHT fallback (ADR-028).

    -- ── Performance counters (ADR-006, ADR-014) ──────────────────────────────
    p95_throughput_kbps     FLOAT           NULL,
    -- NULL until vetting accumulates samples; application substitutes pool median.
    -- DEFAULT 0 is WRONG: causes division by zero in audit deadline formula (ADR-014).

    avg_rtt_ms              FLOAT           NULL,
    -- NULL until first sample; application substitutes pool median.
    -- DEFAULT 2000 is WRONG: hard-coded guess diverges as network median shifts.

    var_rtt_ms              FLOAT           NOT NULL DEFAULT 0,
    -- Zero variance is the correct initial assumption.
    -- RTO = avg_rtt_ms + 4 × var_rtt_ms (ADR-006).

    rto_sample_count        INT             NOT NULL DEFAULT 0,
    -- Below 5: scheduler substitutes pool-median RTO (ADR-006).

    first_chunk_assignment_at   TIMESTAMPTZ,
    -- NULL until first chunk assigned by assignment service (DM §8.6).
    -- Vetting duration check: NOW() - first_chunk_assignment_at >= 120 days (FR-026).

    -- ── Vetting counters (ADR-005) ────────────────────────────────────────────
    consecutive_audit_passes    INT         NOT NULL DEFAULT 0,
    -- 80 consecutive passes → VETTING to ACTIVE transition (Jeffrey's prior, ADR-005).

    -- ── Failure clustering (ADR-008, Paper 32) ───────────────────────────────
    accelerated_reaudit     BOOLEAN         NOT NULL DEFAULT FALSE,
    -- TRUE when >1 FAIL in rolling 7-day window (Paper 32, ADR-008).

    -- ── Escrow freeze (ADR-024) ──────────────────────────────────────────────
    frozen                  BOOLEAN         NOT NULL DEFAULT FALSE,

    -- ── Timestamps ───────────────────────────────────────────────────────────
    created_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW(),

    departed_at             TIMESTAMPTZ,
    -- NULL for active providers. Set on departure declaration. Never cleared (DM §8.5).

    -- ── Constraints ──────────────────────────────────────────────────────────
    CONSTRAINT providers_throughput_nonneg  CHECK (p95_throughput_kbps >= 0),
    CONSTRAINT providers_avg_rtt_nonneg     CHECK (avg_rtt_ms >= 0),
    CONSTRAINT providers_var_rtt_nonneg     CHECK (var_rtt_ms >= 0),
    CONSTRAINT providers_passes_nonneg      CHECK (consecutive_audit_passes >= 0),
    CONSTRAINT providers_departed_status
        CHECK (departed_at IS NULL OR status = 'DEPARTED')
);

COMMENT ON TABLE providers IS
    'Storage providers. One row per verified daemon. Never physically deleted (DM §3 Invariant 3).';

-- ── files / pointer_files ──────────────────────────────────────────────────────
-- TODO(4.3.3): files and pointer_files tables from DM §4 (owner_id, size_bytes,
--              pointer_enc_ciphertext, pointer_nonce, file_key_ciphertext).

-- ── chunk_assignments ───────────────────────────────────────────────────────────
-- PROFILE-VARIABLE: shard_index upper bound = TotalShards-1 = 55 for this profile.
-- [REF: DM §9 Profile rule, MVP §5.5, DM §8.23, ADR-031]
-- TODO(4.4.x): full chunk_assignments schema from DM §4 (file_id, provider_id,
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
-- TODO(4.5.x): audit_periods table from DM §5 (EXCLUDE USING gist for
--              non-overlapping tsrange — requires btree_gist extension).
--              audit_receipts table from DM §5 (challenge_nonce BYTEA(33),
--              NOT BYTEA(32) per IC §5.1 INV-5 / CI check-08).

-- ── repair_jobs ─────────────────────────────────────────────────────────────────
-- PROFILE-VARIABLE: available_shard_count range = [16, 56] for this profile.
-- [REF: DM §9 Profile rule, MVP §5.5, DM §8.23, ADR-031]
-- TODO(4.6.x): full repair_jobs schema from DM §6 (file_id, trigger_type,
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
-- TODO(4.7.x): escrow_events and ledger tables from DM §7.
--              INVARIANT: all monetary amounts are INTEGER (int64 paise). No
--              FLOAT, DECIMAL, or NUMERIC types permitted (IC §5.1 INV-4).

-- ── vetting_chunks ─────────────────────────────────────────────────────────────
-- TODO(4.8.x): vetting_chunks table from DM §8 (synthetic chunk lifecycle:
--              generation, assignment, GC delivery, departure cleanup — ADR-030).

-- ── Row-level security policies ────────────────────────────────────────────────
-- TODO(4.9.x): RSPs from IC §6 (per-provider isolation on chunk_assignments;
--              microservice-role full access; client-role file-owner reads).

-- ── Indexes ────────────────────────────────────────────────────────────────────
-- TODO(4.9.x): B-tree and GiST indexes from DM §9 (covering indexes for
--              audit scheduling, repair queue polling, scoring window queries).

-- ── Triggers ───────────────────────────────────────────────────────────────────
-- TODO(4.9.x): updated_at maintenance triggers from DM §9.

-- ── Views ──────────────────────────────────────────────────────────────────────
-- IMPORTANT: mv_provider_scores is NOT here. It is dropped and recreated at
-- microservice startup from profile.ScoreWindow{Short,Medium,Long} values.
-- Hard-coding scoring windows in a migration violates DM §9 Profile rule.
-- [REF: DM §9, MVP §5.5, build.md Phase 4.1 Session 4.1.1]
-- TODO(4.10.x): any other views defined in DM §9.
