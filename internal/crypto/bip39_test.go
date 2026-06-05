// Package crypto is declared in doc.go.
// Unit tests for the BIP-39 mnemonic system.
//
// Tests:
//   - TestBIP39WordlistCount         wordlist contains exactly 2048 words (Session 2.6.1)
//   - TestBIP39RoundTrip             encode→decode round-trip with known-answer vectors
//   - TestBIP39KnownAnswer           exact mnemonic output matches BIP-39 reference vectors
//   - TestBIP39InvalidWordCount      23 words → ErrInvalidMnemonic
//   - TestBIP39UnknownWord           gibberish word → ErrInvalidMnemonic
//   - TestBIP39BadChecksum           valid words + wrong checksum → ErrInvalidMnemonic
//   - TestSelectConfirmationWordsUnique no equal indices in 1000 calls
//   - TestSelectConfirmationWordsInRange indices always in [0, 23]
//
// Known-answer vectors use fixed, obviously-synthetic entropy (all zeros, all ones)
// from the canonical BIP-39 reference implementations. These are NOT real account
// mnemonics. (IC §11)
//
// [REF: IC §5.1, IC §11, build.md Phase 2.6 Session 2.6.5]

package crypto

import (
	"bytes"
	"errors"
	"testing"
)

// ── Wordlist integrity ────────────────────────────────────────────────────────

// TestBIP39WordlistCount verifies that the embedded wordlist contains exactly
// 2048 entries after init() parsing.
//
// [REF: build.md Phase 2.6 Session 2.6.1]
func TestBIP39WordlistCount(t *testing.T) {
	if len(bip39Words) != bip39WordlistSize {
		t.Errorf("wordlist has %d entries, want %d", len(bip39Words), bip39WordlistSize)
	}
	if len(bip39WordMap) != bip39WordlistSize {
		t.Errorf("wordIndex has %d entries, want %d", len(bip39WordMap), bip39WordlistSize)
	}
}

// TestBIP39WordlistFirstLast verifies the first and last words in the embedded
// wordlist match the BIP-39 English specification.
func TestBIP39WordlistFirstLast(t *testing.T) {
	if bip39Words[0] != "abandon" {
		t.Errorf("word[0] = %q, want \"abandon\"", bip39Words[0])
	}
	if bip39Words[bip39WordlistSize-1] != "zoo" {
		t.Errorf("word[2047] = %q, want \"zoo\"", bip39Words[bip39WordlistSize-1])
	}
}

// ── Known-answer vectors ──────────────────────────────────────────────────────
//
// These vectors use fixed, obviously-synthetic entropy that cannot come from
// any real account. They match the output produced by canonical BIP-39
// reference implementations (Trezor python-mnemonic). (IC §11)

// katAllZeros is 32 bytes of 0x00.
var katAllZeros = [32]byte{}

// katAllFF is 32 bytes of 0xFF.
var katAllFF = [32]byte{
	0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
	0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
}

// katAllZerosMnemonic is the expected mnemonic for 32 zero bytes.
// SHA-256(all zeros)[0] = 0x66; last word index = 102 = "art".
var katAllZerosMnemonic = []string{
	"abandon", "abandon", "abandon", "abandon", "abandon", "abandon",
	"abandon", "abandon", "abandon", "abandon", "abandon", "abandon",
	"abandon", "abandon", "abandon", "abandon", "abandon", "abandon",
	"abandon", "abandon", "abandon", "abandon", "abandon", "art",
}

// katAllFFMnemonic is the expected mnemonic for 32 bytes of 0xFF.
// SHA-256(all 0xff)[0] = 0xaf; last word index = 1967 = "vote".
var katAllFFMnemonic = []string{
	"zoo", "zoo", "zoo", "zoo", "zoo", "zoo",
	"zoo", "zoo", "zoo", "zoo", "zoo", "zoo",
	"zoo", "zoo", "zoo", "zoo", "zoo", "zoo",
	"zoo", "zoo", "zoo", "zoo", "zoo", "vote",
}

// TestBIP39KnownAnswer verifies that MasterSecretToMnemonic produces the
// exact expected words for canonical BIP-39 test vectors.
//
// [REF: build.md Phase 2.6 Session 2.6.5, IC §11]
func TestBIP39KnownAnswer(t *testing.T) {
	vectors := []struct {
		name     string
		entropy  [32]byte
		expected []string
	}{
		{"all-zeros", katAllZeros, katAllZerosMnemonic},
		{"all-0xFF", katAllFF, katAllFFMnemonic},
	}

	for _, v := range vectors {
		v := v
		t.Run(v.name, func(t *testing.T) {
			got, err := MasterSecretToMnemonic(v.entropy)
			if err != nil {
				t.Fatalf("MasterSecretToMnemonic: %v", err)
			}
			if len(got) != bip39MnemonicLen {
				t.Fatalf("got %d words, want %d", len(got), bip39MnemonicLen)
			}
			for i, w := range got {
				if w != v.expected[i] {
					t.Errorf("word[%d] = %q, want %q (full: %v)", i, w, v.expected[i], got)
					break
				}
			}
		})
	}
}

// ── Round-trip tests ──────────────────────────────────────────────────────────

// TestBIP39RoundTrip verifies the round-trip identity for known BIP-39 vectors:
//
//	MnemonicToMasterSecret(MasterSecretToMnemonic(ms)) == ms
//
// [REF: IC §5.1 post-condition, build.md Phase 2.6 Session 2.6.5]
func TestBIP39RoundTrip(t *testing.T) {
	vectors := []struct {
		name    string
		entropy [32]byte
	}{
		{"all-zeros", katAllZeros},
		{"all-0xFF", katAllFF},
	}

	for _, v := range vectors {
		v := v
		t.Run(v.name, func(t *testing.T) {
			// Encode
			words, err := MasterSecretToMnemonic(v.entropy)
			if err != nil {
				t.Fatalf("MasterSecretToMnemonic: %v", err)
			}
			if len(words) != bip39MnemonicLen {
				t.Fatalf("got %d words, want %d", len(words), bip39MnemonicLen)
			}

			// Decode
			recovered, err := MnemonicToMasterSecret(words)
			if err != nil {
				t.Fatalf("MnemonicToMasterSecret: %v", err)
			}

			if !bytes.Equal(recovered[:], v.entropy[:]) {
				t.Errorf("round-trip failed:\ngot  %x\nwant %x", recovered, v.entropy)
			}
		})
	}
}

// TestBIP39RoundTripArbitrary verifies round-trip for an arbitrary (non-trivial)
// entropy value. Uses a deterministic test input derived from incremental bytes.
func TestBIP39RoundTripArbitrary(t *testing.T) {
	var entropy [32]byte
	for i := range entropy {
		entropy[i] = byte(i)
	}

	words, err := MasterSecretToMnemonic(entropy)
	if err != nil {
		t.Fatalf("MasterSecretToMnemonic: %v", err)
	}

	recovered, err := MnemonicToMasterSecret(words)
	if err != nil {
		t.Fatalf("MnemonicToMasterSecret: %v", err)
	}

	if !bytes.Equal(recovered[:], entropy[:]) {
		t.Errorf("arbitrary entropy round-trip failed:\ngot  %x\nwant %x", recovered, entropy)
	}
}

// ── Error case tests ──────────────────────────────────────────────────────────

// TestBIP39InvalidWordCount verifies that MnemonicToMasterSecret returns
// ErrInvalidMnemonic when given fewer than 24 words.
//
// [REF: build.md Phase 2.6 Session 2.6.5]
func TestBIP39InvalidWordCount(t *testing.T) {
	words := make([]string, 23) // one short
	for i := range words {
		words[i] = "abandon"
	}
	_, err := MnemonicToMasterSecret(words)
	if !errors.Is(err, ErrInvalidMnemonic) {
		t.Errorf("expected ErrInvalidMnemonic for 23-word input, got %v", err)
	}
}

// TestBIP39TooManyWords verifies ErrInvalidMnemonic for 25 words.
func TestBIP39TooManyWords(t *testing.T) {
	words := make([]string, 25)
	for i := range words {
		words[i] = "abandon"
	}
	_, err := MnemonicToMasterSecret(words)
	if !errors.Is(err, ErrInvalidMnemonic) {
		t.Errorf("expected ErrInvalidMnemonic for 25-word input, got %v", err)
	}
}

// TestBIP39UnknownWord verifies that a word not in the BIP-39 wordlist causes
// ErrInvalidMnemonic.
//
// [REF: build.md Phase 2.6 Session 2.6.5]
func TestBIP39UnknownWord(t *testing.T) {
	words := make([]string, bip39MnemonicLen)
	for i := range words {
		words[i] = "abandon"
	}
	words[5] = "notaword" // not in any BIP-39 wordlist

	_, err := MnemonicToMasterSecret(words)
	if !errors.Is(err, ErrInvalidMnemonic) {
		t.Errorf("expected ErrInvalidMnemonic for unknown word, got %v", err)
	}
}

// TestBIP39UnknownWordAllPositions verifies that an unknown word at ANY position
// returns ErrInvalidMnemonic (no early-return position leakage).
func TestBIP39UnknownWordAllPositions(t *testing.T) {
	for pos := 0; pos < bip39MnemonicLen; pos++ {
		words := make([]string, bip39MnemonicLen)
		for i := range words {
			words[i] = "abandon"
		}
		words[pos] = "zzzznotaword"

		_, err := MnemonicToMasterSecret(words)
		if !errors.Is(err, ErrInvalidMnemonic) {
			t.Errorf("pos=%d: expected ErrInvalidMnemonic, got %v", pos, err)
		}
	}
}

// TestBIP39BadChecksum verifies that 24 valid BIP-39 words that fail the
// SHA-256 checksum validation return ErrInvalidMnemonic.
//
// "abandon" × 24 encodes entropy = 32 zero bytes with checksum bits = 0x00,
// but SHA-256(32 zeros)[0] = 0x66 ≠ 0x00, so the checksum fails.
//
// [REF: build.md Phase 2.6 Session 2.6.5]
func TestBIP39BadChecksum(t *testing.T) {
	// "abandon" (index 0) repeated 24 times encodes 264 bits of zeros.
	// Entropy portion = 32 zeros, but the checksum byte that was encoded is 0x00,
	// while SHA-256(32 zeros)[0] = 0x66, so checksum verification must fail.
	words := make([]string, bip39MnemonicLen)
	for i := range words {
		words[i] = "abandon"
	}
	_, err := MnemonicToMasterSecret(words)
	if !errors.Is(err, ErrInvalidMnemonic) {
		t.Errorf("expected ErrInvalidMnemonic for bad checksum, got %v", err)
	}
}

// ── SelectConfirmationWords tests ─────────────────────────────────────────────

// TestSelectConfirmationWordsUnique verifies that SelectConfirmationWords never
// returns equal indices across 1000 successive calls.
//
// [REF: IC §5.1 post-condition, build.md Phase 2.6 Session 2.6.5]
func TestSelectConfirmationWordsUnique(t *testing.T) {
	mnemonic := make([]string, bip39MnemonicLen)
	for i := range mnemonic {
		mnemonic[i] = "abandon"
	}

	for iter := 0; iter < 1000; iter++ {
		a, b := SelectConfirmationWords(mnemonic)
		if a == b {
			t.Fatalf("iter=%d: SelectConfirmationWords returned equal indices a=%d b=%d",
				iter, a, b)
		}
	}
}

// TestSelectConfirmationWordsInRange verifies that both returned indices are
// always in [0, bip39MnemonicLen-1].
func TestSelectConfirmationWordsInRange(t *testing.T) {
	mnemonic := make([]string, bip39MnemonicLen)
	for i := range mnemonic {
		mnemonic[i] = "abandon"
	}

	for iter := 0; iter < 200; iter++ {
		a, b := SelectConfirmationWords(mnemonic)
		if a < 0 || a >= bip39MnemonicLen {
			t.Errorf("iter=%d: indexA=%d out of range [0,%d)", iter, a, bip39MnemonicLen)
		}
		if b < 0 || b >= bip39MnemonicLen {
			t.Errorf("iter=%d: indexB=%d out of range [0,%d)", iter, b, bip39MnemonicLen)
		}
	}
}

// TestSelectConfirmationWordsPanicOnBadMnemonic verifies that a mnemonic with
// the wrong number of words causes a panic (pre-condition violation).
func TestSelectConfirmationWordsPanicOnBadMnemonic(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on 12-word mnemonic, got none")
		}
	}()
	SelectConfirmationWords(make([]string, 12))
}

// ── Decode of known-answer mnemonics ─────────────────────────────────────────

// TestBIP39DecodeKnownMnemonics verifies that decoding the known-answer
// mnemonics produces the original entropy (inverse of TestBIP39KnownAnswer).
func TestBIP39DecodeKnownMnemonics(t *testing.T) {
	t.Run("all-zeros-mnemonic", func(t *testing.T) {
		got, err := MnemonicToMasterSecret(katAllZerosMnemonic)
		if err != nil {
			t.Fatalf("MnemonicToMasterSecret: %v", err)
		}
		if !bytes.Equal(got[:], katAllZeros[:]) {
			t.Errorf("decoded secret mismatch:\ngot  %x\nwant %x", got, katAllZeros)
		}
	})

	t.Run("all-0xFF-mnemonic", func(t *testing.T) {
		got, err := MnemonicToMasterSecret(katAllFFMnemonic)
		if err != nil {
			t.Fatalf("MnemonicToMasterSecret: %v", err)
		}
		if !bytes.Equal(got[:], katAllFF[:]) {
			t.Errorf("decoded secret mismatch:\ngot  %x\nwant %x", got, katAllFF)
		}
	})
}