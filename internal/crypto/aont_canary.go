// Package crypto is declared in doc.go.
// This file defines the fixed AONT canary word used by AONTEncodeSegment and
// AONTDecodePackage. The canary is the last plaintext word appended to every
// segment before AONT encryption; its presence is verified after decryption to
// confirm the all-or-nothing property holds (IC §5.1, ARCH §10 Stage 1).
//
// [REF: IC §5.1, ADR-022, build.md Phase 2.4 Session 2.4.1]

package crypto

// aontCanary is the fixed 16-byte canary word appended to every AONT plaintext
// before encryption. Its value is the first 16 bytes of
// SHA-256("vyomanaut-aont-canary-v1"), computed once offline and hardcoded here.
//
// This value is an on-disk format commitment: changing it would silently corrupt
// all existing AONT packages. Do not reassign.
//
// canary must never be changed — it is an on-disk format commitment.
//
// [REF: IC §5.1, ADR-022]
var aontCanary = [16]byte{ //nolint:gochecknoglobals // canary is a format constant, not mutable state
	0x16, 0x14, 0x38, 0x2e, 0x7a, 0x0b, 0x48, 0xc4,
	0xe2, 0xc7, 0x42, 0x13, 0x03, 0x5f, 0xbc, 0x64,
}
