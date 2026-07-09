// Package p2p is declared in doc.go.
// This file defines the Host interface (IC §5.4) and its concrete
// implementation. Per the substitution decision in doc.go, the transport is
// TLS 1.3 over TCP (crypto/tls, stdlib) rather than QUIC v1 + Noise XX; peer
// identity is authenticated via a self-signed Ed25519 certificate verified
// against the expected Peer ID.
//
// [REF: IC §5.4, IC §4, ARCH §13, ADR-021]

package p2p

import (
	"context"
	"crypto/ed25519"
	cryptorand "crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sync"
	"time"
)

// zeroRTTProhibited is the complete, exhaustive set of protocol IDs for which
// 0-RTT / abbreviated-handshake session resumption is forbidden (IC §4,
// ARCH §13 §Session resumption policy).
//
// DESIGN: This is an explicit deny-list, NOT a suffix/pattern match.
// Rationale: /vyomanaut/repair-download/1.0.0 and /vyomanaut/vetting-gc/1.0.0 do not
// contain "-challenge" or "-audit", so suffix matching would silently miss them (IC §4).
//
// /vyomanaut/chunk-upload/1.0.0 is intentionally absent — 0-RTT is PERMITTED (IC §4.1).
var zeroRTTProhibited = map[ProtocolID]struct{}{
	"/vyomanaut/audit-challenge/1.0.0": {},
	"/vyomanaut/repair-download/1.0.0": {},
	"/vyomanaut/vetting-gc/1.0.0":      {},
}

// zeroRTTPermitted reports whether protocolID may use an abbreviated
// (session-resumed) handshake. See doc.go for why TLS session-ticket
// resumption is this package's honest analogue of libp2p's QUIC 0-RTT policy.
func zeroRTTPermitted(protocolID ProtocolID) bool {
	_, prohibited := zeroRTTProhibited[protocolID]
	return !prohibited
}

// Host is the primary transport interface for the provider daemon (IC §5.4).
// Constructed once at daemon startup via NewHost; all methods are goroutine-safe.
type Host interface {
	// PeerID returns the local Ed25519-based Peer ID.
	// Computed as multihash(ed25519_public_key) (ARCH §13, ADR-021).
	PeerID() PeerID

	// Connect dials peerID at the given multiaddrs and performs cryptographic
	// Peer ID verification during the TLS 1.3 handshake (IC §4, NFR-016).
	//
	// Pre-conditions:
	//   - len(addrs) >= 1
	// Post-conditions (on nil error):
	//   - A connection to peerID has been established and torn down again
	//     (Connect is a reachability + identity check; NewStream dials fresh
	//     per-operation connections — see NewStream and IC §4 rule 3).
	//   - The Peer ID of the remote was verified against peerID, not self-reported.
	// Error semantics:
	//   - ErrPeerIDMismatch: the remote peer's actual ID does not match peerID.
	//   - ErrAllAddrsFailed: every provided address failed to connect.
	Connect(ctx context.Context, peerID PeerID, addrs []Multiaddr) error

	// NewStream opens an application-level stream to peerID for protocolID.
	// Requires that Connect(peerID, ...) has previously succeeded, so the Host
	// knows a verified, dialable address for peerID.
	//
	// 0-RTT policy (IC §4, ARCH §13 §Session resumption policy):
	// If protocolID is in zeroRTTProhibited, this method forces a full, fresh
	// TLS handshake (no session-ticket resumption). The caller does NOT need
	// to manage this — the Host enforces it automatically.
	//
	// /vyomanaut/chunk-upload/1.0.0 is excluded from the deny-list; abbreviated
	// handshakes are permitted for it.
	NewStream(ctx context.Context, peerID PeerID, protocolID ProtocolID) (Stream, error)

	// SetStreamHandler registers a handler for incoming streams of protocolID.
	// Each incoming stream runs the handler in a new goroutine (IC §4).
	SetStreamHandler(protocolID ProtocolID, handler StreamHandler)

	// NATType returns the current reachability classification of the local node.
	// Periodically updated by the background reachability prober (nat.go, ARCH §13).
	NATType() NATStatus

	// Close shuts down the host, its listener, and all open connections.
	Close() error
}

// ── concrete implementation ───────────────────────────────────────────────────

// negotiationAckOK / negotiationAckReject are the one-byte accept/reject
// response codes for this package's minimal protocol-negotiation preamble —
// the stand-in for libp2p's multistream-select (IC §4 rule 2).
const (
	negotiationAckOK     = 0x00
	negotiationAckReject = 0x01
)

// maxProtocolIDLen bounds the length-prefixed protocol ID string exchanged in
// the negotiation preamble, mirroring IC §4 rule 5's framing discipline.
const maxProtocolIDLen = 256

// host is the concrete, unexported Host implementation.
type host struct {
	privKey ed25519.PrivateKey
	peerID  PeerID
	cert    tls.Certificate

	listener net.Listener

	mu              sync.RWMutex
	handlers        map[ProtocolID]StreamHandler
	knownAddrs      map[PeerID]Multiaddr // populated by Connect
	sessionCacheFor map[PeerID]tls.ClientSessionCache
	natStatus       NATStatus
	closed          bool
}

// HostConfig supplies the identity and listen address for NewHost.
type HostConfig struct {
	// PrivateKey is the daemon's Ed25519 identity key (see identity.go).
	PrivateKey ed25519.PrivateKey
	// ListenAddr is the local "host:port" to accept inbound streams on.
	// An empty ListenAddr means the host is client-only (no SetStreamHandler
	// traffic can be received) — used by the microservice's outbound-only
	// repair/audit callers.
	ListenAddr string
}

// NewHost constructs a Host bound to the given Ed25519 identity, optionally
// listening on ListenAddr for inbound streams.
//
// [REF: IC §5.4, ARCH §13]
func NewHost(cfg HostConfig) (Host, error) {
	if len(cfg.PrivateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("p2p.NewHost: PrivateKey must be %d bytes, got %d",
			ed25519.PrivateKeySize, len(cfg.PrivateKey))
	}
	peerID, err := PeerIDFromEd25519PrivateKey(cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("p2p.NewHost: derive Peer ID: %w", err)
	}
	cert, err := newSelfSignedCert(cfg.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("p2p.NewHost: generate identity certificate: %w", err)
	}

	h := &host{
		privKey:         cfg.PrivateKey,
		peerID:          peerID,
		cert:            cert,
		handlers:        make(map[ProtocolID]StreamHandler),
		knownAddrs:      make(map[PeerID]Multiaddr),
		sessionCacheFor: make(map[PeerID]tls.ClientSessionCache),
	}

	if cfg.ListenAddr != "" {
		ln, err := tls.Listen("tcp", cfg.ListenAddr, h.serverTLSConfig())
		if err != nil {
			return nil, fmt.Errorf("p2p.NewHost: listen on %q: %w", cfg.ListenAddr, err)
		}
		h.listener = ln
		go h.acceptLoop()
	}

	return h, nil
}

func (h *host) PeerID() PeerID { return h.peerID }

// serverTLSConfig returns the TLS config used for both accepting inbound
// connections and dialing outbound ones. InsecureSkipVerify is set because
// this package does not use a WebPKI certificate chain — peer authentication
// is performed explicitly in verifyPeerCertificate against the expected Peer
// ID, which is the standard Go pattern for pinned/self-authenticated peers.
func (h *host) serverTLSConfig() *tls.Config {
	return &tls.Config{
		Certificates:          []tls.Certificate{h.cert},
		ClientAuth:            tls.RequireAnyClientCert,
		InsecureSkipVerify:    true, //nolint:gosec // peer auth is done explicitly below, not via WebPKI
		MinVersion:            tls.VersionTLS13,
		VerifyPeerCertificate: verifyPeerCertificateAgainst(""), // any peer; handler-level auth applies higher up
	}
}

// Connect dials peerID at each address in turn until one succeeds, verifying
// the remote's authenticated Peer ID against the expected one.
//
// [REF: IC §5.4, IC §4 rule 1, NFR-016]
func (h *host) Connect(ctx context.Context, peerID PeerID, addrs []Multiaddr) error {
	if len(addrs) == 0 {
		return fmt.Errorf("p2p.Connect: at least one address is required")
	}

	var lastErr error
	for _, addr := range addrs {
		hostport, ok := addr.HostPort()
		if !ok {
			lastErr = fmt.Errorf("p2p.Connect: %s: not a directly dialable address (relay addresses require SetupNAT's relay client)", addr)
			continue
		}
		if err := h.dialAndVerify(ctx, peerID, hostport); err != nil {
			lastErr = err
			continue
		}
		h.mu.Lock()
		h.knownAddrs[peerID] = addr
		h.mu.Unlock()
		return nil
	}

	if errors.Is(lastErr, ErrPeerIDMismatch) {
		return lastErr
	}
	return fmt.Errorf("%w: %v", ErrAllAddrsFailed, lastErr)
}

// dialAndVerify opens a short-lived TLS connection purely to verify the
// remote's identity, then closes it. NewStream performs the real, per-
// operation dial.
func (h *host) dialAndVerify(ctx context.Context, peerID PeerID, hostport string) error {
	dialer := &tls.Dialer{
		Config: &tls.Config{
			Certificates:          []tls.Certificate{h.cert},
			InsecureSkipVerify:    true, //nolint:gosec // explicit peer-ID verification below
			MinVersion:            tls.VersionTLS13,
			VerifyPeerCertificate: verifyPeerCertificateAgainst(peerID),
		},
	}
	conn, err := dialer.DialContext(ctx, "tcp", hostport)
	if err != nil {
		if errors.Is(err, errPeerIDMismatchTLS) {
			return ErrPeerIDMismatch
		}
		return fmt.Errorf("p2p.Connect: dial %s: %w", hostport, err)
	}
	return conn.Close()
}

// NewStream dials a fresh connection to peerID (one connection per logical
// operation, per IC §4 rule 3), negotiates protocolID via this package's
// minimal preamble (the stand-in for multistream-select, IC §4 rule 2), and
// applies the 0-RTT policy for protocolID.
//
// [REF: IC §5.4, IC §4 rules 2-3, ADR-021]
func (h *host) NewStream(ctx context.Context, peerID PeerID, protocolID ProtocolID) (Stream, error) {
	if len(protocolID) == 0 || len(protocolID) > maxProtocolIDLen {
		return nil, fmt.Errorf("p2p.NewStream: protocol ID length %d out of bounds", len(protocolID))
	}

	h.mu.RLock()
	addr, known := h.knownAddrs[peerID]
	h.mu.RUnlock()
	if !known {
		return nil, fmt.Errorf("p2p.NewStream: no known address for %s; call Connect first", peerID)
	}
	hostport, ok := addr.HostPort()
	if !ok {
		return nil, fmt.Errorf("p2p.NewStream: %s: known address is not directly dialable", addr)
	}

	tlsCfg := &tls.Config{
		Certificates:          []tls.Certificate{h.cert},
		InsecureSkipVerify:    true, //nolint:gosec // explicit peer-ID verification below
		MinVersion:            tls.VersionTLS13,
		VerifyPeerCertificate: verifyPeerCertificateAgainst(peerID),
	}
	if zeroRTTPermitted(protocolID) {
		// Permitted protocols may resume a cached session (abbreviated handshake).
		tlsCfg.ClientSessionCache = h.sessionCacheForPeer(peerID)
	} else {
		// Prohibited protocols always pay for a full, fresh handshake: no
		// session cache is attached, and ticket issuance is disabled so the
		// server side does not even offer a resumable session on this
		// connection (IC §4, ARCH §13 §Session resumption policy).
		tlsCfg.SessionTicketsDisabled = true
	}

	dialer := &tls.Dialer{Config: tlsCfg}
	conn, err := dialer.DialContext(ctx, "tcp", hostport)
	if err != nil {
		if errors.Is(err, errPeerIDMismatchTLS) {
			return nil, ErrPeerIDMismatch
		}
		return nil, fmt.Errorf("p2p.NewStream: dial %s: %w", hostport, err)
	}

	if err := writeNegotiationPreamble(conn, protocolID); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("p2p.NewStream: negotiate %s: %w", protocolID, err)
	}
	ack := make([]byte, 1)
	if _, err := readFull(conn, ack); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("p2p.NewStream: read negotiation ack: %w", err)
	}
	if ack[0] != negotiationAckOK {
		_ = conn.Close()
		return nil, fmt.Errorf("p2p.NewStream: peer rejected protocol %s", protocolID)
	}

	return &tlsStream{Conn: conn, protocol: protocolID}, nil
}

// tlsSessionCacheSize bounds the per-peer TLS session ticket cache used to
// permit abbreviated (resumed) handshakes for 0-RTT-permitted protocols.
// A handful of tickets per peer is ample: Vyomanaut connections are
// short-lived (one operation per connection, IC §4 rule 3), so there is no
// benefit to caching more than a few recent sessions per peer.
const tlsSessionCacheSize = 4

func (h *host) sessionCacheForPeer(peerID PeerID) tls.ClientSessionCache {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.sessionCacheFor[peerID]
	if !ok {
		c = tls.NewLRUClientSessionCache(tlsSessionCacheSize)
		h.sessionCacheFor[peerID] = c
	}
	return c
}

// SetStreamHandler registers handler for protocolID. Safe to call before or
// after the host starts accepting connections.
func (h *host) SetStreamHandler(protocolID ProtocolID, handler StreamHandler) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handlers[protocolID] = handler
}

// NATType returns the last reachability classification recorded by nat.go's
// prober. Defaults to NATStatusUnknown until a probe completes.
func (h *host) NATType() NATStatus {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.natStatus
}

// setNATStatus is called by nat.go's background prober.
func (h *host) setNATStatus(s NATStatus) {
	h.mu.Lock()
	h.natStatus = s
	h.mu.Unlock()
}

// Close shuts down the listener. Open streams already handed to callers are
// unaffected; each is its own independent TCP+TLS connection.
func (h *host) Close() error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil
	}
	h.closed = true
	ln := h.listener
	h.mu.Unlock()

	if ln != nil {
		return ln.Close()
	}
	return nil
}

// acceptLoop accepts inbound connections, reads the negotiation preamble, and
// dispatches to the registered handler in a new goroutine per stream (IC §4).
func (h *host) acceptLoop() {
	for {
		conn, err := h.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go h.serveConn(conn)
	}
}

func (h *host) serveConn(conn net.Conn) {
	protocolID, err := readNegotiationPreamble(conn)
	if err != nil {
		_ = conn.Close()
		return
	}

	h.mu.RLock()
	handler, ok := h.handlers[protocolID]
	h.mu.RUnlock()

	if !ok {
		_, _ = conn.Write([]byte{negotiationAckReject})
		_ = conn.Close()
		return
	}
	if _, err := conn.Write([]byte{negotiationAckOK}); err != nil {
		_ = conn.Close()
		return
	}

	go handler(&tlsStream{Conn: conn, protocol: protocolID})
}

// ── negotiation preamble (multistream-select stand-in) ────────────────────────

// writeNegotiationPreamble writes a 2-byte big-endian length prefix followed
// by the protocol ID string (IC §4 rule 2 / rule 5 framing discipline).
func writeNegotiationPreamble(w net.Conn, protocolID ProtocolID) error {
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(len(protocolID)))
	if _, err := w.Write(lenBuf[:]); err != nil {
		return err
	}
	_, err := w.Write([]byte(protocolID))
	return err
}

func readNegotiationPreamble(r net.Conn) (ProtocolID, error) {
	var lenBuf [2]byte
	if _, err := readFull(r, lenBuf[:]); err != nil {
		return "", err
	}
	n := binary.BigEndian.Uint16(lenBuf[:])
	if n == 0 || int(n) > maxProtocolIDLen {
		return "", fmt.Errorf("p2p: negotiation preamble length %d out of bounds", n)
	}
	buf := make([]byte, n)
	if _, err := readFull(r, buf); err != nil {
		return "", err
	}
	return ProtocolID(buf), nil
}

func readFull(r net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// ── tlsStream: Stream implementation over a *tls.Conn ────────────────────────

type tlsStream struct {
	net.Conn
	protocol ProtocolID
}

func (s *tlsStream) Protocol() ProtocolID { return s.protocol }

// RemotePeer derives the remote's Peer ID from the certificate it presented
// during the TLS handshake — the same certificate verifyPeerCertificateAgainst
// already authenticated before this stream was ever handed to a caller.
// Returns "" only if the underlying connection is not a *tls.Conn (should not
// happen for streams produced by this package's Host) or presented no
// certificate (impossible given tls.RequireAnyClientCert / the dialer's own
// client certificate — this is a defensive fallback, not an expected path).
func (s *tlsStream) RemotePeer() PeerID {
	tlsConn, ok := s.Conn.(*tls.Conn)
	if !ok {
		return ""
	}
	certs := tlsConn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		return ""
	}
	pub, ok := certs[0].PublicKey.(ed25519.PublicKey)
	if !ok {
		return ""
	}
	id, err := PeerIDFromEd25519PublicKey(pub)
	if err != nil {
		return ""
	}
	return id
}

// Reset closes the stream abruptly: it disables the linger period on the
// underlying TCP connection (where supported) before closing, so the remote
// side observes a reset rather than a clean FIN — the closest TCP analogue
// to libp2p's stream Reset semantics.
func (s *tlsStream) Reset() error {
	if tc, ok := underlyingTCPConn(s.Conn); ok {
		_ = tc.SetLinger(0)
	}
	return s.Close()
}

// underlyingTCPConn unwraps a *tls.Conn (which exposes the underlying
// connection via NetConn(), added in Go 1.21) down to a *net.TCPConn, if
// that's what it actually is. Reset() degrades gracefully to a plain Close
// when it isn't (e.g. in tests that dial over something else).
func underlyingTCPConn(c net.Conn) (*net.TCPConn, bool) {
	u, ok := c.(interface{ NetConn() net.Conn })
	if !ok {
		return nil, false
	}
	tc, ok := u.NetConn().(*net.TCPConn)
	return tc, ok
}

// ── certificate generation and peer verification ──────────────────────────────

// errPeerIDMismatchTLS is a sentinel wrapped by verifyPeerCertificateAgainst
// and detected after Dial to translate into the package-level ErrPeerIDMismatch.
var errPeerIDMismatchTLS = errors.New("p2p: TLS peer certificate does not match expected Peer ID")

// newSelfSignedCert creates a self-signed X.509 certificate binding an
// Ed25519 identity key to a TLS certificate, following the same general
// "self-signed cert authenticated by a custom verify callback" pattern
// go-libp2p's own TLS transport uses (see doc.go) — not wire-compatible with
// it, but the same idiomatic Go approach to peer-pinned TLS.
func newSelfSignedCert(priv ed25519.PrivateKey) (tls.Certificate, error) {
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return tls.Certificate{}, fmt.Errorf("p2p: newSelfSignedCert: unexpected public key type")
	}
	serial, err := randSerial()
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("p2p: newSelfSignedCert: generate serial: %w", err)
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(20 * 365 * 24 * time.Hour), // identity cert; long-lived, pinned by Peer ID not expiry
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(nil, template, template, pub, priv)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("p2p: newSelfSignedCert: %w", err)
	}
	return tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  priv,
	}, nil
}

// certSerialBits is the bit length of the random serial number generated for
// the self-signed identity certificate (crypto/x509 requires a positive,
// non-predictable serial; 128 bits is the same width commonly used for
// WebPKI leaf certificate serials).
const certSerialBits = 128

// randSerial returns a random 128-bit serial number suitable for an X.509
// certificate (crypto/x509 requires a positive, non-predictable serial).
func randSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), certSerialBits)
	return cryptorand.Int(cryptorand.Reader, limit)
}

// verifyPeerCertificateAgainst returns a tls.Config.VerifyPeerCertificate
// callback that derives the Peer ID from the presented leaf certificate's
// Ed25519 public key and compares it against expected. An empty expected
// accepts any well-formed Ed25519 peer certificate (used for the server side,
// which authenticates at the application layer once a protocol is known).
func verifyPeerCertificateAgainst(expected PeerID) func([][]byte, [][]*x509.Certificate) error {
	return func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
		if len(rawCerts) == 0 {
			return fmt.Errorf("p2p: no peer certificate presented")
		}
		cert, err := x509.ParseCertificate(rawCerts[0])
		if err != nil {
			return fmt.Errorf("p2p: parse peer certificate: %w", err)
		}
		pub, ok := cert.PublicKey.(ed25519.PublicKey)
		if !ok {
			return fmt.Errorf("p2p: peer certificate is not Ed25519")
		}
		actual, err := PeerIDFromEd25519PublicKey(pub)
		if err != nil {
			return fmt.Errorf("p2p: derive Peer ID from peer certificate: %w", err)
		}
		if expected != "" && actual != expected {
			return errPeerIDMismatchTLS
		}
		return nil
	}
}
