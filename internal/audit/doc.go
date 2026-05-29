/*
Package audit manages the audit challenge lifecycle on the microservice side.

Provides the microservice-side audit challenge generation, dispatch, cluster secret, receipt validation, and the two-phase crash-safe database write. Goroutine-safe.

Ref: ADR-002, ADR-015, ADR-017, ADR-027
*/
package audit