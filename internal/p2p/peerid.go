// Package p2p is declared in doc.go.
// This file derives libp2p-compatible Peer IDs from Ed25519 keys, per the
// substitution decision recorded in doc.go: this reimplements the actual
// libp2p peer-id algorithm (not a placeholder), so output is byte-for-byte
// identical to what github.com/libp2p/go-libp2p/core/peer.IDFromPublicKey
// would produce for the same key.
//
// Algorithm (libp2p peer-id spec, "identity" multihash path):
//  1. Marshal the public key as the libp2p crypto.proto `PublicKey` message:
//       message PublicKey { required KeyType Type = 1; required bytes Data = 2; }
//     with KeyType Ed25519 = 1.
//  2. Wrap the marshaled bytes in a multihash: if the marshaled length is
//     <= 42 bytes (always true for a 32-byte Ed25519 key -> 36-byte proto),
//     use the "identity" hash function (code 0x00) — the bytes are carried
//     unhashed. Larger keys (e.g. RSA) would use SHA2-256 (code 0x12)
//     instead; that path is implemented for completeness even though
//     Vyomanaut only ever uses Ed25519 keys.
//  3. Base58BTC-encode the multihash bytes. Ed25519 Peer IDs produced this
//     way have the well-known "12D3KooW..." prefix.
//
// [REF: ARCH §13 "libp2p Peer ID is multihash(public_key)", IC §5.4, ADR-021]

package p2p

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/big"
)

// ── libp2p crypto.proto KeyType enum values ──────────────────────────────────

const keyTypeEd25519 = 1

// ── multihash function codes (multiformats/multicodec) ───────────────────────

const (
	multihashIdentity = 0x00
	multihashSHA2_256 = 0x12
)

// ── protobuf wire-format constants ───────────────────────────────────────────

// protobuf tag encoding: (field_number << 3) | wire_type.
const (
	protobufTypeFieldTag byte = 0x08 // field 1, varint wire type
	protobufDataFieldTag byte = 0x12 // field 2, length-delimited wire type

	// Type field tag + Ed25519 enum value + Data field tag + one-byte
	// length varint. Ed25519 public keys are always 32 bytes, so their
	// protobuf length varint occupies exactly one byte.
	ed25519PublicKeyProtoHeaderLength = 4
)

// ── Base58BTC constants ──────────────────────────────────────────────────────

const base58Radix int64 = 58

// maxInlineKeyLength is the libp2p peer-id spec threshold: a marshaled public
// key at or below this length is embedded directly in the multihash via the
// "identity" function (no hashing); longer keys are SHA2-256 hashed instead.
const maxInlineKeyLength = 42

// base58Alphabet is the Bitcoin/IPFS Base58 alphabet (no 0, O, I, or l).
const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// ── protobuf-lite marshal ─────────────────────────────────────────────────────

// marshalEd25519PublicKeyProto hand-encodes the two-field libp2p crypto.proto
// PublicKey message for an Ed25519 key. This is a fixed, tiny, well-known
// message shape, so a full protobuf library is not needed:
//
//	field 1 (Type, varint,  wire type 0): tag 0x08, value 1 (Ed25519)
//	field 2 (Data, bytes,   wire type 2): tag 0x12, varint length, raw bytes
func marshalEd25519PublicKeyProto(pub ed25519.PublicKey) ([]byte, error) {
	if len(pub) != ed25519.PublicKeySize {
		return nil, fmt.Errorf(
			"p2p: marshalEd25519PublicKeyProto: public key must be %d bytes, got %d",
			ed25519.PublicKeySize, len(pub))
	}
	buf := make([]byte, 0, ed25519PublicKeyProtoHeaderLength+len(pub))
	buf = append(buf, protobufTypeFieldTag, keyTypeEd25519) // field 1: Type = Ed25519 (fits one byte)
	buf = append(buf, protobufDataFieldTag)                 // field 2 tag: length-delimited
	buf = appendUvarint(buf, uint64(len(pub)))
	buf = append(buf, pub...)
	return buf, nil
}

// appendUvarint appends x as an unsigned LEB128 varint (the same encoding
// protobuf and multihash both use) to buf and returns the extended slice.
func appendUvarint(buf []byte, x uint64) []byte {
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], x)
	return append(buf, tmp[:n]...)
}

// ── multihash wrapping ────────────────────────────────────────────────────────

// multihashWrap wraps data in a multihash envelope: varint(function code) ||
// varint(digest length) || digest. Per the libp2p peer-id spec, data at or
// below maxInlineKeyLength bytes is embedded via the identity function
// (digest == data, unhashed); longer data is SHA2-256 hashed first.
func multihashWrap(data []byte) []byte {
	var code uint64
	var digest []byte
	if len(data) <= maxInlineKeyLength {
		code = multihashIdentity
		digest = data
	} else {
		sum := sha256.Sum256(data)
		code = multihashSHA2_256
		digest = sum[:]
	}
	buf := appendUvarint(nil, code)
	buf = appendUvarint(buf, uint64(len(digest)))
	return append(buf, digest...)
}

// multihashUnwrap is the inverse of multihashWrap: it parses the function
// code and digest back out. Used by DecodePeerID for round-trip validation
// and testing; not required on the hot path.
func multihashUnwrap(mh []byte) (code uint64, digest []byte, err error) {
	code, n := binary.Uvarint(mh)
	if n <= 0 {
		return 0, nil, fmt.Errorf("p2p: multihashUnwrap: invalid function code varint")
	}
	mh = mh[n:]
	length, n := binary.Uvarint(mh)
	if n <= 0 {
		return 0, nil, fmt.Errorf("p2p: multihashUnwrap: invalid length varint")
	}
	mh = mh[n:]
	if uint64(len(mh)) < length {
		return 0, nil, fmt.Errorf("p2p: multihashUnwrap: digest shorter than declared length")
	}
	return code, mh[:length], nil
}

// ── Base58BTC ──────────────────────────────────────────────────────────────

// base58Encode encodes b using the Bitcoin/IPFS Base58 alphabet. Leading
// zero bytes become leading '1' characters (the Base58Check convention),
// which matters here because a multihash's first byte is the (small)
// function-code varint and can legitimately be zero-valued only in the
// unusual case of a zero function code with no other leading zero bytes —
// included for completeness and correctness on arbitrary input.
func base58Encode(b []byte) string {
	zeros := 0
	for zeros < len(b) && b[zeros] == 0 {
		zeros++
	}

	x := new(big.Int).SetBytes(b)
	base := big.NewInt(base58Radix)
	mod := new(big.Int)
	var out []byte
	for x.Sign() > 0 {
		x.DivMod(x, base, mod)
		out = append(out, base58Alphabet[mod.Int64()])
	}
	for i := 0; i < zeros; i++ {
		out = append(out, base58Alphabet[0])
	}
	// out was built least-significant-digit first; reverse it.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return string(out)
}

// base58Decode is the inverse of base58Encode. Used only for round-trip
// testing of PeerID derivation.
func base58Decode(s string) ([]byte, error) {
	zeros := 0
	for zeros < len(s) && s[zeros] == base58Alphabet[0] {
		zeros++
	}

	x := new(big.Int)
	base := big.NewInt(base58Radix)
	for i := zeros; i < len(s); i++ {
		idx := indexByte(base58Alphabet, s[i])
		if idx < 0 {
			return nil, fmt.Errorf("p2p: base58Decode: invalid character %q", s[i])
		}
		x.Mul(x, base)
		x.Add(x, big.NewInt(int64(idx)))
	}

	decoded := x.Bytes()
	out := make([]byte, zeros+len(decoded))
	copy(out[zeros:], decoded)
	return out, nil
}

func indexByte(alphabet string, c byte) int {
	for i := 0; i < len(alphabet); i++ {
		if alphabet[i] == c {
			return i
		}
	}
	return -1
}

// ── Exported Peer ID derivation ───────────────────────────────────────────────

// PeerIDFromEd25519PublicKey derives the libp2p-compatible Peer ID for an
// Ed25519 public key: multihash(protobuf(PublicKey)), Base58BTC-encoded
// (ARCH §13, ADR-021).
func PeerIDFromEd25519PublicKey(pub ed25519.PublicKey) (PeerID, error) {
	proto, err := marshalEd25519PublicKeyProto(pub)
	if err != nil {
		return "", err
	}
	return PeerID(base58Encode(multihashWrap(proto))), nil
}

// PeerIDFromEd25519PrivateKey derives the Peer ID for the public half of an
// Ed25519 private key.
func PeerIDFromEd25519PrivateKey(priv ed25519.PrivateKey) (PeerID, error) {
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		return "", fmt.Errorf("p2p: PeerIDFromEd25519PrivateKey: unexpected public key type %T", priv.Public())
	}
	return PeerIDFromEd25519PublicKey(pub)
}

// DecodePeerIDEd25519PublicKey recovers the raw 32-byte Ed25519 public key
// embedded in an "identity"-multihash Peer ID. This is the inverse of
// PeerIDFromEd25519PublicKey and is used to verify a Peer ID against a
// certificate's presented public key during the TLS handshake (host.go), and
// for round-trip tests.
//
// Error semantics: returns an error if id is not Base58BTC, not a
// well-formed multihash, not the identity function, or does not decode to a
// well-formed Ed25519 PublicKey protobuf message.
func DecodePeerIDEd25519PublicKey(id PeerID) (ed25519.PublicKey, error) {
	mh, err := base58Decode(string(id))
	if err != nil {
		return nil, fmt.Errorf("p2p: DecodePeerIDEd25519PublicKey: %w", err)
	}
	code, digest, err := multihashUnwrap(mh)
	if err != nil {
		return nil, fmt.Errorf("p2p: DecodePeerIDEd25519PublicKey: %w", err)
	}
	if code != multihashIdentity {
		return nil, fmt.Errorf("p2p: DecodePeerIDEd25519PublicKey: unsupported multihash function 0x%x", code)
	}
	// digest is the marshaled PublicKey protobuf message; parse it back out.
	return unmarshalEd25519PublicKeyProto(digest)
}

// unmarshalEd25519PublicKeyProto parses the fixed two-field PublicKey message
// produced by marshalEd25519PublicKeyProto.
func unmarshalEd25519PublicKeyProto(buf []byte) (ed25519.PublicKey, error) {
	if len(buf) < 2 || buf[0] != protobufTypeFieldTag {
		return nil, fmt.Errorf("p2p: unmarshalEd25519PublicKeyProto: missing/malformed Type field")
	}
	if buf[1] != keyTypeEd25519 {
		return nil, fmt.Errorf("p2p: unmarshalEd25519PublicKeyProto: KeyType %d is not Ed25519", buf[1])
	}
	buf = buf[2:]
	if len(buf) < 1 || buf[0] != protobufDataFieldTag {
		return nil, fmt.Errorf("p2p: unmarshalEd25519PublicKeyProto: missing/malformed Data field tag")
	}
	buf = buf[1:]
	length, n := binary.Uvarint(buf)
	if n <= 0 {
		return nil, fmt.Errorf("p2p: unmarshalEd25519PublicKeyProto: invalid Data length varint")
	}
	buf = buf[n:]
	if uint64(len(buf)) != length || length != ed25519.PublicKeySize {
		return nil, fmt.Errorf(
			"p2p: unmarshalEd25519PublicKeyProto: Data length %d, want exactly %d",
			len(buf), ed25519.PublicKeySize)
	}
	pub := make(ed25519.PublicKey, ed25519.PublicKeySize)
	copy(pub, buf)
	return pub, nil
}
