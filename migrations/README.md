# Vyomanaut V2 — Database Migrations

## Migration Generator

  The schema generator produces profile-specific SQL. Always specify --profile explicitly.

    go run migrations/generator.go --profile=prod > migrations/001_initial_schema.sql
    go run migrations/generator.go --profile=demo > migrations/001_initial_schema_demo.sql

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
