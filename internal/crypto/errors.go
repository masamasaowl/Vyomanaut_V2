// Package crypto is declared in doc.go.
// This file defines all sentinel errors exported by the crypto package.
// Callers must compare using errors.Is; never construct these values inline.
//
// [REF: IC §5.1, ADR-019, ADR-022, build.md Phase 2.4 Session 2.4.3]

package crypto

import "errors"

// Sentinel errors exported by this package.
var (
	// ErrTagMismatch is returned by DecryptPointerFile when the Poly1305
	// authentication tag does not verify. Callers must not use any returned
	// bytes when this error is signalled.
	// [REF: IC §5.1, ADR-019, NFR-019]
	ErrTagMismatch = errors.New("crypto: Poly1305 tag verification failed")

	// Returned by AONTDecodePackage when the canary word in the decrypted output
	// does not match the expected aontCanary value — indicating a corrupt package,
	// bit-flip, or wrong cipher path. Callers must not return any plaintext to
	// the data owner when this error is signalled; the decode buffer is zeroed
	// before the error is returned. [REF: IC §5.1, ADR-022]
	ErrCanaryMismatch = errors.New("crypto: AONT canary word mismatch after decode")

	// ErrInvalidMnemonic is returned by MnemonicToMasterSecret when the word
	// count is wrong, a word is not in the BIP-39 English wordlist, or the
	// BIP-39 checksum fails. Callers must surface a generic "Invalid recovery
	// phrase" message — do not expose which word failed (timing oracle).
	// [REF: IC §5.1, FR-004]
	ErrInvalidMnemonic = errors.New("crypto: invalid BIP-39 mnemonic")
)
