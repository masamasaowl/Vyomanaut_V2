-- Generated for profile: prod
-- Generated at: 2026-06-15T06:28:11Z
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

-- ── files ──────────────────────────────────────────────────────────────────────
-- [REF: DM §4.3, REQ §4.4 FR-019]
CREATE TABLE files (
    -- ── Identity ─────────────────────────────────────────────────────────────
    file_id             UUID            PRIMARY KEY DEFAULT gen_random_uuid(),
    -- UUIDv7 at application layer (ADR-013). Pseudonymous: appears in audit
    -- receipts but cannot be linked to plaintext identity without master secret.

    owner_id            UUID            NOT NULL REFERENCES owners(owner_id),

    -- ── Pointer file storage (ADR-020) ───────────────────────────────────────
    pointer_ciphertext  BYTEA           NOT NULL,
    -- AEAD_CHACHA20_POLY1305 ciphertext of the pointer file struct.
    -- Microservice stores blindly; cannot decrypt (ADR-020, zero-knowledge).

    pointer_nonce       BYTEA           NOT NULL CHECK (octet_length(pointer_nonce) = 12),
    -- 96-bit (12-byte) monotone counter nonce. RFC 8439 §2.3.

    pointer_tag         BYTEA           NOT NULL CHECK (octet_length(pointer_tag) = 16),
    -- 16-byte Poly1305 authentication tag. Constant-time verification (NFR-019).

    -- ── File name (nullable) ─────────────────────────────────────────────────
    display_name_ciphertext  BYTEA      NULL,
    -- AEAD_CHACHA20_POLY1305 ciphertext of the user-provided file name.
    -- NULL if owner provides no label (CLI path). Non-null for UI file list (FR-019).
    -- Microservice stores blindly; cannot read the filename (ADR-020).

    display_name_nonce       BYTEA      NULL CHECK (octet_length(display_name_nonce) = 12 OR display_name_nonce IS NULL),

    display_name_tag         BYTEA      NULL CHECK (octet_length(display_name_tag) = 16 OR display_name_tag IS NULL),

    -- ── File metadata ────────────────────────────────────────────────────────
    original_size_bytes BIGINT          NOT NULL CHECK (original_size_bytes > 0),
    -- Plaintext size before padding. Required to strip AONT padding after RS
    -- decode and AONT decryption on retrieval (FR-008).

    status              file_status     NOT NULL DEFAULT 'ACTIVE',

    schema_version      SMALLINT        NOT NULL DEFAULT 1,
    -- Pointer file schema version. Forward-compatible migration for V3.

    -- ── Timestamps ───────────────────────────────────────────────────────────
    uploaded_at         TIMESTAMPTZ     NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE files IS
    'One row per uploaded file. The microservice holds only encrypted pointer '
    'ciphertext and cannot read the file contents or decryption key.';
COMMENT ON COLUMN files.pointer_ciphertext IS
    'Blind store. Key lives in the owner''s head. Service cannot decrypt (ADR-020).';
COMMENT ON COLUMN files.original_size_bytes IS
    'Strip AONT padding to this length after decoding. Padding is added for '
    'files smaller than one full segment (4 MB = 16 × 256 KB).';

-- ── segments ───────────────────────────────────────────────────────────────────
-- [REF: DM §4.4]
CREATE TABLE segments (
    segment_id      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    file_id         UUID        NOT NULL REFERENCES files(file_id),

    segment_index   INT         NOT NULL CHECK (segment_index >= 0),
    -- 0-based. Segments concatenated in this order on retrieval.

    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    CONSTRAINT segments_unique_index UNIQUE (file_id, segment_index)
    -- A file cannot have two segments at the same position.
);

COMMENT ON TABLE segments IS
    'One row per 14 MB slice of a file. Each segment produces exactly TotalShards chunks '
    'via AONT-RS. Segments are independent: losing one does not affect the others.';

-- ── chunk_assignments ───────────────────────────────────────────────────────────
-- PROFILE-VARIABLE: shard_index upper bound = TotalShards-1 = 55 for this profile.
-- [REF: DM §4.5, DM §3 Invariant 6, DM §8.21, DM §8.22, ADR-030, ADR-031]
CREATE TABLE chunk_assignments (
    assignment_id    UUID                PRIMARY KEY DEFAULT gen_random_uuid(),

    chunk_id         BYTEA               NOT NULL CHECK (octet_length(chunk_id) = 32),
    -- SHA-256(shard_data): content address of this 256 KB shard.
    -- For vetting chunks: SHA-256 of a random 256 KB block (ADR-030).

    is_vetting_chunk BOOLEAN             NOT NULL DEFAULT FALSE,
    -- TRUE for synthetic chunks assigned during provider vetting (ADR-030).
    -- Repair scheduler MUST NOT create repair_jobs for is_vetting_chunk = TRUE.

    segment_id       UUID                REFERENCES segments(segment_id),
    -- NULL when is_vetting_chunk = TRUE (no real file association — DM §8.21).

    shard_index      SMALLINT            CHECK (shard_index BETWEEN 0 AND 55 OR shard_index IS NULL),
    -- NULL when is_vetting_chunk = TRUE (no RS slot — DM §8.22).
    -- Upper bound is profile-variable: TotalShards-1 (ADR-031).

    provider_id      UUID                NOT NULL REFERENCES providers(provider_id),

    status           assignment_status   NOT NULL DEFAULT 'ACTIVE',

    created_at       TIMESTAMPTZ         NOT NULL DEFAULT NOW(),

    deleted_at       TIMESTAMPTZ,
    -- NULL for all non-DELETED assignments.

    -- ── Constraints ──────────────────────────────────────────────────────────
    CONSTRAINT chunk_assignments_segment_and_shard_null_iff_vetting CHECK (
        (is_vetting_chunk = FALSE AND segment_id IS NOT NULL AND shard_index IS NOT NULL)
        OR
        (is_vetting_chunk = TRUE  AND segment_id IS NULL    AND shard_index IS NULL)
    ),
    -- Invariant 6: real chunks always reference a segment and shard;
    -- synthetic chunks never do (ADR-030, DM §3 Invariant 6).

    CONSTRAINT chunk_assignments_one_per_provider_per_chunk
        UNIQUE (chunk_id, provider_id)
);

-- Partial unique index: one active assignment per shard slot per segment (real chunks only).
-- Synthetic chunks excluded (no shard_index, no RS constraint applies).
-- MUST be standalone CREATE UNIQUE INDEX, NOT an inline constraint (DM §9).
CREATE UNIQUE INDEX idx_chunk_assignments_one_active_per_shard
    ON chunk_assignments (segment_id, shard_index)
    WHERE is_vetting_chunk = FALSE
      AND status IN ('ACTIVE', 'REPAIRING');

-- Read view: challenge scheduler sees only ACTIVE assignments.
CREATE VIEW active_chunk_assignments AS
SELECT *
FROM chunk_assignments
WHERE status = 'ACTIVE';

COMMENT ON TABLE chunk_assignments IS
    'Routing table: which provider holds which shard of which segment. '
    '20% ASN cap enforced at INSERT time by the assignment service (ADR-014). '
    'Physical deletion not performed; historical data preserved for audit reconciliation.';
COMMENT ON COLUMN chunk_assignments.chunk_id IS
    'SHA-256(shard_data). RocksDB lookup key on the provider daemon (ADR-023).';
COMMENT ON COLUMN chunk_assignments.is_vetting_chunk IS
    'TRUE for synthetic vetting chunks (ADR-030). Repair scheduler must not enqueue '
    'repair jobs for these rows. Provider daemon cannot distinguish synthetic from real.';
COMMENT ON COLUMN chunk_assignments.segment_id IS
    'NULL for synthetic vetting chunks (is_vetting_chunk = TRUE). '
    'Real shards enforced non-null by CHECK constraint (DM §8.21).';
COMMENT ON COLUMN chunk_assignments.shard_index IS
    'NULL for synthetic vetting chunks (no RS shard slot assigned — DM §8.22). '
    'Real shards: 0 to TotalShards-1; 0..DataShards-1 are systematic, rest parity.';

-- ── audit_periods ──────────────────────────────────────────────────────────────
-- PREREQUISITE: CREATE EXTENSION IF NOT EXISTS btree_gist;
-- (already installed above; required by audit_periods_no_overlap EXCLUDE constraint)
-- [REF: DM §4.6]
CREATE TABLE audit_periods (
    id              UUID            PRIMARY KEY DEFAULT gen_random_uuid(),

    provider_id     UUID            NOT NULL REFERENCES providers(provider_id),

    period_start    TIMESTAMPTZ     NOT NULL,
    period_end      TIMESTAMPTZ     NOT NULL,
    -- Inclusive start, exclusive end. One row per calendar month per provider.

    -- ── Running tallies (denormalised from audit_receipts) ────────────────────
    audit_passes    INT             NOT NULL DEFAULT 0 CHECK (audit_passes >= 0),
    audit_fails     INT             NOT NULL DEFAULT 0 CHECK (audit_fails >= 0),
    audit_timeouts  INT             NOT NULL DEFAULT 0 CHECK (audit_timeouts >= 0),
    -- Materialised tallies updated asynchronously after each receipt is countersigned.

    release_computed BOOLEAN        NOT NULL DEFAULT FALSE,
    -- Set TRUE once the monthly release multiplier has been computed (ADR-024).

    created_at      TIMESTAMPTZ     NOT NULL DEFAULT NOW(),

    CONSTRAINT audit_periods_no_overlap
        -- PREREQUISITE: CREATE EXTENSION IF NOT EXISTS btree_gist;
        EXCLUDE USING gist (
            provider_id WITH =,
            tstzrange(period_start, period_end, '[)') WITH &&
        ),
    -- Two audit periods for the same provider must not overlap.
    -- Requires btree_gist. Prevents double-counting at month boundaries (ADR-016).

    CONSTRAINT audit_periods_start_before_end
        CHECK (period_start < period_end)
);

COMMENT ON TABLE audit_periods IS
    'One row per calendar month per provider. Denormalised tally for scoring '
    'and release computation. Source of truth for the escrow release multiplier.';

-- ── audit_receipts ─────────────────────────────────────────────────────────────
-- [REF: DM §4.7, DM §3 Invariants 1 and 5, DM §8.9–§8.15, DM §8.20]
-- INSERT only (Invariant 1). The only UPDATE promotes PENDING → final state.
-- No DELETE ever.
CREATE TABLE audit_receipts (
    -- ── Primary key ──────────────────────────────────────────────────────────
    receipt_id              UUID            PRIMARY KEY DEFAULT gen_random_uuid(),

    schema_version          SMALLINT        NOT NULL DEFAULT 1,

    -- ── What was challenged ──────────────────────────────────────────────────
    chunk_id                BYTEA           NOT NULL CHECK (octet_length(chunk_id) = 32),

    file_id                 UUID            REFERENCES files(file_id),
    -- NULL for synthetic vetting chunk audits (DM §8.20, ADR-030).
    -- Non-null for all real shard audits.

    provider_id             UUID            NOT NULL REFERENCES providers(provider_id),

    -- ── Challenge parameters (ADR-017, ADR-027) ──────────────────────────────
    challenge_nonce         BYTEA           NOT NULL CHECK (octet_length(challenge_nonce) = 33),
    -- MUST BE 33 BYTES, NOT 32. 1-byte version || HMAC-SHA256(server_secret_vN,
    -- chunk_id || server_ts). Version byte enables cross-replica validation
    -- after failover (ADR-027, DM §3 Invariant 5, CI check-08).

    server_challenge_ts     TIMESTAMPTZ     NOT NULL,

    -- ── Provider response ────────────────────────────────────────────────────
    response_hash           BYTEA           CHECK (octet_length(response_hash) = 32
                                                OR response_hash IS NULL),
    -- NULL for TIMEOUT (no response) or PENDING (in-flight). See DM §8.9.

    response_latency_ms     INT             CHECK (response_latency_ms >= 0
                                                OR response_latency_ms IS NULL),
    -- NULL for TIMEOUT or PENDING. See DM §8.10.

    -- ── Audit result (two-phase write, ADR-015) ──────────────────────────────
    audit_result            audit_result_type,
    -- NULL = PENDING (in-flight, Phase 1 complete; Phase 2 not yet executed).
    -- PASS / FAIL / TIMEOUT = final result set in Phase 2.
    -- NO DEFAULT. NULL is the intended initial state. (DM §9 checklist)

    address_was_stale       BOOLEAN         NOT NULL DEFAULT FALSE,
    -- TRUE if challenge dispatched via DHT fallback (multiaddr_stale = TRUE).
    -- TIMEOUTs with this flag set do NOT reset consecutive_audit_passes (ADR-028).

    -- ── Signatures (dual Ed25519, ADR-017) ───────────────────────────────────
    provider_sig            BYTEA           CHECK (octet_length(provider_sig) = 64
                                                OR provider_sig IS NULL),
    -- NULL for TIMEOUT or PENDING. See DM §8.12.

    service_sig             BYTEA           CHECK (octet_length(service_sig) = 64
                                                OR service_sig IS NULL),
    -- NULL during PENDING. Non-null for TIMEOUT rows (microservice signs TIMEOUT).
    -- See DM §8.13.

    service_countersign_ts  TIMESTAMPTZ,
    -- NULL during PENDING. Set in Phase 2 alongside service_sig. See DM §8.14.

    -- ── Adversarial detection (ADR-014) ─────────────────────────────────────
    jit_flag                BOOLEAN         NOT NULL DEFAULT FALSE,
    -- TRUE when response_latency_ms is anomalously fast (JIT retrieval, ADR-014).

    -- ── Garbage collection (ADR-015) ────────────────────────────────────────
    abandoned_at            TIMESTAMPTZ,
    -- Set by GC on PENDING rows older than 48 hours. See DM §8.15.

    -- ── Constraints ──────────────────────────────────────────────────────────
    CONSTRAINT audit_receipts_nonce_unique
        UNIQUE (challenge_nonce),
    -- Prevents replay: a provider cannot re-submit a response to an
    -- already-recorded challenge (ADR-015).

    CONSTRAINT audit_receipts_response_consistency CHECK (
        (audit_result IN ('PASS', 'FAIL') AND response_hash IS NOT NULL AND provider_sig IS NOT NULL)
        OR
        (audit_result = 'TIMEOUT' AND response_hash IS NULL AND provider_sig IS NULL)
        OR
        (audit_result IS NULL)
    ),

    CONSTRAINT audit_receipts_service_sig_consistency CHECK (
        (service_sig IS NULL) = (service_countersign_ts IS NULL)
    )
    -- No FK to chunk_assignments: chunk_assignments may be soft-deleted while
    -- audit_receipts must remain permanently (Invariant 1).
);

-- Nightly data integrity check — must return 0:
-- SELECT COUNT(*) FROM audit_receipts ar
--   JOIN chunk_assignments ca ON ca.chunk_id = ar.chunk_id
--     AND ca.provider_id = ar.provider_id
--   WHERE ar.file_id IS NULL AND ca.is_vetting_chunk = FALSE;

COMMENT ON TABLE audit_receipts IS
    'Immutable audit log. Every storage proof event: PASS, FAIL, TIMEOUT, or '
    'in-flight PENDING. INSERT only — the only permitted UPDATE promotes a '
    'PENDING row to its final state. No DELETE ever. (ADR-015, NFR-021)';
COMMENT ON COLUMN audit_receipts.challenge_nonce IS
    'BYTEA(33): 1-byte version || 32-byte HMAC. NOT BYTEA(32). '
    'Version byte enables cross-replica validation after failover (ADR-027).';
COMMENT ON COLUMN audit_receipts.audit_result IS
    'NULL = PENDING (in-flight, Phase 1 complete). '
    'PASS/FAIL/TIMEOUT = final state set in Phase 2. '
    'NULL is a meaningful state, not a missing value.';

-- ── escrow_events ──────────────────────────────────────────────────────────────
-- [REF: DM §4.8, DM §3 Invariants 2 and 4, DM §8.16]
-- INSERT only (Invariant 2). No UPDATE. No DELETE.
CREATE TABLE escrow_events (
    event_id            UUID                PRIMARY KEY DEFAULT gen_random_uuid(),

    provider_id         UUID                NOT NULL REFERENCES providers(provider_id),

    event_type          escrow_event_type   NOT NULL,
    -- Includes REVERSAL (DM §9 checklist, DM §7 mv_provider_escrow_balance).

    amount_paise        BIGINT              NOT NULL CHECK (amount_paise > 0),
    -- BIGINT ONLY. No FLOAT, NUMERIC, DECIMAL anywhere in the payment path.
    -- Sign implied by event_type: DEPOSIT/REVERSAL adds; RELEASE/SEIZURE subtracts.
    -- RS1 = 100 paise (ADR-016, Invariant 4, NFR-046).

    audit_period_id     UUID                REFERENCES audit_periods(id),
    -- NULL for DEPOSIT (triggered by owner UPI payment) and SEIZURE
    -- (full balance seized at departure). Non-null for RELEASE. See DM §8.16.

    idempotency_key     VARCHAR(64)         NOT NULL UNIQUE,
    -- Prevents double-payment. Passed to Razorpay as X-Payout-Idempotency.
    -- RELEASE:  SHA-256(provider_id || audit_period) as 64 hex chars.
    -- REVERSAL: SHA-256('reversal' || original_idempotency_key).

    created_at          TIMESTAMPTZ         NOT NULL DEFAULT NOW()
);

COMMENT ON TABLE escrow_events IS
    'Append-only escrow ledger. Balance = SUM(DEPOSIT) - SUM(RELEASE + SEIZURE + REVERSAL) '
    'per provider_id. No UPDATE. No DELETE. All amounts in integer paise (ADR-016, Invariant 2).';
COMMENT ON COLUMN escrow_events.amount_paise IS
    'Integer paise ONLY. BIGINT. No FLOAT. RS1 = 100 paise (NFR-046).';

-- ── owner_escrow_events ─────────────────────────────────────────────────────────
-- [REF: DM §4.9, FR-014, FR-021, FR-059]
-- Required for: FR-014 (balance check before upload), FR-021 (balance view),
-- FR-059 (withdrawal). INSERT only. No UPDATE. No DELETE.
CREATE TABLE owner_escrow_events (
    event_id            UUID                        PRIMARY KEY DEFAULT gen_random_uuid(),

    owner_id            UUID                        NOT NULL REFERENCES owners(owner_id),

    event_type          owner_escrow_event_type     NOT NULL,

    amount_paise        BIGINT                      NOT NULL CHECK (amount_paise > 0),
    -- BIGINT ONLY. No FLOAT, NUMERIC, DECIMAL. RS1 = 100 paise (Invariant 4).

    file_id             UUID                        REFERENCES files(file_id),
    -- Non-null for CHARGE and REFUND (links to the specific file).
    -- NULL for DEPOSIT and WITHDRAWAL.

    idempotency_key     VARCHAR(64)                 NOT NULL UNIQUE,
    -- SHA-256(owner_id || razorpay_webhook_id) for DEPOSIT.
    -- SHA-256(owner_id || file_id || billing_period) for CHARGE.

    created_at          TIMESTAMPTZ                 NOT NULL DEFAULT NOW()
);

-- Balance query (used by mv_owner_escrow_balance and FR-021 endpoint):
-- SUM(DEPOSIT) - SUM(CHARGE + WITHDRAWAL) + SUM(REFUND) per owner_id

COMMENT ON TABLE owner_escrow_events IS
    'Append-only owner prepaid balance ledger. '
    'Balance = SUM(DEPOSIT + REFUND) - SUM(CHARGE + WITHDRAWAL) per owner_id. '
    'No UPDATE. No DELETE. All amounts in integer paise (Invariant 4). '
    'Required for FR-014, FR-021, FR-059.';
COMMENT ON COLUMN owner_escrow_events.amount_paise IS
    'Integer paise ONLY. BIGINT. No FLOAT. RS1 = 100 paise (NFR-046).';

-- ── repair_jobs ─────────────────────────────────────────────────────────────────
-- PROFILE-VARIABLE: available_shard_count range = [16, 56] for this profile.
-- [REF: DM §4.10, DM §8.17–§8.19, IC §5.7, ADR-004, ADR-031]
-- Departure-trigger deduplication is at application layer (IC §5.7).
CREATE TABLE repair_jobs (
    job_id                  UUID                PRIMARY KEY DEFAULT gen_random_uuid(),

    chunk_id                BYTEA               NOT NULL CHECK (octet_length(chunk_id) = 32),
    -- Content address of the chunk needing repair.

    segment_id              UUID                NOT NULL REFERENCES segments(segment_id),

    provider_id             UUID                REFERENCES providers(provider_id),
    -- NULL for THRESHOLD_WARNING / EMERGENCY_FLOOR triggers (DM §8.17).
    -- No single departure caused the drop; count drifted below threshold.

    trigger_type            repair_trigger_type NOT NULL,

    priority                repair_priority     NOT NULL,

    status                  repair_job_status   NOT NULL DEFAULT 'QUEUED',

    available_shard_count   SMALLINT            NOT NULL
                            CHECK (available_shard_count BETWEEN 16 AND 56),
    -- PROFILE-VARIABLE bounds (generator.go, ADR-031).
    -- prod: [16, 56]  demo: [3, 5]

    created_at              TIMESTAMPTZ         NOT NULL DEFAULT NOW(),

    started_at              TIMESTAMPTZ,
    -- NULL until a repair worker picks up the job (DM §8.18).

    completed_at            TIMESTAMPTZ,
    -- NULL until the job reaches COMPLETED or FAILED (DM §8.19).

    -- ── Constraints ──────────────────────────────────────────────────────────
    CONSTRAINT repair_jobs_priority_matches_trigger CHECK (
        (trigger_type = 'EMERGENCY_FLOOR' AND priority = 'EMERGENCY')
        OR
        (trigger_type IN ('SILENT_DEPARTURE', 'ANNOUNCED_DEPARTURE')
                AND priority = 'PERMANENT_DEPARTURE')
        OR
        (trigger_type = 'THRESHOLD_WARNING' AND priority = 'PRE_WARNING')
    ),
    -- Priority derived from trigger_type; prevents drift at application layer.

    CONSTRAINT repair_jobs_completed_after_started CHECK (
        completed_at IS NULL OR started_at IS NOT NULL
    )
    -- Departure-trigger deduplication is at application layer (IC §5.7).
    -- UNIQUE (chunk_id, provider_id, trigger_type) was removed; see build.md §4.4.5.
);

-- Partial unique index for threshold deduplication (DM §5, IC §5.7).
-- Prevents multiple QUEUED/IN_PROGRESS threshold jobs for the same chunk.
CREATE UNIQUE INDEX idx_repair_jobs_threshold_no_dup
    ON repair_jobs (chunk_id, trigger_type)
    WHERE provider_id IS NULL AND status IN ('QUEUED', 'IN_PROGRESS');

COMMENT ON TABLE repair_jobs IS
    'Repair queue. Priority ordering: EMERGENCY first, then PERMANENT_DEPARTURE, '
    'then PRE_WARNING (ADR-004, Paper 39). FIFO within each priority tier.';
COMMENT ON COLUMN repair_jobs.provider_id IS
    'NULL for threshold-triggered repairs (THRESHOLD_WARNING, EMERGENCY_FLOOR) '
    'where no single departure caused the drop. Non-null for departure-triggered.';
COMMENT ON COLUMN repair_jobs.available_shard_count IS
    'Shard count at job creation. Profile-variable CHECK bounds: '
    'prod=[16,56], demo=[3,5] (generated by generator.go, ADR-031).';

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
