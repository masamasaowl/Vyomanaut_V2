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
// All other DDL (ENUMs, RSPs, indexes, triggers) is profile-invariant.
//
// Sessions 4.2.1–4.7.5 supply the full SQL body for each schema section.
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

	// ── Sessions 4.2.1–4.7.5 will expand each section below ─────────────────────
	// For Session 4.1.1 the stub tables below exist solely to embed the two
	// profile-variable CHECK constraints in valid SQL so that VERIFY:
	// PROFILE_VARIABLE_CONSTRAINTS passes. Sessions 4.3.2 and 4.5.1 replace
	// these stubs with the full DDL from DM §4 and DM §6 respectively.
	// [REF: build.md Phase 4.1 Session 4.1.1 VERIFY: PROFILE_VARIABLE_CONSTRAINTS]

	// Session 4.2.1 — ENUM type definitions (DM §4).
	//
	// All nine types are profile-invariant: identical values in demo and
	// production. Per DM §9 migration ordering, every CREATE TYPE statement in
	// this section is emitted ahead of the CREATE TABLE statements produced by
	// chunkAssignmentsSection, repairJobsSection, and the sections later
	// sessions (4.2.2–4.7.5) will add.
	//
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

	// TODO(4.2.2): providers table from DM §3.
	providersSection := "" +
		"-- ── providers ──────────────────────────────────────────────────────────────────\n" +
		"-- TODO(4.2.2): providers table from DM §3 (declared_storage_gb, asn, region,\n" +
		"--              heartbeat_ts, departure_ts, status, ed25519_pubkey).\n\n"

	// TODO(4.3.1): files / pointer_files tables from DM §4.
	filesSection := "" +
		"-- ── files / pointer_files ──────────────────────────────────────────────────────\n" +
		"-- TODO(4.3.1): files and pointer_files tables from DM §4 (owner_id, size_bytes,\n" +
		"--              pointer_enc_ciphertext, pointer_nonce, file_key_ciphertext).\n\n"

	// Session 4.3.2 — chunk_assignments.
	// Profile-variable: shard_index upper bound = TotalShards-1 for this profile.
	// [REF: DM §9 Profile rule, MVP §5.5, DM §8.23, ADR-031]
	chunkAssignmentsSection := fmt.Sprintf(""+
		"-- ── chunk_assignments ───────────────────────────────────────────────────────────\n"+
		"-- PROFILE-VARIABLE: shard_index upper bound = TotalShards-1 = %d for this profile.\n"+
		"-- [REF: DM §9 Profile rule, MVP §5.5, DM §8.23, ADR-031]\n"+
		"-- TODO(4.3.2): full chunk_assignments schema from DM §4 (file_id, provider_id,\n"+
		"--              shard_state, assigned_at, confirmed_at, vlog_offset).\n"+
		"CREATE TABLE IF NOT EXISTS chunk_assignments (\n"+
		"    id          UUID    NOT NULL DEFAULT gen_random_uuid(),\n"+
		"    shard_index INTEGER,\n"+
		"    CONSTRAINT chunk_assignments_pkey\n"+
		"        PRIMARY KEY (id),\n"+
		"    CONSTRAINT chunk_assignments_shard_index_range\n"+
		"        %s\n"+
		");\n\n",
		profile.TotalShards-1,
		shardIndexCheck,
	)

	// TODO(4.4.1): audit_periods / audit_receipts from DM §5.
	auditSection := "" +
		"-- ── audit_periods / audit_receipts ─────────────────────────────────────────────\n" +
		"-- TODO(4.4.1): audit_periods table from DM §5 (EXCLUDE USING gist for\n" +
		"--              non-overlapping tsrange — requires btree_gist extension).\n" +
		"--              audit_receipts table from DM §5 (challenge_nonce BYTEA(33),\n" +
		"--              NOT BYTEA(32) per IC §5.1 INV-5 / CI check-08).\n\n"

	// Session 4.5.1 — repair_jobs.
	// Profile-variable: available_shard_count range = [DataShards, TotalShards] for this profile.
	// [REF: DM §9 Profile rule, MVP §5.5, DM §8.23, ADR-031]
	repairJobsSection := fmt.Sprintf(""+
		"-- ── repair_jobs ─────────────────────────────────────────────────────────────────\n"+
		"-- PROFILE-VARIABLE: available_shard_count range = [%d, %d] for this profile.\n"+
		"-- [REF: DM §9 Profile rule, MVP §5.5, DM §8.23, ADR-031]\n"+
		"-- TODO(4.5.1): full repair_jobs schema from DM §6 (file_id, trigger_type,\n"+
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

	// TODO(4.6.1): escrow / payment ledger from DM §7.
	paymentSection := "" +
		"-- ── escrow / payment ledger ─────────────────────────────────────────────────────\n" +
		"-- TODO(4.6.1): escrow_events and ledger tables from DM §7.\n" +
		"--              INVARIANT: all monetary amounts are INTEGER (int64 paise). No\n" +
		"--              FLOAT, DECIMAL, or NUMERIC types permitted (IC §5.1 INV-4).\n\n"

	// TODO(4.7.1): vetting_chunks from DM §8.
	vettingSection := "" +
		"-- ── vetting_chunks ─────────────────────────────────────────────────────────────\n" +
		"-- TODO(4.7.1): vetting_chunks table from DM §8 (synthetic chunk lifecycle:\n" +
		"--              generation, assignment, GC delivery, departure cleanup — ADR-030).\n\n"

	// TODO(4.7.2–4.7.4): RSPs, indexes, triggers.
	infraSection := "" +
		"-- ── Row-level security policies ────────────────────────────────────────────────\n" +
		"-- TODO(4.7.2): RSPs from IC §6 (per-provider isolation on chunk_assignments;\n" +
		"--              microservice-role full access; client-role file-owner reads).\n\n" +
		"-- ── Indexes ────────────────────────────────────────────────────────────────────\n" +
		"-- TODO(4.7.3): B-tree and GiST indexes from DM §9 (covering indexes for\n" +
		"--              audit scheduling, repair queue polling, scoring window queries).\n\n" +
		"-- ── Triggers ───────────────────────────────────────────────────────────────────\n" +
		"-- TODO(4.7.4): updated_at maintenance triggers from DM §9.\n\n"

	// Session 4.7.5 — Views.
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
		"-- TODO(4.7.5): any other views defined in DM §9.\n"

	return header +
		extensions +
		enumsSection +
		providersSection +
		filesSection +
		chunkAssignmentsSection +
		auditSection +
		repairJobsSection +
		paymentSection +
		vettingSection +
		infraSection +
		viewsSection
}