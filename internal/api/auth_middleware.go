// Package api is declared in doc.go.
// This file implements the BearerAuth middleware — JWT verification for
// every route tagged BearerAuth in OAS, replacing router.go's Phase 11.3
// placeholder (routes registered without auth enforcement, per that file's
// own header note that Phase 11.4 would supply it).
//
// [REF: OAS securitySchemes.BearerAuth, build.md Phase 11.4]

package api

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"strings"
)

// claimsContextKeyType is an unexported type for the context key, per the
// standard Go convention preventing collisions with other packages' context
// keys.
type claimsContextKeyType struct{}

var claimsContextKey = claimsContextKeyType{}

// ClaimsFromContext retrieves the VerifiedClaims a successful bearerAuth
// middleware call placed on the request context. The second return value
// is false if no claims are present (the route is public, or this is
// called somewhere bearerAuth was never applied).
func ClaimsFromContext(ctx context.Context) (VerifiedClaims, bool) {
	claims, ok := ctx.Value(claimsContextKey).(VerifiedClaims)
	return claims, ok
}

// bearerAuthAny accepts a validly-signed, unexpired token for ANY role
// (including a registration token, whose Role is ""). Handlers that must
// distinguish a registration token from an owner/provider token (e.g. every
// non-register endpoint, which OAS's BearerAuth description implies must
// reject a token still carrying "role not yet established") check
// claims.Role themselves via ClaimsFromContext; this middleware only proves
// the signature and expiry are valid.
func bearerAuthAny(publicKey ed25519.PublicKey) func(http.Handler) http.Handler {
	return bearerAuthRole(publicKey, "")
}

// bearerAuthRole requires a validly-signed, unexpired token. If requiredRole
// is non-empty, claims.Role must equal it exactly (rejecting a registration
// token, whose Role is "", from any route that names a specific role). If
// requiredRole is "" (bearerAuthAny), no role check is performed at all —
// any validly-signed token is accepted, registration token included, which
// is exactly what the two register endpoints need.
func bearerAuthRole(publicKey ed25519.PublicKey, requiredRole string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			const prefix = "Bearer "
			if !strings.HasPrefix(authHeader, prefix) {
				WriteError(w, http.StatusUnauthorized, ErrUnauthorized, "missing Bearer token", nil, "", nil)
				return
			}
			token := strings.TrimPrefix(authHeader, prefix)

			claims, err := VerifyJWT(publicKey, token)
			if err != nil {
				status := http.StatusUnauthorized
				msg := "invalid token"
				if errors.Is(err, ErrJWTExpired) {
					msg = "token expired"
				}
				WriteError(w, status, ErrUnauthorized, msg, nil, "", nil)
				return
			}

			if requiredRole != "" && claims.Role != requiredRole {
				WriteError(w, http.StatusForbidden, ErrWrongRole, "token role does not match this endpoint", nil, "", nil)
				return
			}

			ctx := context.WithValue(r.Context(), claimsContextKey, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
