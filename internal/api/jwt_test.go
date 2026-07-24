// Package api is declared in doc.go.
// Unit tests for JWT issuance/verification and JWKS.
//
// Tests:
//   - TestIssueAndVerifyJWTRoundTrip
//   - TestVerifyJWTRejectsExpired
//   - TestVerifyJWTRejectsBadSignature
//   - TestVerifyJWTRejectsMalformedToken
//   - TestJWTUsesStandardEdDSANotHashThenSign
//   - TestJWKSResponseShape
//
// [REF: OAS securitySchemes.BearerAuth, JwksResponse, build.md Phase 11.4]

package api

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestIssueAndVerifyJWTRoundTrip(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	subject := uuid.New()

	token, err := IssueJWT(priv, subject, "owner", OwnerTokenTTL)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}

	claims, err := VerifyJWT(pub, token)
	if err != nil {
		t.Fatalf("VerifyJWT: %v", err)
	}
	if claims.Subject != subject {
		t.Errorf("Subject = %v, want %v", claims.Subject, subject)
	}
	if claims.Role != "owner" {
		t.Errorf("Role = %q, want %q", claims.Role, "owner")
	}
	if claims.Issuer != jwtIssuer {
		t.Errorf("Issuer = %q, want %q", claims.Issuer, jwtIssuer)
	}
}

func TestVerifyJWTRejectsExpired(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	token, err := IssueJWT(priv, uuid.New(), "provider", -1*time.Minute) // already-expired TTL
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}

	if _, err := VerifyJWT(pub, token); err != ErrJWTExpired {
		t.Errorf("VerifyJWT(expired) = %v, want ErrJWTExpired", err)
	}
}

func TestVerifyJWTRejectsBadSignature(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	otherPub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey (other): %v", err)
	}

	token, err := IssueJWT(priv, uuid.New(), "owner", OwnerTokenTTL)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}

	if _, err := VerifyJWT(otherPub, token); err != ErrJWTInvalidSignature {
		t.Errorf("VerifyJWT(wrong public key) = %v, want ErrJWTInvalidSignature", err)
	}

	// Tamper with the payload segment directly.
	parts := strings.Split(token, ".")
	tampered := parts[0] + "." + parts[1] + "x" + "." + parts[2]
	if _, err := VerifyJWT(pub, tampered); err == nil {
		t.Error("VerifyJWT(tampered payload) = nil error, want a rejection")
	}
}

func TestVerifyJWTRejectsMalformedToken(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	for _, bad := range []string{"", "not-a-jwt", "a.b", "a.b.c.d"} {
		if _, err := VerifyJWT(pub, bad); err != ErrJWTMalformed {
			t.Errorf("VerifyJWT(%q) = %v, want ErrJWTMalformed", bad, err)
		}
	}
}

// TestJWTUsesStandardEdDSANotHashThenSign confirms tokens issued by
// IssueJWT are independently verifiable with plain crypto/ed25519.Verify
// over the raw signing input — proving they follow RFC 8037's EdDSA JOSE
// algorithm rather than internal/crypto's own hash-then-sign convention
// (this file's own header note).
func TestJWTUsesStandardEdDSANotHashThenSign(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	token, err := IssueJWT(priv, uuid.New(), "owner", OwnerTokenTTL)
	if err != nil {
		t.Fatalf("IssueJWT: %v", err)
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts, want 3", len(parts))
	}
	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}

	if !ed25519.Verify(pub, []byte(signingInput), sig) {
		t.Error("plain ed25519.Verify over the raw signing input failed — " +
			"token was not signed with standard RFC 8037 EdDSA")
	}
}

func TestJWKSResponseShape(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	handler := HandleJWKS(pub, "vyomanaut-ms-2026-q2")

	req := httptest.NewRequest("GET", "/.well-known/jwks.json", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	keys, ok := body["keys"].([]any)
	if !ok || len(keys) != 1 {
		t.Fatalf("keys = %v, want a 1-element array", body["keys"])
	}
	key, ok := keys[0].(map[string]any)
	if !ok {
		t.Fatal("keys[0] is not an object")
	}
	for _, field := range []string{"kty", "crv", "x", "use", "kid"} {
		if _, ok := key[field]; !ok {
			t.Errorf("keys[0] missing required field %q", field)
		}
	}
	if key["kty"] != "OKP" {
		t.Errorf("kty = %v, want OKP", key["kty"])
	}
	if key["crv"] != "Ed25519" {
		t.Errorf("crv = %v, want Ed25519", key["crv"])
	}
}
