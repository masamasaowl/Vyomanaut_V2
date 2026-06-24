//go:build linux

// Package storage is declared in doc.go.

package storage

import (
	"os"
	"strings"
)

// isRotationalDevice reports whether the block device backing path is a
// rotational HDD. Reads /sys/block/<dev>/queue/rotational: "1" = HDD,
// "0" = SSD (ARCH §16 §rotational.go).
//
// Failure (missing sysfs entry, permission error, non-block device) returns
// false — the conservative SSD assumption. Used for monitoring and GC
// scheduling only; core vLog logic is storage-medium-agnostic.
//
// TODO(M5): resolve the actual block device name from path via os.Stat and
// /proc/mounts instead of the hardcoded "sda" fallback below.
func isRotationalDevice(path string) bool {
	_ = path // used by the TODO above; suppresses unused-parameter lint until resolved
	data, err := os.ReadFile("/sys/block/sda/queue/rotational")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "1"
}
