//go:build !linux

// Package storage is declared in doc.go.

package storage

// isRotationalDevice always returns false (assume SSD) on non-Linux platforms.
// Block device sysfs is Linux-specific; for Darwin/Windows development builds
// the conservative SSD assumption is safe — the value is used for monitoring
// and GC scheduling only.
func isRotationalDevice(_ string) bool { return false }
