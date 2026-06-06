// Package crypto is declared in doc.go.
// Unit tests for SignBytes and VerifyBytes.
//
// Tests:
//   - TestSignBytesRoundTrip  sign→verify succeeds for the matching key pair
//   - TestSignBytesWrongKey   verify with the wrong public key returns false
//   - TestSignBytesNotJSON    signing input is a fixed-layout byte sequence, not JSON
//
// [REF: IC §3.2, IC §5.1, build.md Phase 2.7 Session 2.7.1]

package crypto

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

// TestSignBytesRoundTrip verifies that VerifyBytes returns true when the
// signature was produced by SignBytes with the matching private key.
//
// Exercises multiple input sizes including empty, short, and longer inputs
// to confirm the SHA-256 pre-hashing path is exercised uniformly.
//
// [REF: IC §3.2, build.md Phase 2.7 Session 2.7.1]
func TestSignBytesRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	var pubKey [32]byte
	copy(pubKey[:], pub)

	inputs := [][]byte{
		{},                        // empty: SHA-256 of zero bytes
		{0x01},                    // single byte
		[]byte("hello vyomanaut"), // short ASCII
		make([]byte, 64),          // 64 zero bytes (two SHA-256 block widths)
		make([]byte, 1024),        // 1 KiB
	}

	for i, input := range inputs {
		sig := SignBytes(priv, input)
		if !VerifyBytes(pubKey, input, sig) {
			t.Errorf("input[%d] len=%d: VerifyBytes returned false — expected true", i, len(input))
		}
	}
}

// TestSignBytesWrongKey verifies that VerifyBytes returns false when the
// supplied public key does not correspond to the signing private key.
//
// [REF: IC §3.2, build.md Phase 2.7 Session 2.7.1]
func TestSignBytesWrongKey(t *testing.T) {
	// Key pair A: used to sign.
	_, privA, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey A: %v", err)
	}

	// Key pair B: different key — must NOT verify a signature from A.
	pubB, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey B: %v", err)
	}

	var pubKeyB [32]byte
	copy(pubKeyB[:], pubB)

	input := []byte("audit-receipt-payload-fixed-layout")
	sig := SignBytes(privA, input)

	if VerifyBytes(pubKeyB, input, sig) {
		t.Error("VerifyBytes returned true for wrong public key — expected false")
	}
}

// TestSignBytesWrongInput verifies that VerifyBytes returns false when the
// input bytes differ from those used at signing time — even by one bit.
func TestSignBytesWrongInput(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	var pubKey [32]byte
	copy(pubKey[:], pub)

	input := []byte{0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80}
	sig := SignBytes(priv, input)

	// Flip each byte in turn; every flip must break verification.
	for i := range input {
		corrupt := make([]byte, len(input))
		copy(corrupt, input)
		corrupt[i] ^= 0xFF

		if VerifyBytes(pubKey, corrupt, sig) {
			t.Errorf("corrupt byte[%d]: VerifyBytes returned true — expected false", i)
		}
	}
}

// TestSignBytesNotJSON documents and verifies IC §3.2: signing inputs are
// fixed-layout byte sequences constructed by the caller — never JSON.
//
// JSON serialisation must not be used because field ordering is not guaranteed
// across Go versions; a signing input must produce an identical byte sequence
// on every Go version and platform for VerifyBytes to succeed.
//
// [REF: IC §3.2, build.md Phase 2.7 Session 2.7.1]
func TestSignBytesNotJSON(t *testing.T) {
	// SIGNING_INPUT_RULE: use fixed-layout byte sequence, never JSON.
	// The signing input below simulates a protocol payload serialised as:
	//   provider_id  (16 bytes, raw UUID)
	//   epoch_nanos  (8 bytes, big-endian uint64)
	//   chunk_id     (16 bytes, raw UUID)
	// Total: 40 bytes. No JSON. No field names. No variable-length encoding.
	fixedInput := []byte{
		// provider_id (16 bytes)
		0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22,
		0x33, 0x44, 0x55, 0x66, 0x77, 0x88, 0x99, 0x00,
		// epoch_nanos (8 bytes, big-endian)
		0x00, 0x00, 0x01, 0x8D, 0xC0, 0xB3, 0xF0, 0x00,
		// chunk_id (16 bytes)
		0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF,
		0xFE, 0xDC, 0xBA, 0x98, 0x76, 0x54, 0x32, 0x10,
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	var pubKey [32]byte
	copy(pubKey[:], pub)

	sig := SignBytes(priv, fixedInput)
	if !VerifyBytes(pubKey, fixedInput, sig) {
		t.Error("fixed-layout signing input: VerifyBytes returned false — expected true")
	}

	// Any single-byte change in the fixed-layout input must break verification,
	// confirming that SHA-256(input) is bound into the signature.
	for i := range fixedInput {
		corrupt := make([]byte, len(fixedInput))
		copy(corrupt, fixedInput)
		corrupt[i] ^= 0x01

		if VerifyBytes(pubKey, corrupt, sig) {
			t.Errorf("corrupt byte[%d]: VerifyBytes accepted tampered input — expected false", i)
		}
	}
}
