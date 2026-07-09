// Package p2p is declared in doc.go.
// This file defines the local, dependency-free types that stand in for
// github.com/libp2p/go-libp2p's core/{peer,protocol,network} and
// multiformats/go-multiaddr types, per the substitution decision recorded in
// doc.go. Every type here mirrors the shape and semantics of its libp2p
// counterpart closely enough to be a drop-in replacement later.
//
// [REF: IC §5.4, IC §4]

package p2p

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// ── PeerID ───────────────────────────────────────────────────────────────────

// PeerID is a libp2p-compatible peer identifier: the Base58BTC encoding of the
// multihash of a peer's public key. See peerid.go for derivation.
//
// Mirrors github.com/libp2p/go-libp2p/core/peer.ID (also a string type).
type PeerID string

// String returns the Base58BTC-encoded Peer ID string, e.g. "12D3KooW...".
func (id PeerID) String() string { return string(id) }

// Empty reports whether id is the zero value.
func (id PeerID) Empty() bool { return id == "" }

// ── ProtocolID ───────────────────────────────────────────────────────────────

// ProtocolID identifies an application-level stream protocol, e.g.
// "/vyomanaut/audit-challenge/1.0.0".
//
// Mirrors github.com/libp2p/go-libp2p/core/protocol.ID (also a string type).
type ProtocolID string

// ── NATStatus ────────────────────────────────────────────────────────────────

// NATStatus is the AutoNAT-style reachability classification of the local node.
//
// Mirrors github.com/libp2p/go-libp2p/p2p/host/autonat.NATStatus's three states.
type NATStatus int

const (
	// NATStatusUnknown means no classification has completed yet.
	NATStatusUnknown NATStatus = iota
	// NATStatusPublic means the local node is directly dialable from the internet.
	NATStatusPublic
	// NATStatusPrivate means the local node is behind a NAT/firewall that
	// blocks unsolicited inbound connections; hole-punching or a relay is
	// required for inbound reachability.
	NATStatusPrivate
)

// String implements fmt.Stringer.
func (s NATStatus) String() string {
	switch s {
	case NATStatusUnknown:
		return "unknown"
	case NATStatusPublic:
		return "public"
	case NATStatusPrivate:
		return "private"
	default:
		return "unknown"
	}
}

// ── Multiaddr ────────────────────────────────────────────────────────────────

// Multiaddr is a minimal, self-contained address type covering the subset of
// the multiaddr spec Vyomanaut actually uses:
//
//	/ip4/<addr>/tcp/<port>
//	/ip4/<addr>/udp/<port>/quic-v1
//	/ip6/<addr>/tcp/<port>
//	/dns4/<host>/tcp/<port>
//	/p2p-circuit/p2p/<PeerID>              (relay-forwarded address; not directly dialable)
//
// Mirrors github.com/multiformats/go-multiaddr.Multiaddr closely enough to be a
// drop-in replacement later; this package does not attempt full multiaddr
// generality (no arbitrary protocol stacking).
type Multiaddr interface {
	fmt.Stringer

	// Network returns the net.Dial-compatible network ("tcp" or "udp"), or ""
	// for addresses that are not directly dialable (e.g. relay circuits).
	Network() string

	// HostPort returns "host:port" for directly dialable addresses, and
	// ok=false for relay/circuit addresses.
	HostPort() (hostport string, ok bool)

	// IsRelay reports whether this is a /p2p-circuit relay address.
	IsRelay() bool
}

// multiaddr is the concrete Multiaddr implementation.
type multiaddr struct {
	raw      string
	network  string // "tcp", "udp", or "" for relay
	hostport string
	isRelay  bool
	relayTo  PeerID // target peer ID when isRelay
}

func (m *multiaddr) String() string  { return m.raw }
func (m *multiaddr) Network() string { return m.network }
func (m *multiaddr) IsRelay() bool   { return m.isRelay }

func (m *multiaddr) HostPort() (string, bool) {
	if m.isRelay {
		return "", false
	}
	return m.hostport, true
}

// minRelayMultiaddrSegments and minDirectMultiaddrSegments bound the parsed
// segment count for the two Multiaddr forms ParseMultiaddr accepts:
// relay ("p2p-circuit", "p2p", <PeerID>) and direct
// (family, addr, transport, port [, "quic-v1"]).
const (
	minRelayMultiaddrSegments  = 2
	minDirectMultiaddrSegments = 4
)

// minValidPort is the lowest valid TCP/UDP port number; the upper bound is
// math.MaxUint16 (65535), the largest value a 16-bit port field can hold.
const minValidPort = 1

// ParseMultiaddr parses the subset of multiaddr syntax described on Multiaddr.
//
// Error semantics: returns an error for any address outside the supported
// subset rather than silently misinterpreting it.
func ParseMultiaddr(s string) (Multiaddr, error) {
	parts := strings.Split(strings.Trim(s, "/"), "/")
	if len(parts) < minRelayMultiaddrSegments {
		return nil, fmt.Errorf("p2p: ParseMultiaddr: %q: too few segments", s)
	}

	// Relay form: /p2p-circuit/p2p/<PeerID>
	if parts[0] == "p2p-circuit" {
		if len(parts) != 3 || parts[1] != "p2p" {
			return nil, fmt.Errorf("p2p: ParseMultiaddr: %q: malformed relay address", s)
		}
		return &multiaddr{raw: s, isRelay: true, relayTo: PeerID(parts[2])}, nil
	}

	if len(parts) < minDirectMultiaddrSegments {
		return nil, fmt.Errorf("p2p: ParseMultiaddr: %q: too few segments for a direct address", s)
	}

	var host string
	switch parts[0] {
	case "ip4", "ip6", "dns4", "dns6":
		host = parts[1]
	default:
		return nil, fmt.Errorf("p2p: ParseMultiaddr: %q: unsupported address family %q", s, parts[0])
	}

	var network string
	switch parts[2] {
	case "tcp":
		network = "tcp"
	case "udp":
		network = "udp"
		// A trailing /quic-v1 is accepted and recorded as UDP transport, but
		// this package does not implement QUIC — see doc.go. The address is
		// still parseable so configuration and tests are not blocked on it.
	default:
		return nil, fmt.Errorf("p2p: ParseMultiaddr: %q: unsupported transport %q", s, parts[2])
	}

	port, err := strconv.Atoi(parts[3])
	if err != nil || port < minValidPort || port > math.MaxUint16 {
		return nil, fmt.Errorf("p2p: ParseMultiaddr: %q: invalid port %q", s, parts[3])
	}

	return &multiaddr{
		raw:      s,
		network:  network,
		hostport: host + ":" + parts[3],
	}, nil
}

// ── Stream ───────────────────────────────────────────────────────────────────

// Stream is one independent, ordered, reliable byte stream between two peers,
// carrying exactly one logical operation (IC §4 rule 3: "Each logical
// operation... occupies one independent stream").
//
// Mirrors the subset of github.com/libp2p/go-libp2p/core/network.Stream this
// codebase uses.
type Stream interface {
	io.Reader
	io.Writer
	io.Closer

	// Protocol returns the protocol ID this stream was opened/accepted for.
	Protocol() ProtocolID

	// RemotePeer returns the Peer ID of the remote side, as authenticated
	// during the transport handshake (NFR-016) — never self-reported by the
	// remote in application data. Handlers that need to attribute an
	// incoming request to a peer (e.g. the DHT's PUT_PROVIDER handler) must
	// use this rather than trusting any peer-ID field carried in the
	// message body.
	RemotePeer() PeerID

	// Reset closes the stream abruptly in both directions, signalling an
	// error to the remote side rather than a clean EOF (used on protocol
	// violations, e.g. IC §4 rule 5 FRAME_TOO_LARGE).
	Reset() error

	// SetDeadline, SetReadDeadline, and SetWriteDeadline mirror net.Conn
	// timeout semantics for stream-level timeouts (e.g. IC §4.2's RTO).
	SetDeadline(t time.Time) error
	SetReadDeadline(t time.Time) error
	SetWriteDeadline(t time.Time) error
}

// StreamHandler processes one incoming stream. The Host invokes it in a new
// goroutine per stream (IC §4).
type StreamHandler func(Stream)
