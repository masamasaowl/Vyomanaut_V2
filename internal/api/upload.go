// Package api is declared in doc.go.
// This file implements build.md Milestone 11 Phase 11.7 Session 11.7.1:
// POST /api/v1/upload/assign.
//
// [FLAGGED — schema gap, ShardAssignment needs a chunk_id field] OAS's
// ShardAssignment schema has no chunk_id property, yet IC §4.1's capability
// token signing_input is bound to a specific chunk_id
// (SHA-256("vyomanaut-chunk-upload-cap-v1" || chunk_id || provider_id ||
// file_id || expiry_unix_ms)), and IC §4.1 step 5 says the provider
// "re-derives signing_input using the chunk_id received in the frame
// header" — i.e. the client must send the SAME chunk_id the token was
// signed over, or every upload fails signature verification. Real content
// hashing can't produce this value in advance: ADR-022's AONT step 2 draws
// a fresh random key per segment, so the eventual shard bytes (and their
// true SHA-256) are not knowable at assignment time, before the client has
// even performed the AONT-RS transform. The client has no source for this
// value except this response. Resolved here as: chunk_id is a
// microservice-assigned 32-byte identifier for the (segment, shard_index)
// slot, generated once at first assignment and persisted in
// chunk_assignments.chunk_id (already NOT NULL there), returned verbatim on
// every idempotent re-call. This is the same "code needs a field the OAS
// schema omits" situation as Phase 11.6's promised_return_at gap — flagged
// and added rather than left unimplementable. ShardAssignmentBody below adds
// ChunkID accordingly.
//
// [Decision — provider selection reuses internal/repair] OAS: "Selects
// providers using Power of Two Choices weighted by reliability score" +
// "Enforces the 20% ASN cap" is exactly internal/repair.
// SelectReplacementProvider's existing, already-tested algorithm (built for
// repair reassignment, Session 9.4.1) — same P2C-over-score-composite
// selection, same floor(TotalShards*ASNCapFraction) cap, same
// ErrNoEligibleReplacement exhaustion signal. internal/api sits above
// internal/repair in the import layering (confirmed already in Phase 11.6:
// "internal/api — not internal/repair — is the layer permitted to call both
// packages"), so reusing it directly here — rather than re-implementing P2C
// and the ASN cap a second time — is both permitted and the more consistent
// choice; a second, drifting copy of this algorithm would be a worse
// outcome. ErrNoEligibleReplacement is treated as this endpoint's own
// INSUFFICIENT_ASN_DIVERSITY signal.
//
// [Decision — idempotency skips the three gates on repeat calls] The
// TASK's three checks (readiness, escrow, ASN cap) and its "idempotent on
// file_id" note are two separate concerns; IC §4's own ERRATA describes a
// repeat call (after a provider returns CAPABILITY_EXPIRED) as returning
// "the same provider set... but generates new tokens with a fresh expiry" —
// no mention of re-validating readiness/escrow. A client mid-upload,
// retrying only because a token expired, should not be newly blocked by a
// transient readiness or escrow hiccup after the real assignment work is
// already done. Implemented as: if file_id already has persisted segments,
// skip straight to regenerating tokens.
//
// [Decision — escrow check uses available, not raw, balance] FR-014's own
// wording says "balance < cost_for_30_days(file_size)", but
// ownerBalanceAndReserved (built in Phase 11.5 for exactly this purpose —
// see its own doc comment) already establishes that "available" (balance
// minus every other ACTIVE file's reserved 30-day cost) is this codebase's
// operative check, not raw balance — using raw balance here would let an
// owner oversubscribe escrow across many files. Reused directly, same as
// Phase 11.6 reused Phase 11.5's helpers.
//
// [REF: OAS paths./api/v1/upload/assign, components/schemas/
// UploadAssignRequest/Response, SegmentAssignment, ShardAssignment,
// FR-007-FR-020, ADR-014, ADR-022, IC §4.1, build.md Phase 11.7
// Session 11.7.1]

package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/masamasaowl/Vyomanaut_V2/internal/config"
	localcrypto "github.com/masamasaowl/Vyomanaut_V2/internal/crypto"
	"github.com/masamasaowl/Vyomanaut_V2/internal/repair"
)

// ── Capability token (IC §4.1) ──────────────────────────────────────────

const (
	capabilityTokenDomainPrefix = "vyomanaut-chunk-upload-cap-v1"
	capabilityTokenLifetime     = 1 * time.Hour
	capabilityTokenByteLen      = 72 // 8-byte expiry_unix_ms || 64-byte Ed25519 signature
)

// generateCapabilityToken implements IC §4.1's exact byte layout:
//
//	signing_input = SHA-256(domain_prefix || chunk_id || provider_id || file_id || expiry_unix_ms)
//	capability_token = expiry_unix_ms (8B) || Ed25519_sign(microservice_signing_key, signing_input)
//
// crypto.SignBytes already performs the SHA-256-then-Ed25519-sign
// composition internally (IC §3.2's SIGNING_INPUT_RULE convention used
// throughout this package), so the raw, pre-hash field concatenation is
// passed directly — SignBytes hashing it is what produces IC §4.1's
// signing_input, not a second, additional hash.
func generateCapabilityToken(msSigningKey ed25519.PrivateKey, chunkID [32]byte, providerID, fileID uuid.UUID, issuedAt time.Time) [capabilityTokenByteLen]byte {
	expiryUnixMs := issuedAt.Add(capabilityTokenLifetime).UnixMilli()
	var expiryBytes [8]byte
	binary.BigEndian.PutUint64(expiryBytes[:], uint64(expiryUnixMs))

	input := make([]byte, 0, len(capabilityTokenDomainPrefix)+32+16+16+8)
	input = append(input, []byte(capabilityTokenDomainPrefix)...)
	input = append(input, chunkID[:]...)
	input = append(input, providerID[:]...)
	input = append(input, fileID[:]...)
	input = append(input, expiryBytes[:]...)

	sig := localcrypto.SignBytes(msSigningKey, input)

	var token [capabilityTokenByteLen]byte
	copy(token[0:8], expiryBytes[:])
	copy(token[8:capabilityTokenByteLen], sig[:])
	return token
}

// ── Request/response bodies ─────────────────────────────────────────────

type uploadAssignRequestBody struct {
	FileID             uuid.UUID `json:"file_id"`
	NumSegments        int       `json:"num_segments"`
	OriginalSizeBytes  int64     `json:"original_size_bytes"`
}

// ShardAssignmentBody mirrors OAS ShardAssignment, plus ChunkID — see this
// file's header note on why that field is a necessary addition.
type ShardAssignmentBody struct {
	ShardIndex      int       `json:"shard_index"`
	ProviderID      uuid.UUID `json:"provider_id"`
	Multiaddrs      []string  `json:"multiaddrs"`
	ASN             string    `json:"asn"`
	CapabilityToken string    `json:"capability_token"`
	ChunkID         string    `json:"chunk_id"`
}

type segmentAssignmentBody struct {
	SegmentIndex int                   `json:"segment_index"`
	SegmentID    uuid.UUID             `json:"segment_id"`
	Providers    []ShardAssignmentBody `json:"providers"`
}

type uploadAssignResponseBody struct {
	Assignments         []segmentAssignmentBody `json:"assignments"`
	MonthlyCostPaise    int64                   `json:"monthly_cost_paise"`
	RequiredEscrowPaise int64                   `json:"required_escrow_paise"`
}

// ── Handler ──────────────────────────────────────────────────────────────

const (
	minNumSegments = 1
	maxNumSegments = 10000
)

// UploadAssignHandler serves POST /api/v1/upload/assign (FR-007–FR-020,
// ADR-014, ADR-022).
type UploadAssignHandler struct {
	db         *sql.DB
	profile    config.NetworkProfile
	signingKey ed25519.PrivateKey // same microservice identity key as JWT/microservice_sig (IC §4.1)
	readiness  *ReadinessEvaluator
}

func NewUploadAssignHandler(db *sql.DB, profile config.NetworkProfile, signingKey ed25519.PrivateKey, readiness *ReadinessEvaluator) *UploadAssignHandler {
	return &UploadAssignHandler{db: db, profile: profile, signingKey: signingKey, readiness: readiness}
}

func (h *UploadAssignHandler) HandleAssign(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "missing auth claims", nil, "", nil)
		return
	}

	var req uploadAssignRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		WriteError(w, http.StatusBadRequest, ErrInvalidRequest, "invalid JSON body", nil, "", nil)
		return
	}
	if req.NumSegments < minNumSegments || req.NumSegments > maxNumSegments {
		WriteError(w, http.StatusBadRequest, ErrInvalidRequest, fmt.Sprintf("num_segments must be between %d and %d", minNumSegments, maxNumSegments), nil, "num_segments", nil)
		return
	}
	if req.OriginalSizeBytes < 1 {
		WriteError(w, http.StatusBadRequest, ErrInvalidRequest, "original_size_bytes must be positive", nil, "original_size_bytes", nil)
		return
	}

	monthlyCost := fileMonthlyCostPaiseForBytes(req.OriginalSizeBytes, h.profile)

	// Idempotency: a prior successful call already persisted segments for
	// this file_id — skip the three gates entirely (see file header) and
	// just refresh tokens.
	existing, err := h.loadExistingAssignments(ctx, req.FileID)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to check existing assignment", nil, "", nil)
		return
	}
	if len(existing) > 0 {
		h.respondWithFreshTokens(w, req.FileID, existing, monthlyCost)
		return
	}

	// Check 1 — readiness gate.
	readinessResp, err := h.readiness.Evaluate(ctx)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "readiness evaluation failed", nil, "", nil)
		return
	}
	if !readinessResp.AllConditionsMet {
		writeNetworkNotReadyError(w)
		return
	}

	// Check 2 — escrow balance (available, not raw — see file header).
	balance, reserved, err := ownerBalanceAndReserved(ctx, h.db, h.profile, claims.Subject)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "escrow balance lookup failed", nil, "", nil)
		return
	}
	available := balance - reserved
	if available < monthlyCost {
		WriteError(w, http.StatusConflict, ErrInsufficientEscrow, "escrow balance insufficient for 30-day storage cost", nil, "", nil)
		return
	}

	// files row created now (placeholder ciphertext fields — see file.go's
	// header for the file/register handshake this sets up), satisfying
	// segments.file_id's FK before any segment can be inserted.
	if err := h.createPlaceholderFile(ctx, req.FileID, claims.Subject, req.OriginalSizeBytes); err != nil {
		WriteError(w, http.StatusInternalServerError, ErrInternal, "failed to create file record", nil, "", nil)
		return
	}

	// Check 3 — ASN cap, enforced per-shard by repair.SelectReplacementProvider.
	segments := make([]segmentAssignmentBody, 0, req.NumSegments)
	for segIdx := 0; segIdx < req.NumSegments; segIdx++ {
		segAssignment, availableASNs, err := h.assignSegment(ctx, req.FileID, segIdx)
		if errors.Is(err, repair.ErrNoEligibleReplacement) {
			writeInsufficientASNDiversityError(w, h.profile.TotalShards, availableASNs)
			return
		}
		if err != nil {
			WriteError(w, http.StatusInternalServerError, ErrInternal, "shard assignment failed", nil, "", nil)
			return
		}
		segments = append(segments, segAssignment)
	}

	resp := uploadAssignResponseBody{Assignments: segments, MonthlyCostPaise: monthlyCost, RequiredEscrowPaise: monthlyCost}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// createPlaceholderFile inserts the files row that segments.file_id's FK
// requires, with real original_size_bytes (known from the request, needed
// for cost math regardless of registration state) but placeholder
// pointer_ciphertext/nonce/tag — file.go's register handler fills in the
// real values and uses pointer_ciphertext's emptiness as the "not yet
// registered" signal (see that file's header for the full reasoning).
func (h *UploadAssignHandler) createPlaceholderFile(ctx context.Context, fileID, ownerID uuid.UUID, originalSizeBytes int64) error {
	placeholderNonce := make([]byte, 12)
	placeholderTag := make([]byte, 16)
	_, err := h.db.ExecContext(ctx, `
		INSERT INTO files (file_id, owner_id, pointer_ciphertext, pointer_nonce, pointer_tag, original_size_bytes)
		VALUES ($1, $2, ''::bytea, $3, $4, $5)`,
		fileID, ownerID, placeholderNonce, placeholderTag, originalSizeBytes)
	return err
}

// assignSegment creates one segment row and its TotalShards shard
// assignments. shard_index 0..profile.DataShards-1 are the systematic AONT
// data words; profile.DataShards..profile.TotalShards-1 are RS parity
// (never the hardcoded "0-15/16-55" OAS's schema descriptions use — this
// phase's own flagged note).
func (h *UploadAssignHandler) assignSegment(ctx context.Context, fileID uuid.UUID, segIdx int) (segmentAssignmentBody, int, error) {
	var segmentID uuid.UUID
	if err := h.db.QueryRowContext(ctx, `INSERT INTO segments (file_id, segment_index) VALUES ($1, $2) RETURNING segment_id`,
		fileID, segIdx).Scan(&segmentID); err != nil {
		return segmentAssignmentBody{}, 0, fmt.Errorf("api: assignSegment: insert segment: %w", err)
	}

	shards := make([]ShardAssignmentBody, 0, h.profile.TotalShards)
	var excludeIDs []uuid.UUID
	now := time.Now()

	for shardIdx := 0; shardIdx < h.profile.TotalShards; shardIdx++ {
		providerID, err := repair.SelectReplacementProvider(ctx, h.db, h.profile, segmentID, excludeIDs)
		if err != nil {
			if errors.Is(err, repair.ErrNoEligibleReplacement) {
				availableASNs, countErr := h.countDistinctActiveASNs(ctx)
				if countErr != nil {
					availableASNs = 0
				}
				return segmentAssignmentBody{}, availableASNs, err
			}
			return segmentAssignmentBody{}, 0, fmt.Errorf("api: assignSegment: select provider: %w", err)
		}
		excludeIDs = append(excludeIDs, providerID)

		var chunkID [32]byte
		if _, err := rand.Read(chunkID[:]); err != nil {
			return segmentAssignmentBody{}, 0, fmt.Errorf("api: assignSegment: rand chunk_id: %w", err)
		}
		if _, err := h.db.ExecContext(ctx, `
			INSERT INTO chunk_assignments (chunk_id, is_vetting_chunk, segment_id, shard_index, provider_id)
			VALUES ($1, FALSE, $2, $3, $4)`,
			chunkID[:], segmentID, shardIdx, providerID,
		); err != nil {
			return segmentAssignmentBody{}, 0, fmt.Errorf("api: assignSegment: insert chunk_assignment: %w", err)
		}

		var multiaddrsJSON []byte
		var asn string
		if err := h.db.QueryRowContext(ctx, `SELECT last_known_multiaddrs, asn FROM providers WHERE provider_id = $1`, providerID).
			Scan(&multiaddrsJSON, &asn); err != nil {
			return segmentAssignmentBody{}, 0, fmt.Errorf("api: assignSegment: provider lookup: %w", err)
		}
		var multiaddrs []string
		_ = json.Unmarshal(multiaddrsJSON, &multiaddrs)

		token := generateCapabilityToken(h.signingKey, chunkID, providerID, fileID, now)

		shards = append(shards, ShardAssignmentBody{
			ShardIndex:      shardIdx,
			ProviderID:      providerID,
			Multiaddrs:      multiaddrs,
			ASN:             asn,
			CapabilityToken: hex.EncodeToString(token[:]),
			ChunkID:         hex.EncodeToString(chunkID[:]),
		})
	}

	return segmentAssignmentBody{SegmentIndex: segIdx, SegmentID: segmentID, Providers: shards}, 0, nil
}

func (h *UploadAssignHandler) countDistinctActiveASNs(ctx context.Context) (int, error) {
	var count int
	err := h.db.QueryRowContext(ctx, `SELECT COUNT(DISTINCT asn) FROM providers WHERE status = 'ACTIVE'`).Scan(&count)
	return count, err
}

// existingShardRow is one persisted chunk_assignments row for an
// already-assigned segment, joined with its provider's current multiaddrs
// and ASN (which may have changed since the original assignment — always
// re-fetched fresh, matching heartbeat's own "current" framing).
type existingShardRow struct {
	segmentIndex int
	segmentID    uuid.UUID
	shardIndex   int
	providerID   uuid.UUID
	chunkID      [32]byte
	multiaddrs   []string
	asn          string
}

// loadExistingAssignments returns every persisted real (non-vetting) shard
// assignment for fileID's segments, or an empty slice if fileID has none
// yet (the common, first-call case — not an error).
func (h *UploadAssignHandler) loadExistingAssignments(ctx context.Context, fileID uuid.UUID) ([]existingShardRow, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT s.segment_index, s.segment_id, ca.shard_index, ca.provider_id, ca.chunk_id,
		       p.last_known_multiaddrs, p.asn
		FROM segments s
		JOIN chunk_assignments ca ON ca.segment_id = s.segment_id AND ca.is_vetting_chunk = FALSE
		JOIN providers p ON p.provider_id = ca.provider_id
		WHERE s.file_id = $1
		ORDER BY s.segment_index, ca.shard_index`, fileID)
	if err != nil {
		return nil, fmt.Errorf("api: loadExistingAssignments: %w", err)
	}
	defer rows.Close()

	var out []existingShardRow
	for rows.Next() {
		var row existingShardRow
		var chunkIDRaw, multiaddrsJSON []byte
		if err := rows.Scan(&row.segmentIndex, &row.segmentID, &row.shardIndex, &row.providerID, &chunkIDRaw,
			&multiaddrsJSON, &row.asn); err != nil {
			return nil, fmt.Errorf("api: loadExistingAssignments: scan: %w", err)
		}
		copy(row.chunkID[:], chunkIDRaw)
		_ = json.Unmarshal(multiaddrsJSON, &row.multiaddrs)
		out = append(out, row)
	}
	return out, rows.Err()
}

// respondWithFreshTokens rebuilds the UploadAssignResponse from persisted
// rows, regenerating every capability_token with a fresh 1-hour expiry
// (IC §4's ERRATA) without touching the provider set itself.
func (h *UploadAssignHandler) respondWithFreshTokens(w http.ResponseWriter, fileID uuid.UUID, rows []existingShardRow, monthlyCost int64) {
	now := time.Now()
	segmentsByIndex := make(map[int]*segmentAssignmentBody)
	var order []int

	for _, row := range rows {
		seg, ok := segmentsByIndex[row.segmentIndex]
		if !ok {
			seg = &segmentAssignmentBody{SegmentIndex: row.segmentIndex, SegmentID: row.segmentID}
			segmentsByIndex[row.segmentIndex] = seg
			order = append(order, row.segmentIndex)
		}
		token := generateCapabilityToken(h.signingKey, row.chunkID, row.providerID, fileID, now)
		seg.Providers = append(seg.Providers, ShardAssignmentBody{
			ShardIndex:      row.shardIndex,
			ProviderID:      row.providerID,
			Multiaddrs:      row.multiaddrs,
			ASN:             row.asn,
			CapabilityToken: hex.EncodeToString(token[:]),
			ChunkID:         hex.EncodeToString(row.chunkID[:]),
		})
	}

	segments := make([]segmentAssignmentBody, 0, len(order))
	for _, idx := range order {
		segments = append(segments, *segmentsByIndex[idx])
	}

	resp := uploadAssignResponseBody{Assignments: segments, MonthlyCostPaise: monthlyCost, RequiredEscrowPaise: monthlyCost}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

// ── 503 error responses ─────────────────────────────────────────────────

// networkNotReadyErrorBody extends the standard error envelope with
// readiness_url — present in OAS's own NetworkNotReady example but not
// declared on the formal Error/allOf schema (schema vs. example mismatch;
// see this file's header). Built inline here rather than by extending the
// shared WriteError/errorBody used by every other handler in this package,
// to avoid rippling a rarely-needed field through every existing call site.
type networkNotReadyErrorBody struct {
	ErrorCode    ErrorCode `json:"error_code"`
	Message      string    `json:"message"`
	RequestID    string    `json:"request_id"`
	RetryAfter   int       `json:"retry_after"`
	ReadinessURL string    `json:"readiness_url"`
}

const networkNotReadyRetryAfterSeconds = 60

func writeNetworkNotReadyError(w http.ResponseWriter) {
	requestID, err := uuid.NewV7()
	requestIDStr := uuid.Nil.String()
	if err == nil {
		requestIDStr = requestID.String()
	}
	body := networkNotReadyErrorBody{
		ErrorCode:    ErrNetworkNotReady,
		Message:      "Upload rejected: network readiness conditions are not yet satisfied.",
		RequestID:    requestIDStr,
		RetryAfter:   networkNotReadyRetryAfterSeconds,
		ReadinessURL: "/api/v1/admin/readiness",
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", requestIDStr)
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(body)
}

const insufficientASNDiversityRetryAfterSeconds = 300

func writeInsufficientASNDiversityError(w http.ResponseWriter, totalShards, availableASNs int) {
	retryAfter := insufficientASNDiversityRetryAfterSeconds
	asns := availableASNs
	WriteError(w, http.StatusServiceUnavailable, ErrInsufficientASNDiversity,
		fmt.Sprintf("Cannot place %d shards while respecting the per-ASN cap. Current distinct ASNs: %d.", totalShards, availableASNs),
		&retryAfter, "", &asns)
}