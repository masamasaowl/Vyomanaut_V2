package p2p

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
)

// TestPeerIDFromEd25519_KnownPrefix verifies that every Ed25519-derived Peer ID
// carries the well-known "12D3KooW" prefix produced by the identity-multihash
// path (fixed 0x00 0x24 multihash header for any 32-byte Ed25519 key).
func TestPeerIDFromEd25519_KnownPrefix(t *testing.T) {
	for i := 0; i < 20; i++ {
		pub, _, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		id, err := PeerIDFromEd25519PublicKey(pub)
		if err != nil {
			t.Fatalf("PeerIDFromEd25519PublicKey: %v", err)
		}
		if !strings.HasPrefix(string(id), "12D3KooW") {
			t.Errorf("iter=%d: PeerID %q does not have expected libp2p Ed25519 prefix", i, id)
		}
		if len(id) != 52 {
			t.Errorf("iter=%d: PeerID %q length = %d, want 52", i, id, len(id))
		}
	}
}

// TestPeerIDRoundTrip verifies DecodePeerIDEd25519PublicKey(PeerIDFromEd25519PublicKey(pub)) == pub.
func TestPeerIDRoundTrip(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	id, err := PeerIDFromEd25519PublicKey(pub)
	if err != nil {
		t.Fatalf("PeerIDFromEd25519PublicKey: %v", err)
	}
	recovered, err := DecodePeerIDEd25519PublicKey(id)
	if err != nil {
		t.Fatalf("DecodePeerIDEd25519PublicKey: %v", err)
	}
	if !pub.Equal(recovered) {
		t.Errorf("round-trip mismatch:\ngot  %x\nwant %x", recovered, pub)
	}
}

// TestPeerIDFromPrivateKeyMatchesPublicKey verifies the private-key-derived and
// public-key-derived Peer IDs for the same key pair are identical.
func TestPeerIDFromPrivateKeyMatchesPublicKey(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	fromPub, err := PeerIDFromEd25519PublicKey(pub)
	if err != nil {
		t.Fatalf("PeerIDFromEd25519PublicKey: %v", err)
	}
	fromPriv, err := PeerIDFromEd25519PrivateKey(priv)
	if err != nil {
		t.Fatalf("PeerIDFromEd25519PrivateKey: %v", err)
	}
	if fromPub != fromPriv {
		t.Errorf("PeerID from pubkey (%q) != PeerID from privkey (%q)", fromPub, fromPriv)
	}
}

// TestPeerIDDeterministic verifies the same key always derives the same Peer ID.
func TestPeerIDDeterministic(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	a, err := PeerIDFromEd25519PublicKey(pub)
	if err != nil {
		t.Fatalf("PeerIDFromEd25519PublicKey: %v", err)
	}
	b, err := PeerIDFromEd25519PublicKey(pub)
	if err != nil {
		t.Fatalf("PeerIDFromEd25519PublicKey: %v", err)
	}
	if a != b {
		t.Errorf("non-deterministic: %q != %q", a, b)
	}
}

// TestPeerIDNonCollision verifies different keys derive different Peer IDs.
func TestPeerIDNonCollision(t *testing.T) {
	pubA, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey A: %v", err)
	}
	pubB, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey B: %v", err)
	}
	idA, _ := PeerIDFromEd25519PublicKey(pubA)
	idB, _ := PeerIDFromEd25519PublicKey(pubB)
	if idA == idB {
		t.Errorf("two different keys produced the same PeerID: %q", idA)
	}
}

// TestBase58RoundTrip is a direct test of the base58 codec independent of the
// peer-ID-specific framing above.
func TestBase58RoundTrip(t *testing.T) {
	cases := [][]byte{
		{},
		{0x00},
		{0x00, 0x00, 0x01},
		{0xff, 0xfe, 0xfd},
		[]byte("hello vyomanaut"),
	}
	for _, c := range cases {
		enc := base58Encode(c)
		dec, err := base58Decode(enc)
		if err != nil {
			t.Fatalf("base58Decode(%x): %v", c, err)
		}
		if len(c) == 0 {
			if len(dec) != 0 {
				t.Errorf("empty input: got %x", dec)
			}
			continue
		}
		if string(dec) != string(c) {
			t.Errorf("round-trip mismatch: in=%x enc=%s out=%x", c, enc, dec)
		}
	}
}

// TestDecodePeerIDRejectsGarbage verifies decoding a non-multihash string fails
// cleanly rather than panicking.
func TestDecodePeerIDRejectsGarbage(t *testing.T) {
	_, err := DecodePeerIDEd25519PublicKey(PeerID("not-a-real-peer-id"))
	if err == nil {
		t.Error("expected error decoding garbage PeerID, got nil")
	}
}
