// Package audit is declared in doc.go.
// Unit and live-database integration tests for WriteReceiptPhase1,
// WriteReceiptRecordResponse, and WriteReceiptPhase2 — the three-phase
// crash-safe audit_receipts write (Option B, Milestone 7 corrections
// session).
//
// DB-dependent tests use TWO connections:
//   - db (openTestDB): authenticated as vyomanaut_app, the restricted role
//     WriteReceiptPhase1/WriteReceiptRecordResponse/WriteReceiptPhase2
//     actually run as in production. Only this connection is ever passed
//     into the functions under test.
//   - verify (openVerifyDB): authenticated as a privileged role (default:
//     the postgres superuser; CI: vyomanaut_migrator), used ONLY to read
//     back and independently confirm state, and to seed/inspect fixtures
//     (e.g. simulating GC abandonment) that vyomanaut_app's own RLS policies
//     correctly forbid it from doing itself.
//
// Both connections use the PG*-style environment variable convention
// scripts/ci/migration_check.sh already established (PGHOST, PGPORT,
// PGDATABASE, PGSSLMODE shared; PGUSER/PGPASSWORD for the actor,
// PGVERIFY_USER/PGVERIFY_PASSWORD for the verifier), defaulting to
// localhost / vyomanaut_app / vyomanaut_test / postgres to match ci.yml's
// postgres service as closely as a sensible default can. If either
// connection is unreachable, the calling test skips individually — this
// keeps `go test ./...` green in local development without Postgres
// running, while still exercising the real row security policies end to end
// whenever a live database is available.
//
// Tests:
//   - TestWriteReceiptPhase1InsertsPending             audit_result IS NULL after insert
//   - TestWriteReceiptPhase1ReturnsUUIDv7               receipt_id is a valid, time-ordered UUIDv7
//   - TestWriteReceiptPhase1RejectsBadFields            missing required field -> error, no row created
//   - TestWriteReceiptPhase1RejectsReplayedNonce        same nonce, different server_challenge_ts -> ErrReplayDetected, no row created
//   - TestWriteReceiptRecordResponsePersistsJITFlag     response_hash/provider_sig/response_latency_ms/jit_flag all land
//   - TestWriteReceiptRecordResponseIdempotent          second call for the same receipt -> ErrResponseAlreadyRecorded
//   - TestWriteReceiptPhase2PromotesToTerminal          PENDING -> PASS/FAIL/TIMEOUT all succeed (PASS/FAIL via RecordResponse first)
//   - TestWriteReceiptPhase2RejectsPassFailWithoutRecordResponse  PASS/FAIL skipping RecordResponse -> CHECK-constraint error, not silent success
//   - TestWriteReceiptPhase2Idempotent                  second call -> ErrReceiptAlreadyFinal, no second row
//   - TestWriteReceiptPhase2RejectsAbandonedRow          abandoned_at IS NOT NULL -> error, not silent success
//   - TestWriteReceiptPhase2OnlyTouchesAllowedColumns    all other columns unchanged after update
//
// HISTORICAL NOTE (both resolved as of the Milestone 7 corrections session —
// left here so the resolution is traceable, not because either still
// applies):
//   1. An earlier schema revision defined no SELECT policy for vyomanaut_app
//      on audit_receipts, which made every WriteReceiptPhase2 call affect
//      zero rows under RLS regardless of the target row's actual state.
//      audit_receipts_app_select (ADR-032) has since closed this; confirmed
//      live in this session against the current migration.
//   2. WriteReceiptPhase2's signature originally had no path for
//      response_hash/provider_sig ever to become non-NULL, so only
//      AuditTimeout's branch of audit_receipts_response_consistency was
//      reachable. WriteReceiptRecordResponse (this session, Option B) closes
//      this — see receipt.go.
//
// [REF: IC §5.5, IC §6, DM §4.7, DM §6, ADR-015, ADR-032, ADR-033, build.md
//  Phase 7.3 Sessions 7.3.1 and 7.3.2]

package audit

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq" // registers the "postgres" driver used by openTestDB
)

// ── DB fixture plumbing ───────────────────────────────────────────────────────

// openTestDB returns a *sql.DB connected to a live Postgres instance,
// authenticated as vyomanaut_app — the same restricted role WriteReceiptPhase1
// and WriteReceiptPhase2 run as in production, subject to DM §6's row
// security policies. If no live database is reachable within a short
// timeout, the calling test is skipped — see the file header for why this
// must be a per-test skip rather than a package-wide TestMain gate
// (challenge_test.go and validate_test.go share this test binary and need no
// database at all).
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return openAndPing(t, testDSN("PGUSER", "vyomanaut_app", "PGPASSWORD"))
}

// openVerifyDB returns a second *sql.DB, authenticated as a privileged role
// (default: the postgres superuser; CI: vyomanaut_migrator, matching ci.yml's
// postgres service — see the CI ROLE NOTE below), used ONLY to read back and
// independently verify state after calling
// WriteReceiptPhase1/WriteReceiptRecordResponse/WriteReceiptPhase2 through
// the vyomanaut_app-authenticated db from openTestDB, and to seed fixtures
// vyomanaut_app's own RLS policies correctly forbid it from creating itself
// (e.g. TestWriteReceiptPhase2RejectsAbandonedRow's simulated GC abandonment).
//
// CI ROLE NOTE, corrected as of the Milestone 7 corrections session: an
// earlier version of this comment claimed ci.yml's postgres service bootstrapped
// POSTGRES_USER as vyomanaut_app itself, making it a superuser that bypasses
// RLS entirely and meaning this file's Phase 2 tests would pass in CI even
// with every policy deleted. That was true of an older ci.yml revision.
// Current ci.yml (confirmed by reading it directly, not by trusting this
// comment) bootstraps as vyomanaut_migrator; migrations/generator.go creates
// vyomanaut_app and vyomanaut_gc as ordinary, non-superuser roles via
// CREATE ROLE ... IF NOT EXISTS, and scripts/ci/migration_check.sh's
// app_gc_no_bypassrls / app_select_policies_present checks are a hard CI
// gate confirming neither role is a superuser or has BYPASSRLS. Re-verified
// live in this session by running migration_check.sh against a database
// bootstrapped exactly like ci.yml's service — all 30 checks pass. RLS is
// genuinely exercised in CI today; this test file's two-connection split
// exercises the identical boundary in local development.
func openVerifyDB(t *testing.T) *sql.DB {
	t.Helper()
	return openAndPing(t, testDSN("PGVERIFY_USER", "postgres", "PGVERIFY_PASSWORD"))
}

func openAndPing(t *testing.T, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Skipf("sql.Open failed, skipping live-DB test: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		t.Skipf("live Postgres not reachable, skipping live-DB test: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// testDSN builds a connection string from PG*-style environment variables,
// matching scripts/ci/migration_check.sh's convention (PGHOST, PGPORT,
// PGDATABASE, PGSSLMODE shared; userEnvKey/passEnvKey select which
// user/password pair to read, so the actor and verifier connections can use
// different roles).
func testDSN(userEnvKey, userFallback, passEnvKey string) string {
	host := envOr("PGHOST", "localhost")
	port := envOr("PGPORT", "5432")
	user := envOr(userEnvKey, userFallback)
	password := os.Getenv(passEnvKey)
	dbname := envOr("PGDATABASE", "vyomanaut_test")
	sslmode := envOr("PGSSLMODE", "disable")
	return fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		host, port, user, password, dbname, sslmode)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

var (
	testProviderOnce sync.Once
	testProviderID   uuid.UUID
)

// ensureTestProvider creates (once per test binary run, idempotently within
// that run) a throwaway providers row so audit_receipts.provider_id's
// foreign key can be satisfied. This is pure test fixture plumbing — it does
// not exercise or depend on any providers-table business logic, since that
// package doesn't exist yet.
func ensureTestProvider(t *testing.T, db *sql.DB) uuid.UUID {
	t.Helper()
	testProviderOnce.Do(func() {
		var pubKey [32]byte
		_, _ = rand.Read(pubKey[:])
		var phoneSuffix [5]byte
		_, _ = rand.Read(phoneSuffix[:])
		phone := fmt.Sprintf("+91%x", phoneSuffix[:])

		id := uuid.New()
		_, err := db.Exec(
			`INSERT INTO providers (provider_id, phone_number, ed25519_public_key, declared_storage_gb, city, region, asn)
			 VALUES ($1, $2, $3, 50, 'TestCity', 'TestRegion', 'SIM-AS1')`,
			id, phone, pubKey[:],
		)
		if err != nil {
			t.Fatalf("ensureTestProvider: insert throwaway providers row: %v", err)
		}
		testProviderID = id
	})
	return testProviderID
}

func randChunkID() [32]byte {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return b
}

func randNonce() [33]byte {
	var b [33]byte
	_, _ = rand.Read(b[:])
	return b
}

func freshFields(providerID uuid.UUID) ReceiptFields {
	return ReceiptFields{
		ChunkID:           randChunkID(),
		ProviderID:        providerID,
		ChallengeNonce:    randNonce(),
		ServerChallengeTs: time.Now().UTC(),
	}
}

// ── WriteReceiptPhase1 ─────────────────────────────────────────────────────────

// TestWriteReceiptPhase1InsertsPending verifies that a freshly-inserted row
// has audit_result = NULL (the PENDING state, DM §8.9).
func TestWriteReceiptPhase1InsertsPending(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := ensureTestProvider(t, db)

	id, err := WriteReceiptPhase1(context.Background(), db, freshFields(providerID))
	if err != nil {
		t.Fatalf("WriteReceiptPhase1: %v", err)
	}

	var auditResult sql.NullString
	err = verify.QueryRow(`SELECT audit_result FROM audit_receipts WHERE receipt_id = $1`, id).Scan(&auditResult)
	if err != nil {
		t.Fatalf("query back the inserted row: %v", err)
	}
	if auditResult.Valid {
		t.Errorf("audit_result = %q, want NULL (PENDING)", auditResult.String)
	}
}

// TestWriteReceiptPhase1ReturnsUUIDv7 verifies receipt_id is a version-7 UUID
// and that successive receipt_ids sort in time order — UUIDv7's defining
// property, and the reason IC §5.5 requires application-layer generation
// instead of gen_random_uuid() (which produces v4, not time-ordered).
func TestWriteReceiptPhase1ReturnsUUIDv7(t *testing.T) {
	db := openTestDB(t)
	providerID := ensureTestProvider(t, db)

	id1, err := WriteReceiptPhase1(context.Background(), db, freshFields(providerID))
	if err != nil {
		t.Fatalf("WriteReceiptPhase1 (first): %v", err)
	}
	if id1.Version() != 7 {
		t.Errorf("receipt_id version = %d, want 7 (UUIDv7)", id1.Version())
	}

	time.Sleep(2 * time.Millisecond) // ensure a distinct millisecond timestamp component
	id2, err := WriteReceiptPhase1(context.Background(), db, freshFields(providerID))
	if err != nil {
		t.Fatalf("WriteReceiptPhase1 (second): %v", err)
	}

	if bytes.Compare(id1[:], id2[:]) >= 0 {
		t.Errorf("UUIDv7 ordering: id1=%s should sort before id2=%s", id1, id2)
	}
}

// TestWriteReceiptPhase1RejectsBadFields verifies that each required
// ReceiptFields left at its zero value is rejected before any row is
// created — no partial or garbage row should ever land in audit_receipts.
func TestWriteReceiptPhase1RejectsBadFields(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := ensureTestProvider(t, db)
	base := freshFields(providerID)

	cases := []struct {
		name   string
		mutate func(f ReceiptFields) ReceiptFields
	}{
		{"zero_ChunkID", func(f ReceiptFields) ReceiptFields { f.ChunkID = [32]byte{}; return f }},
		{"zero_ProviderID", func(f ReceiptFields) ReceiptFields { f.ProviderID = uuid.Nil; return f }},
		{"zero_ChallengeNonce", func(f ReceiptFields) ReceiptFields { f.ChallengeNonce = [33]byte{}; return f }},
		{"zero_ServerChallengeTs", func(f ReceiptFields) ReceiptFields { f.ServerChallengeTs = time.Time{}; return f }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var before int
			if err := verify.QueryRow(`SELECT COUNT(*) FROM audit_receipts`).Scan(&before); err != nil {
				t.Fatalf("count before: %v", err)
			}

			_, err := WriteReceiptPhase1(context.Background(), db, tc.mutate(base))
			if err == nil {
				t.Error("expected an error for a missing required field, got nil")
			}

			var after int
			if err := verify.QueryRow(`SELECT COUNT(*) FROM audit_receipts`).Scan(&after); err != nil {
				t.Fatalf("count after: %v", err)
			}
			if after != before {
				t.Errorf("row count changed from %d to %d — a row was created despite the validation error", before, after)
			}
		})
	}
}

// ── WriteReceiptPhase1 replay protection ──────────────────────────────────

// TestWriteReceiptPhase1RejectsReplayedNonce verifies the fix for the
// Milestone 7 corrections session's highest-priority finding: a
// challenge_nonce reused with a DIFFERENT server_challenge_ts must be
// rejected, not silently accepted as a second row. Before the fix, this
// exact scenario was reproduced live (a duplicate nonce with a later
// timestamp was accepted, producing two audit_receipts rows for one
// challenge) — audit_receipts_nonce_unique alone
// (UNIQUE(challenge_nonce, server_challenge_ts), necessarily scoped to the
// partition key per ADR-033) cannot catch this; only audit_receipt_nonces'
// PRIMARY KEY on challenge_nonce ALONE can, and WriteReceiptPhase1 never
// wrote to that table until this session.
func TestWriteReceiptPhase1RejectsReplayedNonce(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := ensureTestProvider(t, db)

	fields := freshFields(providerID)
	id1, err := WriteReceiptPhase1(context.Background(), db, fields)
	if err != nil {
		t.Fatalf("first WriteReceiptPhase1: %v", err)
	}

	// Same nonce, different server_challenge_ts, different chunk — exactly
	// the scenario audit_receipts_nonce_unique's per-partition scope misses.
	replay := fields
	replay.ChunkID = randChunkID()
	replay.ServerChallengeTs = fields.ServerChallengeTs.Add(5 * time.Minute)

	_, err = WriteReceiptPhase1(context.Background(), db, replay)
	if !errors.Is(err, ErrReplayDetected) {
		t.Fatalf("replayed nonce: got %v, want ErrReplayDetected", err)
	}

	var count int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM audit_receipts WHERE challenge_nonce = $1`, fields.ChallengeNonce[:]).Scan(&count); err != nil {
		t.Fatalf("count for challenge_nonce: %v", err)
	}
	if count != 1 {
		t.Errorf("audit_receipts rows for this nonce = %d, want exactly 1 (the replay must not create a second row)", count)
	}

	var nonceCount int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM audit_receipt_nonces WHERE challenge_nonce = $1`, fields.ChallengeNonce[:]).Scan(&nonceCount); err != nil {
		t.Fatalf("count for audit_receipt_nonces: %v", err)
	}
	if nonceCount != 1 {
		t.Errorf("audit_receipt_nonces rows for this nonce = %d, want exactly 1", nonceCount)
	}

	// id1 must be untouched and still the only receipt for this nonce.
	var stillPending sql.NullString
	if err := verify.QueryRow(`SELECT audit_result FROM audit_receipts WHERE receipt_id = $1`, id1).Scan(&stillPending); err != nil {
		t.Fatalf("query back original receipt: %v", err)
	}
	if stillPending.Valid {
		t.Errorf("original receipt's audit_result = %q, want NULL (untouched by the rejected replay)", stillPending.String)
	}
}

// ── WriteReceiptRecordResponse ──────────────────────────────────────────────

// TestWriteReceiptRecordResponsePersistsJITFlag verifies that
// WriteReceiptRecordResponse actually persists response_hash, provider_sig,
// response_latency_ms, and jit_flag — closing the gap where EvaluateJIT's
// result was computed but had no caller anywhere in the codebase to persist
// it. p95ThroughputKbps = 1000 and the two chosen latencies are far enough
// from EvaluateJIT's floor (76.8ms at that throughput — see jit.go) that
// this is not a boundary-precision test; jit_test.go already covers
// EvaluateJIT's own math at the boundary. This test's job is persistence,
// not arithmetic.
func TestWriteReceiptRecordResponsePersistsJITFlag(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := ensureTestProvider(t, db)
	p95 := 1000.0 // KB/s -> EvaluateJIT floor = (256/1000)*0.3*1000 = 76.8ms

	cases := []struct {
		name        string
		latencyMs   int
		wantJITFlag bool
	}{
		{"fast_response_flags_jit", 50, true},   // 50ms < 76.8ms floor
		{"normal_response_no_flag", 500, false}, // 500ms > 76.8ms floor
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := WriteReceiptPhase1(context.Background(), db, freshFields(providerID))
			if err != nil {
				t.Fatalf("WriteReceiptPhase1: %v", err)
			}

			var hash [32]byte
			var sig [64]byte
			_, _ = rand.Read(hash[:])
			_, _ = rand.Read(sig[:])

			if err := WriteReceiptRecordResponse(context.Background(), db, id, hash, sig, tc.latencyMs, &p95); err != nil {
				t.Fatalf("WriteReceiptRecordResponse: %v", err)
			}

			var gotHash, gotSig []byte
			var gotLatency int
			var gotJIT bool
			var gotAuditResult sql.NullString
			err = verify.QueryRow(`
				SELECT response_hash, provider_sig, response_latency_ms, jit_flag, audit_result
				FROM audit_receipts WHERE receipt_id = $1`, id).
				Scan(&gotHash, &gotSig, &gotLatency, &gotJIT, &gotAuditResult)
			if err != nil {
				t.Fatalf("query back: %v", err)
			}

			if !bytes.Equal(gotHash, hash[:]) {
				t.Error("response_hash was not persisted correctly")
			}
			if !bytes.Equal(gotSig, sig[:]) {
				t.Error("provider_sig was not persisted correctly")
			}
			if gotLatency != tc.latencyMs {
				t.Errorf("response_latency_ms = %d, want %d", gotLatency, tc.latencyMs)
			}
			if gotJIT != tc.wantJITFlag {
				t.Errorf("jit_flag = %v, want %v", gotJIT, tc.wantJITFlag)
			}
			if gotAuditResult.Valid {
				t.Errorf("audit_result = %q, want still NULL — RecordResponse must not adjudicate", gotAuditResult.String)
			}
		})
	}
}

// TestWriteReceiptRecordResponseIdempotent verifies a second
// WriteReceiptRecordResponse call for the same receipt returns
// ErrResponseAlreadyRecorded and does not overwrite the first response —
// guarding against a second, potentially different, signed response for the
// same challenge silently clobbering the first.
func TestWriteReceiptRecordResponseIdempotent(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := ensureTestProvider(t, db)
	p95 := 1000.0

	id, err := WriteReceiptPhase1(context.Background(), db, freshFields(providerID))
	if err != nil {
		t.Fatalf("WriteReceiptPhase1: %v", err)
	}

	var hash1, hash2 [32]byte
	var sig1, sig2 [64]byte
	_, _ = rand.Read(hash1[:])
	_, _ = rand.Read(sig1[:])
	_, _ = rand.Read(hash2[:])
	_, _ = rand.Read(sig2[:])

	if err := WriteReceiptRecordResponse(context.Background(), db, id, hash1, sig1, 100, &p95); err != nil {
		t.Fatalf("first WriteReceiptRecordResponse: %v", err)
	}

	err = WriteReceiptRecordResponse(context.Background(), db, id, hash2, sig2, 200, &p95)
	if !errors.Is(err, ErrResponseAlreadyRecorded) {
		t.Errorf("second call: got %v, want ErrResponseAlreadyRecorded", err)
	}

	var gotHash []byte
	if err := verify.QueryRow(`SELECT response_hash FROM audit_receipts WHERE receipt_id = $1`, id).Scan(&gotHash); err != nil {
		t.Fatalf("query back: %v", err)
	}
	if !bytes.Equal(gotHash, hash1[:]) {
		t.Error("response_hash was overwritten by the second (rejected) call — must retain the first response")
	}
}

// ── WriteReceiptPhase2 ─────────────────────────────────────────────────────────

// TestWriteReceiptPhase2PromotesToTerminal verifies all three terminal
// results (PASS, FAIL, TIMEOUT) correctly promote a PENDING row.
//
// All three now succeed (Milestone 7 corrections session, Option B): PASS
// and FAIL first go through WriteReceiptRecordResponse to populate
// response_hash/provider_sig, satisfying DM §4.7's
// audit_receipts_response_consistency CHECK constraint before
// WriteReceiptPhase2 sets a terminal audit_result. TIMEOUT skips
// RecordResponse entirely, same as before. See
// TestWriteReceiptPhase2RejectsPassFailWithoutRecordResponse for the
// negative case (PASS/FAIL WITHOUT RecordResponse first).
func TestWriteReceiptPhase2PromotesToTerminal(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := ensureTestProvider(t, db)
	p95 := 1000.0

	t.Run("TIMEOUT_succeeds", func(t *testing.T) {
		id, err := WriteReceiptPhase1(context.Background(), db, freshFields(providerID))
		if err != nil {
			t.Fatalf("WriteReceiptPhase1: %v", err)
		}

		var sig [64]byte
		_, _ = rand.Read(sig[:])
		if err := WriteReceiptPhase2(context.Background(), db, id, AuditTimeout, sig, time.Now().UTC()); err != nil {
			t.Fatalf("WriteReceiptPhase2(AuditTimeout): %v", err)
		}

		var got string
		if err := verify.QueryRow(`SELECT audit_result::text FROM audit_receipts WHERE receipt_id = $1`, id).Scan(&got); err != nil {
			t.Fatalf("query back: %v", err)
		}
		if got != "TIMEOUT" {
			t.Errorf("audit_result = %q, want %q", got, "TIMEOUT")
		}
	})

	for _, result := range []AuditResult{AuditPass, AuditFail} {
		result := result
		want, _ := auditResultToSQL(result)
		t.Run(want+"_succeeds_after_RecordResponse", func(t *testing.T) {
			id, err := WriteReceiptPhase1(context.Background(), db, freshFields(providerID))
			if err != nil {
				t.Fatalf("WriteReceiptPhase1: %v", err)
			}

			var hash [32]byte
			var providerSig [64]byte
			_, _ = rand.Read(hash[:])
			_, _ = rand.Read(providerSig[:])
			if err := WriteReceiptRecordResponse(context.Background(), db, id, hash, providerSig, 100, &p95); err != nil {
				t.Fatalf("WriteReceiptRecordResponse: %v", err)
			}

			var serviceSig [64]byte
			_, _ = rand.Read(serviceSig[:])
			if err := WriteReceiptPhase2(context.Background(), db, id, result, serviceSig, time.Now().UTC()); err != nil {
				t.Fatalf("WriteReceiptPhase2(%s): %v", want, err)
			}

			var got string
			var gotHash []byte
			if err := verify.QueryRow(`SELECT audit_result::text, response_hash FROM audit_receipts WHERE receipt_id = $1`, id).Scan(&got, &gotHash); err != nil {
				t.Fatalf("query back: %v", err)
			}
			if got != want {
				t.Errorf("audit_result = %q, want %q", got, want)
			}
			if !bytes.Equal(gotHash, hash[:]) {
				t.Error("response_hash written by RecordResponse was disturbed by Phase 2")
			}
		})
	}
}

// TestWriteReceiptPhase2RejectsPassFailWithoutRecordResponse verifies that
// calling WriteReceiptPhase2 with PASS or FAIL WITHOUT a prior
// WriteReceiptRecordResponse call still fails DM §4.7's
// audit_receipts_response_consistency CHECK constraint, rather than being
// silently allowed through with NULL response_hash/provider_sig. This is now
// a caller-ordering-contract regression test (see the PRECONDITION note on
// WriteReceiptPhase2 in receipt.go), not a "known gap": if a future change
// ever let this succeed, it would mean the CHECK constraint was silently
// weakened or bypassed.
func TestWriteReceiptPhase2RejectsPassFailWithoutRecordResponse(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := ensureTestProvider(t, db)

	for _, result := range []AuditResult{AuditPass, AuditFail} {
		result := result
		want, _ := auditResultToSQL(result)
		t.Run(want+"_fails_response_consistency_check", func(t *testing.T) {
			id, err := WriteReceiptPhase1(context.Background(), db, freshFields(providerID))
			if err != nil {
				t.Fatalf("WriteReceiptPhase1: %v", err)
			}

			var sig [64]byte
			_, _ = rand.Read(sig[:])
			err = WriteReceiptPhase2(context.Background(), db, id, result, sig, time.Now().UTC())
			if err == nil {
				t.Fatalf("WriteReceiptPhase2(%s) without RecordResponse: got nil error, want a CHECK-constraint failure", want)
			}
			if errors.Is(err, ErrReceiptAlreadyFinal) {
				t.Fatalf("WriteReceiptPhase2(%s): got ErrReceiptAlreadyFinal, want a database CHECK-constraint "+
					"error — a fresh PENDING row should reach the constraint, not be treated as already-final", want)
			}
			t.Logf("WriteReceiptPhase2(%s) without RecordResponse correctly failed: %v", want, err)

			var auditResult sql.NullString
			if qErr := verify.QueryRow(`SELECT audit_result FROM audit_receipts WHERE receipt_id = $1`, id).Scan(&auditResult); qErr != nil {
				t.Fatalf("query back: %v", qErr)
			}
			if auditResult.Valid {
				t.Errorf("audit_result = %q after a failed Phase 2 call — the row must be untouched", auditResult.String)
			}
		})
	}
}

// TestWriteReceiptPhase2Idempotent verifies a second Phase 2 call for the
// same receiptID returns ErrReceiptAlreadyFinal and never creates or
// modifies a second row (IC §5.5 idempotent-retry protocol).
func TestWriteReceiptPhase2Idempotent(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := ensureTestProvider(t, db)

	id, err := WriteReceiptPhase1(context.Background(), db, freshFields(providerID))
	if err != nil {
		t.Fatalf("WriteReceiptPhase1: %v", err)
	}

	var sig [64]byte
	_, _ = rand.Read(sig[:])
	if err := WriteReceiptPhase2(context.Background(), db, id, AuditTimeout, sig, time.Now().UTC()); err != nil {
		t.Fatalf("first WriteReceiptPhase2: %v", err)
	}

	err = WriteReceiptPhase2(context.Background(), db, id, AuditTimeout, sig, time.Now().UTC())
	if !errors.Is(err, ErrReceiptAlreadyFinal) {
		t.Errorf("second call: got %v, want ErrReceiptAlreadyFinal", err)
	}

	var count int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM audit_receipts WHERE receipt_id = $1`, id).Scan(&count); err != nil {
		t.Fatalf("count for receipt_id: %v", err)
	}
	if count != 1 {
		t.Errorf("row count for receipt_id = %d, want exactly 1 (no second row created)", count)
	}
}

// TestWriteReceiptPhase2RejectsAbandonedRow verifies that a row GC has
// marked abandoned_at IS NOT NULL is rejected by Phase 2 rather than
// silently promoted to a terminal state.
func TestWriteReceiptPhase2RejectsAbandonedRow(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := ensureTestProvider(t, db)

	id, err := WriteReceiptPhase1(context.Background(), db, freshFields(providerID))
	if err != nil {
		t.Fatalf("WriteReceiptPhase1: %v", err)
	}

	// Simulate the GC process abandoning this PENDING row (DM §6
	// audit_receipts_gc_abandon policy) via the privileged verify
	// connection: vyomanaut_app's own audit_receipts_phase2_update policy's
	// WITH CHECK requires a terminal audit_result, so vyomanaut_app cannot
	// set abandoned_at alone even if it wanted to — that update belongs to
	// vyomanaut_gc's policy. This test only needs the resulting row state,
	// not a correct exercise of the (not-yet-built) GC path itself.
	if _, err := verify.Exec(`UPDATE audit_receipts SET abandoned_at = NOW() WHERE receipt_id = $1`, id); err != nil {
		t.Fatalf("simulate abandonment: %v", err)
	}

	var sig [64]byte
	_, _ = rand.Read(sig[:])
	err = WriteReceiptPhase2(context.Background(), db, id, AuditTimeout, sig, time.Now().UTC())
	if !errors.Is(err, ErrReceiptAlreadyFinal) {
		t.Errorf("abandoned row: got %v, want ErrReceiptAlreadyFinal (the WHERE clause makes an "+
			"abandoned row indistinguishable from an already-final one — see receipt.go's doc comment)", err)
	}

	var auditResult sql.NullString
	if err := verify.QueryRow(`SELECT audit_result FROM audit_receipts WHERE receipt_id = $1`, id).Scan(&auditResult); err != nil {
		t.Fatalf("query back: %v", err)
	}
	if auditResult.Valid {
		t.Errorf("audit_result = %q after a rejected Phase 2 call on an abandoned row — the row must be untouched, not silently promoted", auditResult.String)
	}
}

// TestWriteReceiptPhase2OnlyTouchesAllowedColumns verifies that Phase 2
// modifies only audit_result, service_sig, and service_countersign_ts —
// every Phase-1-written column is unchanged afterwards.
func TestWriteReceiptPhase2OnlyTouchesAllowedColumns(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := ensureTestProvider(t, db)
	fields := freshFields(providerID)
	fields.AddressWasStale = true

	id, err := WriteReceiptPhase1(context.Background(), db, fields)
	if err != nil {
		t.Fatalf("WriteReceiptPhase1: %v", err)
	}

	type snapshot struct {
		ChunkID         []byte
		ProviderID      uuid.UUID
		ChallengeNonce  []byte
		ServerChallenge time.Time
		AddressWasStale bool
	}
	fetch := func() snapshot {
		var s snapshot
		err := verify.QueryRow(`
			SELECT chunk_id, provider_id, challenge_nonce, server_challenge_ts, address_was_stale
			FROM audit_receipts WHERE receipt_id = $1`, id).
			Scan(&s.ChunkID, &s.ProviderID, &s.ChallengeNonce, &s.ServerChallenge, &s.AddressWasStale)
		if err != nil {
			t.Fatalf("fetch snapshot: %v", err)
		}
		return s
	}

	before := fetch()

	var sig [64]byte
	_, _ = rand.Read(sig[:])
	if err := WriteReceiptPhase2(context.Background(), db, id, AuditTimeout, sig, time.Now().UTC()); err != nil {
		t.Fatalf("WriteReceiptPhase2: %v", err)
	}

	after := fetch()

	if !bytes.Equal(before.ChunkID, after.ChunkID) {
		t.Error("chunk_id changed after Phase 2 — must be immutable once Phase 1 completes")
	}
	if before.ProviderID != after.ProviderID {
		t.Error("provider_id changed after Phase 2")
	}
	if !bytes.Equal(before.ChallengeNonce, after.ChallengeNonce) {
		t.Error("challenge_nonce changed after Phase 2")
	}
	if !before.ServerChallenge.Equal(after.ServerChallenge) {
		t.Error("server_challenge_ts changed after Phase 2")
	}
	if before.AddressWasStale != after.AddressWasStale {
		t.Error("address_was_stale changed after Phase 2")
	}
}
