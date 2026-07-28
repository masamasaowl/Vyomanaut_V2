// Package api is declared in doc.go.
// Tests for file.go: Sessions 11.7.2-11.7.4.

package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/masamasaowl/Vyomanaut_V2/internal/config"
)

// ── Shared fixtures ────────────────────────────────────────────────────────

func insertTestOwnerWithKey(t *testing.T, db *sql.DB) (uuid.UUID, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	var id uuid.UUID
	if err := db.QueryRow(`INSERT INTO owners (phone_number, ed25519_public_key) VALUES ($1, $2) RETURNING owner_id`,
		randPhoneForOwner(), []byte(pub)).Scan(&id); err != nil {
		t.Fatalf("insert test owner: %v", err)
	}
	return id, pub, priv
}

// insertPlaceholderFile creates a files row exactly as upload/assign would
// (reusing UploadAssignHandler.createPlaceholderFile — same package,
// unexported method), without going through the full HTTP handler.
func insertPlaceholderFile(t *testing.T, db *sql.DB, fileID, ownerID uuid.UUID, originalSizeBytes int64) {
	t.Helper()
	h := &UploadAssignHandler{db: db}
	if err := h.createPlaceholderFile(context.Background(), fileID, ownerID, originalSizeBytes); err != nil {
		t.Fatalf("insert placeholder file: %v", err)
	}
}

func signFileRegisterRequest(t *testing.T, priv ed25519.PrivateKey, req fileRegisterRequestBody) fileRegisterRequestBody {
	t.Helper()
	sig := ed25519.Sign(priv, canonicalFileRegisterSigningInput(req))
	req.OwnerSig = hexEncode(sig)
	return req
}

func hexEncode(b []byte) string {
	const hextable = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hextable[v>>4]
		out[i*2+1] = hextable[v&0x0f]
	}
	return string(out)
}

// ═══════════════════════════════════════════════════════════════════════
// Session 11.7.2 — File Register
// ═══════════════════════════════════════════════════════════════════════

func TestFileRegisterSucceeds(t *testing.T) {
	db := openTestDB(t)
	ownerID, _, priv := insertTestOwnerWithKey(t, db)
	fileID := uuid.New()
	insertPlaceholderFile(t, db, fileID, ownerID, 2048)

	req := fileRegisterRequestBody{
		FileID:            fileID,
		PointerCiphertext: base64.StdEncoding.EncodeToString([]byte("ciphertext-bytes")),
		PointerNonce:      base64.StdEncoding.EncodeToString(make([]byte, 12)),
		PointerTag:        base64.StdEncoding.EncodeToString(make([]byte, 16)),
		OriginalSizeBytes: 2048,
		SchemaVersion:     1,
	}
	req = signFileRegisterRequest(t, priv, req)
	body, _ := json.Marshal(req)

	r := withClaims(httptest.NewRequest(http.MethodPost, "/api/v1/file/register", bytes.NewReader(body)),
		VerifiedClaims{Subject: ownerID, Role: "owner"})
	w := httptest.NewRecorder()

	h := NewFileRegisterHandler(db)
	h.HandleRegister(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	resp := decodeJSON[fileRegisterResponseBody](t, w.Body.Bytes())
	if resp.FileID != fileID {
		t.Fatalf("file_id = %v, want %v", resp.FileID, fileID)
	}

	var storedLen int
	if err := db.QueryRow(`SELECT octet_length(pointer_ciphertext) FROM files WHERE file_id = $1`, fileID).Scan(&storedLen); err != nil {
		t.Fatalf("query stored ciphertext: %v", err)
	}
	if storedLen != len("ciphertext-bytes") {
		t.Fatalf("stored pointer_ciphertext length = %d, want %d", storedLen, len("ciphertext-bytes"))
	}
}

func TestFileRegisterRejectsUnknownFileID(t *testing.T) {
	db := openTestDB(t)
	_, _, priv := insertTestOwnerWithKey(t, db)

	req := fileRegisterRequestBody{
		FileID:            uuid.New(), // never assigned
		PointerCiphertext: base64.StdEncoding.EncodeToString([]byte("x")),
		PointerNonce:      base64.StdEncoding.EncodeToString(make([]byte, 12)),
		PointerTag:        base64.StdEncoding.EncodeToString(make([]byte, 16)),
		OriginalSizeBytes: 1024,
		SchemaVersion:     1,
	}
	req = signFileRegisterRequest(t, priv, req)
	body, _ := json.Marshal(req)

	ownerID := uuid.New() // doesn't matter; the 404 fires on file lookup first
	r := withClaims(httptest.NewRequest(http.MethodPost, "/api/v1/file/register", bytes.NewReader(body)),
		VerifiedClaims{Subject: ownerID, Role: "owner"})
	w := httptest.NewRecorder()

	h := NewFileRegisterHandler(db)
	h.HandleRegister(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", w.Code, w.Body.String())
	}
}

func TestFileRegisterRejectsReRegistrationOfActive(t *testing.T) {
	db := openTestDB(t)
	ownerID, _, priv := insertTestOwnerWithKey(t, db)
	fileID := uuid.New()
	insertPlaceholderFile(t, db, fileID, ownerID, 2048)

	base := fileRegisterRequestBody{
		FileID:            fileID,
		PointerCiphertext: base64.StdEncoding.EncodeToString([]byte("first-registration")),
		PointerNonce:      base64.StdEncoding.EncodeToString(make([]byte, 12)),
		PointerTag:        base64.StdEncoding.EncodeToString(make([]byte, 16)),
		OriginalSizeBytes: 2048,
		SchemaVersion:     1,
	}
	first := signFileRegisterRequest(t, priv, base)
	body1, _ := json.Marshal(first)
	r1 := withClaims(httptest.NewRequest(http.MethodPost, "/api/v1/file/register", bytes.NewReader(body1)),
		VerifiedClaims{Subject: ownerID, Role: "owner"})
	w1 := httptest.NewRecorder()
	h := NewFileRegisterHandler(db)
	h.HandleRegister(w1, r1)
	if w1.Code != http.StatusCreated {
		t.Fatalf("first registration: status = %d, body = %s", w1.Code, w1.Body.String())
	}

	second := signFileRegisterRequest(t, priv, base)
	body2, _ := json.Marshal(second)
	r2 := withClaims(httptest.NewRequest(http.MethodPost, "/api/v1/file/register", bytes.NewReader(body2)),
		VerifiedClaims{Subject: ownerID, Role: "owner"})
	w2 := httptest.NewRecorder()
	h.HandleRegister(w2, r2)
	if w2.Code != http.StatusConflict {
		t.Fatalf("second registration: status = %d, want 409, body = %s", w2.Code, w2.Body.String())
	}
}

func TestFileRegisterStoresDisplayNameWhenPresent(t *testing.T) {
	db := openTestDB(t)
	ownerID, _, priv := insertTestOwnerWithKey(t, db)
	fileID := uuid.New()
	insertPlaceholderFile(t, db, fileID, ownerID, 512)

	displayCiphertext := base64.StdEncoding.EncodeToString([]byte("my-file.pdf"))
	displayNonce := base64.StdEncoding.EncodeToString(make([]byte, 12))
	displayTag := base64.StdEncoding.EncodeToString(make([]byte, 16))
	req := fileRegisterRequestBody{
		FileID:                fileID,
		PointerCiphertext:     base64.StdEncoding.EncodeToString([]byte("ciphertext")),
		PointerNonce:          base64.StdEncoding.EncodeToString(make([]byte, 12)),
		PointerTag:            base64.StdEncoding.EncodeToString(make([]byte, 16)),
		OriginalSizeBytes:     512,
		DisplayNameCiphertext: &displayCiphertext,
		DisplayNameNonce:      &displayNonce,
		DisplayNameTag:        &displayTag,
		SchemaVersion:         1,
	}
	req = signFileRegisterRequest(t, priv, req)
	body, _ := json.Marshal(req)

	r := withClaims(httptest.NewRequest(http.MethodPost, "/api/v1/file/register", bytes.NewReader(body)),
		VerifiedClaims{Subject: ownerID, Role: "owner"})
	w := httptest.NewRecorder()

	h := NewFileRegisterHandler(db)
	h.HandleRegister(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	var storedDisplayLen sql.NullInt64
	if err := db.QueryRow(`SELECT octet_length(display_name_ciphertext) FROM files WHERE file_id = $1`, fileID).Scan(&storedDisplayLen); err != nil {
		t.Fatalf("query display name: %v", err)
	}
	if !storedDisplayLen.Valid || storedDisplayLen.Int64 != int64(len("my-file.pdf")) {
		t.Fatalf("stored display_name_ciphertext length = %v, want %d", storedDisplayLen, len("my-file.pdf"))
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Session 11.7.3 — Pointer File Retrieval
// ═══════════════════════════════════════════════════════════════════════

func TestPointerFileReturnsCiphertextFieldsVerbatim(t *testing.T) {
	db := openTestDB(t)
	ownerID, _, priv := insertTestOwnerWithKey(t, db)
	fileID := uuid.New()
	insertPlaceholderFile(t, db, fileID, ownerID, 4096)

	req := fileRegisterRequestBody{
		FileID:            fileID,
		PointerCiphertext: base64.StdEncoding.EncodeToString([]byte("exact-pointer-bytes")),
		PointerNonce:      base64.StdEncoding.EncodeToString([]byte("123456789012")),     // 12 bytes
		PointerTag:        base64.StdEncoding.EncodeToString([]byte("1234567890123456")), // 16 bytes
		OriginalSizeBytes: 4096,
		SchemaVersion:     1,
	}
	req = signFileRegisterRequest(t, priv, req)
	regBody, _ := json.Marshal(req)
	regReq := withClaims(httptest.NewRequest(http.MethodPost, "/api/v1/file/register", bytes.NewReader(regBody)),
		VerifiedClaims{Subject: ownerID, Role: "owner"})
	regW := httptest.NewRecorder()
	NewFileRegisterHandler(db).HandleRegister(regW, regReq)
	if regW.Code != http.StatusCreated {
		t.Fatalf("register: status = %d, body = %s", regW.Code, regW.Body.String())
	}

	r := withClaims(httptest.NewRequest(http.MethodGet, "/api/v1/file/"+fileID.String()+"/pointer", nil),
		VerifiedClaims{Subject: ownerID, Role: "owner"})
	r.SetPathValue("file_id", fileID.String())
	w := httptest.NewRecorder()
	NewPointerFileHandler(db).HandlePointer(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	resp := decodeJSON[pointerFileResponseBody](t, w.Body.Bytes())
	if resp.PointerCiphertext != req.PointerCiphertext {
		t.Fatalf("pointer_ciphertext = %q, want %q", resp.PointerCiphertext, req.PointerCiphertext)
	}
	if resp.PointerNonce != req.PointerNonce || resp.PointerTag != req.PointerTag {
		t.Fatalf("pointer_nonce/tag did not round-trip verbatim")
	}
	if resp.OriginalSizeBytes != 4096 {
		t.Fatalf("original_size_bytes = %d, want 4096", resp.OriginalSizeBytes)
	}
}

func TestPointerFileRejectsUnknownFileID(t *testing.T) {
	db := openTestDB(t)
	ownerID := uuid.New()
	r := withClaims(httptest.NewRequest(http.MethodGet, "/api/v1/file/"+uuid.New().String()+"/pointer", nil),
		VerifiedClaims{Subject: ownerID, Role: "owner"})
	r.SetPathValue("file_id", uuid.New().String())
	w := httptest.NewRecorder()

	NewPointerFileHandler(db).HandlePointer(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", w.Code, w.Body.String())
	}
}

func TestPointerFileRejectsMismatchedOwner(t *testing.T) {
	db := openTestDB(t)
	ownerID, _, _ := insertTestOwnerWithKey(t, db)
	fileID := uuid.New()
	insertPlaceholderFile(t, db, fileID, ownerID, 1024)

	otherOwnerID := uuid.New()
	r := withClaims(httptest.NewRequest(http.MethodGet, "/api/v1/file/"+fileID.String()+"/pointer", nil),
		VerifiedClaims{Subject: otherOwnerID, Role: "owner"})
	r.SetPathValue("file_id", fileID.String())
	w := httptest.NewRecorder()

	NewPointerFileHandler(db).HandlePointer(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", w.Code, w.Body.String())
	}
}

// ═══════════════════════════════════════════════════════════════════════
// Session 11.7.4 — File Delete
// ═══════════════════════════════════════════════════════════════════════

func insertSegmentAndChunkAssignmentForFile(t *testing.T, db *sql.DB, fileID, providerID uuid.UUID, shardIndex int) {
	t.Helper()
	var segmentID uuid.UUID
	if err := db.QueryRow(`INSERT INTO segments (file_id, segment_index) VALUES ($1, 0) RETURNING segment_id`, fileID).Scan(&segmentID); err != nil {
		// segment for this file may already exist from a prior call within the same test
		if err2 := db.QueryRow(`SELECT segment_id FROM segments WHERE file_id = $1 AND segment_index = 0`, fileID).Scan(&segmentID); err2 != nil {
			t.Fatalf("insert or find segment: %v / %v", err, err2)
		}
	}
	chunkID := randChunkID(t)
	if _, err := db.Exec(`
		INSERT INTO chunk_assignments (chunk_id, is_vetting_chunk, segment_id, shard_index, provider_id, status)
		VALUES ($1, FALSE, $2, $3, $4, 'ACTIVE')`,
		chunkID[:], segmentID, shardIndex, providerID); err != nil {
		t.Fatalf("insert chunk assignment: %v", err)
	}
}

func TestFileDeleteMarksAllAssignmentsPendingDeletion(t *testing.T) {
	db := openTestDB(t)
	ownerID, _, _ := insertTestOwnerWithKey(t, db)
	fileID := uuid.New()
	insertPlaceholderFile(t, db, fileID, ownerID, 1024)

	p1 := insertActiveProviderWithASN(t, db, "AS100")
	p2 := insertActiveProviderWithASN(t, db, "AS200")
	insertSegmentAndChunkAssignmentForFile(t, db, fileID, p1, 0)
	insertSegmentAndChunkAssignmentForFile(t, db, fileID, p2, 1)

	r := withClaims(httptest.NewRequest(http.MethodDelete, "/api/v1/file/"+fileID.String(), nil),
		VerifiedClaims{Subject: ownerID, Role: "owner"})
	r.SetPathValue("file_id", fileID.String())
	w := httptest.NewRecorder()

	h := NewFileDeleteHandler(db, config.ProductionProfile)
	h.HandleDelete(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	resp := decodeJSON[fileDeleteResponseBody](t, w.Body.Bytes())
	if resp.AssignmentsMarked != 2 {
		t.Fatalf("assignments_marked = %d, want 2", resp.AssignmentsMarked)
	}
	if resp.Status != "DELETED" {
		t.Fatalf("status = %q, want DELETED", resp.Status)
	}

	var pendingCount int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM chunk_assignments
		WHERE segment_id IN (SELECT segment_id FROM segments WHERE file_id = $1) AND status = 'PENDING_DELETION'`,
		fileID).Scan(&pendingCount); err != nil {
		t.Fatalf("query pending count: %v", err)
	}
	if pendingCount != 2 {
		t.Fatalf("PENDING_DELETION rows = %d, want 2", pendingCount)
	}

	var fileStatus string
	if err := db.QueryRow(`SELECT status FROM files WHERE file_id = $1`, fileID).Scan(&fileStatus); err != nil {
		t.Fatalf("query file status: %v", err)
	}
	if fileStatus != "DELETED" {
		t.Fatalf("files.status = %q, want DELETED", fileStatus)
	}
}

func TestFileDeleteRejectsAlreadyDeleted(t *testing.T) {
	db := openTestDB(t)
	ownerID, _, _ := insertTestOwnerWithKey(t, db)
	fileID := uuid.New()
	insertPlaceholderFile(t, db, fileID, ownerID, 1024)

	h := NewFileDeleteHandler(db, config.ProductionProfile)
	r1 := withClaims(httptest.NewRequest(http.MethodDelete, "/api/v1/file/"+fileID.String(), nil),
		VerifiedClaims{Subject: ownerID, Role: "owner"})
	r1.SetPathValue("file_id", fileID.String())
	w1 := httptest.NewRecorder()
	h.HandleDelete(w1, r1)
	if w1.Code != http.StatusOK {
		t.Fatalf("first delete: status = %d, body = %s", w1.Code, w1.Body.String())
	}

	r2 := withClaims(httptest.NewRequest(http.MethodDelete, "/api/v1/file/"+fileID.String(), nil),
		VerifiedClaims{Subject: ownerID, Role: "owner"})
	r2.SetPathValue("file_id", fileID.String())
	w2 := httptest.NewRecorder()
	h.HandleDelete(w2, r2)
	if w2.Code != http.StatusConflict {
		t.Fatalf("second delete: status = %d, want 409, body = %s", w2.Code, w2.Body.String())
	}
}

func TestFileDeleteReportsUnreachableProvidersAsPending(t *testing.T) {
	db := openTestDB(t)
	ownerID, _, _ := insertTestOwnerWithKey(t, db)
	fileID := uuid.New()
	insertPlaceholderFile(t, db, fileID, ownerID, 1024)

	freshProvider := insertActiveProviderWithASN(t, db, "AS100")
	if _, err := db.Exec(`UPDATE providers SET last_heartbeat_ts = NOW() WHERE provider_id = $1`, freshProvider); err != nil {
		t.Fatalf("set fresh heartbeat: %v", err)
	}
	staleProvider := insertActiveProviderWithASN(t, db, "AS200")
	if _, err := db.Exec(`UPDATE providers SET last_heartbeat_ts = $2 WHERE provider_id = $1`,
		staleProvider, time.Now().Add(-72*time.Hour)); err != nil {
		t.Fatalf("set stale heartbeat: %v", err)
	}
	insertSegmentAndChunkAssignmentForFile(t, db, fileID, freshProvider, 0)
	insertSegmentAndChunkAssignmentForFile(t, db, fileID, staleProvider, 1)

	r := withClaims(httptest.NewRequest(http.MethodDelete, "/api/v1/file/"+fileID.String(), nil),
		VerifiedClaims{Subject: ownerID, Role: "owner"})
	r.SetPathValue("file_id", fileID.String())
	w := httptest.NewRecorder()

	h := NewFileDeleteHandler(db, config.ProductionProfile)
	h.HandleDelete(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	resp := decodeJSON[fileDeleteResponseBody](t, w.Body.Bytes())
	if resp.ProvidersNotified != 1 {
		t.Fatalf("providers_notified = %d, want 1 (only the fresh-heartbeat provider)", resp.ProvidersNotified)
	}
	if resp.ProvidersPending != 1 {
		t.Fatalf("providers_pending = %d, want 1 (the stale-heartbeat provider)", resp.ProvidersPending)
	}
}
