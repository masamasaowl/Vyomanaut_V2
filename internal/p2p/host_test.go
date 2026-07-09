package p2p

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"testing"
	"time"
)

// newTestHost builds a Host with a fresh Ed25519 identity, listening on an
// OS-assigned loopback port.
func newTestHost(t *testing.T) (Host, PeerID, string) {
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

	concrete, ok := h.(*host)
	if !ok {
		t.Fatalf("newTestHost: NewHost did not return a *host")
	}
	addr := concrete.listener.Addr().String()
	return h, h.PeerID(), addr
}

// TestHostConnectAndStreamRoundTrip verifies the full path: two hosts,
// Connect (with Peer ID verification), NewStream, write/read, close.
func TestHostConnectAndStreamRoundTrip(t *testing.T) {
	serverHost, serverID, serverAddr := newTestHost(t)
	clientHost, _, _ := newTestHost(t)

	const testProtocol ProtocolID = "/vyomanaut/test-echo/1.0.0"
	received := make(chan string, 1)
	serverHost.SetStreamHandler(testProtocol, func(s Stream) {
		defer func() { _ = s.Close() }()
		buf := make([]byte, 64)
		n, err := s.Read(buf)
		if err != nil && err != io.EOF {
			t.Errorf("server read: %v", err)
			return
		}
		received <- string(buf[:n])
		_, _ = s.Write([]byte("ack:" + string(buf[:n])))
	})

	addr, err := ParseMultiaddr(fmt.Sprintf("/ip4/127.0.0.1/tcp/%s", portOf(t, serverAddr)))
	if err != nil {
		t.Fatalf("ParseMultiaddr: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := clientHost.Connect(ctx, serverID, []Multiaddr{addr}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	stream, err := clientHost.NewStream(ctx, serverID, testProtocol)
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if stream.Protocol() != testProtocol {
		t.Errorf("stream.Protocol() = %q, want %q", stream.Protocol(), testProtocol)
	}

	if _, err := stream.Write([]byte("hello")); err != nil {
		t.Fatalf("stream.Write: %v", err)
	}

	select {
	case got := <-received:
		if got != "hello" {
			t.Errorf("server received %q, want %q", got, "hello")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for server to receive data")
	}

	ackBuf := make([]byte, 64)
	n, err := stream.Read(ackBuf)
	if err != nil && err != io.EOF {
		t.Fatalf("stream.Read (ack): %v", err)
	}
	if string(ackBuf[:n]) != "ack:hello" {
		t.Errorf("client received %q, want %q", ackBuf[:n], "ack:hello")
	}
}

// TestHostConnectRejectsWrongPeerID verifies that Connect fails with
// ErrPeerIDMismatch when the address actually belongs to a different peer
// than the one requested — this is the concrete manifestation of NFR-016
// ("a provider cannot impersonate another provider's Peer ID").
func TestHostConnectRejectsWrongPeerID(t *testing.T) {
	serverHost, _, serverAddr := newTestHost(t)
	clientHost, _, _ := newTestHost(t)
	_ = serverHost

	addr, err := ParseMultiaddr(fmt.Sprintf("/ip4/127.0.0.1/tcp/%s", portOf(t, serverAddr)))
	if err != nil {
		t.Fatalf("ParseMultiaddr: %v", err)
	}

	wrongPeerID := PeerID("12D3KooWNotTheRealPeerIdAtAll00000000000000000000")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = clientHost.Connect(ctx, wrongPeerID, []Multiaddr{addr})
	if err == nil {
		t.Fatal("expected Connect to fail for a mismatched Peer ID, got nil")
	}
}

// TestHostNewStreamRequiresConnectFirst verifies NewStream fails cleanly when
// no prior Connect has established a known address for the peer.
func TestHostNewStreamRequiresConnectFirst(t *testing.T) {
	clientHost, _, _ := newTestHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	_, err := clientHost.NewStream(ctx, PeerID("12D3KooWSomePeerNeverConnectedTo00000000000000000"), "/vyomanaut/test/1.0.0")
	if err == nil {
		t.Fatal("expected NewStream to fail without a prior Connect, got nil")
	}
}

// TestZeroRTTDenyListExact documents and verifies the exact three-protocol
// deny list from IC §4 / ARCH §13, including that chunk-upload is absent.
func TestZeroRTTDenyListExact(t *testing.T) {
	mustProhibit := []ProtocolID{
		"/vyomanaut/audit-challenge/1.0.0",
		"/vyomanaut/repair-download/1.0.0",
		"/vyomanaut/vetting-gc/1.0.0",
	}
	for _, p := range mustProhibit {
		if zeroRTTPermitted(p) {
			t.Errorf("%s: expected 0-RTT prohibited, got permitted", p)
		}
	}
	if !zeroRTTPermitted("/vyomanaut/chunk-upload/1.0.0") {
		t.Error("/vyomanaut/chunk-upload/1.0.0: expected 0-RTT permitted, got prohibited")
	}
	if len(zeroRTTProhibited) != 3 {
		t.Errorf("zeroRTTProhibited has %d entries, want exactly 3", len(zeroRTTProhibited))
	}
}

// TestZeroRTTNotSuffixMatch verifies the deny-list is not suffix-based: two
// real protocol IDs that do NOT end in "-audit" or "-challenge" must still be
// prohibited, and a protocol whose name merely CONTAINS "-challenge" but is
// not exactly one of the three listed IDs must NOT be prohibited by accident.
func TestZeroRTTNotSuffixMatch(t *testing.T) {
	if zeroRTTPermitted("/vyomanaut/repair-download/1.0.0") {
		t.Error("repair-download must be prohibited despite not ending in -audit/-challenge")
	}
	if zeroRTTPermitted("/vyomanaut/vetting-gc/1.0.0") {
		t.Error("vetting-gc must be prohibited despite not ending in -audit/-challenge")
	}
	// A made-up protocol that merely contains "-challenge" mid-string but is
	// not one of the three canonical IDs must not match the explicit map.
	if !zeroRTTPermitted("/vyomanaut/pre-challenge-warmup/1.0.0") {
		t.Error("an unrelated protocol containing \"-challenge\" must not be swept in by the exact-match deny-list")
	}
}

// TestStreamRemotePeerMatchesAuthenticatedIdentity verifies that a stream's
// RemotePeer() reports the caller's true, cryptographically authenticated
// Peer ID — this is what dht.go's PUT_PROVIDER handler relies on to attribute
// a provider record to the correct peer instead of trusting a self-reported
// field in the message body.
func TestStreamRemotePeerMatchesAuthenticatedIdentity(t *testing.T) {
	serverHost, serverID, serverAddr := newTestHost(t)
	clientHost, clientID, _ := newTestHost(t)

	const testProtocol ProtocolID = "/vyomanaut/test-remote-peer/1.0.0"
	serverSawClientAs := make(chan PeerID, 1)
	serverHost.SetStreamHandler(testProtocol, func(s Stream) {
		defer func() { _ = s.Close() }()
		serverSawClientAs <- s.RemotePeer()
	})

	addr, err := ParseMultiaddr(fmt.Sprintf("/ip4/127.0.0.1/tcp/%s", portOf(t, serverAddr)))
	if err != nil {
		t.Fatalf("ParseMultiaddr: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := clientHost.Connect(ctx, serverID, []Multiaddr{addr}); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	stream, err := clientHost.NewStream(ctx, serverID, testProtocol)
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	defer func() { _ = stream.Close() }()

	if got := stream.RemotePeer(); got != serverID {
		t.Errorf("client-side stream.RemotePeer() = %q, want server ID %q", got, serverID)
	}

	select {
	case got := <-serverSawClientAs:
		if got != clientID {
			t.Errorf("server saw caller as %q, want client ID %q", got, clientID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for server handler to observe RemotePeer")
	}
}

// portOf extracts the numeric port from a "host:port" string (helper for tests).
func portOf(t *testing.T, hostport string) string {
	t.Helper()
	for i := len(hostport) - 1; i >= 0; i-- {
		if hostport[i] == ':' {
			return hostport[i+1:]
		}
	}
	t.Fatalf("portOf: no colon in %q", hostport)
	return ""
}
