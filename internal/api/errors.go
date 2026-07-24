// Package api is declared in doc.go.
// This file implements the standard Vyomanaut error envelope (OAS
// components/schemas/Error) and the full, OAS-reconciled ErrorCode set.
//
// [Flagged and corrected, build.md Phase 11.1] IC §3.3's error-code table is
// neither exhaustive nor fully consistent with OAS. Scanning every
// error_code value that actually appears in openapi.yaml turns up codes IC
// §3.3 never lists at all, plus two direct naming conflicts on codes both
// documents share (UNAUTHENTICATED vs. OAS's UNAUTHORIZED;
// INSUFFICIENT_ESCROW vs. OAS's INSUFFICIENT_ESCROW_BALANCE). Per IC §3's
// own rule ("do not duplicate REST contracts here... OAS is exclusive"),
// OAS's strings win both conflicts. The reconciled list below is
// implemented in full so that later sessions in this milestone compile
// against constants that actually exist.
//
// [REF: OAS components/schemas/Error, IC §3.3, build.md Phase 11.1
// Session 11.1.1]

package api

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
)

// ErrorCode is a machine-readable, stable-across-releases error identifier
// (OAS Error.properties.error_code).
type ErrorCode string

const (
	// From IC §3.3, unchanged from OAS.
	ErrInvalidRequest             ErrorCode = "INVALID_REQUEST"
	ErrProviderDeparted           ErrorCode = "PROVIDER_DEPARTED"
	ErrEscrowFrozen               ErrorCode = "ESCROW_FROZEN"
	ErrNetworkNotReady            ErrorCode = "NETWORK_NOT_READY"
	ErrInsufficientASNDiversity   ErrorCode = "INSUFFICIENT_ASN_DIVERSITY"
	ErrRazorpayUnavailable        ErrorCode = "RAZORPAY_UNAVAILABLE"
	ErrInternal                   ErrorCode = "INTERNAL_ERROR"
	ErrVettingCapExceeded         ErrorCode = "VETTING_CAP_EXCEEDED"
	ErrRealShardOnVettingProvider ErrorCode = "REAL_SHARD_ON_VETTING_PROVIDER"
	ErrDemoModeRealPayment        ErrorCode = "DEMO_MODE_REAL_PAYMENT"
	ErrProdModeEnvSecret          ErrorCode = "PROD_MODE_ENV_SECRET"

	// Corrected from IC §3.3 to match OAS's actual strings — IC §3.3
	// disagreed with OAS; OAS wins per IC §3's own precedence rule.
	ErrUnauthorized       ErrorCode = "UNAUTHORIZED"                // IC §3.3 wrongly said UNAUTHENTICATED
	ErrInsufficientEscrow ErrorCode = "INSUFFICIENT_ESCROW_BALANCE" // IC §3.3 wrongly said INSUFFICIENT_ESCROW

	// Present in OAS path/example bodies but absent from IC §3.3's table entirely.
	ErrInvalidPhoneNumber      ErrorCode = "INVALID_PHONE_NUMBER"
	ErrInvalidAmount           ErrorCode = "INVALID_AMOUNT"
	ErrInvalidChallengeNonce   ErrorCode = "INVALID_CHALLENGE_NONCE"
	ErrWrongRole               ErrorCode = "WRONG_ROLE"
	ErrInvalidBodySignature    ErrorCode = "INVALID_BODY_SIGNATURE"
	ErrNotFound                ErrorCode = "NOT_FOUND"
	ErrDuplicateChallengeNonce ErrorCode = "DUPLICATE_CHALLENGE_NONCE"
	ErrPhoneAlreadyRegistered  ErrorCode = "PHONE_ALREADY_REGISTERED"
	ErrOTPRateLimited          ErrorCode = "OTP_RATE_LIMITED"
	ErrInvalidOTP              ErrorCode = "INVALID_OTP"
	ErrTokenRefreshRateLimited ErrorCode = "TOKEN_REFRESH_RATE_LIMITED"
	ErrDowntimeAlreadyActive   ErrorCode = "DOWNTIME_ALREADY_ACTIVE"
	ErrFileAlreadyDeleted      ErrorCode = "FILE_ALREADY_DELETED"

	// New — corrects a wrong-code reuse in Session 11.7.2 (Phase 11.7's own
	// flag: the original task returned FILE_ALREADY_DELETED for a file that
	// exists in ACTIVE status, the opposite of what that code name means).
	ErrFileAlreadyRegistered ErrorCode = "FILE_ALREADY_REGISTERED"

	// Referenced in Session 11.11.1 but not yet present in openapi.yaml —
	// flagged for an OAS addition before this ships; the constant is
	// implemented now so the Go code compiles and is ready the moment OAS
	// catches up.
	ErrInsufficientProviderCapacity ErrorCode = "INSUFFICIENT_PROVIDER_CAPACITY"
)

// errorBody mirrors OAS components/schemas/Error exactly. AvailableASNs is a
// single optional field on this one shared struct — OAS re-adds
// available_asns via a redundant allOf composition on the specific
// /api/v1/upload/assign 503 response, but the base schema already declares
// it, so no second wrapper type is created here to mirror that redundancy.
type errorBody struct {
	ErrorCode     ErrorCode      `json:"error_code"`
	Message       string         `json:"message"`
	RequestID     string         `json:"request_id"`
	RetryAfter    *int           `json:"retry_after"`
	Field         string         `json:"field,omitempty"`
	Details       map[string]any `json:"details,omitempty"`
	AvailableASNs *int           `json:"available_asns,omitempty"`
}

// WriteError writes the standard Vyomanaut error envelope (OAS
// components/schemas/Error):
//
//	{"error_code": "...", "message": "...", "request_id": "...", "retry_after": null}
//
// request_id is a fresh UUIDv7, also set on the X-Request-ID response
// header (OAS, IC §3.3), so a caller can correlate the header and the body
// without parsing JSON. field is optional (OAS Error.properties.field) —
// pass "" when not applicable, which omits it from the JSON body via
// `omitempty`. availableASNs is optional (OAS Error.properties.
// available_asns) and should be non-nil only for
// INSUFFICIENT_ASN_DIVERSITY — pass nil otherwise.
func WriteError(w http.ResponseWriter, statusCode int, errorCode ErrorCode, message string, retryAfter *int, field string, availableASNs *int) {
	requestID, err := uuid.NewV7()
	var requestIDStr string
	if err != nil {
		// crypto/rand failure is effectively unrecoverable, but a missing
		// request_id must never prevent the error itself from being
		// reported — fall back to a nil UUID rather than panicking.
		requestIDStr = uuid.Nil.String()
	} else {
		requestIDStr = requestID.String()
	}

	body := errorBody{
		ErrorCode:     errorCode,
		Message:       message,
		RequestID:     requestIDStr,
		RetryAfter:    retryAfter,
		Field:         field,
		AvailableASNs: availableASNs,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", requestIDStr)
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(body)
}
