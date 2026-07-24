# Vyomanaut V2 — Database Migrations

## Migration Generator

  The schema generator produces profile-specific SQL. Always specify --profile explicitly.

    go run migrations/generator.go --profile=prod > migrations/001_initial_schema.sql
    go run migrations/generator.go --profile=demo > migrations/001_initial_schema_demo.sql

## Roles & who runs the migration (ADR-032)

The migration must be applied by **`vyomanaut_migrator`** — the schema owner, which
holds `BYPASSRLS` (or is a superuser). In dev/CI this is the bootstrap
`POSTGRES_USER`; in prod the DBA provisions it. It is **not** created by the
migration (a migration cannot create the role that runs it).

The migration itself creates the two request-path roles as
`LOGIN NOSUPERUSER NOBYPASSRLS`:

- **`vyomanaut_app`** — the microservice hot path. Subject to `FORCE` RLS; granted
  `SELECT/INSERT/UPDATE` (never `DELETE`) per least privilege.
- **`vyomanaut_gc`** — the garbage collector. Abandons stale `PENDING` receipts.

Set their passwords from your secrets store after applying (`ALTER ROLE … PASSWORD`);
never commit passwords. The migration aborts if either role has `SUPERUSER` or
`BYPASSRLS`. MV refresh, partition creation, and archival all run as
`vyomanaut_migrator`.

## Partitioning & archival (ADR-033)

`audit_receipts` is `PARTITION BY RANGE (server_challenge_ts)` with a `DEFAULT`
partition. Create next month's partition ahead of demand and archive old months by
**`DETACH`** (never `DELETE` — Invariant 1):

    SELECT vyomanaut_create_audit_receipts_partition((date_trunc('month', now()) + interval '1 month')::date);
    ALTER TABLE audit_receipts DETACH PARTITION audit_receipts_2026_01;  -- after cold-storage export

Global nonce uniqueness (replay protection) lives in `audit_receipt_nonces`; the app
writes the nonce there in the same transaction as the receipt.

## Rules (DM §9, MVP §6.4)

- Never apply a demo-profile schema to a production database.
- The generator embeds `-- Generated for profile: {prod|demo}` as the first SQL comment.
- Two CHECK constraints are profile-variable (shard_index range, available_shard_count range).
- All other DDL (RSPs, ENUMs, indexes, triggers) is profile-invariant.
- migrations/ requires 3 reviewers per .github/CODEOWNERS.
- Migration files: NNN_description.sql (zero-padded 3 digits, sequential, no gaps).
- Never edit a committed migration; write a new one.

## Checklist

  Run scripts/ci/migration_check.sh after every migration apply to verify DM §9 invariants.
