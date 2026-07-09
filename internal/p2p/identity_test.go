package p2p

import (
	"bytes"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

var testOwnerID = [16]byte{
	0xa0, 0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7,
	0xa8, 0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf,
}

var testMasterSecret = [32]byte{
	0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07,
	0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f,
	0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17,
	0x18, 0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f,
}

// TestLoadOrGenerateIdentity_FirstLaunchThenReload verifies that a fresh
// dataDir generates a new identity, and a second call against the same
// dataDir loads back the SAME key and Peer ID rather than generating a new one.
func TestLoadOrGenerateIdentity_FirstLaunchThenReload(t *testing.T) {
	dir := t.TempDir()

	priv1, id1, err := LoadOrGenerateIdentity(dir, testMasterSecret, testOwnerID[:])
	if err != nil {
		t.Fatalf("first LoadOrGenerateIdentity: %v", err)
	}
	if len(priv1) != ed25519.PrivateKeySize {
		t.Fatalf("priv1 length = %d, want %d", len(priv1), ed25519.PrivateKeySize)
	}

	priv2, id2, err := LoadOrGenerateIdentity(dir, testMasterSecret, testOwnerID[:])
	if err != nil {
		t.Fatalf("second LoadOrGenerateIdentity (reload): %v", err)
	}

	if !bytes.Equal(priv1, priv2) {
		t.Error("reloaded private key differs from the originally generated key")
	}
	if id1 != id2 {
		t.Errorf("reloaded Peer ID %q differs from original %q", id2, id1)
	}

	// Sanity: the Peer ID actually corresponds to this key's public half.
	wantID, err := PeerIDFromEd25519PrivateKey(priv1)
	if err != nil {
		t.Fatalf("PeerIDFromEd25519PrivateKey: %v", err)
	}
	if id1 != wantID {
		t.Errorf("returned Peer ID %q does not match derivation from the returned key (%q)", id1, wantID)
	}
}

// TestIdentityFileIsEncryptedAtRest verifies that the raw 64-byte Ed25519
// private key never appears verbatim in the on-disk identity file.
func TestIdentityFileIsEncryptedAtRest(t *testing.T) {
	dir := t.TempDir()
	priv, _, err := LoadOrGenerateIdentity(dir, testMasterSecret, testOwnerID[:])
	if err != nil {
		t.Fatalf("LoadOrGenerateIdentity: %v", err)
	}

	blob, err := os.ReadFile(filepath.Join(dir, identityFileName))
	if err != nil {
		t.Fatalf("read identity file: %v", err)
	}

	if bytes.Contains(blob, priv) {
		t.Error("raw private key bytes found verbatim in the on-disk identity file — must be encrypted")
	}
	// nonce(12) + ciphertext(64 priv key bytes + 16-byte Poly1305 tag) = 92 bytes.
	wantLen := 12 + len(priv) + 16
	if len(blob) != wantLen {
		t.Errorf("identity file length = %d, want %d (12-byte nonce + %d-byte key + 16-byte tag)",
			len(blob), wantLen, len(priv))
	}
}

// TestIdentityFilePermissions verifies the identity file is created with
// mode 0600 (owner-only read/write) — skipped on Windows, where POSIX file
// mode bits are not meaningful.
func TestIdentityFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file permissions are not meaningful on Windows")
	}
	dir := t.TempDir()
	if _, _, err := LoadOrGenerateIdentity(dir, testMasterSecret, testOwnerID[:]); err != nil {
		t.Fatalf("LoadOrGenerateIdentity: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, identityFileName))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("identity file mode = %o, want 0600", perm)
	}
}

// TestLoadOrGenerateIdentity_WrongOwnerIDLength verifies the pre-condition
// check on ownerID length.
func TestLoadOrGenerateIdentity_WrongOwnerIDLength(t *testing.T) {
	dir := t.TempDir()
	_, _, err := LoadOrGenerateIdentity(dir, testMasterSecret, []byte("too-short"))
	if err == nil {
		t.Error("expected error for wrong-length ownerID, got nil")
	}
}

// TestLoadOrGenerateIdentity_WrongMasterSecretFailsDecrypt verifies that
// loading an existing identity file with the wrong master secret fails
// (rather than silently returning a wrong key) — the AEAD tag check does
// this for free, but it is worth locking in as an explicit regression test
// since a silent wrong-key return would be a serious identity-confusion bug.
func TestLoadOrGenerateIdentity_WrongMasterSecretFailsDecrypt(t *testing.T) {
	dir := t.TempDir()
	if _, _, err := LoadOrGenerateIdentity(dir, testMasterSecret, testOwnerID[:]); err != nil {
		t.Fatalf("LoadOrGenerateIdentity (create): %v", err)
	}

	var wrongSecret [32]byte
	copy(wrongSecret[:], bytes.Repeat([]byte{0xFF}, 32))

	_, _, err := LoadOrGenerateIdentity(dir, wrongSecret, testOwnerID[:])
	if err == nil {
		t.Error("expected error loading identity with the wrong master secret, got nil")
	}
}

// TestTwoDataDirsProduceDifferentIdentities verifies identities are per-daemon
// (per dataDir), not accidentally shared or deterministic from
// masterSecret+ownerID alone (generation uses fresh crypto/rand entropy).
func TestTwoDataDirsProduceDifferentIdentities(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	_, idA, err := LoadOrGenerateIdentity(dirA, testMasterSecret, testOwnerID[:])
	if err != nil {
		t.Fatalf("dirA: %v", err)
	}
	_, idB, err := LoadOrGenerateIdentity(dirB, testMasterSecret, testOwnerID[:])
	if err != nil {
		t.Fatalf("dirB: %v", err)
	}
	if idA == idB {
		t.Error("two independently generated identities collided — crypto/rand entropy is not being used")
	}
}
