// Package api is declared in doc.go.
// Unit tests for WriteError and the error envelope.
//
// Tests:
//   - TestWriteErrorBodyShape
//   - TestWriteErrorRequestIDIsUUIDv7
//   - TestWriteErrorSetsXRequestIDHeader
//   - TestWriteErrorAvailableASNsOnlyWhenSet
//
// [REF: OAS components/schemas/Error, IC §3.3, build.md Phase 11.1
// Session 11.1.1]

package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
)

func TestWriteErrorBodyShape(t *testing.T) {
	rec := httptest.NewRecorder()
	retryAfter := 60
	WriteError(rec, 503, ErrNetworkNotReady, "network not ready", &retryAfter, "", nil)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}

	for _, key := range []string{"error_code", "message", "request_id", "retry_after"} {
		if _, ok := body[key]; !ok {
			t.Errorf("body missing required key %q", key)
		}
	}
	// field, details, available_asns all use omitempty and were not set —
	// they must not appear in the body at all.
	for _, key := range []string{"field", "details", "available_asns"} {
		if _, ok := body[key]; ok {
			t.Errorf("body unexpectedly contains %q, which was not set (omitempty should have dropped it)", key)
		}
	}

	if body["error_code"] != string(ErrNetworkNotReady) {
		t.Errorf("error_code = %v, want %q", body["error_code"], ErrNetworkNotReady)
	}
	if body["retry_after"] != float64(60) {
		t.Errorf("retry_after = %v, want 60", body["retry_after"])
	}
}

func TestWriteErrorRequestIDIsUUIDv7(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, 500, ErrInternal, "boom", nil, "", nil)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}

	requestID, ok := body["request_id"].(string)
	if !ok {
		t.Fatal("request_id is not a string")
	}
	parsed, err := uuid.Parse(requestID)
	if err != nil {
		t.Fatalf("request_id %q does not parse as a UUID: %v", requestID, err)
	}
	if parsed.Version() != 7 {
		t.Errorf("request_id version = %d, want 7 (UUIDv7)", parsed.Version())
	}
}

func TestWriteErrorSetsXRequestIDHeader(t *testing.T) {
	rec := httptest.NewRecorder()
	WriteError(rec, 400, ErrInvalidRequest, "bad request", nil, "", nil)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}

	headerRequestID := rec.Header().Get("X-Request-ID")
	if headerRequestID == "" {
		t.Fatal("X-Request-ID header is empty")
	}
	if headerRequestID != body["request_id"] {
		t.Errorf("X-Request-ID header = %q, want it to equal body's request_id %q", headerRequestID, body["request_id"])
	}
}

func TestWriteErrorAvailableASNsOnlyWhenSet(t *testing.T) {
	// Any code other than INSUFFICIENT_ASN_DIVERSITY: nil in, absent from the body.
	rec := httptest.NewRecorder()
	WriteError(rec, 409, ErrInsufficientEscrow, "insufficient escrow", nil, "", nil)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if _, ok := body["available_asns"]; ok {
		t.Error("available_asns present in the body when nil was passed")
	}

	// INSUFFICIENT_ASN_DIVERSITY with a set value: present in the body.
	rec2 := httptest.NewRecorder()
	available := 3
	WriteError(rec2, 503, ErrInsufficientASNDiversity, "insufficient ASN diversity", nil, "", &available)
	var body2 map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &body2); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if body2["available_asns"] != float64(3) {
		t.Errorf("available_asns = %v, want 3", body2["available_asns"])
	}
}
