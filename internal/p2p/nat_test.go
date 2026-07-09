package p2p

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"testing"
	"time"
)

// TestHolePunchRetryCount documents and locks in the ARCH §13 constant: 1
// retry, not the naive default of 3.
func TestHolePunchRetryCount(t *testing.T) {
	if maxHolePunchRetries != 1 {
		t.Errorf("maxHolePunchRetries = %d, want 1 (ARCH §13: 97.6%% succeed on first attempt)", maxHolePunchRetries)
	}
}

// TestRelayReservationTTL documents and locks in the IC §4.3 constant.
func TestRelayReservationTTL(t *testing.T) {
	if relayReservationTTL != 30*time.Minute {
		t.Errorf("relayReservationTTL = %v, want 30m", relayReservationTTL)
	}
}

// TestRelayMaxConcurrentReservations documents the per-node cap from ARCH §13
// (128/node, 384 total across the three launch nodes).
func TestRelayMaxConcurrentReservations(t *testing.T) {
	if relayMaxConcurrentReservations != 128 {
		t.Errorf("relayMaxConcurrentReservations = %d, want 128", relayMaxConcurrentReservations)
	}
}

// TestHolePunchSucceedsAgainstListeningPeer verifies HolePunch succeeds
// (first attempt) when the target is actually listening and reachable —
// the common case cone-NAT peers hit after DCUtR coordination.
func TestHolePunchSucceedsAgainstListeningPeer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	result := HolePunch(ctx, ln.Addr().String())
	if !result.Succeeded {
		t.Error("expected HolePunch to succeed against a live listener")
	}
	if result.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1 (first-attempt success)", result.Attempts)
	}
}

// TestHolePunchFailsAndRespectsRetryBudget verifies HolePunch against an
// address nothing is listening on fails after exactly
// maxHolePunchRetries+1 attempts, never more.
func TestHolePunchFailsAndRespectsRetryBudget(t *testing.T) {
	// Bind and immediately close to get a port that is very likely free but
	// guaranteed to have nothing listening.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	result := HolePunch(ctx, addr)
	if result.Succeeded {
		t.Skip("port was reused by another process during the test; inconclusive")
	}
	if result.Attempts != maxHolePunchRetries+1 {
		t.Errorf("Attempts = %d, want %d (maxHolePunchRetries+1)", result.Attempts, maxHolePunchRetries+1)
	}
}

// TestProbeReachabilityNoListener verifies a host with no listener (e.g. a
// client-only microservice caller) is classified Private, never Public.
func TestProbeReachabilityNoListener(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	h, err := NewHost(HostConfig{PrivateKey: priv}) // no ListenAddr
	if err != nil {
		t.Fatalf("NewHost: %v", err)
	}
	defer func() { _ = h.Close() }()

	concrete, ok := h.(*host)
	if !ok {
		t.Fatalf("TestProbeReachabilityNoListener: NewHost did not return a *host")
	}
	status := probeReachability(concrete, nil)
	if status != NATStatusPrivate {
		t.Errorf("probeReachability with no listener = %v, want %v", status, NATStatusPrivate)
	}
}

// TestSetupNATNoHelpersIsNoop verifies SetupNAT does not panic or block when
// no reachability helpers are configured, and leaves NATType at Unknown.
func TestSetupNATNoHelpersIsNoop(t *testing.T) {
	h, _, _ := newTestHost(t)
	if err := SetupNAT(h, NATConfig{}); err != nil {
		t.Fatalf("SetupNAT: %v", err)
	}
	if got := h.NATType(); got != NATStatusUnknown {
		t.Errorf("NATType() = %v, want %v when no helpers configured", got, NATStatusUnknown)
	}
}

// TestSetupNATClassifiesPublicViaHelper verifies SetupNAT reaches
// NATStatusPublic when a reachable helper address is supplied and the host
// itself has a live listener.
func TestSetupNATClassifiesPublicViaHelper(t *testing.T) {
	h, _, addr := newTestHost(t)

	helperAddr, err := ParseMultiaddr("/ip4/127.0.0.1/tcp/" + portOf(t, addr))
	if err != nil {
		t.Fatalf("ParseMultiaddr: %v", err)
	}

	if err := SetupNAT(h, NATConfig{ReachabilityHelpers: []Multiaddr{helperAddr}}); err != nil {
		t.Fatalf("SetupNAT: %v", err)
	}
	if got := h.NATType(); got != NATStatusPublic {
		t.Errorf("NATType() = %v, want %v", got, NATStatusPublic)
	}
}

// TestRelayReserveAndForwardRoundTrip spins up a minimal fake relay speaking
// this package's tiny VRL1 protocol and verifies ReserveRelaySlot and
// DialViaRelay both complete their handshakes against it.
func TestRelayReserveAndForwardRoundTrip(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer func() { _ = ln.Close() }()

	go fakeRelay(t, ln)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	resv, err := ReserveRelaySlot(ctx, ln.Addr().String(), PeerID("12D3KooWSelfTestPeerId0000000000000000000000000000"))
	if err != nil {
		t.Fatalf("ReserveRelaySlot: %v", err)
	}
	if resv.RelayAddr != ln.Addr().String() {
		t.Errorf("RelayAddr = %q, want %q", resv.RelayAddr, ln.Addr().String())
	}

	conn, err := DialViaRelay(ctx, ln.Addr().String(), PeerID("12D3KooWTargetTestPeerId000000000000000000000000000"))
	if err != nil {
		t.Fatalf("DialViaRelay: %v", err)
	}
	_ = conn.Close()
}

// fakeRelay is a minimal test double for a Vyomanaut relay node: it accepts
// exactly the VRL1 request framing ReserveRelaySlot/DialViaRelay produce and
// always acknowledges OK, for one connection at a time.
func fakeRelay(t *testing.T, ln net.Listener) {
	t.Helper()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer func() { _ = c.Close() }()
			prefix := make([]byte, 4)
			if _, err := readFull(c, prefix); err != nil {
				return
			}
			kind := make([]byte, 1)
			if _, err := readFull(c, kind); err != nil {
				return
			}
			idLen := make([]byte, 1)
			if _, err := readFull(c, idLen); err != nil {
				return
			}
			idBuf := make([]byte, idLen[0])
			if _, err := readFull(c, idBuf); err != nil {
				return
			}
			_, _ = c.Write([]byte{negotiationAckOK})
		}(conn)
	}
}
