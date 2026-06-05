// Package crypto is declared in doc.go.
// This file implements the BIP-39 mnemonic system for Vyomanaut V2.
//
// The mnemonic IS the master secret expressed as 24 English words — it is not derived from the master secret. BIP-39 §From mnemonic to seed is not used; Vyomanaut never applies a passphrase on top of the mnemonic.
//
// Encoding path (MasterSecretToMnemonic):
//   32-byte entropy → SHA-256 checksum (8 bits) → 264-bit stream → 24 × 11-bit groups → 24 words
//
// Decoding path (MnemonicToMasterSecret):
//   24 words → 24 × 11-bit indices → 264-bit stream → verify checksum → 32-byte entropy
//
// INVARIANT (IC §11): BIP-39 mnemonic words for any real account must never appear in source files. Only RFC/BIP test vectors with fixed entropy are
// permitted in tests.
//
// TIMING (IC §5.1): MnemonicToMasterSecret processes all 24 words before returning any error so callers cannot determine which word failed by timing.
//
// [REF: IC §5.1, MVP §3.5, build.md Phase 2.6 Sessions 2.6.1–2.6.4]

package crypto

import (
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"fmt"
	"strings"
)

// ── BIP-39 constants ─────────────────────────────────────────────────────────

const (
	// bip39WordlistSize is the number of entries in the BIP-39 English wordlist (2^11).
	bip39WordlistSize = 2048

	// bip39WordBits is the bit width of each BIP-39 word index (log₂ of wordlist size).
	bip39WordBits = 11

	// bip39MnemonicLen is the number of words in a 256-bit BIP-39 mnemonic.
	// 264 total bits / 11 bits per word = 24 words.
	bip39MnemonicLen = 24

	// bip39EntropyBytes is the master-secret byte length (256-bit entropy).
	bip39EntropyBytes = 32

	// bip39ChecksumBits is the number of checksum bits for 256-bit entropy (= 256/32).
	bip39ChecksumBits = 8

	// bip39TotalBytes is the size of the packed encoding buffer in bytes.
	// 256 bits entropy + 8 bits checksum = 264 bits = 33 bytes.
	bip39TotalBytes = 33

	// bip39RandCeiling is the exclusive upper bound for the rejection-sampling
	// loop in SelectConfirmationWords: the largest multiple of bip39MnemonicLen
	// that fits in one byte (10 × 24 = 240). Bytes ≥ 240 are discarded to
	// eliminate modulo bias.
	bip39RandCeiling = 10 * bip39MnemonicLen
)

// ── Embedded wordlist ─────────────────────────────────────────────────────────

//go:embed wordlist_en.txt
var wordlistRaw string //nolint:gochecknoglobals // BIP-39 English wordlist; read-only after init

// bip39Words maps a 0-based index (0–2047) to the BIP-39 English word at that
// position. Populated once in init(); never mutated afterwards.
var bip39Words [bip39WordlistSize]string //nolint:gochecknoglobals

// bip39WordMap maps a BIP-39 English word to its 0-based index (0–2047).
// Used for O(1) decoding in MnemonicToMasterSecret.
// Populated once in init(); never mutated afterwards.
var bip39WordMap map[string]int //nolint:gochecknoglobals

func init() {
	lines := strings.Split(strings.TrimSpace(wordlistRaw), "\n")
	if len(lines) != bip39WordlistSize {
		panic(fmt.Sprintf(
			"crypto: BIP-39 wordlist has %d entries, want %d",
			len(lines), bip39WordlistSize))
	}
	bip39WordMap = make(map[string]int, bip39WordlistSize)
	for i, w := range lines {
		w = strings.TrimSpace(w)
		bip39Words[i] = w
		bip39WordMap[w] = i
	}
}

// ── Internal bit-manipulation helpers ────────────────────────────────────────

// bip39ExtractBits reads count bits from b starting at bit position start,
// treating b as a big-endian bit string (bit 0 is the MSB of b[0]).
// Returns the bits packed into an int, MSB first.
func bip39ExtractBits(b []byte, start, count int) int {
	result := 0
	for i := 0; i < count; i++ {
		bitPos := start + i
		byteIdx := bitPos / 8
		bitOff := 7 - (bitPos % 8) // MSB within a byte has offset 7
		result = (result << 1) | (int(b[byteIdx])>>uint(bitOff))&1
	}
	return result
}

// bip39SetBits stores the lower count bits of value into b starting at bit
// position start, MSB first. b must be pre-zeroed; SetBits only ORs in 1-bits.
func bip39SetBits(b []byte, start, count, value int) {
	for i := 0; i < count; i++ {
		if (value>>uint(count-1-i))&1 == 1 {
			bitPos := start + i
			byteIdx := bitPos / 8
			bitOff := 7 - (bitPos % 8)
			b[byteIdx] |= byte(1 << uint(bitOff))
		}
	}
}

// ── Exported functions ────────────────────────────────────────────────────────

// MasterSecretToMnemonic encodes the 32-byte master secret as a BIP-39 24-word
// mnemonic phrase. The mnemonic IS the master secret expressed as English words
// — it is not derived from the master secret; it encodes it directly.
//
// Algorithm (BIP-39 §Generating the mnemonic, 256-bit path):
//  1. Compute checksum = SHA-256(entropy), take the first 8 bits.
//  2. Concatenate: 256 bits entropy ‖ 8 bits checksum = 264 bits.
//  3. Split into 24 groups of 11 bits. Each group is a word-list index.
//
// Pre-conditions (panic on violation):
//   - none (the [32]byte type already enforces the length constraint)
//
// Post-conditions:
//   - returns exactly 24 lowercase English words from the BIP-39 wordlist
//   - MnemonicToMasterSecret(result) == masterSecret  (round-trip identity)
//
// Error semantics: returns error only if the wordlist is missing or corrupt
// (caught by init(); treat as fatal startup condition if it ever occurs).
// Goroutine-safe: yes (pure function, reads only immutable package state).
//
// [REF: IC §5.1, build.md Phase 2.6 Session 2.6.2]
func MasterSecretToMnemonic(masterSecret [32]byte) ([]string, error) {
	checksum := sha256.Sum256(masterSecret[:])

	// Pack entropy (32 bytes) and first checksum byte into a 33-byte buffer.
	var bits [bip39TotalBytes]byte
	copy(bits[:bip39EntropyBytes], masterSecret[:])
	bits[bip39EntropyBytes] = checksum[0]

	words := make([]string, bip39MnemonicLen)
	for i := 0; i < bip39MnemonicLen; i++ {
		idx := bip39ExtractBits(bits[:], i*bip39WordBits, bip39WordBits)
		words[i] = bip39Words[idx]
	}
	return words, nil
}

// MnemonicToMasterSecret recovers the 32-byte master secret from a 24-word
// BIP-39 mnemonic. This is the recovery path for owners who have lost their
// passphrase but retained their mnemonic backup. (FR-004)
//
// CRITICAL (IC §5.1 — timing oracle): all 24 word lookups are performed
// unconditionally before any error is returned. Do not add an early-return
// inside the loop — that would allow callers to infer which word position
// failed by measuring elapsed time.
//
// Pre-conditions:
//   - len(words) == 24
//   - all words are entries in the BIP-39 English wordlist
//
// Post-conditions (on nil error):
//   - returned masterSecret is the 32-byte value encoded by this mnemonic
//   - MasterSecretToMnemonic(result) == words  (round-trip identity)
//
// Error semantics:
//   - ErrInvalidMnemonic: wrong word count, unknown word, or BIP-39 checksum failure.
//     Surface "Invalid recovery phrase" to the user — never expose which word failed.
//
// Goroutine-safe: yes (pure function, reads only immutable package state).
//
// [REF: IC §5.1, build.md Phase 2.6 Session 2.6.3]
func MnemonicToMasterSecret(words []string) ([32]byte, error) {
	var zero [32]byte

	if len(words) != bip39MnemonicLen {
		return zero, ErrInvalidMnemonic
	}

	// Look up all 24 words unconditionally — no early return on missing word.
	// This prevents timing-based leakage of which position is invalid.
	indices := make([]int, bip39MnemonicLen)
	invalidFound := false
	for i, w := range words {
		idx, ok := bip39WordMap[strings.ToLower(w)]
		if !ok {
			invalidFound = true
			// Do NOT return here — continue so all 24 lookups always run.
		} else {
			indices[i] = idx
		}
	}
	if invalidFound {
		return zero, ErrInvalidMnemonic
	}

	// Reconstruct the 264-bit stream from 24 × 11-bit indices.
	var bits [bip39TotalBytes]byte // pre-zeroed by Go
	for i, idx := range indices {
		bip39SetBits(bits[:], i*bip39WordBits, bip39WordBits, idx)
	}

	// Extract entropy (first 32 bytes) and verify the BIP-39 checksum.
	var entropy [32]byte
	copy(entropy[:], bits[:bip39EntropyBytes])

	expectedChecksum := sha256.Sum256(entropy[:])
	if bits[bip39EntropyBytes] != expectedChecksum[0] {
		return zero, ErrInvalidMnemonic
	}

	return entropy, nil
}

// SelectConfirmationWords returns two distinct random word indices (0–23) for
// the mnemonic confirmation gate. The UI prompts the data owner to type the
// words at these positions before allowing the registration flow to proceed.
// (FR-003, MVP §3.5)
//
// In demo mode (profile.SkipMnemonicConfirm == true), the mnemonic is displayed
// but the caller skips prompting — this function may still be called; the caller
// simply does not block on the result (IC §5.1 note).
//
// Rejection sampling eliminates modulo bias: only byte values in [0, 240)
// are accepted (240 = 10 × bip39MnemonicLen is the largest multiple of 24
// that fits in one byte). The expected number of draws per index is 256/240 ≈ 1.07.
//
// Pre-conditions (panic on violation):
//   - len(mnemonic) == 24
//
// Post-conditions:
//   - 0 ≤ indexA, indexB ≤ 23
//   - indexA ≠ indexB
//   - indices are drawn via crypto/rand
//
// Goroutine-safe: yes (no shared mutable state).
//
// [REF: IC §5.1, MVP §3.5, build.md Phase 2.6 Session 2.6.4]
func SelectConfirmationWords(mnemonic []string) (indexA, indexB int) {
	if len(mnemonic) != bip39MnemonicLen {
		panic(fmt.Sprintf(
			"crypto.SelectConfirmationWords: mnemonic must have %d words, got %d",
			bip39MnemonicLen, len(mnemonic)))
	}

	buf := make([]byte, 1)
	// randIdx returns a uniform random value in [0, bip39MnemonicLen) via
	// rejection sampling: discard values ≥ bip39RandCeiling (= 240) to avoid
	// modulo bias (256 % 24 = 16 ≠ 0).
	randIdx := func() int {
		for {
			if _, err := rand.Read(buf); err != nil {
				panic(fmt.Sprintf(
					"crypto.SelectConfirmationWords: crypto/rand failure: %v", err))
			}
			if int(buf[0]) < bip39RandCeiling {
				return int(buf[0]) % bip39MnemonicLen
			}
		}
	}

	indexA = randIdx()
	for {
		indexB = randIdx()
		if indexB != indexA {
			return
		}
	}
}
