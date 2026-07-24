// Package api is declared in doc.go.
// Unit and live-database integration tests for provider token refresh.
//
// Tests:
//   - TestRefreshAcceptsValidJWT
//   - TestRefreshAcceptsExpiredWithinGraceWindow
//   - TestRefreshRejectsExpiredBeyondGraceWindow
//   - TestRefreshRejectsBadEd25519Signature
//   - TestRefreshRejectsDepartedProvider
//   - TestRefreshRateLimitedWithinThirtyMinutes
//
// [REF: OAS paths./api/v1/provider/token/refresh, build.md Phase 11.4]

package api

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

// insertTestProviderWithKey inserts a provider row with a REAL Ed25519
// keypair (returning the private key), needed to produce a valid
// provider_sig in these tests.
func insertTestProviderWithKey(t *testing.T, db *sql.DB, status string) (uuid.UUID, ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	id := uuid.New()
	var phoneSuffix [4]byte
	_, _ = rand.Read(phoneSuffix[:])
	phone := fmt.Sprintf("+91%010d", uint64(phoneSuffix[0])<<24|uint64(phoneSuffix[1])<<16|uint64(phoneSuffix[2])<<8|uint64(phoneSuffix[3]))

	_, err = db.Exec(`
		INSERT INTO providers (provider_id, phone_number, ed25519_public_key, status, declared_storage_gb, city, region, asn)
		VALUES ($1,$2,$3,$4,50,'TestCity','TestRegion','SIM-AS1')`,
		id, phone, []byte(pub), status,
	)
	if err != nil {
		t.Fatalf("insertTestProviderWithKey: %v", err)
	}
	return id, pub, priv
}

// signRefreshRequest computes a valid provider_sig for (providerID, timestamp).
func signRefreshRequest(priv ed25519.PrivateKey, providerID uuid.UUID, timestamp string) string {
	digest := sha256.Sum256([]byte(providerID.String() + timestamp))
	sig := ed25519.Sign(priv, digest[:])
	return hex.EncodeToString(sig)
}

func TestRefreshAcceptsValidJWT(t *testing.T) {
	db := openTestDB(t)
	msPub, msPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey (microservice): %v", err)
	}
	providerID, providerPub, providerPriv := insertTestProviderWithKey(t, db, "ACTIVE")
	handler := NewProviderTokenRefreshHandler(db, msPub, msPriv)

	currentToken, err := IssueJWT(msPriv, providerID, "provider", ProviderTokenTTL)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	sig := signRefreshRequest(providerPriv, providerID, timestamp)

	reqBody, _ := json.Marshal(tokenRefreshRequestBody{ProviderID: providerID, Timestamp: timestamp, ProviderSig: sig})
	req := httptest.NewRequest("POST", "/api/v1/provider/token/refresh", bytes.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+currentToken)
	rec := httptest.NewRecorder()
	handler.HandleRefresh(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp tokenRefreshResponseBody
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	newClaims, err := VerifyJWT(msPub, resp.Token)
	if err != nil {
		t.Fatalf("VerifyJWT(new token): %v", err)
	}
	if newClaims.Subject != providerID || newClaims.Role != "provider" {
		t.Errorf("new token claims = %+v, want subject=%v role=provider", newClaims, providerID)
	}
	_ = providerPub
}

func TestRefreshAcceptsExpiredWithinGraceWindow(t *testing.T) {
	db := openTestDB(t)
	msPub, msPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	providerID, _, providerPriv := insertTestProviderWithKey(t, db, "ACTIVE")
	handler := NewProviderTokenRefreshHandler(db, msPub, msPriv)

	// Token expired 30 minutes ago — within the 1-hour grace window.
	expiredToken, err := IssueJWT(msPriv, providerID, "provider", -30*time.Minute)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	sig := signRefreshRequest(providerPriv, providerID, timestamp)

	reqBody, _ := json.Marshal(tokenRefreshRequestBody{ProviderID: providerID, Timestamp: timestamp, ProviderSig: sig})
	req := httptest.NewRequest("POST", "/api/v1/provider/token/refresh", bytes.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+expiredToken)
	rec := httptest.NewRecorder()
	handler.HandleRefresh(rec, req)

	if rec.Code != 200 {
		t.Errorf("status = %d, want 200 (within grace window), body = %s", rec.Code, rec.Body.String())
	}
}

func TestRefreshRejectsExpiredBeyondGraceWindow(t *testing.T) {
	db := openTestDB(t)
	msPub, msPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	providerID, _, providerPriv := insertTestProviderWithKey(t, db, "ACTIVE")
	handler := NewProviderTokenRefreshHandler(db, msPub, msPriv)

	// Token expired 2 hours ago — beyond the 1-hour grace window.
	expiredToken, err := IssueJWT(msPriv, providerID, "provider", -2*time.Hour)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	sig := signRefreshRequest(providerPriv, providerID, timestamp)

	reqBody, _ := json.Marshal(tokenRefreshRequestBody{ProviderID: providerID, Timestamp: timestamp, ProviderSig: sig})
	req := httptest.NewRequest("POST", "/api/v1/provider/token/refresh", bytes.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+expiredToken)
	rec := httptest.NewRecorder()
	handler.HandleRefresh(rec, req)

	if rec.Code != 401 {
		t.Errorf("status = %d, want 401 (beyond grace window)", rec.Code)
	}
}

func TestRefreshRejectsBadEd25519Signature(t *testing.T) {
	db := openTestDB(t)
	msPub, msPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	providerID, _, _ := insertTestProviderWithKey(t, db, "ACTIVE")
	handler := NewProviderTokenRefreshHandler(db, msPub, msPriv)

	currentToken, err := IssueJWT(msPriv, providerID, "provider", ProviderTokenTTL)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)

	reqBody, _ := json.Marshal(tokenRefreshRequestBody{
		ProviderID:  providerID,
		Timestamp:   timestamp,
		ProviderSig: hex.EncodeToString(make([]byte, 64)), // all-zero, definitely wrong
	})
	req := httptest.NewRequest("POST", "/api/v1/provider/token/refresh", bytes.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+currentToken)
	rec := httptest.NewRecorder()
	handler.HandleRefresh(rec, req)

	if rec.Code != 403 {
		t.Errorf("status = %d, want 403 (bad signature)", rec.Code)
	}
}

func TestRefreshRejectsDepartedProvider(t *testing.T) {
	db := openTestDB(t)
	msPub, msPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	providerID, _, providerPriv := insertTestProviderWithKey(t, db, "DEPARTED")
	handler := NewProviderTokenRefreshHandler(db, msPub, msPriv)

	currentToken, err := IssueJWT(msPriv, providerID, "provider", ProviderTokenTTL)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	sig := signRefreshRequest(providerPriv, providerID, timestamp)

	reqBody, _ := json.Marshal(tokenRefreshRequestBody{ProviderID: providerID, Timestamp: timestamp, ProviderSig: sig})
	req := httptest.NewRequest("POST", "/api/v1/provider/token/refresh", bytes.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer "+currentToken)
	rec := httptest.NewRecorder()
	handler.HandleRefresh(rec, req)

	if rec.Code != 403 {
		t.Errorf("status = %d, want 403 (departed provider)", rec.Code)
	}
}

func TestRefreshRateLimitedWithinThirtyMinutes(t *testing.T) {
	db := openTestDB(t)
	msPub, msPriv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	providerID, _, providerPriv := insertTestProviderWithKey(t, db, "ACTIVE")
	handler := NewProviderTokenRefreshHandler(db, msPub, msPriv)

	doRefresh := func() int {
		token, err := IssueJWT(msPriv, providerID, "provider", ProviderTokenTTL)
		if err != nil {
			t.Fatalf("IssueJWT: %v", err)
		}
		timestamp := time.Now().UTC().Format(time.RFC3339)
		sig := signRefreshRequest(providerPriv, providerID, timestamp)
		reqBody, _ := json.Marshal(tokenRefreshRequestBody{ProviderID: providerID, Timestamp: timestamp, ProviderSig: sig})
		req := httptest.NewRequest("POST", "/api/v1/provider/token/refresh", bytes.NewReader(reqBody))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		handler.HandleRefresh(rec, req)
		return rec.Code
	}

	if got := doRefresh(); got != 200 {
		t.Fatalf("first refresh: status = %d, want 200", got)
	}
	if got := doRefresh(); got != 429 {
		t.Errorf("second refresh (immediately after): status = %d, want 429", got)
	}
}
