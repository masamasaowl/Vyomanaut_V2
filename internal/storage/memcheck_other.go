//go:build !linux && !darwin && !windows

// Package storage is declared in doc.go.
// This file is the build-tag hygiene stub for any platform other than
// linux/darwin/windows (mirroring the existing rotational_other.go pattern
// in this package). AvailableRAMBytes returns ErrRAMCheckUnsupported so
// callers can WARN and proceed rather than fail to build or fail closed.
//
// [REF: NFR-045, ARCH §27.5, build.md Session 13.6.1 (A1)]
package storage

import "errors"

// ErrRAMCheckUnsupported is returned by AvailableRAMBytes on any platform
// without a concrete implementation. Callers (main.go's RAM guard) treat
// this the same as an unreadable value: WARN and proceed, never halt.
var ErrRAMCheckUnsupported = errors.New("storage: AvailableRAMBytes: unsupported platform")

// AvailableRAMBytes always fails on unsupported platforms.
func AvailableRAMBytes() (uint64, error) {
	return 0, ErrRAMCheckUnsupported
}
