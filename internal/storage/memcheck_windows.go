//go:build windows

// Package storage is declared in doc.go.
// This file implements AvailableRAMBytes for Windows via
// GlobalMemoryStatusEx (kernel32.dll), called through syscall.LazyDLL so
// this file needs no dependency beyond the standard library — deliberately
// avoiding a dependency on golang.org/x/sys/windows's exact vendored
// surface, which this session cannot verify against a real Windows
// toolchain in this Linux build/CI environment (see this session's build
// report).
//
// [REF: NFR-045, ARCH §27.5, build.md Session 13.6.1 (A1)]
package storage

import (
	"fmt"
	"syscall"
	"unsafe"
)

// memoryStatusEx mirrors the Win32 MEMORYSTATUSEX structure exactly
// (field order and sizes matter — this is passed by pointer to a syscall).
type memoryStatusEx struct {
	dwLength                uint32
	dwMemoryLoad            uint32
	ullTotalPhys            uint64
	ullAvailPhys            uint64
	ullTotalPageFile        uint64
	ullAvailPageFile        uint64
	ullTotalVirtual         uint64
	ullAvailVirtual         uint64
	ullAvailExtendedVirtual uint64
}

var (
	modkernel32              = syscall.NewLazyDLL("kernel32.dll")
	procGlobalMemoryStatusEx = modkernel32.NewProc("GlobalMemoryStatusEx")
)

// AvailableRAMBytes returns the currently free/available physical RAM in
// bytes, via GlobalMemoryStatusEx's ullAvailPhys field.
func AvailableRAMBytes() (uint64, error) {
	var status memoryStatusEx
	status.dwLength = uint32(unsafe.Sizeof(status))

	ret, _, err := procGlobalMemoryStatusEx.Call(uintptr(unsafe.Pointer(&status)))
	if ret == 0 {
		return 0, fmt.Errorf("storage: AvailableRAMBytes: GlobalMemoryStatusEx: %w", err)
	}
	return status.ullAvailPhys, nil
}
