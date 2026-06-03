//go:build !amd64

package crypto

// DetectAESNI always returns false on non-amd64 platforms.
func DetectAESNI() bool { return false }
