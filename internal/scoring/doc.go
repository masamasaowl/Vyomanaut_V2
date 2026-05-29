/*
Package scoring computes per-provider reliability scores from the audit_receipts table. Scores are non-I-confluent (floor ≥ 0 constraint) and must be computed by a single authoritative scorer instance. Read-only against the database. Goroutine-safe.

Components:
  - Three-window reliability score
  - Consecutive-pass counter
  - EWMA RTO

Ref: ADR-008, ADR-013
*/
package scoring
