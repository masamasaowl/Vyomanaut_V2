//go:build debug

// Package repair is declared in doc.go.
// This file (and its !debug counterpart, debug_off.go) provides buildDebug as
// a compile-time constant so EnqueueJob's second-line-of-defence panic
// (build.md Phase 9.1 Session 9.1.1, ADR-030, DM §3 Invariant 6) exists only
// in debug builds — `go build -tags debug` — and compiles away to nothing
// (dead-code-eliminated) in ordinary release builds, where the departure
// handler's own IsVettingChunk pre-check (Session 9.1.3) is the sole,
// non-panicking line of defence.

package repair

const buildDebug = true
