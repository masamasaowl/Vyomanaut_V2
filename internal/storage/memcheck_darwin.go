//go:build darwin

// Package storage is declared in doc.go.
// This file implements AvailableRAMBytes for macOS by shelling out to
// `vm_stat`, the standard macOS utility reporting page-granularity memory
// statistics. A raw cgo binding against <mach/mach.h> host_statistics64
// would avoid the subprocess, but this codebase already established a
// no-cgo, subprocess-based pattern for platform-specific system queries it
// cannot exercise from this Linux build/CI environment (see this session's
// build report — darwin/windows cross-compilation could not be verified
// here) — exec.Command needs no OS-specific cgo toolchain to at least
// *compile* cross-platform, unlike a cgo call into Mach APIs would.
//
// [REF: NFR-045, ARCH §27.5, build.md Session 13.6.1 (A1)]
package storage

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// AvailableRAMBytes returns the currently free/available RAM in bytes,
// computed as (free + inactive) pages × page size — inactive pages are
// reclaimable without swapping, so they count as "available" the same way
// Linux's MemAvailable does.
func AvailableRAMBytes() (uint64, error) {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0, fmt.Errorf("storage: AvailableRAMBytes: vm_stat: %w", err)
	}

	pageSize := uint64(4096) // vm_stat's default; overridden below if stated explicitly
	var freePages, inactivePages uint64

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, "Mach Virtual Memory Statistics:"):
			// e.g. "Mach Virtual Memory Statistics: (page size of 16384 bytes)"
			if idx := strings.Index(line, "page size of "); idx >= 0 {
				rest := line[idx+len("page size of "):]
				fields := strings.Fields(rest)
				if len(fields) > 0 {
					if v, err := strconv.ParseUint(fields[0], 10, 64); err == nil {
						pageSize = v
					}
				}
			}
		case strings.HasPrefix(line, "Pages free:"):
			freePages = parseVMStatCount(line)
		case strings.HasPrefix(line, "Pages inactive:"):
			inactivePages = parseVMStatCount(line)
		}
	}

	if freePages == 0 && inactivePages == 0 {
		return 0, fmt.Errorf("storage: AvailableRAMBytes: could not parse vm_stat output")
	}
	return (freePages + inactivePages) * pageSize, nil
}

// parseVMStatCount extracts the trailing numeric page count from a vm_stat
// line of the form "Pages free:                              12345.".
func parseVMStatCount(line string) uint64 {
	parts := strings.Split(line, ":")
	if len(parts) < 2 {
		return 0
	}
	numeric := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(parts[1]), "."))
	v, err := strconv.ParseUint(numeric, 10, 64)
	if err != nil {
		return 0
	}
	return v
}
