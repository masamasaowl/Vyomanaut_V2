// Command generator produces the initial PostgreSQL schema for Vyomanaut V2.
// It is parameterised by the active NetworkProfile so that demo and production
// schemas differ only in the CHECK constraint bounds for shard_index and
// available_shard_count (DM §9 Profile rule, MVP §5.5, DM §3 Invariant 7).
//
// Usage:
//
//	go run migrations/generator.go --profile=prod
//	go run migrations/generator.go --profile=demo
//
// Output is the full migration SQL to stdout. Redirect to file:
//
//	go run migrations/generator.go --profile=prod > migrations/001_initial_schema.sql
package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/masamasaowl/Vyomanaut_V2/internal/config"
)

func main() {
	profileFlag := flag.String("profile", "", "prod or demo (required)")
	flag.Parse()

	if *profileFlag == "" {
		log.Fatal("--profile is required: use --profile=prod or --profile=demo")
	}

	profile := selectProfile(*profileFlag)
	fmt.Print(generateSchema(profile))
}

// selectProfile returns the canonical NetworkProfile for the given mode string.
// Fatals on unknown mode — the migration generator must never produce schema for
// an unrecognised profile.
//
// [REF: MVP §5.5, DM §9 Profile rule]
func selectProfile(mode string) config.NetworkProfile {
	switch mode {
	case "demo":
		return config.DemoProfile
	case "prod":
		return config.ProductionProfile
	default:
		log.Fatalf("unknown --profile=%q; must be 'demo' or 'prod'", mode)
		return config.ProductionProfile // unreachable; satisfies compiler
	}
}

// generateSchema returns the complete initial migration SQL for the given
// NetworkProfile.
//
// PROFILE-VARIABLE CONSTRAINTS (DM §9 Profile rule, MVP §5.5, ADR-031):
// Only two CHECK constraints differ between demo and production:
//
//  1. chunk_assignments.shard_index:
//     CHECK (shard_index BETWEEN 0 AND (TotalShards-1) OR shard_index IS NULL)
//
//  2. repair_jobs.available_shard_count:
//     CHECK (available_shard_count BETWEEN DataShards AND TotalShards)
//
// All other DDL (ENUMs, owners, providers, RSPs, indexes, triggers) is
// profile-invariant.
//
// [REF: DM §2–§9, MVP §5.5, ADR-031, build.md Phase 4.1 Session 4.1.1]
func generateSchema(profile config.NetworkProfile) string {
	// Header block.
	// INVARIANT: first line must be "-- Generated for profile: {mode}".
	// Verified by build.md Phase 4.1 Session 4.1.1 VERIFY: GENERATOR_RUNS.
	header := fmt.Sprintf(
		"-- Generated for profile: %s\n"+
			"-- Generated at: %s\n"+
			"-- ShardSize: 262144 (compile-time constant; NOT profile-variable)\n"+
			"-- DataShards: %d\n"+
			"-- TotalShards: %d\n\n",
		profile.Mode,
		time.Now().UTC().Format(time.RFC3339),
		profile.DataShards,
		profile.TotalShards,
	)

	// Profile-variable CHECK constraint expressions.
	// These are the ONLY two items in the schema that differ between demo and
	// production. All other DDL is profile-invariant.
	// [REF: DM §9 Profile rule §8.23, MVP §5.5, ADR-031]
	shardIndexCheck := fmt.Sprintf(
		"CHECK (shard_index BETWEEN 0 AND %d OR shard_index IS NULL)",
		profile.TotalShards-1,
	)
	shardCountCheck := fmt.Sprintf(
		"CHECK (available_shard_count BETWEEN %d AND %d)",
		profile.DataShards,
		profile.TotalShards,
	)

	// Extensions required by the schema.
	//   btree_gist — audit_periods EXCLUDE USING gist (tsrange WITH &&) overlap constraint.
	//   pgcrypto   — gen_random_uuid() for UUID primary-key defaults.
	// [REF: DM §9, deployments/dev/init-db.sql, CI check-07]
	extensions := "" +
		"-- ── Extensions ─────────────────────────────────────────────────────────────────\n" +
		"-- btree_gist: required by audit_periods EXCLUDE USING gist (tsrange WITH &&).\n" +
		"-- pgcrypto:   provides gen_random_uuid() for UUID primary-key column defaults.\n" +
		"-- [REF: DM §9, deployments/dev/init-db.sql, CI check-07]\n" +
		"CREATE EXTENSION IF NOT EXISTS btree_gist;\n" +
		"CREATE EXTENSION IF NOT EXISTS pgcrypto;\n\n"

	// ── ENUMs — profile-invariant ───────────────────────────────────────────────────
	// Session 4.2.1 — ENUM type definitions (DM §4).
	// All nine types are profile-invariant: identical values in demo and production.
	// Per DM §9 migration ordering, every CREATE TYPE statement is emitted ahead of
	// the CREATE TABLE statements that follow.
	// [REF: DM §4, DM §9, build.md Phase 4.2 Session 4.2.1]
	enumsSection := `-- ── ENUMs ──────────────────────────────────────────────────────────────────────
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

`

	// ── owners — profile-invariant ──────────────────────────────────────────────────
	// Session 4.3.1 — owners table (DM §4.1, DM §8.1).
	// [REF: build.md Phase 4.3 Session 4.3.1, DM §4.1, DM §8.1]
	ownersSection := "" +
		"-- ── owners ─────────────────────────────────────────────────────────────────────\n" +
		"-- [REF: DM §4.1, DM §8.1]\n" +
		"CREATE TABLE owners (\n" +
		"    -- ── Identity ─────────────────────────────────────────────────────────────\n" +
		"    owner_id            UUID            PRIMARY KEY DEFAULT gen_random_uuid(),\n" +
		"    -- UUIDv7 preferred at application layer for time-ordered PKs (ADR-013).\n" +
		"\n" +
		"    phone_number        VARCHAR(15)     NOT NULL UNIQUE,\n" +
		"    -- E.164 format (e.g. +919876543210). OTP-verified at registration (FR-001).\n" +
		"    -- UNIQUE: one identity per phone number prevents trivial Sybil registration.\n" +
		"\n" +
		"    ed25519_public_key  BYTEA           NOT NULL CHECK (octet_length(ed25519_public_key) = 32),\n" +
		"    -- 32-byte compressed Ed25519 public key (ADR-020). Never the private key.\n" +
		"\n" +
		"    -- ── Payment ──────────────────────────────────────────────────────────────\n" +
		"    smart_collect_vpa   VARCHAR(255)    NULL,\n" +
		"    -- Razorpay Smart Collect 2.0 virtual UPI payment address.\n" +
		"    -- NULL until Razorpay completes VPA provisioning (DM §8.1).\n" +
		"\n" +
		"    -- ── Timestamps ───────────────────────────────────────────────────────────\n" +
		"    created_at          TIMESTAMPTZ     NOT NULL DEFAULT NOW()\n" +
		");\n" +
		"\n" +
		"COMMENT ON TABLE owners IS 'Registered data owners. One row per verified phone number.';\n" +
		"COMMENT ON COLUMN owners.smart_collect_vpa IS\n" +
		"    'Razorpay UPI VPA for escrow deposits. NULL until provisioned by Razorpay webhook.';\n" +
		"\n"

	// ── providers — profile-invariant ──────────────────────────────────────────────
	// Session 4.3.2 — providers table (DM §4.2, DM §8.2–§8.6).
	// Physical deletion is prohibited (DM §3 Invariant 3).
	// CRITICAL NULL rules (DM §8 justifications):
	//   p95_throughput_kbps NULL — DEFAULT 0 causes division-by-zero in deadline formula
	//   avg_rtt_ms          NULL — DEFAULT 2000 is a hard-coded guess that diverges over time
	//   var_rtt_ms          NOT NULL DEFAULT 0 — zero variance is the correct initial assumption
	// [REF: build.md Phase 4.3 Session 4.3.2, DM §4.2, DM §8.2–§8.6]
	providersSection := "" +
		"-- ── providers ──────────────────────────────────────────────────────────────────\n" +
		"-- [REF: DM §4.2, DM §8.2–§8.6]\n" +
		"CREATE TABLE providers (\n" +
		"    -- ── Identity ─────────────────────────────────────────────────────────────\n" +
		"    provider_id             UUID            PRIMARY KEY DEFAULT gen_random_uuid(),\n" +
		"\n" +
		"    phone_number            VARCHAR(15)     NOT NULL UNIQUE,\n" +
		"    -- OTP-verified at registration. UNIQUE prevents Sybil attacks (ADR-005).\n" +
		"\n" +
		"    ed25519_public_key      BYTEA           NOT NULL CHECK (octet_length(ed25519_public_key) = 32),\n" +
		"    -- libp2p peer key. Authenticates every heartbeat and audit receipt (ADR-021).\n" +
		"\n" +
		"    -- ── Lifecycle ────────────────────────────────────────────────────────────\n" +
		"    status                  provider_status NOT NULL DEFAULT 'PENDING_ONBOARDING',\n" +
		"\n" +
		"    -- ── Hardware declaration ─────────────────────────────────────────────────\n" +
		"    declared_storage_gb     INT             NOT NULL CHECK (declared_storage_gb BETWEEN 10 AND 100000),\n" +
		"    -- Minimum 10 GB, maximum 100 TB. Verified indirectly by vetting audits (ADR-030).\n" +
		"\n" +
		"    city                    VARCHAR(100)    NOT NULL,\n" +
		"\n" +
		"    region                  VARCHAR(100)    NOT NULL,\n" +
		"    -- Readiness gate: >=3 distinct metro regions required (ADR-029).\n" +
		"\n" +
		"    asn                     VARCHAR(32)     NOT NULL,\n" +
		"    -- e.g. 'AS24560' (Airtel); 'SIM-AS1'...'SIM-AS5' in simulation mode.\n" +
		"    -- 20% ASN cap: no single ASN holds >20% of any file's shards (ADR-014).\n" +
		"\n" +
		"    -- ── Payment rails ────────────────────────────────────────────────────────\n" +
		"    razorpay_linked_account_id  VARCHAR(255),\n" +
		"    -- NULL until account.created webhook fires. Assignments blocked until set (DM §8.2).\n" +
		"\n" +
		"    razorpay_cooling_until  TIMESTAMPTZ,\n" +
		"    -- NULL until account created; set to NOW() + 24h on webhook receipt (DM §8.3).\n" +
		"\n" +
		"    -- ── Network addresses (ADR-028) ──────────────────────────────────────────\n" +
		"    last_known_multiaddrs   JSONB           NOT NULL DEFAULT '[]',\n" +
		"    -- Ordered JSON array of libp2p multiaddrs from the most recent heartbeat.\n" +
		"\n" +
		"    last_heartbeat_ts       TIMESTAMPTZ,\n" +
		"    -- NULL during PENDING_ONBOARDING before first heartbeat (DM §8.4).\n" +
		"\n" +
		"    multiaddr_stale         BOOLEAN         NOT NULL DEFAULT FALSE,\n" +
		"    -- TRUE when 2+ consecutive heartbeats missed; triggers DHT fallback (ADR-028).\n" +
		"\n" +
		"    -- ── Performance counters (ADR-006, ADR-014) ──────────────────────────────\n" +
		"    p95_throughput_kbps     FLOAT           NULL,\n" +
		"    -- NULL until vetting accumulates samples; application substitutes pool median.\n" +
		"    -- DEFAULT 0 is WRONG: causes division by zero in audit deadline formula (ADR-014).\n" +
		"\n" +
		"    avg_rtt_ms              FLOAT           NULL,\n" +
		"    -- NULL until first sample; application substitutes pool median.\n" +
		"    -- DEFAULT 2000 is WRONG: hard-coded guess diverges as network median shifts.\n" +
		"\n" +
		"    var_rtt_ms              FLOAT           NOT NULL DEFAULT 0,\n" +
		"    -- Zero variance is the correct initial assumption.\n" +
		"    -- RTO = avg_rtt_ms + 4 × var_rtt_ms (ADR-006).\n" +
		"\n" +
		"    rto_sample_count        INT             NOT NULL DEFAULT 0,\n" +
		"    -- Below 5: scheduler substitutes pool-median RTO (ADR-006).\n" +
		"\n" +
		"    first_chunk_assignment_at   TIMESTAMPTZ,\n" +
		"    -- NULL until first chunk assigned by assignment service (DM §8.6).\n" +
		"    -- Vetting duration check: NOW() - first_chunk_assignment_at >= 120 days (FR-026).\n" +
		"\n" +
		"    -- ── Vetting counters (ADR-005) ────────────────────────────────────────────\n" +
		"    consecutive_audit_passes    INT         NOT NULL DEFAULT 0,\n" +
		"    -- 80 consecutive passes → VETTING to ACTIVE transition (Jeffrey's prior, ADR-005).\n" +
		"\n" +
		"    -- ── Failure clustering (ADR-008, Paper 32) ───────────────────────────────\n" +
		"    accelerated_reaudit     BOOLEAN         NOT NULL DEFAULT FALSE,\n" +
		"    -- TRUE when >1 FAIL in rolling 7-day window (Paper 32, ADR-008).\n" +
		"\n" +
		"    -- ── Escrow freeze (ADR-024) ──────────────────────────────────────────────\n" +
		"    frozen                  BOOLEAN         NOT NULL DEFAULT FALSE,\n" +
		"\n" +
		"    -- ── Timestamps ───────────────────────────────────────────────────────────\n" +
		"    created_at              TIMESTAMPTZ     NOT NULL DEFAULT NOW(),\n" +
		"\n" +
		"    departed_at             TIMESTAMPTZ,\n" +
		"    -- NULL for active providers. Set on departure declaration. Never cleared (DM §8.5).\n" +
		"\n" +
		"    -- ── Constraints ──────────────────────────────────────────────────────────\n" +
		"    CONSTRAINT providers_throughput_nonneg  CHECK (p95_throughput_kbps >= 0),\n" +
		"    CONSTRAINT providers_avg_rtt_nonneg     CHECK (avg_rtt_ms >= 0),\n" +
		"    CONSTRAINT providers_var_rtt_nonneg     CHECK (var_rtt_ms >= 0),\n" +
		"    CONSTRAINT providers_passes_nonneg      CHECK (consecutive_audit_passes >= 0),\n" +
		"    CONSTRAINT providers_departed_status\n" +
		"        CHECK (departed_at IS NULL OR status = 'DEPARTED')\n" +
		");\n" +
		"\n" +
		"COMMENT ON TABLE providers IS\n" +
		"    'Storage providers. One row per verified daemon. Never physically deleted (DM §3 Invariant 3).';\n" +
		"\n"

	// ── files — profile-invariant ──────────────────────────────────────────────────
	// Session 4.3.3 — files table (DM §4.3, REQ §4.4 FR-019).
	// The microservice holds only encrypted pointer ciphertext; it cannot decrypt
	// the file contents or derive the file key (ADR-020, zero-knowledge property).
	// [REF: build.md Phase 4.3 Session 4.3.3, DM §4.3, REQ §4.4 FR-019]
	filesSection := "" +
		"-- ── files ──────────────────────────────────────────────────────────────────────\n" +
		"-- [REF: DM §4.3, REQ §4.4 FR-019]\n" +
		"CREATE TABLE files (\n" +
		"    -- ── Identity ─────────────────────────────────────────────────────────────\n" +
		"    file_id             UUID            PRIMARY KEY DEFAULT gen_random_uuid(),\n" +
		"    -- UUIDv7 at application layer (ADR-013). Pseudonymous: appears in audit\n" +
		"    -- receipts but cannot be linked to plaintext identity without master secret.\n" +
		"\n" +
		"    owner_id            UUID            NOT NULL REFERENCES owners(owner_id),\n" +
		"\n" +
		"    -- ── Pointer file storage (ADR-020) ───────────────────────────────────────\n" +
		"    pointer_ciphertext  BYTEA           NOT NULL,\n" +
		"    -- AEAD_CHACHA20_POLY1305 ciphertext of the pointer file struct.\n" +
		"    -- Microservice stores blindly; cannot decrypt (ADR-020, zero-knowledge).\n" +
		"\n" +
		"    pointer_nonce       BYTEA           NOT NULL CHECK (octet_length(pointer_nonce) = 12),\n" +
		"    -- 96-bit (12-byte) monotone counter nonce. RFC 8439 §2.3.\n" +
		"\n" +
		"    pointer_tag         BYTEA           NOT NULL CHECK (octet_length(pointer_tag) = 16),\n" +
		"    -- 16-byte Poly1305 authentication tag. Constant-time verification (NFR-019).\n" +
		"\n" +
		"    -- ── File name (nullable) ─────────────────────────────────────────────────\n" +
		"    display_name_ciphertext  BYTEA      NULL,\n" +
		"    -- AEAD_CHACHA20_POLY1305 ciphertext of the user-provided file name.\n" +
		"    -- NULL if owner provides no label (CLI path). Non-null for UI file list (FR-019).\n" +
		"    -- Microservice stores blindly; cannot read the filename (ADR-020).\n" +
		"\n" +
		"    display_name_nonce       BYTEA      NULL CHECK (octet_length(display_name_nonce) = 12 OR display_name_nonce IS NULL),\n" +
		"\n" +
		"    display_name_tag         BYTEA      NULL CHECK (octet_length(display_name_tag) = 16 OR display_name_tag IS NULL),\n" +
		"\n" +
		"    -- ── File metadata ────────────────────────────────────────────────────────\n" +
		"    original_size_bytes BIGINT          NOT NULL CHECK (original_size_bytes > 0),\n" +
		"    -- Plaintext size before padding. Required to strip AONT padding after RS\n" +
		"    -- decode and AONT decryption on retrieval (FR-008).\n" +
		"\n" +
		"    status              file_status     NOT NULL DEFAULT 'ACTIVE',\n" +
		"\n" +
		"    schema_version      SMALLINT        NOT NULL DEFAULT 1,\n" +
		"    -- Pointer file schema version. Forward-compatible migration for V3.\n" +
		"\n" +
		"    -- ── Timestamps ───────────────────────────────────────────────────────────\n" +
		"    uploaded_at         TIMESTAMPTZ     NOT NULL DEFAULT NOW()\n" +
		");\n" +
		"\n" +
		"COMMENT ON TABLE files IS\n" +
		"    'One row per uploaded file. The microservice holds only encrypted pointer '\n" +
		"    'ciphertext and cannot read the file contents or decryption key.';\n" +
		"COMMENT ON COLUMN files.pointer_ciphertext IS\n" +
		"    'Blind store. Key lives in the owner''s head. Service cannot decrypt (ADR-020).';\n" +
		"COMMENT ON COLUMN files.original_size_bytes IS\n" +
		"    'Strip AONT padding to this length after decoding. Padding is added for '\n" +
		"    'files smaller than one full segment (4 MB = 16 × 256 KB).';\n" +
		"\n"

	// ── segments — profile-invariant ───────────────────────────────────────────────
	// Session 4.3.4 — segments table (DM §4.4).
	// Files larger than one encoded segment (56 × 256 KB on the wire) are split
	// into multiple independent segments processed through AONT-RS separately.
	// [REF: build.md Phase 4.3 Session 4.3.4, DM §4.4]
	segmentsSection := "" +
		"-- ── segments ───────────────────────────────────────────────────────────────────\n" +
		"-- [REF: DM §4.4]\n" +
		"CREATE TABLE segments (\n" +
		"    segment_id      UUID        PRIMARY KEY DEFAULT gen_random_uuid(),\n" +
		"\n" +
		"    file_id         UUID        NOT NULL REFERENCES files(file_id),\n" +
		"\n" +
		"    segment_index   INT         NOT NULL CHECK (segment_index >= 0),\n" +
		"    -- 0-based. Segments concatenated in this order on retrieval.\n" +
		"\n" +
		"    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),\n" +
		"\n" +
		"    CONSTRAINT segments_unique_index UNIQUE (file_id, segment_index)\n" +
		"    -- A file cannot have two segments at the same position.\n" +
		");\n" +
		"\n" +
		"COMMENT ON TABLE segments IS\n" +
		"    'One row per 14 MB slice of a file. Each segment produces exactly TotalShards chunks '\n" +
		"    'via AONT-RS. Segments are independent: losing one does not affect the others.';\n" +
		"\n"

	// ── chunk_assignments — profile-variable (shard_index range) ──────────────────
	// Session 4.3.5 — chunk_assignments table (DM §4.5, DM §3 Invariant 6).
	// PROFILE-VARIABLE: shard_index upper bound = TotalShards-1 for this profile.
	// CRITICAL NULL rules (DM §8.21, DM §8.22):
	//   segment_id  NULL when is_vetting_chunk = TRUE (no real segment exists)
	//   shard_index NULL when is_vetting_chunk = TRUE (no RS slot applies)
	// The partial unique index MUST be a standalone CREATE UNIQUE INDEX (DM §9).
	// [REF: build.md Phase 4.3 Session 4.3.5, DM §4.5, DM §3 Invariant 6, DM §8.21–§8.22]
	chunkAssignmentsSection := fmt.Sprintf(""+
		"-- ── chunk_assignments ───────────────────────────────────────────────────────────\n"+
		"-- PROFILE-VARIABLE: shard_index upper bound = TotalShards-1 = %d for this profile.\n"+
		"-- [REF: DM §4.5, DM §3 Invariant 6, DM §8.21, DM §8.22, ADR-030, ADR-031]\n"+
		"CREATE TABLE chunk_assignments (\n"+
		"    assignment_id    UUID                PRIMARY KEY DEFAULT gen_random_uuid(),\n"+
		"\n"+
		"    chunk_id         BYTEA               NOT NULL CHECK (octet_length(chunk_id) = 32),\n"+
		"    -- SHA-256(shard_data): content address of this 256 KB shard.\n"+
		"    -- For vetting chunks: SHA-256 of a random 256 KB block (ADR-030).\n"+
		"\n"+
		"    is_vetting_chunk BOOLEAN             NOT NULL DEFAULT FALSE,\n"+
		"    -- TRUE for synthetic chunks assigned during provider vetting (ADR-030).\n"+
		"    -- Repair scheduler MUST NOT create repair_jobs for is_vetting_chunk = TRUE.\n"+
		"\n"+
		"    segment_id       UUID                REFERENCES segments(segment_id),\n"+
		"    -- NULL when is_vetting_chunk = TRUE (no real file association — DM §8.21).\n"+
		"\n"+
		"    shard_index      SMALLINT            %s,\n"+
		"    -- NULL when is_vetting_chunk = TRUE (no RS slot — DM §8.22).\n"+
		"    -- Upper bound is profile-variable: TotalShards-1 (ADR-031).\n"+
		"\n"+
		"    provider_id      UUID                NOT NULL REFERENCES providers(provider_id),\n"+
		"\n"+
		"    status           assignment_status   NOT NULL DEFAULT 'ACTIVE',\n"+
		"\n"+
		"    created_at       TIMESTAMPTZ         NOT NULL DEFAULT NOW(),\n"+
		"\n"+
		"    deleted_at       TIMESTAMPTZ,\n"+
		"    -- NULL for all non-DELETED assignments.\n"+
		"\n"+
		"    -- ── Constraints ──────────────────────────────────────────────────────────\n"+
		"    CONSTRAINT chunk_assignments_segment_and_shard_null_iff_vetting CHECK (\n"+
		"        (is_vetting_chunk = FALSE AND segment_id IS NOT NULL AND shard_index IS NOT NULL)\n"+
		"        OR\n"+
		"        (is_vetting_chunk = TRUE  AND segment_id IS NULL    AND shard_index IS NULL)\n"+
		"    ),\n"+
		"    -- Invariant 6: real chunks always reference a segment and shard;\n"+
		"    -- synthetic chunks never do (ADR-030, DM §3 Invariant 6).\n"+
		"\n"+
		"    CONSTRAINT chunk_assignments_one_per_provider_per_chunk\n"+
		"        UNIQUE (chunk_id, provider_id)\n"+
		");\n"+
		"\n"+
		"-- Partial unique index: one active assignment per shard slot per segment (real chunks only).\n"+
		"-- Synthetic chunks excluded (no shard_index, no RS constraint applies).\n"+
		"-- MUST be standalone CREATE UNIQUE INDEX, NOT an inline constraint (DM §9).\n"+
		"CREATE UNIQUE INDEX idx_chunk_assignments_one_active_per_shard\n"+
		"    ON chunk_assignments (segment_id, shard_index)\n"+
		"    WHERE is_vetting_chunk = FALSE\n"+
		"      AND status IN ('ACTIVE', 'REPAIRING');\n"+
		"\n"+
		"-- Read view: challenge scheduler sees only ACTIVE assignments.\n"+
		"CREATE VIEW active_chunk_assignments AS\n"+
		"SELECT *\n"+
		"FROM chunk_assignments\n"+
		"WHERE status = 'ACTIVE';\n"+
		"\n"+
		"COMMENT ON TABLE chunk_assignments IS\n"+
		"    'Routing table: which provider holds which shard of which segment. '\n"+
		"    '20%% ASN cap enforced at INSERT time by the assignment service (ADR-014). '\n"+
		"    'Physical deletion not performed; historical data preserved for audit reconciliation.';\n"+
		"COMMENT ON COLUMN chunk_assignments.chunk_id IS\n"+
		"    'SHA-256(shard_data). RocksDB lookup key on the provider daemon (ADR-023).';\n"+
		"COMMENT ON COLUMN chunk_assignments.is_vetting_chunk IS\n"+
		"    'TRUE for synthetic vetting chunks (ADR-030). Repair scheduler must not enqueue '\n"+
		"    'repair jobs for these rows. Provider daemon cannot distinguish synthetic from real.';\n"+
		"COMMENT ON COLUMN chunk_assignments.segment_id IS\n"+
		"    'NULL for synthetic vetting chunks (is_vetting_chunk = TRUE). '\n"+
		"    'Real shards enforced non-null by CHECK constraint (DM §8.21).';\n"+
		"COMMENT ON COLUMN chunk_assignments.shard_index IS\n"+
		"    'NULL for synthetic vetting chunks (no RS shard slot assigned — DM §8.22). '\n"+
		"    'Real shards: 0 to TotalShards-1; 0..DataShards-1 are systematic, rest parity.';\n"+
		"\n",
		profile.TotalShards-1,
		shardIndexCheck,
	)

	// TODO(4.5.x): audit_periods / audit_receipts from DM §5.
	auditSection := "" +
		"-- ── audit_periods / audit_receipts ─────────────────────────────────────────────\n" +
		"-- TODO(4.5.x): audit_periods table from DM §5 (EXCLUDE USING gist for\n" +
		"--              non-overlapping tsrange — requires btree_gist extension).\n" +
		"--              audit_receipts table from DM §5 (challenge_nonce BYTEA(33),\n" +
		"--              NOT BYTEA(32) per IC §5.1 INV-5 / CI check-08).\n\n"

	// Session 4.6.x — repair_jobs.
	// Profile-variable: available_shard_count range = [DataShards, TotalShards] for this profile.
	// [REF: DM §9 Profile rule, MVP §5.5, DM §8.23, ADR-031]
	repairJobsSection := fmt.Sprintf(""+
		"-- ── repair_jobs ─────────────────────────────────────────────────────────────────\n"+
		"-- PROFILE-VARIABLE: available_shard_count range = [%d, %d] for this profile.\n"+
		"-- [REF: DM §9 Profile rule, MVP §5.5, DM §8.23, ADR-031]\n"+
		"-- TODO(4.6.x): full repair_jobs schema from DM §6 (file_id, trigger_type,\n"+
		"--              priority, missing_shards, state, created_at, promoted_at).\n"+
		"CREATE TABLE IF NOT EXISTS repair_jobs (\n"+
		"    id                    UUID    NOT NULL DEFAULT gen_random_uuid(),\n"+
		"    available_shard_count INTEGER NOT NULL DEFAULT 0,\n"+
		"    CONSTRAINT repair_jobs_pkey\n"+
		"        PRIMARY KEY (id),\n"+
		"    CONSTRAINT repair_jobs_shard_count_range\n"+
		"        %s\n"+
		");\n\n",
		profile.DataShards,
		profile.TotalShards,
		shardCountCheck,
	)

	// TODO(4.7.x): escrow / payment ledger from DM §7.
	paymentSection := "" +
		"-- ── escrow / payment ledger ─────────────────────────────────────────────────────\n" +
		"-- TODO(4.7.x): escrow_events and ledger tables from DM §7.\n" +
		"--              INVARIANT: all monetary amounts are INTEGER (int64 paise). No\n" +
		"--              FLOAT, DECIMAL, or NUMERIC types permitted (IC §5.1 INV-4).\n\n"

	// TODO(4.8.x): vetting_chunks from DM §8.
	vettingSection := "" +
		"-- ── vetting_chunks ─────────────────────────────────────────────────────────────\n" +
		"-- TODO(4.8.x): vetting_chunks table from DM §8 (synthetic chunk lifecycle:\n" +
		"--              generation, assignment, GC delivery, departure cleanup — ADR-030).\n\n"

	// TODO(4.9.x): RSPs, indexes, triggers.
	infraSection := "" +
		"-- ── Row-level security policies ────────────────────────────────────────────────\n" +
		"-- TODO(4.9.x): RSPs from IC §6 (per-provider isolation on chunk_assignments;\n" +
		"--              microservice-role full access; client-role file-owner reads).\n\n" +
		"-- ── Indexes ────────────────────────────────────────────────────────────────────\n" +
		"-- TODO(4.9.x): B-tree and GiST indexes from DM §9 (covering indexes for\n" +
		"--              audit scheduling, repair queue polling, scoring window queries).\n\n" +
		"-- ── Triggers ───────────────────────────────────────────────────────────────────\n" +
		"-- TODO(4.9.x): updated_at maintenance triggers from DM §9.\n\n"

	// Session 4.10.x — Views.
	// CRITICAL: mv_provider_scores is NOT included in the migration.
	// It is dropped and recreated at microservice startup using the active
	// NetworkProfile's scoring window values (profile.ScoreWindowShort/Medium/Long).
	// [REF: DM §9 Profile rule, MVP §5.5, build.md Phase 4.1 Session 4.1.1]
	viewsSection := "" +
		"-- ── Views ──────────────────────────────────────────────────────────────────────\n" +
		"-- IMPORTANT: mv_provider_scores is NOT here. It is dropped and recreated at\n" +
		"-- microservice startup from profile.ScoreWindow{Short,Medium,Long} values.\n" +
		"-- Hard-coding scoring windows in a migration violates DM §9 Profile rule.\n" +
		"-- [REF: DM §9, MVP §5.5, build.md Phase 4.1 Session 4.1.1]\n" +
		"-- TODO(4.10.x): any other views defined in DM §9.\n"

	return header +
		extensions +
		enumsSection +
		ownersSection +
		providersSection +
		filesSection +
		segmentsSection +
		chunkAssignmentsSection +
		auditSection +
		repairJobsSection +
		paymentSection +
		vettingSection +
		infraSection +
		viewsSection
}
