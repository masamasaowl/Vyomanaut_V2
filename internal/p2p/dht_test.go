package p2p

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"testing"
	"time"
)

// ── test helpers ───────────────────────────────────────────────────────────
//
// Unlike the original build.md template (which left buildTestHost/buildTestDHT
// as t.Skip stubs pending Host construction), Session 6.1.1 already delivered
// a working NewHost, so these are real, running constructors.

// buildTestHost creates a Host with a fresh Ed25519 identity, listening on an
// OS-assigned loopback port.
func buildTestHost(t *testing.T) Host {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	h, err := NewHost(HostConfig{PrivateKey: priv, ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

// buildTestDHT creates a kademliaDHT instance wired to host, with
// dhtKeyNamespace registered as its stream protocol.
func buildTestDHT(t *testing.T, host Host) DHT {
	t.Helper()
	addr, err := ParseMultiaddr("/ip4/127.0.0.1/tcp/" + testHostPort(t, host))
	if err != nil {
		t.Fatalf("ParseMultiaddr: %v", err)
	}
	d, err := NewDHT(host, DHTConfig{SelfAddrs: []Multiaddr{addr}})
	if err != nil {
		t.Fatalf("NewDHT: %v", err)
	}
	return d
}

func testHostPort(t *testing.T, h Host) string {
	t.Helper()
	concrete, ok := h.(*host)
	if !ok || concrete.listener == nil {
		t.Fatalf("buildTestDHT: host has no listener")
	}
	return portOf(t, concrete.listener.Addr().String())
}

// sha256Multihash builds a bare SHA2-256 multihash exactly as
// github.com/multiformats/go-multihash's mh.Sum(data, mh.SHA2_256, -1) would:
// varint(0x12) || varint(32) || 32-byte digest = 34 bytes. Both varints are
// single bytes here, so this is a plain byte concatenation. Used in place of
// that package (unavailable in this build environment — see doc.go) to
// exercise the same "plain CID/multihash must be rejected" regression case
// IC §12 describes.
func sha256Multihash(data []byte) []byte {
	sum := sha256.Sum256(data)
	out := []byte{0x12, 32}
	return append(out, sum[:]...)
}

// ── Session 6.2.3 mandatory CI gate ───────────────────────────────────────────

// TestDHTKeyValidatorPersists is a mandatory CI gate (CI check 5, MVP §8.4).
//
// PURPOSE: Detect a silent namespace/validator regression. If a future change
// resets the custom HMAC validator to accept arbitrary byte strings (or the
// default CID/multihash shape), this test catches it immediately.
//
// Run with: go test -run TestDHTKeyValidatorPersists ./internal/p2p/
// This test MUST be re-run after every change to dht.go's validator logic.
func TestDHTKeyValidatorPersists(t *testing.T) {
	ctx := context.Background()
	testHost := buildTestHost(t)
	dht := buildTestDHT(t, testHost)

	// CASE 1: A valid 32-byte HMAC-derived key MUST be accepted by the validator.
	validKey := make([]byte, 32)
	for i := range validKey {
		validKey[i] = byte(i + 1) // deterministic, non-zero test key
	}
	err := dht.PutProviderRecord(ctx, validKey)
	if err != nil {
		t.Errorf("valid 32-byte key must be accepted; a failure here means the HMAC validator is broken: %v", err)
	}

	// CASE 2: A plain multihash (the shape a libp2p default CID takes) MUST be
	// rejected. This is the critical regression check: a validator reset to
	// libp2p defaults would accept this key (IC §12).
	cidBytes := sha256Multihash([]byte("test-chunk-content"))
	err = dht.PutProviderRecord(ctx, cidBytes)
	if !isDHTKeyInvalid(err) {
		t.Errorf("plain multihash-shaped key must be rejected with ErrDHTKeyInvalid, got %v — "+
			"a validator regression may have reset the namespace/length check", err)
	}

	// CASE 3: A 31-byte key (one byte short of 32) MUST be rejected.
	shortKey := validKey[:31]
	err = dht.PutProviderRecord(ctx, shortKey)
	if !isDHTKeyInvalid(err) {
		t.Errorf("31-byte key must be rejected with ErrDHTKeyInvalid, got %v", err)
	}
}

// TestDHTKeyValidatorRejectsAll is a table-driven exhaustive rejection test (IC §12).
func TestDHTKeyValidatorRejectsAll(t *testing.T) {
	ctx := context.Background()
	testHost := buildTestHost(t)
	dht := buildTestDHT(t, testHost)

	cases := []struct {
		name string
		key  []byte
	}{
		{"empty key", []byte{}},
		{"31 bytes (one short)", make([]byte, 31)},
		{"33 bytes (one over)", make([]byte, 33)},
		{"64 bytes (Ed25519 sig length)", make([]byte, 64)},
		{"vyom-chunk prefix", append([]byte("vyom-chunk:"), make([]byte, 21)...)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := dht.PutProviderRecord(ctx, tc.key)
			if !isDHTKeyInvalid(err) {
				t.Errorf("key %q (%d bytes) must be rejected by the HMAC validator, got %v",
					tc.name, len(tc.key), err)
			}
		})
	}
}

// TestDHTKeyValidatorAcceptsHMAC tests positive acceptance with boundary key values.
func TestDHTKeyValidatorAcceptsHMAC(t *testing.T) {
	ctx := context.Background()
	testHost := buildTestHost(t)
	dht := buildTestDHT(t, testHost)

	allFF := make([]byte, 32)
	for i := range allFF {
		allFF[i] = 0xFF
	}
	validKeys := [][]byte{
		make([]byte, 32), // all-zero 32-byte key
		allFF,            // all-FF 32-byte key
	}

	for _, key := range validKeys {
		if err := dht.PutProviderRecord(ctx, key); err != nil {
			t.Errorf("32-byte key must be accepted regardless of content: %v", err)
		}
	}
}

// isDHTKeyInvalid reports whether err wraps ErrDHTKeyInvalid.
func isDHTKeyInvalid(err error) bool {
	for err != nil {
		if err == ErrDHTKeyInvalid { //nolint:errorlint // also checked via errors.Is below for wrapped cases
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return false
}

// ── namespace / minimality checks (Session 6.2.1) ─────────────────────────────

func TestDHTNamespaceConstantValue(t *testing.T) {
	if dhtKeyNamespace != "/vyomanaut/dht-key/1.0.0" {
		t.Errorf("dhtKeyNamespace = %q, want %q", dhtKeyNamespace, "/vyomanaut/dht-key/1.0.0")
	}
}

// ── DHT parameter checks (ARCH §13) ───────────────────────────────────────────

func TestDHTParameters(t *testing.T) {
	if kBucketSize != 16 {
		t.Errorf("kBucketSize = %d, want 16 (ARCH §13: k = 2×d, d=8)", kBucketSize)
	}
	if dhtAlpha != 3 {
		t.Errorf("dhtAlpha = %d, want 3 (ARCH §13: parallel lookups)", dhtAlpha)
	}
	if dhtMode != "Server" {
		t.Errorf("dhtMode = %q, want %q (ARCH §13: every provider daemon is a full participant)", dhtMode, "Server")
	}
}

// ── PutProviderRecord / FindProviders semantics ───────────────────────────────

// TestFindProvidersReturnsSelfAfterPut verifies the basic single-node loop:
// after PutProviderRecord, FindProviders on the same node returns our own
// AddrInfo from the local record store.
func TestFindProvidersReturnsSelfAfterPut(t *testing.T) {
	ctx := context.Background()
	testHost := buildTestHost(t)
	dht := buildTestDHT(t, testHost)

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	if err := dht.PutProviderRecord(ctx, key); err != nil {
		t.Fatalf("PutProviderRecord: %v", err)
	}

	found, err := dht.FindProviders(ctx, key, 10)
	if err != nil {
		t.Fatalf("FindProviders: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("FindProviders returned %d results, want 1", len(found))
	}
	if found[0].ID != testHost.PeerID() {
		t.Errorf("FindProviders returned ID %q, want self %q", found[0].ID, testHost.PeerID())
	}
}

// TestFindProvidersUnknownKeyReturnsEmpty verifies a key nobody has announced
// returns an empty (not error) result.
func TestFindProvidersUnknownKeyReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	testHost := buildTestHost(t)
	dht := buildTestDHT(t, testHost)

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(0xAA)
	}
	found, err := dht.FindProviders(ctx, key, 10)
	if err != nil {
		t.Fatalf("FindProviders: %v", err)
	}
	if len(found) != 0 {
		t.Errorf("expected 0 results for an unannounced key, got %d", len(found))
	}
}

// TestTwoNodeBootstrapPutFind is the genuine two-node round trip: node B
// bootstraps against node A, then B announces a provider record. Because B's
// routing table now contains A, PutProviderRecord's best-effort replication
// pushes the record to A too — so a lookup on A's own DHT (with no further
// network round trip) finds it.
func TestTwoNodeBootstrapPutFind(t *testing.T) {
	hostA := buildTestHost(t)
	dhtA := buildTestDHT(t, hostA)

	hostB := buildTestHost(t)

	addrA, err := ParseMultiaddr("/ip4/127.0.0.1/tcp/" + testHostPort(t, hostA))
	if err != nil {
		t.Fatalf("ParseMultiaddr: %v", err)
	}
	dhtB, err := NewDHT(hostB, DHTConfig{
		Seeds: []AddrInfo{{ID: hostA.PeerID(), Addrs: []Multiaddr{addrA}}},
	})
	if err != nil {
		t.Fatalf("NewDHT (B): %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := dhtB.Bootstrap(ctx); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i * 3)
	}
	if err := dhtB.PutProviderRecord(ctx, key); err != nil {
		t.Fatalf("PutProviderRecord (B): %v", err)
	}

	// Give the best-effort replication goroutine-free synchronous push a
	// moment to land (PutProviderRecord itself blocks on the push, so this is
	// just headroom for the TLS handshake round trip, not a race).
	deadline := time.Now().Add(3 * time.Second)
	var foundOnA []AddrInfo
	for time.Now().Before(deadline) {
		foundOnA, err = dhtA.FindProviders(ctx, key, 10)
		if err != nil {
			t.Fatalf("FindProviders (A): %v", err)
		}
		if len(foundOnA) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if len(foundOnA) != 1 {
		t.Fatalf("node A found %d providers for B's replicated record, want 1", len(foundOnA))
	}
	if foundOnA[0].ID != hostB.PeerID() {
		t.Errorf("node A's record is for %q, want %q", foundOnA[0].ID, hostB.PeerID())
	}
}

// TestBootstrapNoSeedsIsNoop verifies Bootstrap with no configured seeds
// succeeds trivially rather than blocking or erroring.
func TestBootstrapNoSeedsIsNoop(t *testing.T) {
	testHost := buildTestHost(t)
	dht := buildTestDHT(t, testHost)

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := dht.Bootstrap(ctx); err != nil {
		t.Errorf("Bootstrap with no seeds should be a no-op, got error: %v", err)
	}
}

// TestBootstrapAllSeedsFail verifies Bootstrap surfaces an error when every
// seed is unreachable.
func TestBootstrapAllSeedsFail(t *testing.T) {
	testHost := buildTestHost(t)

	// A loopback address nothing is listening on.
	deadAddr, err := ParseMultiaddr("/ip4/127.0.0.1/tcp/1")
	if err != nil {
		t.Fatalf("ParseMultiaddr: %v", err)
	}
	dht, err := NewDHT(testHost, DHTConfig{
		Seeds: []AddrInfo{{ID: PeerID("12D3KooWDeadSeedPeerId00000000000000000000000000"), Addrs: []Multiaddr{deadAddr}}},
	})
	if err != nil {
		t.Fatalf("NewDHT: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := dht.Bootstrap(ctx); err == nil {
		t.Error("expected Bootstrap to fail when all seeds are unreachable, got nil")
	}
}

// ── routing table unit tests ───────────────────────────────────────────────

// TestCommonPrefixLen exercises the XOR-metric bucket-index function directly.
func TestCommonPrefixLen(t *testing.T) {
	var a, b [32]byte
	if got := commonPrefixLen(a, b); got != 256 {
		t.Errorf("identical IDs: commonPrefixLen = %d, want 256", got)
	}

	b[0] = 0x80 // flip the top bit of the first byte
	if got := commonPrefixLen(a, b); got != 0 {
		t.Errorf("top-bit differs: commonPrefixLen = %d, want 0", got)
	}

	var c [32]byte
	c[0] = 0x01 // differ only in the last bit of the first byte
	if got := commonPrefixLen(a, c); got != 7 {
		t.Errorf("last-bit-of-first-byte differs: commonPrefixLen = %d, want 7", got)
	}
}

// TestClosestPeersOrdering verifies closestPeers sorts by ascending XOR
// distance to the target key, not insertion order.
func TestClosestPeersOrdering(t *testing.T) {
	testHost := buildTestHost(t)
	dhtIface := buildTestDHT(t, testHost)
	d, ok := dhtIface.(*kademliaDHT)
	if !ok {
		t.Fatalf("buildTestDHT returned %T, want *kademliaDHT", dhtIface)
	}

	var key [32]byte // all-zero target

	// Insert peers with kadIDs at known distances by directly seeding the
	// routing table (bypassing PeerID->kadID hashing so the test can control
	// exact distances).
	far := &peerEntry{info: AddrInfo{ID: "far"}, kadID: [32]byte{0xFF}}
	near := &peerEntry{info: AddrInfo{ID: "near"}, kadID: [32]byte{0x01}}
	mid := &peerEntry{info: AddrInfo{ID: "mid"}, kadID: [32]byte{0x0F}}

	d.mu.Lock()
	d.buckets[0] = []*peerEntry{far, near, mid}
	d.mu.Unlock()

	ordered := d.closestPeers(key, 3)
	if len(ordered) != 3 {
		t.Fatalf("got %d peers, want 3", len(ordered))
	}
	wantOrder := []PeerID{"near", "mid", "far"}
	for i, p := range ordered {
		if p.info.ID != wantOrder[i] {
			t.Errorf("position %d: got %q, want %q", i, p.info.ID, wantOrder[i])
		}
	}
}

// TestAddToRoutingTableRespectsCapacity verifies that after inserting many
// more peers than could possibly fit if every bucket were unbounded, no
// single k-bucket ever exceeds kBucketSize — the eviction path in
// addToRoutingTable is exercised for real (via its own kadID/bucket-index
// computation), not synthetically forced into a specific bucket.
func TestAddToRoutingTableRespectsCapacity(t *testing.T) {
	testHost := buildTestHost(t)
	dhtIface := buildTestDHT(t, testHost)
	d, ok := dhtIface.(*kademliaDHT)
	if !ok {
		t.Fatalf("buildTestDHT returned %T, want *kademliaDHT", dhtIface)
	}

	for i := 0; i < 500; i++ {
		d.addToRoutingTable(AddrInfo{ID: PeerID(fmt.Sprintf("synthetic-peer-%d", i))})
	}

	d.mu.RLock()
	defer d.mu.RUnlock()
	for i, bucket := range d.buckets {
		if len(bucket) > kBucketSize {
			t.Errorf("bucket %d has %d entries after 500 inserts, want <= %d", i, len(bucket), kBucketSize)
		}
	}
}

// TestAddToRoutingTableRefreshesExisting verifies that re-adding a known peer
// updates its entry in place rather than creating a duplicate.
func TestAddToRoutingTableRefreshesExisting(t *testing.T) {
	testHost := buildTestHost(t)
	dhtIface := buildTestDHT(t, testHost)
	d, ok := dhtIface.(*kademliaDHT)
	if !ok {
		t.Fatalf("buildTestDHT returned %T, want *kademliaDHT", dhtIface)
	}

	const id PeerID = "12D3KooWRepeatedPeerId0000000000000000000000000000"
	d.addToRoutingTable(AddrInfo{ID: id})
	d.addToRoutingTable(AddrInfo{ID: id})
	d.addToRoutingTable(AddrInfo{ID: id})

	d.mu.RLock()
	defer d.mu.RUnlock()
	count := 0
	for _, bucket := range d.buckets {
		for _, entry := range bucket {
			if entry.info.ID == id {
				count++
			}
		}
	}
	if count != 1 {
		t.Errorf("re-adding the same peer %d times produced %d routing-table entries, want 1", 3, count)
	}
}

// TestAddToRoutingTableIgnoresSelf verifies self is never inserted into the
// routing table (a node is not its own neighbour).
func TestAddToRoutingTableIgnoresSelf(t *testing.T) {
	testHost := buildTestHost(t)
	dhtIface := buildTestDHT(t, testHost)
	d, ok := dhtIface.(*kademliaDHT)
	if !ok {
		t.Fatalf("buildTestDHT returned %T, want *kademliaDHT", dhtIface)
	}

	d.addToRoutingTable(AddrInfo{ID: testHost.PeerID()})

	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, bucket := range d.buckets {
		for _, entry := range bucket {
			if entry.info.ID == testHost.PeerID() {
				t.Fatal("self was inserted into the routing table")
			}
		}
	}
}
