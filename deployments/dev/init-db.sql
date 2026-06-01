-- deployments/dev/init-db.sql
-- Runs once on first Postgres container startup (docker-entrypoint-initdb.d).
-- Installs the GiST operator extension required by the audit_periods table constraint (CONSTRAINT audit_periods_no_overlap). Must exist before any migration runs.
--
-- REF: MVP §8.5
-- REF: data-model.md §migrations (extension prerequisite)
-- REF: CI workflow check-07 (migration apply gate mirrors this call)

CREATE EXTENSION IF NOT EXISTS btree_gist;