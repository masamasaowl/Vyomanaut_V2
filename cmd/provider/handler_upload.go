// Command provider is the Vyomanaut V2 provider daemon entrypoint.
// This file implements the /vyomanaut/chunk-upload/1.0.0 stream handler
// (IC §4.1, Session 13.2.1). 0-RTT session resumption is PERMITTED for this
// protocol (IC §4.1) — internal/p2p's Host enforces the zeroRTTProhibited
// deny-list on the dialing side automatically; this handler does not need to
// do anything special to allow it.
//
// KNOWN GAP — capability-token verification (flagged, not resolved here;
// see the build report's CAP-TOKEN-FILE-ID-GAP / CAP-TOKEN-PROVIDER-ID-GAP
// findings):
//
// IC §4.1's capability_token signing_input is
// domain_prefix || chunk_id(32) || provider_id(16) || file_id(16) || expiry_unix_ms(8),
// exactly matching what internal/api/upload.go's generateCapabilityToken and
// internal/repair/executor.go's mintCapabilityToken already sign, server-side,
// today. Two of those four fields are structurally unavailable to a provider
// daemon at this session's scope:
//
//   - file_id: IC §4.1's UploadRequest Frame 1 (chunk_id, shard_index,
//     capability_token, chunk_data) carries no file_id field at all. A
//     provider has no way to learn it. (chunk_id already uniquely identifies
//     the (segment, shard_index) slot in the shipped Milestone 11 design —
//     chunk_id is a fresh, microservice-assigned random value per slot, not
//     a content hash — so file_id is arguably redundant in the signing input
//     and could be dropped there instead; that is a call for the design
//     council, not this session.)
//   - provider_id: MVP §8.3's cmd/provider flag table (this session's own
//     reference) has no flag carrying the daemon's microservice-assigned
//     UUID identity — only the Ed25519-derived Peer ID exists at this scope.
//     No registration/OTP flow that would yield one is in scope for M13.
//
// verifyCapabilityToken below is written to the full, correct 4-field
// formula so no reshaping is needed once both gaps close, but every call
// site in this file passes the zero UUID for both fields until then. This
// makes every real capability_token fail verification (0x03 NOT_ASSIGNED) —
// a safe, fail-closed, and honestly-incomplete state, not a silent bypass.
// The structural checks (length, expiry, chunk_id content-hash match) are
// fully functional independent of this gap.
//
// [REF: IC §4.1, IC §4 rule 5 framing, IC §11, ADR-021, build.md Session 13.2.1]
package main

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	localcrypto "github.com/masamasaowl/Vyomanaut_V2/internal/crypto"
	"github.com/masamasaowl/Vyomanaut_V2/internal/metrics"
	"github.com/masamasaowl/Vyomanaut_V2/internal/p2p"
	"github.com/masamasaowl/Vyomanaut_V2/internal/storage"
)

// ── Protocol ID (IC §4.1) ────────────────────────────────────────────────

const chunkUploadProtocolID = p2p.ProtocolID("/vyomanaut/chunk-upload/1.0.0")

// ── Wire-format field sizes (IC §4.1 Frame 1) ────────────────────────────
// Named rather than inlined so no raw byte-count literal appears in the
// framing arithmetic below (this codebase's "no magic numbers" standard).
const (
	uploadLengthPrefixSize      = 4                     // uint32 big-endian frame length prefix
	uploadChunkIDSize           = 32                    // SHA-256-shaped content address
	uploadShardIndexSize        = 4                     // uint32 big-endian
	uploadCapabilityTokenSize   = 72                    // expiry_unix_ms(8) || Ed25519 sig(64)
	uploadChunkDataSize         = storage.ChunkDataSize // 262144 (= erasure.ShardSize)
	uploadFrame1PayloadMaxBytes = uploadChunkIDSize + uploadShardIndexSize +
		uploadCapabilityTokenSize + uploadChunkDataSize // 262252 (IC §4.1)
	uploadProviderSigSize = 64 // Ed25519 signature (Frame 2, status 0x00/0x06)
)

// ── Frame 2 status codes (IC §4.1) ───────────────────────────────────────
const (
	uploadStatusOK                = 0x00
	uploadStatusFrameTooLarge     = 0x01
	uploadStatusChunkIDMismatch   = 0x02
	uploadStatusNotAssigned       = 0x03
	uploadStatusStorageFull       = 0x04
	uploadStatusInternalError     = 0x05
	uploadStatusAlreadyStored     = 0x06 // idempotent; treat as 0x00 (Frame 2 carries provider_sig too)
	uploadStatusCapabilityExpired = 0x07
)

// capabilityTokenExpiryGraceMs is the 30-second clock-skew grace period
// (IC §4.1 step 3): a token is accepted while
// expiry_unix_ms > NOW_unix_ms - capabilityTokenExpiryGraceMs.
const capabilityTokenExpiryGraceMs = 30_000

// capabilityTokenDomainPrefix is the domain-separation prefix prepended to
// every capability_token signing_input (IC §4.1), matching
// internal/api/upload.go's capabilityTokenDomainPrefix exactly.
const capabilityTokenDomainPrefix = "vyomanaut-chunk-upload-cap-v1"

// uploadStreamTimeout is the initiator-side response deadline (IC §4.1);
// applied here as the responder's own read/write deadline so a slow or
// stalled initiator cannot hold a handler goroutine open indefinitely.
const uploadStreamTimeout = 5 * time.Second

// providerStatus values this daemon may hold, mirroring the microservice's
// providers.status enum (IC §4.1 pre-conditions: ACTIVE, VETTING, DEPARTED).
const (
	providerStatusActive   = "ACTIVE"
	providerStatusVetting  = "VETTING"
	providerStatusDeparted = "DEPARTED"
)

// ── providerStatusHolder ──────────────────────────────────────────────────

// providerStatusHolder is a small goroutine-safe holder for this daemon's
// last-known registration status, as reported by the microservice.
//
// GAP (flagged): no wire format in this session's scope actually updates
// this from the microservice (a heartbeat-response status field, if one
// exists, is not part of the pasted IC §3.1 text this session was given).
// Defaults to ACTIVE — the daemon's own reasonable belief about itself
// absent any contrary signal — and provides Set for a future session (e.g.
// heartbeat-response parsing) to wire up. This is not a security-critical
// gate the way peer/signature authentication is: DEPARTED here is a
// courtesy fast-reject, not the network's authoritative enforcement point
// (a truly departed provider's chunk_assignments rows are what the
// microservice itself will stop dispatching audits/repairs against).
type providerStatusHolder struct {
	mu     sync.RWMutex
	status string
}

func newProviderStatusHolder(initial string) *providerStatusHolder {
	return &providerStatusHolder{status: initial}
}

func (h *providerStatusHolder) Get() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.status
}

func (h *providerStatusHolder) Set(status string) {
	h.mu.Lock()
	h.status = status
	h.mu.Unlock()
}

// ── single-writer channel plumbing ───────────────────────────────────────
//
// chunkWriteRequest/chunkWriteResult are declared here (the type shape the
// handler sends/receives), but the goroutine that actually calls
// store.AppendChunk — runChunkStoreWriter — is declared in main.go, not
// here: IC §11's single-writer rule is enforced structurally by keeping
// every AppendChunk call site out of this file entirely (this file only
// ever sends on writeCh). See main.go step 6 (Session 13.1.1).

// chunkWriteRequest is submitted to the single writer goroutine (IC §11,
// storage.ChunkStore.AppendChunk's own "single designated writer goroutine"
// contract). resultCh is buffered size 1 so the writer goroutine never
// blocks handing back a result to a handler that gave up early (e.g. a
// stream-level timeout).
type chunkWriteRequest struct {
	chunkID  [32]byte
	data     []byte
	resultCh chan chunkWriteResult
}

type chunkWriteResult struct {
	vlogOffset uint64
	err        error
}

// ── UploadHandler ──────────────────────────────────────────────────────

// UploadHandler implements the /vyomanaut/chunk-upload/1.0.0 responder
// (IC §4.1).
type UploadHandler struct {
	store   storage.ChunkStore
	writeCh chan<- chunkWriteRequest

	msPublicKey ed25519.PublicKey // microservice signing key; verifies capability_token

	providerID         [16]byte           // GAP: always zero, see file header
	providerSigningKey ed25519.PrivateKey // this daemon's own Ed25519 identity key
	providerIDBytes    [16]byte           // provider_id_bytes embedded in the upload receipt signing input

	status *providerStatusHolder
}

// NewUploadHandler constructs an UploadHandler. providerIDBytes is the
// 16-byte provider identifier embedded in the upload-receipt signing input
// (IC §4.1 step 6); it is intentionally a caller-supplied opaque value
// (typically the low 16 bytes of a stable local identifier) since no
// microservice-assigned UUID is available at this session's scope — see the
// file header's provider_id gap note. This is independent of the
// (currently-unusable) capability-token provider_id field.
func NewUploadHandler(
	store storage.ChunkStore,
	writeCh chan<- chunkWriteRequest,
	msPublicKey ed25519.PublicKey,
	providerSigningKey ed25519.PrivateKey,
	providerIDBytes [16]byte,
	status *providerStatusHolder,
) *UploadHandler {
	return &UploadHandler{
		store:              store,
		writeCh:            writeCh,
		msPublicKey:        msPublicKey,
		providerSigningKey: providerSigningKey,
		providerIDBytes:    providerIDBytes,
		status:             status,
	}
}

// HandleStream implements p2p.StreamHandler.
func (h *UploadHandler) HandleStream(s p2p.Stream) {
	defer func() { _ = s.Close() }()

	deadline := time.Now().Add(uploadStreamTimeout)
	_ = s.SetDeadline(deadline)

	// Pre-condition: DEPARTED providers reset the stream immediately, with
	// no application response at all (IC §4.1 pre-conditions).
	if h.status != nil && h.status.Get() == providerStatusDeparted {
		_ = s.Reset()
		return
	}

	length, ok := h.readLengthPrefix(s)
	if !ok {
		return
	}
	if length > uploadFrame1PayloadMaxBytes {
		h.writeStatusOnly(s, uploadStatusFrameTooLarge)
		_ = s.Reset()
		return
	}
	if length != uploadFrame1PayloadMaxBytes {
		// Malformed/short frame: IC §4.1 defines no status code for this
		// (only the over-large case gets one); fail closed with a reset and
		// no response rather than guessing at a partial parse.
		_ = s.Reset()
		return
	}

	payload := make([]byte, length)
	if _, err := readStreamFull(s, payload); err != nil {
		return
	}

	var chunkID [32]byte
	copy(chunkID[:], payload[0:uploadChunkIDSize])
	offset := uploadChunkIDSize
	shardIndex := binary.BigEndian.Uint32(payload[offset : offset+uploadShardIndexSize])
	_ = shardIndex // carried in the frame; not otherwise consumed by this handler
	offset += uploadShardIndexSize
	var token [uploadCapabilityTokenSize]byte
	copy(token[:], payload[offset:offset+uploadCapabilityTokenSize])
	offset += uploadCapabilityTokenSize
	chunkData := payload[offset : offset+uploadChunkDataSize]

	// ── Capability token verification (IC §4.1 steps 1-5) ──────────────
	status := h.verifyCapabilityTokenFrame(chunkID, token)
	if status != uploadStatusOK {
		h.writeStatusOnly(s, status)
		return
	}

	// ── Content-hash check BEFORE any disk write (IC §4.1 post-conditions,
	// IC §4 rule for status 0x02) ────────────────────────────────────────
	computedHash := sha256.Sum256(chunkData)
	if computedHash != chunkID {
		h.writeStatusOnly(s, uploadStatusChunkIDMismatch)
		return
	}

	// ── Idempotency: ALREADY_STORED (IC §4.1 status 0x06) ───────────────
	// LookupChunk is goroutine-safe and does not touch the single-writer
	// path; checking first avoids relying on storage.ChunkStore's internal
	// duplicate-key error semantics (undocumented) to distinguish a genuine
	// insert failure from a harmless retry.
	if _, err := h.store.LookupChunk(chunkID); err == nil {
		sig := h.signUploadReceipt(chunkID, shardIndex)
		h.writeSuccessFrame(s, uploadStatusAlreadyStored, sig)
		return
	}

	// ── Write via the single writer goroutine (IC §11 single-writer rule)
	resultCh := make(chan chunkWriteResult, 1)
	dataCopy := make([]byte, len(chunkData))
	copy(dataCopy, chunkData)
	h.writeCh <- chunkWriteRequest{chunkID: chunkID, data: dataCopy, resultCh: resultCh}

	select {
	case res := <-resultCh:
		if res.err != nil {
			h.writeStatusOnly(s, uploadStatusInternalError)
			return
		}
	case <-time.After(uploadStreamTimeout):
		// The writer goroutine is still processing (or backed up); the
		// initiator's own 5s deadline has already elapsed by the time this
		// fires, so there is no useful response left to send.
		return
	}

	metrics.DaemonChunksStoredTotal.Inc()

	sig := h.signUploadReceipt(chunkID, shardIndex)
	h.writeSuccessFrame(s, uploadStatusOK, sig)
}

// verifyCapabilityTokenFrame implements IC §4.1 steps 1-5, returning the
// Frame 2 status code to send (uploadStatusOK on success).
func (h *UploadHandler) verifyCapabilityTokenFrame(chunkID [32]byte, token [uploadCapabilityTokenSize]byte) byte {
	// Step 1 is structurally guaranteed here: token is a fixed-size array,
	// so "len(capability_token) == 72" always holds once the frame parses.

	// Step 2: parse expiry_unix_ms from token bytes 0-7.
	expiryUnixMs := int64(binary.BigEndian.Uint64(token[0:8]))

	// Step 3: 30-second clock-skew grace.
	nowMs := time.Now().UnixMilli()
	if expiryUnixMs <= nowMs-capabilityTokenExpiryGraceMs {
		return uploadStatusCapabilityExpired
	}

	// Step 4-5: verify Ed25519 signature over signing_input, re-derived
	// using the chunk_id received in the frame header (implicitly binding
	// it — a mismatch fails verification here, returning 0x03).
	if h.msPublicKey == nil {
		return uploadStatusNotAssigned
	}
	var sig [64]byte
	copy(sig[:], token[8:uploadCapabilityTokenSize])
	var pub [32]byte
	copy(pub[:], h.msPublicKey)

	signingInput := capabilityTokenSigningInput(chunkID, h.providerID, [16]byte{}, expiryUnixMs)
	if !localcrypto.VerifyBytes(pub, signingInput, sig) {
		return uploadStatusNotAssigned
	}
	return uploadStatusOK
}

// capabilityTokenSigningInput reconstructs the IC §4.1 signing_input:
//
//	digest(domain_prefix || chunk_id(32) || provider_id(16) || file_id(16) || expiry_unix_ms(8))
//
// (internal/crypto.SignBytes/VerifyBytes perform the pre-hash step
// internally; the raw concatenation is what's passed here — see
// internal/api/upload.go's generateCapabilityToken, which this mirrors.)
//
// See this file's header for why providerID/fileID are currently always
// passed as the zero value by every caller in this file.
func capabilityTokenSigningInput(chunkID [32]byte, providerID, fileID [16]byte, expiryUnixMs int64) []byte {
	var expiryBytes [8]byte
	binary.BigEndian.PutUint64(expiryBytes[:], uint64(expiryUnixMs))

	input := make([]byte, 0, len(capabilityTokenDomainPrefix)+len(chunkID)+len(providerID)+len(fileID)+len(expiryBytes))
	input = append(input, []byte(capabilityTokenDomainPrefix)...)
	input = append(input, chunkID[:]...)
	input = append(input, providerID[:]...)
	input = append(input, fileID[:]...)
	input = append(input, expiryBytes[:]...)
	return input
}

// signUploadReceipt computes the upload-receipt signature (IC §4.1 step 6):
//
//	provider_sig = Ed25519(digest(chunk_id ‖ shard_index ‖ provider_id_bytes ‖ timestamp_unix_ms))
func (h *UploadHandler) signUploadReceipt(chunkID [32]byte, shardIndex uint32) [64]byte {
	var shardIndexBytes [4]byte
	binary.BigEndian.PutUint32(shardIndexBytes[:], shardIndex)
	var tsBytes [8]byte
	binary.BigEndian.PutUint64(tsBytes[:], uint64(time.Now().UnixMilli()))

	input := make([]byte, 0, len(chunkID)+len(shardIndexBytes)+len(h.providerIDBytes)+len(tsBytes))
	input = append(input, chunkID[:]...)
	input = append(input, shardIndexBytes[:]...)
	input = append(input, h.providerIDBytes[:]...)
	input = append(input, tsBytes[:]...)

	return localcrypto.SignBytes(h.providerSigningKey, input)
}

// ── framing helpers ────────────────────────────────────────────────────

// readLengthPrefix reads the 4-byte big-endian length prefix common to
// every Vyomanaut libp2p protocol (IC §4 rule 5). ok is false on any read
// error, in which case the caller should simply return (the stream is
// already unusable).
func (h *UploadHandler) readLengthPrefix(s p2p.Stream) (length uint32, ok bool) {
	var buf [uploadLengthPrefixSize]byte
	if _, err := readStreamFull(s, buf[:]); err != nil {
		return 0, false
	}
	return binary.BigEndian.Uint32(buf[:]), true
}

// readStreamFull reads exactly len(buf) bytes from s, or returns the first
// error encountered (including a short read at EOF).
func readStreamFull(s p2p.Stream, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := s.Read(buf[total:])
		total += n
		if err != nil {
			return total, fmt.Errorf("cmd/provider: short read (%d/%d bytes): %w", total, len(buf), err)
		}
	}
	return total, nil
}

// writeStatusOnly writes a 1-byte error Frame 2 (length=1, status) — every
// non-success status defined for this protocol (IC §4.1 Frame 2: "Error: 1
// byte").
func (h *UploadHandler) writeStatusOnly(s p2p.Stream, status byte) {
	frame := make([]byte, uploadLengthPrefixSize+1)
	binary.BigEndian.PutUint32(frame[0:uploadLengthPrefixSize], 1)
	frame[uploadLengthPrefixSize] = status
	_, _ = s.Write(frame)
}

// writeSuccessFrame writes a 65-byte success Frame 2 (length=65, status,
// provider_sig) — used for both 0x00 OK and 0x06 ALREADY_STORED, which
// IC §4.1 describes as "idempotent; treat as 0x00" and therefore carries the
// same signed receipt.
func (h *UploadHandler) writeSuccessFrame(s p2p.Stream, status byte, sig [uploadProviderSigSize]byte) {
	payloadLen := 1 + uploadProviderSigSize
	frame := make([]byte, uploadLengthPrefixSize+payloadLen)
	binary.BigEndian.PutUint32(frame[0:uploadLengthPrefixSize], uint32(payloadLen))
	frame[uploadLengthPrefixSize] = status
	copy(frame[uploadLengthPrefixSize+1:], sig[:])
	_, _ = s.Write(frame)
}
