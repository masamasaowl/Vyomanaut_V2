/*
Package repair manages the repair job queue and orchestrates lazy repair. Goroutine-safe.

Components:
  - Departure detector
  - Repair job queue
  - Repair executor

Ref: ADR-004, ADR-014
*/
package repair
