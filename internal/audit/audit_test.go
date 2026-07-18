// Package audit is declared in doc.go.
// Consolidated cross-session tests for Milestone 7, per mvp.md §8.2's file
// inventory: "audit_test.go (two-phase crash safety, idempotent retry,
// cross-replica nonce validation)". No earlier M7 session owned this file;
// Sessions 7.1.1–7.5.1's own UNIT_TESTS blocks already exercise each
// function in isolation — this file exercises the FULL pipeline (nonce ->
// response -> two-phase write, and cross-cutting properties like JIT/
// signature independence) that only makes sense once every prior M7 piece
// exists together.
//
// This file reuses fixtures and helpers already declared elsewhere in this
// package rather than redeclaring them: openTestDB/openVerifyDB/
// ensureTestProvider/randChunkID/randNonce/freshFields (receipt_test.go),
// signValidResponse (validate_test.go), fakeSecretsManager/
// newFakeSecretsManager/testSecret (secret_test.go).
//
// TWO KNOWN GAPS from earlier sessions apply here too, since this file
// exercises the same live schema: DM §6 has no SELECT policy for
// vyomanaut_app (hence the openVerifyDB pattern, same as receipt_test.go),
// and audit_receipts_response_consistency makes AuditPass/AuditFail
// unreachable through WriteReceiptPhase1/WriteReceiptPhase2 as currently
// specified (hence AuditTimeout below, even where the narrative is "the
// response validated" — see receipt.go's WriteReceiptPhase2 doc comment).
//
// Tests:
//   - TestTwoPhaseCrashSafety           Phase 1 only, no Phase 2 -> row is queryable, PENDING, not lost
//   - TestTwoPhaseIdempotentRetry       same receiptID twice -> ErrReceiptAlreadyFinal, exactly one row
//   - TestCrossReplicaNonceValidation   a nonce from replica A validates against replica B's independently-loaded cache
//   - TestFullChallengeResponseCycle    nonce -> signed response -> ValidateResponse -> Phase1 -> Phase2, end to end
//   - TestJITAndValidationAreIndependent a response can fail EvaluateJIT while ValidateResponse still passes
//
// [REF: MVP §8.2, DM §3 Invariant 1, IC §8, build.md Phase 7.6 Session 7.6.1]

package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestTwoPhaseCrashSafety verifies that a Phase 1 INSERT with no subsequent
// Phase 2 call — simulating a microservice crash between challenge dispatch
// and response processing — leaves the row durably queryable as PENDING,
// never silently lost. Real garbage collection of stale PENDING rows after
// 48 hours (DM §4.7 abandoned_at) is a separate, later concern; this test
// only checks that nothing is lost immediately after the "crash".
//
// [REF: ADR-015, DM §3 Invariant 1, build.md Phase 7.6 Session 7.6.1]
func TestTwoPhaseCrashSafety(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := ensureTestProvider(t, db)

	id, err := WriteReceiptPhase1(context.Background(), db, freshFields(providerID))
	if err != nil {
		t.Fatalf("WriteReceiptPhase1: %v", err)
	}

	// Simulated crash: no WriteReceiptPhase2 call ever happens.

	var auditResult sql.NullString
	err = verify.QueryRow(`SELECT audit_result FROM audit_receipts WHERE receipt_id = $1`, id).Scan(&auditResult)
	if err != nil {
		t.Fatalf("row not found after simulated crash — Phase 1's durability guarantee was violated: %v", err)
	}
	if auditResult.Valid {
		t.Errorf("audit_result = %q after simulated crash, want NULL (PENDING)", auditResult.String)
	}
}

// TestTwoPhaseIdempotentRetry verifies that a second WriteReceiptPhase2 call
// for the same receiptID and result returns ErrReceiptAlreadyFinal and never
// creates a duplicate row (IC §5.5 idempotent-retry protocol).
//
// [REF: IC §5.5, ADR-015, build.md Phase 7.6 Session 7.6.1]
func TestTwoPhaseIdempotentRetry(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := ensureTestProvider(t, db)

	countRows := func(id uuid.UUID) int {
		var n int
		if err := verify.QueryRow(`SELECT COUNT(*) FROM audit_receipts WHERE receipt_id = $1`, id).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	id, err := WriteReceiptPhase1(context.Background(), db, freshFields(providerID))
	if err != nil {
		t.Fatalf("WriteReceiptPhase1: %v", err)
	}

	var sig [64]byte
	_, _ = rand.Read(sig[:])
	if err := WriteReceiptPhase2(context.Background(), db, id, AuditTimeout, sig, time.Now().UTC()); err != nil {
		t.Fatalf("first WriteReceiptPhase2: %v", err)
	}
	if n := countRows(id); n != 1 {
		t.Fatalf("row count after first Phase 2 write = %d, want 1", n)
	}

	// Retry: same receiptID, same result. Must fail closed with
	// ErrReceiptAlreadyFinal — never a second INSERT, never a silent
	// no-op success on some other row.
	err = WriteReceiptPhase2(context.Background(), db, id, AuditTimeout, sig, time.Now().UTC())
	if !errors.Is(err, ErrReceiptAlreadyFinal) {
		t.Errorf("retry: got %v, want ErrReceiptAlreadyFinal", err)
	}
	if n := countRows(id); n != 1 {
		t.Errorf("row count after retry = %d, want exactly 1 (no duplicate insert)", n)
	}
}

// TestCrossReplicaNonceValidation verifies the property IC §27's failover
// story depends on: a nonce generated by one replica (using server_secret_vN
// it loaded) is recognised as valid by a second, independent replica that
// loaded the same version from the same (simulated, shared) secrets
// manager — without replica B ever having seen this specific nonce
// generated. This is what makes cross-replica challenge validation work
// during a failover, per IC §8's rotation/versioning contract.
//
// [REF: IC §8, ADR-027, build.md Phase 7.6 Session 7.6.1]
func TestCrossReplicaNonceValidation(t *testing.T) {
	// A single fake stands in for the one real secrets manager both
	// replicas would actually talk to.
	shared := newFakeSecretsManager()
	shared.set(1, testSecret(0xaa))

	cacheA := NewClusterSecretCache(shared)
	cacheB := NewClusterSecretCache(shared)

	if err := cacheA.Load(context.Background()); err != nil {
		t.Fatalf("replica A Load: %v", err)
	}
	if err := cacheB.Load(context.Background()); err != nil {
		t.Fatalf("replica B Load: %v", err)
	}

	secretA, versionA, err := cacheA.CurrentSecret()
	if err != nil {
		t.Fatalf("replica A CurrentSecret: %v", err)
	}

	chunkID := randChunkID()
	serverTsMs := time.Now().UnixMilli()
	nonce := ChallengeNonce(secretA, versionA, chunkID, serverTsMs)

	if !cacheB.IsVersionValid(nonce[0]) {
		t.Errorf("replica B: IsVersionValid(%d) = false, want true — replica B never generated "+
			"this nonce but must still recognise its version as currently valid", nonce[0])
	}

	secretB, err := cacheB.SecretForVersion(nonce[0])
	if err != nil {
		t.Fatalf("replica B SecretForVersion(%d): %v", nonce[0], err)
	}
	if string(secretB) != string(secretA) {
		t.Fatal("replica A and replica B loaded different secret bytes for the same version — " +
			"cross-replica validation would silently fail in a real failover")
	}

	// The deeper property: byte-identical secret material means replica B
	// can independently reconstruct the exact same nonce replica A produced.
	recomputed := ChallengeNonce(secretB, nonce[0], chunkID, serverTsMs)
	if recomputed != nonce {
		t.Error("replica B recomputes a different nonce than replica A produced, " +
			"despite agreeing on both version and secret bytes")
	}
}

// TestFullChallengeResponseCycle exercises the entire pipeline end to end:
// ClusterSecretCache -> ChallengeNonce -> a genuinely signed provider
// response -> ValidateResponse -> WriteReceiptPhase1 -> WriteReceiptPhase2,
// then reads the final row back and asserts challenge_nonce is exactly 33
// bytes and audit_result is the expected terminal value.
//
// Uses AuditTimeout as the terminal value, not AuditPass: Session 7.3.2's
// TestWriteReceiptPhase2PromotesToTerminal established that AuditPass/
// AuditFail always fail audit_receipts_response_consistency, since neither
// Phase 1 nor Phase 2 has a parameter for response_hash/provider_sig. This
// cycle runs through to the one terminal value that actually completes
// against the real schema; the signature validating successfully in the
// middle of the cycle is the property this test is actually chasing,
// independent of which terminal value the row ends up with.
//
// [REF: IC §5.5, IC §4.2, ADR-015, build.md Phase 7.6 Session 7.6.1]
func TestFullChallengeResponseCycle(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := ensureTestProvider(t, db)
	providerIDBytes := [16]byte(providerID)

	// ── Secret cache, as the microservice would set up at startup ──────────
	fake := newFakeSecretsManager()
	fake.set(1, testSecret(0x10))
	cache := NewClusterSecretCache(fake)
	if err := cache.Load(context.Background()); err != nil {
		t.Fatalf("ClusterSecretCache.Load: %v", err)
	}
	secret, versionByte, err := cache.CurrentSecret()
	if err != nil {
		t.Fatalf("CurrentSecret: %v", err)
	}

	// ── Challenge dispatch + Phase 1 (Sessions 7.1.1, 7.3.1) ────────────────
	chunkID := randChunkID()
	serverTsMs := time.Now().UnixMilli()
	nonce := ChallengeNonce(secret, versionByte, chunkID, serverTsMs)
	if len(nonce) != 33 {
		t.Fatalf("ChallengeNonce produced %d bytes, want 33", len(nonce))
	}

	receiptID, err := WriteReceiptPhase1(context.Background(), db, ReceiptFields{
		ChunkID:           chunkID,
		ProviderID:        providerID,
		ChallengeNonce:    nonce,
		ServerChallengeTs: time.UnixMilli(serverTsMs).UTC(),
	})
	if err != nil {
		t.Fatalf("WriteReceiptPhase1: %v", err)
	}

	// ── Simulated provider response + signature ─────────────────────────────
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	var pubArr [32]byte
	copy(pubArr[:], pub)
	// responseHash's actual content is outside ValidateResponse's scope (see
	// its own LIMITATION note) — any 32 bytes exercise the signature path.
	responseHash := randChunkID()
	sig := signValidResponse(t, priv, responseHash, nonce, serverTsMs, providerIDBytes)

	// ── Validation (Session 7.2.1) ───────────────────────────────────────────
	if err := ValidateResponse(nonce, responseHash, serverTsMs, providerIDBytes, sig, pubArr); err != nil {
		t.Fatalf("ValidateResponse: %v", err)
	}
	if !cache.IsVersionValid(nonce[0]) {
		t.Fatalf("IsVersionValid(%d) = false — this is the caller-responsibility check "+
			"ValidateResponse's own NOTE describes; a real caller must not skip it", nonce[0])
	}

	// ── Phase 2 (Session 7.3.2) ──────────────────────────────────────────────
	var serviceSig [64]byte
	_, _ = rand.Read(serviceSig[:])
	if err := WriteReceiptPhase2(context.Background(), db, receiptID, AuditTimeout, serviceSig, time.Now().UTC()); err != nil {
		t.Fatalf("WriteReceiptPhase2: %v", err)
	}

	// ── Final assertions ──────────────────────────────────────────────────────
	var gotNonce []byte
	var gotResult string
	err = verify.QueryRow(`SELECT challenge_nonce, audit_result::text FROM audit_receipts WHERE receipt_id = $1`, receiptID).
		Scan(&gotNonce, &gotResult)
	if err != nil {
		t.Fatalf("query back final row: %v", err)
	}
	if len(gotNonce) != 33 {
		t.Errorf("stored challenge_nonce is %d bytes, want 33", len(gotNonce))
	}
	if gotResult != "TIMEOUT" {
		t.Errorf("audit_result = %q, want %q", gotResult, "TIMEOUT")
	}
}

// TestJITAndValidationAreIndependent verifies that EvaluateJIT and
// ValidateResponse are orthogonal: a response can be flagged by one while
// still passing the other, matching how audit_result and jit_flag are
// independent columns in DM §4.7 rather than one gating the other.
//
// [REF: DM §4.7, ADR-014 Defence 3, build.md Phase 7.6 Session 7.6.1]
func TestJITAndValidationAreIndependent(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	var pubArr [32]byte
	copy(pubArr[:], pub)

	nonce := randNonce()
	responseHash := randChunkID()
	var providerID [16]byte
	_, _ = rand.Read(providerID[:])
	serverTsMs := time.Now().UnixMilli()

	sig := signValidResponse(t, priv, responseHash, nonce, serverTsMs, providerID)

	// The signature is genuinely valid...
	if err := ValidateResponse(nonce, responseHash, serverTsMs, providerID, sig, pubArr); err != nil {
		t.Fatalf("ValidateResponse: got %v, want nil (this response's signature is genuinely valid)", err)
	}

	// ...yet the same response can still be flagged by EvaluateJIT if its
	// latency is anomalously fast. Signature validity says nothing about
	// retrieval timing, and vice versa.
	highThroughput := 500.0
	if !EvaluateJIT(50, &highThroughput) {
		t.Error("EvaluateJIT(50ms, 500KB/s): expected true (anomalously fast) for this test's premise to hold")
	}
}
