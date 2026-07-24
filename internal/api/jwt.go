// Package api is declared in doc.go.
// This file implements JWT issuance/verification (BearerAuth, OAS
// securitySchemes.BearerAuth) and GET /.well-known/jwks.json.
//
// [Decision] internal/crypto.SignBytes/VerifyBytes implement Vyomanaut's own
// signing convention — Ed25519 over a SHA-256 digest of the input, NOT
// RFC 8032 plain Ed25519, and explicitly NOT the standardised "Ed25519ph"
// prehash variant either (see internal/crypto/ed25519.go's own header).
// That convention is deliberately NOT used here. A JWT's "EdDSA" JOSE
// algorithm (RFC 8037) is defined as plain Ed25519 signing over the raw
// ASCII signing input (base64url(header) + "." + base64url(payload)) — no
// prehash of any kind. OAS's JwksResponse is a standard JWK Set
// (kty=OKP, crv=Ed25519), meaning any standard JWT/JOSE library must be able
// to verify tokens issued here; signing them with Vyomanaut's internal
// hash-then-sign convention instead would silently produce tokens no
// standard library can verify, defeating the entire purpose of publishing a
// JWKS endpoint. This file therefore calls crypto/ed25519.Sign and .Verify
// directly, never internal/crypto's helpers.
//
// [Decision] Single active signing key, not a rotation-overlap scheme. OAS's
// AdminApiKey and IC §8's ClusterSecretCache both have an explicit,
// documented rotation-overlap window; no equivalent design exists anywhere
// in scope for the JWT signing key itself (OAS's kid description just says
// "rotated quarterly" with no overlap-window semantics specified). Building
// a full multi-key JWKS with kid-based selection and an overlap window here
// would be inventing a mechanism no document actually specifies. A single
// active keypair is implemented; multi-key rotation is a natural, clearly
// separable extension point once that design actually exists, flagged here
// rather than guessed at.
//
// [REF: OAS securitySchemes.BearerAuth, JwksResponse, OtpVerifyResponse,
// build.md Phase 11.4]

package api

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// jwtIssuer is the fixed "iss" claim (OAS BearerAuth, OtpVerifyResponse).
const jwtIssuer = "vyomanaut-microservice-v1"

// Token TTLs (OAS OtpVerifyResponse.token description).
const (
	RegistrationTokenTTL = 1 * time.Hour      // is_new_entity == true
	OwnerTokenTTL        = 24 * time.Hour     // is_new_entity == false, role == owner
	ProviderTokenTTL     = 7 * 24 * time.Hour // is_new_entity == false, role == provider
)

// jwtHeaderB64 is the fixed, constant JOSE header {"alg":"EdDSA","typ":"JWT"},
// base64url-encoded once at package init since it never varies.
var jwtHeaderB64 = base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","typ":"JWT"}`))

// jwtClaims is the wire representation of the JWT payload (OAS BearerAuth:
// "sub (entity UUID), role, iss, exp"). Role omits empty via omitempty so a
// registration token's payload has no "role" key at all, matching OAS's own
// "role is not yet established" description more precisely than emitting
// "role": null would (null is nonetheless also accepted on decode, since
// omitempty only affects encoding).
type jwtClaims struct {
	Sub  string `json:"sub"`
	Role string `json:"role,omitempty"`
	Iss  string `json:"iss"`
	Exp  int64  `json:"exp"`
}

// IssueJWT signs a new BearerAuth token. role is "" for a registration
// token (OtpVerifyResponse.is_new_entity == true — "role is not yet
// established"); "owner" or "provider" otherwise.
func IssueJWT(privateKey ed25519.PrivateKey, subject uuid.UUID, role string, ttl time.Duration) (string, error) {
	claims := jwtClaims{
		Sub:  subject.String(),
		Role: role,
		Iss:  jwtIssuer,
		Exp:  time.Now().UTC().Add(ttl).Unix(),
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("api.IssueJWT: marshal claims: %w", err)
	}
	signingInput := jwtHeaderB64 + "." + base64.RawURLEncoding.EncodeToString(claimsJSON)
	sig := ed25519.Sign(privateKey, []byte(signingInput))
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

var (
	ErrJWTMalformed        = errors.New("api: malformed JWT")
	ErrJWTInvalidSignature = errors.New("api: invalid JWT signature")
	ErrJWTExpired          = errors.New("api: JWT expired")
)

// VerifiedClaims is the parsed, verified result of VerifyJWT.
type VerifiedClaims struct {
	Subject uuid.UUID
	Role    string // "" for a registration token
	Issuer  string
	Expiry  time.Time
}

// VerifyJWT checks the signature (constant-time, via ed25519.Verify) and
// expiry of token, returning its parsed claims. Does not check Issuer
// against jwtIssuer itself — callers that care can compare
// VerifiedClaims.Issuer, but a wrong issuer on an otherwise-validly-signed
// token would only ever happen if this exact signing key were reused
// elsewhere, which is a deployment error, not a per-request check.
// jwtPartsCount is the number of dot-separated segments in a JWT
// (header.payload.signature).
const jwtPartsCount = 3

func VerifyJWT(publicKey ed25519.PublicKey, token string) (VerifiedClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != jwtPartsCount {
		return VerifiedClaims{}, ErrJWTMalformed
	}
	signingInput := parts[0] + "." + parts[1]

	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return VerifiedClaims{}, ErrJWTMalformed
	}
	if !ed25519.Verify(publicKey, []byte(signingInput), sig) {
		return VerifiedClaims{}, ErrJWTInvalidSignature
	}

	claimsJSON, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return VerifiedClaims{}, ErrJWTMalformed
	}
	var claims jwtClaims
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return VerifiedClaims{}, ErrJWTMalformed
	}
	subject, err := uuid.Parse(claims.Sub)
	if err != nil {
		return VerifiedClaims{}, ErrJWTMalformed
	}

	expiry := time.Unix(claims.Exp, 0).UTC()
	if time.Now().UTC().After(expiry) {
		return VerifiedClaims{}, ErrJWTExpired
	}

	return VerifiedClaims{Subject: subject, Role: claims.Role, Issuer: claims.Iss, Expiry: expiry}, nil
}

// ── JWKS ───────────────────────────────────────────────────────────────────────

// jwksKey mirrors one entry of OAS JwksResponse.keys.
type jwksKey struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Use string `json:"use"`
	Kid string `json:"kid"`
}

// jwksResponseBody mirrors OAS components/schemas/JwksResponse.
type jwksResponseBody struct {
	Keys []jwksKey `json:"keys"`
}

// HandleJWKS serves GET /.well-known/jwks.json, publishing publicKey as a
// single-entry JWK Set. kid should follow OAS's own example convention
// (e.g. "vyomanaut-ms-2026-q2") — supplied by the caller (Milestone 12
// wiring), never hardcoded here, since it changes on each quarterly
// rotation.
func HandleJWKS(publicKey ed25519.PublicKey, kid string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		resp := jwksResponseBody{
			Keys: []jwksKey{{
				Kty: "OKP",
				Crv: "Ed25519",
				X:   base64.RawURLEncoding.EncodeToString(publicKey),
				Use: "sig",
				Kid: kid,
			}},
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(resp)
	}
}
