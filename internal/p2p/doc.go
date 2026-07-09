/*
Package p2p provides peer identity management, transport stack initialisation, and the
Kademlia DHT interface. Goroutine-safe after construction.

# Design Role (ARCH §13, IC §5.4, ADR-021)

File data moves directly between data owner and providers, and between providers during
repair; the microservice is never in this path. This package owns: peer identity
(Ed25519 → Peer ID), the Host transport interface (Connect / NewStream / SetStreamHandler),
NAT traversal wiring, and (in a later session) the Kademlia DHT with its custom HMAC key
validator.

Included components:

  - libp2p-compatible Peer ID derivation (peerid.go)
  - Host interface + transport (host.go)
  - NAT traversal wiring (nat.go)
  - Identity persistence (identity.go)
  - DHT (custom HMAC key validator) — later session

# Design notes after Session 6.1.1-6.1.3 — dependency environment constraint

build.md Session 6.1.1 specifies importing github.com/libp2p/go-libp2p directly
(core/peer, core/network, core/protocol, core/crypto, p2p/host/autonat,
p2p/protocol/circuitv2/client, multiformats/go-multiaddr). That import was attempted
first. It could not be completed in this build environment:

 1. go-libp2p v0.35.0's go.mod alone declares 117 direct requires. Its full transitive
    graph (quic-go, decred secp256k1, multiformats, prometheus client_golang,
    google.golang.org/protobuf, and the golang.org/x/{net,sync,exp,tools,mod,term,text}
    family) runs into the hundreds of modules.
 2. This environment's network egress does not include golang.org, go.dev, or
    proxy.golang.org - only codeload.github.com / raw.githubusercontent.com / github.com
    (for git-hosted deps) and a short list of language package registries. golang.org/x/*
    and google.golang.org/* are vanity import paths: resolving them normally requires an
    HTTP GET against golang.org / google.golang.org for the go-import meta tag, which is
    blocked here.
 3. This was worked around for the two golang.org/x/* modules this package's sibling
    internal/crypto actually needs (x/crypto, x/sys) by hand-repackaging GitHub archive
    zips into the exact module-zip layout `module@version/path` that `go mod` requires
    (GitHub's codeload zips use `repo-tag/path`, and - critically - any nested go.mod
    below the root, such as x/crypto's `argon2/_asm/go.mod` asm-generator submodule, must
    be stripped from the parent module's zip or the Go toolchain rejects it as malformed).
    That is a bounded, one-time fix for two modules. Doing the same for a 100+
    (transitively, several-hundred) module graph, several of which (quic-go,
    decred/dcrd, prometheus) have their own nested submodules and go-version floors above
    what this sandbox's toolchain provides, is not a bounded fix - it is effectively
    vendoring an entire third-party ecosystem by hand.

DECISION: internal/p2p is implemented with zero non-stdlib, non-project dependencies -
the same "pivot to self-contained implementation" already made for internal/erasure when
github.com/klauspost/reedsolomon hit the identical class of problem (see
internal/erasure/doc.go). Concretely:

  - Peer ID: computed with the REAL libp2p peer-id algorithm (protobuf-lite marshal of
    the Ed25519 PublicKey message -> multihash -> Base58BTC), reimplemented in stdlib Go
    (encoding/binary varints + math/big for base58). This is spec-compatible output, not
    a placeholder: an Ed25519-based Vyomanaut Peer ID is byte-for-byte what go-libp2p's
    peer.IDFromPublicKey would produce for the same key, "12D3Koo..." prefix included.
    See peerid.go.
  - Transport: TLS 1.3 over TCP (crypto/tls, crypto/x509 - both stdlib) replaces
    QUIC v1 + Noise XX. Peer identity is bound into a self-signed Ed25519 certificate and
    verified via a custom VerifyPeerCertificate callback (the same general pattern
    go-libp2p's own TLS transport uses, though not wire-compatible with it) - this
    satisfies NFR-016 (cryptographic transport authentication) without QUIC or Noise.
    True QUIC v1 (RFC 9000) requires an external implementation (there is no QUIC in the
    Go standard library); swapping one in later is an internal change only - the Host
    interface does not change. See host.go.
  - 0-RTT policy: Go's crypto/tls does not expose 0-RTT/early-data for plain TCP+TLS
    connections (only via the QUIC-specific tls.QUICConn API). The honest analogue
    implemented here is TLS session-ticket resumption: permitted protocols may resume a
    cached session (an abbreviated handshake); every protocol in zeroRTTProhibited always
    pays for a full fresh handshake. The security property IC §4 / ADR-021 actually cares
    about - a prohibited protocol can never ride on a replayable shortcut - holds either
    way. See host.go.
  - NAT traversal: AutoNAT / DCUtR / Circuit Relay v2 are replaced with a from-scratch,
    same-shape three-tier implementation (self-reachability probe, TCP simultaneous-open
    hole punching, and a minimal Vyomanaut-only relay client/protocol) using only net and
    crypto/tls. See nat.go.
  - Identity persistence is unaffected by any of the above beyond the type swap
    (libp2pcrypto.PrivKey / peer.ID -> crypto/ed25519 + this package's PeerID): the
    encrypted-keystore design in the original session plan (DeriveKeystoreEncKey +
    EncryptPointerFile/DecryptPointerFile from internal/crypto) is implemented unchanged.
    See identity.go.

All three Session 6.1.x deliverables (Host interface, NAT wiring, identity persistence)
are functionally complete against this substitution. If/when this project is built in an
environment with unrestricted network access, github.com/libp2p/go-libp2p can be dropped
in behind the same Host/DHT contract (IC §5.4) - no public interface in this package would
need to change: PeerID, ProtocolID, Multiaddr, Stream, and StreamHandler are all designed
to be drop-in-replaceable by their libp2p counterparts.

Ref: ADR-021, ADR-001, IC §4, IC §5.4, ARCH §13
*/
package p2p
