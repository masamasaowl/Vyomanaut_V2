//go:build linux

// Package storage is declared in doc.go.
// This file implements AvailableRAMBytes for Linux, reading the kernel's own
// MemAvailable estimate from /proc/meminfo — a better free-RAM figure than
// MemFree alone since it accounts for reclaimable page cache/buffers the
// kernel would actually surrender under memory pressure.
//
// [REF: NFR-045, ARCH §27.5, build.md Session 13.6.1 (A1)]
package storage

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// AvailableRAMBytes returns the currently free/available RAM in bytes.
func AvailableRAMBytes() (uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("storage: AvailableRAMBytes: %w", err)
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "MemAvailable:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0, fmt.Errorf("storage: AvailableRAMBytes: malformed MemAvailable line %q", line)
		}
		kb, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return 0, fmt.Errorf("storage: AvailableRAMBytes: parse MemAvailable: %w", err)
		}
		return kb * 1024, nil
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("storage: AvailableRAMBytes: scanning /proc/meminfo: %w", err)
	}
	return 0, fmt.Errorf("storage: AvailableRAMBytes: MemAvailable not found in /proc/meminfo")
}
