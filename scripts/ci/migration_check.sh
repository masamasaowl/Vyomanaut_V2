#!/usr/bin/env bash
# scripts/ci/migration_check.sh
#
# Verifies the DM §9 migration checklist against a live, migrated Postgres
# database. This is CI check 7 (MVP §8.4) — run after the migration has been
# applied (see .github/workflows/ci.yml check-07).
#
# Connection parameters are taken from the standard PG* environment variables
# so the same script works against the CI Postgres service and a local dev
# database without modification.
#
# REF: build.md Phase 4.8 Session 4.8.1, DM §9, MVP §8.4

set -euo pipefail

DB="${PGDATABASE:-vyomanaut_test}"
USER="${PGUSER:-vyomanaut_app}"
HOST="${PGHOST:-localhost}"
FAIL=0

# psql_check runs a single-value query and compares the (whitespace-stripped)
# result against the expected value.
#
# The psql invocation is deliberately allowed to fail without aborting the
# script (`|| true`): under `set -e`, an assignment whose right-hand side is a
# failing command substitution would otherwise kill the whole script on the
# FIRST broken check, defeating the point of a checklist that must report on
# every item. A psql failure (bad connection, missing relation, etc.) simply
# surfaces as a result that won't match `expected`, which is reported as a
# normal FAIL line below.
psql_check() {
  local name="$1"; local query="$2"; local expected="$3"
  local result
  result=$(psql -h "$HOST" -U "$USER" -d "$DB" -t -c "$query" 2>&1 | tr -d '[:space:]') || true
  if [ "$result" = "$expected" ]; then
    echo "PASS [$name]"
  else
    echo "FAIL [$name]: got '$result', expected '$expected'"
    FAIL=1
  fi
}

# ── DM §9 checklist ──────────────────────────────────────────────────────────

# btree_gist installed (required by audit_periods EXCLUDE USING gist).
psql_check "btree_gist_installed" \
  "SELECT COUNT(*) FROM pg_extension WHERE extname = 'btree_gist'" \
  "1"

# challenge_nonce is BYTEA with octet_length = 33, never 32 (DM §3 Invariant 5).
psql_check "challenge_nonce_33_bytes" \
  "SELECT COUNT(*) FROM information_schema.check_constraints
   WHERE constraint_name LIKE '%challenge_nonce%'
   AND check_clause LIKE '%33%'" \
  "1"

# No FLOAT/DOUBLE PRECISION columns in escrow_events (Invariant 4, NFR-046).
psql_check "no_float_in_escrow" \
  "SELECT COUNT(*) FROM information_schema.columns
   WHERE table_name = 'escrow_events'
   AND data_type IN ('real', 'double precision')" \
  "0"

# audit_result has no DEFAULT — NULL is the intentional PENDING state.
psql_check "audit_result_no_default" \
  "SELECT COUNT(*) FROM information_schema.columns
   WHERE table_name = 'audit_receipts'
   AND column_name = 'audit_result'
   AND column_default IS NOT NULL" \
  "0"

# REVERSAL is a member of the escrow_event_type ENUM.
psql_check "reversal_in_enum" \
  "SELECT COUNT(*) FROM pg_enum
   WHERE enumtypid = 'escrow_event_type'::regtype
   AND enumlabel = 'REVERSAL'" \
  "1"

# is_vetting_chunk column present on chunk_assignments (ADR-030).
psql_check "is_vetting_chunk_present" \
  "SELECT COUNT(*) FROM information_schema.columns
   WHERE table_name = 'chunk_assignments'
   AND column_name = 'is_vetting_chunk'" \
  "1"

# audit_receipts.file_id is nullable (NULL for synthetic vetting audits, DM §8.20).
psql_check "audit_receipts_file_id_nullable" \
  "SELECT is_nullable FROM information_schema.columns
   WHERE table_name = 'audit_receipts'
   AND column_name = 'file_id'" \
  "YES"

# Partial unique index on chunk_assignments filters out synthetic chunks.
psql_check "vetting_filter_in_partial_index" \
  "SELECT COUNT(*) FROM pg_indexes
   WHERE indexname = 'idx_chunk_assignments_one_active_per_shard'
   AND indexdef LIKE '%is_vetting_chunk%'" \
  "1"

# scores_as_of column present in mv_provider_scores (ADR-024 staleness check).
# NOTE: information_schema.columns excludes materialized views entirely in
# PostgreSQL (matviews are a PostgreSQL extension, not a SQL-standard object
# type information_schema covers) — it would silently report zero rows here.
# pg_attribute joined to pg_class is the catalog that actually lists matview
# columns.
psql_check "scores_as_of_column" \
  "SELECT COUNT(*) FROM pg_attribute a
   JOIN pg_class c ON a.attrelid = c.oid
   WHERE c.relname = 'mv_provider_scores'
   AND a.attname = 'scores_as_of'
   AND a.attnum > 0
   AND NOT a.attisdropped" \
  "1"

# REVERSAL is included in the mv_provider_escrow_balance formula.
# NOTE: mv_provider_escrow_balance is a MATERIALIZED VIEW, so it is catalogued
# in pg_matviews, not pg_views — pg_views only lists plain CREATE VIEW objects
# and would silently report zero rows here.
psql_check "reversal_in_balance_view" \
  "SELECT COUNT(*) FROM pg_matviews
   WHERE matviewname = 'mv_provider_escrow_balance'
   AND definition LIKE '%REVERSAL%'" \
  "1"

# owner_escrow_events table present (required for FR-014, FR-021, FR-059).
psql_check "owner_escrow_events_exists" \
  "SELECT COUNT(*) FROM information_schema.tables
   WHERE table_name = 'owner_escrow_events'" \
  "1"

# providers.first_chunk_assignment_at present (required for FR-026 120-day check).
psql_check "first_chunk_assignment_at_exists" \
  "SELECT COUNT(*) FROM information_schema.columns
   WHERE table_name = 'providers'
   AND column_name = 'first_chunk_assignment_at'" \
  "1"

# files.display_name_{ciphertext,nonce,tag} columns present (FR-019).
psql_check "display_name_columns" \
  "SELECT COUNT(*) FROM information_schema.columns
   WHERE table_name = 'files'
   AND column_name IN ('display_name_ciphertext', 'display_name_nonce', 'display_name_tag')" \
  "3"

# p95_throughput_kbps and avg_rtt_ms default to NULL, never 0 or 2000 (ADR-014/ADR-006).
psql_check "throughput_default_null" \
  "SELECT COUNT(*) FROM information_schema.columns
   WHERE table_name = 'providers'
   AND column_name IN ('p95_throughput_kbps', 'avg_rtt_ms')
   AND column_default IS NOT NULL" \
  "0"

# repair_priority ENUM has exactly three values.
psql_check "repair_priority_three_values" \
  "SELECT COUNT(*) FROM pg_enum
   WHERE enumtypid = 'repair_priority'::regtype" \
  "3"

# file_status ENUM has exactly three values.
psql_check "file_status_three_values" \
  "SELECT COUNT(*) FROM pg_enum
   WHERE enumtypid = 'file_status'::regtype" \
  "3"

# Row security is enabled on audit_receipts (Invariant 1).
psql_check "audit_receipts_rls_enabled" \
  "SELECT COUNT(*) FROM pg_tables
   WHERE tablename = 'audit_receipts' AND rowsecurity = true" \
  "1"

# Row security is enabled on escrow_events (Invariant 2).
psql_check "escrow_events_rls_enabled" \
  "SELECT COUNT(*) FROM pg_tables
   WHERE tablename = 'escrow_events' AND rowsecurity = true" \
  "1"

# mv_provider_scores has a unique index, required for REFRESH ... CONCURRENTLY.
psql_check "mv_provider_scores_unique_idx" \
  "SELECT COUNT(*) FROM pg_indexes
   WHERE tablename = 'mv_provider_scores' AND indexdef LIKE '%UNIQUE%'" \
  "1"

# ── MVP §6.4 MR-01/MR-02: demo and prod schema files have different shard_index
#    bounds. Runs only when both generated schema files are present on disk —
#    this script is also invoked against a single already-migrated database
#    where the source .sql files may not be checked out alongside it.
# [REF: MVP §6.4 MR-01, MR-02, ADR-031]
if [ -f migrations/001_initial_schema.sql ] && [ -f migrations/001_initial_schema_demo.sql ]; then
  prod_bound=$(grep "shard_index BETWEEN" migrations/001_initial_schema.sql | grep -o "AND [0-9]*" | awk '{print $2}') || true
  demo_bound=$(grep "shard_index BETWEEN" migrations/001_initial_schema_demo.sql | grep -o "AND [0-9]*" | awk '{print $2}') || true
  if [ "$prod_bound" = "55" ] && [ "$demo_bound" = "4" ]; then
    echo "PASS [profile_bound_separation]"
  else
    echo "FAIL [profile_bound_separation]: prod=$prod_bound (want 55) demo=$demo_bound (want 4)"
    FAIL=1
  fi
fi

exit $FAIL