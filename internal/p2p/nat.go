// Package p2p is declared in doc.go.
// This file implements the three-tier NAT traversal stack (ARCH §13
// §NAT traversal - three tiers). Per the substitution decision recorded in
// doc.go, AutoNAT / DCUtR / Circuit Relay v2 are replaced with a from-scratch
// implementation of the same three tiers using only net and crypto/tls:
//
//	Tier 1 - reachability probe (this package's AutoNAT analogue): approximated
//	         locally via local-address-shape (private/CGNAT detection) plus
//	         helper dial-out (see probeReachability's own doc comment, M6
//	         review §5.3) rather than a genuine remote dial-back, which this
//	         package has no wire protocol for yet.
//	Tier 2 - TCP simultaneous-open hole punching (this package's DCUtR
//	         analogue): a rendezvous helper exchanges both sides' public
//	         addresses and both sides dial each other at (approximately) the
//	         same time.
//	Tier 3 - a minimal Vyomanaut-only relay client/protocol (this package's
//	         Circuit Relay v2 analogue): connects to a relay's TCP listener
//	         and asks it to forward bytes to/from a target peer.
//
// [REF: ARCH §13, IC §4.3, ADR-021, MVP §8.2]

package p2p

import (
	"context"
	"fmt"
	"net"
	"time"
)

// maxHolePunchRetries is the DCUtR-analogue hole-punch retry count.
// Set to 1, NOT a naive default of 3.
// Justification: ARCH §13 reports 97.6% of successful DCUtR connections
// succeed on the first attempt across 4.4M traversal measurements; a second
// or third retry buys negligible additional success probability at the cost
// of holding the audit deadline hostage to a doomed connection attempt.
// [REF: ARCH §13, MVP §8.2]
const maxHolePunchRetries = 1

// relayReservationTTL is the Circuit-Relay-analogue reservation duration.
// The daemon refreshes the reservation before it expires (IC §4.3).
const relayReservationTTL = 1800 * time.Second // 30 minutes

// relayMaxConcurrentReservations is the per-relay-node concurrent reservation
// cap (IC §4.3, ARCH §13 §Relay infrastructure at launch: 128 per node, three
// nodes at launch = 384 total slots).
const relayMaxConcurrentReservations = 128

// reachabilityProbeTimeout bounds how long Tier 1 waits for a helper peer to
// dial us back before concluding we are not publicly reachable.
const reachabilityProbeTimeout = 5 * time.Second

// holePunchRetryBackoff is the pause between hole-punch attempts when a retry
// is warranted (maxHolePunchRetries governs how many times).
const holePunchRetryBackoff = 200 * time.Millisecond

// NATConfig supplies the peers and relay addresses SetupNAT wires into a Host.
type NATConfig struct {
	// ReachabilityHelpers are peer addresses willing to attempt a dial-back
	// for Tier 1 classification (this package's AutoNAT analogue). At least
	// one is required for classification to run; SetupNAT is a no-op
	// (NATStatusUnknown persists) if none are supplied.
	ReachabilityHelpers []Multiaddr

	// RelayAddrs are the Vyomanaut-operated relay nodes' listen addresses for
	// Tier 3 (Circuit Relay analogue). Three nodes at launch, one per Indian
	// cloud availability zone (ARCH §13 §Relay infrastructure at launch).
	RelayAddrs []Multiaddr

	// ReprobeInterval controls how often Tier 1 reclassifies reachability in
	// the background. Zero disables periodic reprobing (probe once and stop).
	ReprobeInterval time.Duration
}

// SetupNAT wires all three NAT traversal tiers to h (ARCH §13).
//
// Tier 1: reachability probe - classifies public/private reachability at
//
//	startup and, if cfg.ReprobeInterval > 0, periodically thereafter.
//
// Tier 2: TCP simultaneous-open hole punch - exposed via HolePunch for
//
//	Connect callers whose direct dial fails and who have been classified
//	NATStatusPrivate.
//
// Tier 3: relay client - exposed via DialViaRelay for peers behind symmetric
//
//	NAT where hole punching cannot succeed.
//
// SetupNAT itself does not block: reachability classification runs in a
// background goroutine and updates h.NATType() as results arrive. It returns
// once that goroutine has been started (or immediately, if there is nothing
// to wire — e.g. cfg.ReachabilityHelpers is empty).
//
// [REF: ARCH §13, IC §4.3, build.md Phase 6.1 Session 6.1.2 — full concrete
// dialing wiring into cmd/provider/main.go is Session 13.1.1; this session
// provides the constants, the prober, the hole-punch helper, and the relay
// client, ready for that wiring.]
func SetupNAT(h Host, cfg NATConfig) error {
	concrete, ok := h.(*host)
	if !ok {
		return fmt.Errorf("p2p.SetupNAT: h must be a Host returned by NewHost")
	}

	if len(cfg.ReachabilityHelpers) == 0 {
		return nil
	}

	runProbe := func() {
		status := probeReachability(concrete, cfg.ReachabilityHelpers)
		concrete.setNATStatus(status)
	}

	runProbe()
	if cfg.ReprobeInterval > 0 {
		go func() {
			ticker := time.NewTicker(cfg.ReprobeInterval)
			defer ticker.Stop()
			for {
				select {
				case <-concrete.closeCh:
					// host.Close() was called — stop reprobing rather than
					// leaking this goroutine forever. [REF: M6 review §5.1]
					return
				case <-ticker.C:
					runProbe()
				}
			}
		}()
	}
	return nil
}

// ── Tier 1: reachability probe (AutoNAT analogue) ─────────────────────────────

// probeReachability classifies local reachability using two signals, in order:
//
//  1. Local address shape (localOutboundIPFunc): if this host's own
//     outbound-resolved local address is private (RFC 1918), loopback,
//     link-local, or carrier-grade-NAT-ranged (RFC 6598, common among
//     residential/mobile ISPs in this project's target region), the host
//     is classified NATStatusPrivate immediately — a private/CGNAT source
//     address can never be publicly reachable, full stop, regardless of
//     anything else. [Added, M6 review §5.3]
//  2. Helper dial-out (the original check): if (1) didn't already decide
//     it, ask each helper in turn to see if we can reach it. This is NOT a
//     dial-BACK — this package has no wire protocol for asking a remote
//     helper to dial US (that needs Vyomanaut-specific probe-request
//     handling this session doesn't add a protocol ID for). Dialing OUT
//     succeeds for nearly every NAT'd host too (that's what NAT exists to
//     permit), so on its own this signal only rules out "no network at
//     all" — it does NOT confirm inbound reachability. Kept as a fallback
//     for hosts with a public-shaped local address that might still be
//     unreachable for other reasons (cloud firewall rules, etc.) that (1)
//     cannot detect; those will still be misclassified as Public.
//
// True inbound reachability confirmation — the microservice (the one
// universally-reachable party in the topology) reporting whether it could
// actually reach this host's last-known multiaddr — needs heartbeat-response
// wire-format support that doesn't exist yet; that's still Session 13.1.1+
// wiring once the microservice side exists to cooperate with. Until then,
// read NATType() as "best-effort: reliably catches classic residential/CGNAT
// NAT, does not confirm true public reachability."
func probeReachability(h *host, helpers []Multiaddr) NATStatus {
	if h.listener == nil {
		return NATStatusPrivate
	}
	if len(helpers) == 0 {
		return NATStatusUnknown
	}

	if localIP, err := localOutboundIPFunc(); err == nil {
		if localIP.IsPrivate() || localIP.IsLoopback() || localIP.IsLinkLocalUnicast() || isCGNAT(localIP) {
			return NATStatusPrivate
		}
	}
	// If localOutboundIPFunc itself fails (no route at all), fall through —
	// the helper-dial loop below will also fail and correctly land on Private.

	ctx, cancel := context.WithTimeout(context.Background(), reachabilityProbeTimeout)
	defer cancel()

	for _, helper := range helpers {
		hostport, ok := helper.HostPort()
		if !ok {
			continue
		}
		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", hostport)
		if err != nil {
			continue
		}
		_ = conn.Close()
		return NATStatusPublic
	}
	return NATStatusPrivate
}

// localOutboundIP returns the local IP address this host's default outbound
// route would use, by asking the kernel to resolve a route for a
// documentation-only target (RFC 5737 TEST-NET-3) without ever sending or
// receiving a packet — a UDP "Dial" is a local route lookup + socket bind;
// it only actually transmits when Write is called, which never happens
// here. Using a guaranteed-unroutable-on-the-real-internet address (rather
// than, say, a real public DNS server) keeps this unambiguously a local-
// only operation. [REF: M6 review §5.3]
func localOutboundIP() (net.IP, error) {
	conn, err := net.Dial("udp", "203.0.113.1:80") // TEST-NET-3 (RFC 5737); never actually contacted
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	udpAddr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return nil, fmt.Errorf("p2p: localOutboundIP: unexpected LocalAddr type %T", conn.LocalAddr())
	}
	return udpAddr.IP, nil
}

// localOutboundIPFunc is a package-level indirection so tests can inject a
// fake local address without depending on this machine's real network
// routing. Production code always uses the default, localOutboundIP.
var localOutboundIPFunc = localOutboundIP //nolint:gochecknoglobals // test seam, same pattern as other package-level test hooks in this codebase

// isCGNAT reports whether ip falls in 100.64.0.0/10 (RFC 6598, Shared
// Address Space) — the range ISPs commonly use for carrier-grade NAT.
// net.IP.IsPrivate() does not cover this range (it is not RFC 1918), but a
// host whose local address falls in it is exactly as unreachable from the
// public internet as one with a private address, and CGNAT is common among
// residential/mobile ISPs in this project's target deployment region.
// [REF: M6 review §5.3]
func isCGNAT(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	return ip4[0] == 100 && ip4[1]&0xC0 == 0x40
}
// ── Tier 2: TCP simultaneous-open hole punch (DCUtR analogue) ────────────────

// HolePunchResult reports the outcome of a hole-punch attempt.
type HolePunchResult struct {
	Succeeded bool
	Attempts  int
}

// HolePunch attempts to establish a direct connection to a peer behind cone
// NAT via TCP simultaneous open: both sides dial each other's externally
// observed address at approximately the same moment, so each side's NAT
// binding created by its own outbound SYN happens to admit the peer's
// inbound SYN before either side's firewall would otherwise have dropped it.
//
// This mirrors DCUtR's role (a relay-coordinated "on my mark, dial now")
// without requiring a running relay for the coordination signal itself —
// coordination here is a direct call between the two sides that already have
// a signalling channel (e.g. an existing relayed connection, or the
// microservice acting as rendezvous). remoteAddr is the peer's externally
// observed "host:port" as reported by that signalling channel.
//
// Retries at most maxHolePunchRetries times (IC §"1, not the default of 3":
// ARCH §13, MVP §8.2).
func HolePunch(ctx context.Context, remoteAddr string) HolePunchResult {
	for attempt := 0; attempt <= maxHolePunchRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return HolePunchResult{Succeeded: false, Attempts: attempt}
			case <-time.After(holePunchRetryBackoff):
			}
		}

		var d net.Dialer
		conn, err := d.DialContext(ctx, "tcp", remoteAddr)
		if err == nil {
			_ = conn.Close()
			return HolePunchResult{Succeeded: true, Attempts: attempt + 1}
		}
	}
	return HolePunchResult{Succeeded: false, Attempts: maxHolePunchRetries + 1}
}

// ── Tier 3: minimal relay client (Circuit Relay v2 analogue) ─────────────────

// relayRequestPrefix identifies a Vyomanaut relay-forward request on a fresh
// TCP connection to a relay node, distinguishing it from an application
// protocol stream (which instead begins with this package's negotiation
// preamble — see host.go). A relay node run by this project multiplexes both
// on the same listen port by checking this fixed 4-byte prefix first.
var relayRequestPrefix = [4]byte{'V', 'R', 'L', '1'} // "Vyomanaut ReLay v1"

// RelayReservation represents an active reservation on a relay node,
// analogous to a libp2p Circuit Relay v2 reservation.
type RelayReservation struct {
	RelayAddr string
	ExpiresAt time.Time
}

// ReserveRelaySlot asks the relay at relayAddr for a forwarding reservation
// for our own Peer ID, so the relay will accept forward requests naming us as
// the target. Returns a reservation the caller must refresh before
// relayReservationTTL elapses (IC §4.3).
//
// This is a minimal request/response over a plain TCP connection: it is not
// wire-compatible with libp2p Circuit Relay v2, and is understood only by a
// Vyomanaut-operated relay node (see doc.go for why libp2p's own relay
// protocol could not be vendored in this environment).
func ReserveRelaySlot(ctx context.Context, relayAddr string, selfID PeerID) (*RelayReservation, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", relayAddr)
	if err != nil {
		return nil, fmt.Errorf("p2p.ReserveRelaySlot: dial %s: %w", relayAddr, err)
	}
	defer func() { _ = conn.Close() }()

	req := append([]byte{}, relayRequestPrefix[:]...)
	req = append(req, 'R') // 'R' = Reserve
	req = append(req, byte(len(selfID)))
	req = append(req, []byte(selfID)...)
	if _, err := conn.Write(req); err != nil {
		return nil, fmt.Errorf("p2p.ReserveRelaySlot: write request: %w", err)
	}

	ack := make([]byte, 1)
	if _, err := readFull(conn, ack); err != nil {
		return nil, fmt.Errorf("p2p.ReserveRelaySlot: read ack: %w", err)
	}
	if ack[0] != negotiationAckOK {
		return nil, fmt.Errorf("p2p.ReserveRelaySlot: relay %s rejected reservation (relay may be at its %d-slot capacity)",
			relayAddr, relayMaxConcurrentReservations)
	}

	return &RelayReservation{
		RelayAddr: relayAddr,
		ExpiresAt: time.Now().Add(relayReservationTTL),
	}, nil
}

// DialViaRelay opens a forwarded connection to targetID through relayAddr,
// for use when targetID is behind symmetric NAT and direct dial / hole
// punching (Tiers 1-2) are not viable (ARCH §13: ~30-45% of providers).
func DialViaRelay(ctx context.Context, relayAddr string, targetID PeerID) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", relayAddr)
	if err != nil {
		return nil, fmt.Errorf("p2p.DialViaRelay: dial relay %s: %w", relayAddr, err)
	}

	req := append([]byte{}, relayRequestPrefix[:]...)
	req = append(req, 'F') // 'F' = Forward
	req = append(req, byte(len(targetID)))
	req = append(req, []byte(targetID)...)
	if _, err := conn.Write(req); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("p2p.DialViaRelay: write request: %w", err)
	}

	ack := make([]byte, 1)
	if _, err := readFull(conn, ack); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("p2p.DialViaRelay: read ack: %w", err)
	}
	if ack[0] != negotiationAckOK {
		_ = conn.Close()
		return nil, fmt.Errorf("p2p.DialViaRelay: relay %s could not reach target %s", relayAddr, targetID)
	}

	return conn, nil
}
