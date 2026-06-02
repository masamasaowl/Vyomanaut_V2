//go:build amd64

package crypto

import "golang.org/x/sys/cpu"

// DetectAESNI reports whether the CPU supports AES-NI instructions.
// Called exactly once at daemon startup (cmd/provider/main.go); result
// stored in a local variable and passed as a parameter to all
// AONTEncodeSegment and AONTDecodePackage calls.
// Never re-checked at runtime (IC §5.1).
func DetectAESNI() bool {
	return cpu.X86.HasAES
}
