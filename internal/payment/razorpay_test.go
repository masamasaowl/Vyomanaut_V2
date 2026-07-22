// Package payment is declared in doc.go.
// Unit and live-database integration tests for the Razorpay webhook
// handlers. This file also declares the shared DB fixture plumbing
// (openTestDB/openVerifyDB/envOr/testDSN/insertTestOwner/insertTestProvider)
// that mock_test.go, ledger_test.go, and release_test.go reuse rather than
// redeclaring, mirroring internal/scoring's and internal/repair's fixture
// pattern.
//
// Tests:
//   - TestDepositWebhookCreditsOwnerLedgerNotProviderLedger
//   - TestDepositWebhookIdempotentOnRedelivery
//   - TestDepositWebhookRejectsBadSignature
//   - TestReversalWebhookInsertsReversalEvent
//   - TestAccountCreatedSetsProfileDrivenCoolingPeriod
//   - TestAccountCreatedIdempotentOnRedelivery
//
// [REF: IC §7, ARCH §17, build.md Phase 10.3 Session 10.3.1]

package payment

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/lib/pq" // registers the "postgres" driver used by openTestDB

	"github.com/masamasaowl/Vyomanaut_V2/internal/config"
)

// ── DB fixture plumbing (reused across this package's test files) ────────────

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	return openAndPing(t, testDSN("PGUSER", "vyomanaut_app", "PGPASSWORD"))
}

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

func randPhone() string {
	var suffix [5]byte
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("+91%x", suffix[:])
}

func randPubKey() []byte {
	var k [32]byte
	_, _ = rand.Read(k[:])
	return k[:]
}

func randVPA() string {
	var suffix [6]byte
	_, _ = rand.Read(suffix[:])
	return fmt.Sprintf("vyomanaut.%x@icici", suffix[:])
}

// insertTestOwner inserts a throwaway owners row (with a random Smart
// Collect VPA unless vpa is given) and returns its owner_id.
func insertTestOwner(t *testing.T, db *sql.DB, vpa string) uuid.UUID {
	t.Helper()
	if vpa == "" {
		vpa = randVPA()
	}
	id := uuid.New()
	_, err := db.Exec(`INSERT INTO owners (owner_id, phone_number, ed25519_public_key, smart_collect_vpa) VALUES ($1,$2,$3,$4)`,
		id, randPhone(), randPubKey(), vpa)
	if err != nil {
		t.Fatalf("insertTestOwner: %v", err)
	}
	return id
}

// testProviderSpec configures the subset of providers columns this
// package's tests need to control per row.
type testProviderSpec struct {
	linkedAccountID string // "" -> SQL NULL
	coolingUntil    *time.Time
}

func insertTestProvider(t *testing.T, db *sql.DB, spec testProviderSpec) uuid.UUID {
	t.Helper()
	id := uuid.New()
	var linkedAccountID sql.NullString
	if spec.linkedAccountID != "" {
		linkedAccountID = sql.NullString{String: spec.linkedAccountID, Valid: true}
	}
	_, err := db.Exec(`
		INSERT INTO providers (
			provider_id, phone_number, ed25519_public_key, status,
			declared_storage_gb, city, region, asn,
			razorpay_linked_account_id, razorpay_cooling_until
		) VALUES ($1,$2,$3,'ACTIVE',50,'TestCity','TestRegion','SIM-AS1',$4,$5)`,
		id, randPhone(), randPubKey(), linkedAccountID, spec.coolingUntil,
	)
	if err != nil {
		t.Fatalf("insertTestProvider: %v", err)
	}
	return id
}

func signBody(body []byte, secret []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// ── Deposit webhook (virtual_account.payment.captured) ────────────────────────

func TestDepositWebhookCreditsOwnerLedgerNotProviderLedger(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)

	vpa := randVPA()
	ownerID := insertTestOwner(t, db, vpa)
	secret := []byte("test-webhook-secret")
	body := []byte(`{"payload":{"payment":{"entity":{"id":"pay_test123","amount":50000}}}}`)
	sig := signBody(body, secret)

	// escrow_events (provider ledger) accumulates DEPOSIT-type rows across
	// this whole shared test database from OTHER packages' unrelated, valid
	// fixtures (e.g. internal/repair's departure detector tests seed a
	// DEPOSIT row to simulate a provider's pre-existing held balance — a
	// different concern entirely). An absolute "must be zero" count is
	// fragile against that; a before/after delta is not.
	var providerRowsBefore int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM escrow_events WHERE event_type = 'DEPOSIT'`).Scan(&providerRowsBefore); err != nil {
		t.Fatalf("count escrow_events before: %v", err)
	}

	payload := DepositWebhookPayload{PaymentID: "pay_test123", VPA: vpa, AmountPaise: 50000}
	if err := HandleDepositCaptured(context.Background(), db, secret, body, sig, payload); err != nil {
		t.Fatalf("HandleDepositCaptured: %v", err)
	}

	var ownerRows int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM owner_escrow_events WHERE owner_id = $1 AND event_type = 'DEPOSIT'`,
		ownerID).Scan(&ownerRows); err != nil {
		t.Fatalf("count owner_escrow_events: %v", err)
	}
	if ownerRows != 1 {
		t.Errorf("owner_escrow_events DEPOSIT rows = %d, want 1", ownerRows)
	}

	var providerRowsAfter int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM escrow_events WHERE event_type = 'DEPOSIT'`).Scan(&providerRowsAfter); err != nil {
		t.Fatalf("count escrow_events after: %v", err)
	}
	if providerRowsAfter != providerRowsBefore {
		t.Errorf("escrow_events DEPOSIT rows changed from %d to %d, want unchanged — a deposit must never land in "+
			"the provider ledger (the core bug this milestone fixes)", providerRowsBefore, providerRowsAfter)
	}
}

func TestDepositWebhookIdempotentOnRedelivery(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)

	vpa := randVPA()
	ownerID := insertTestOwner(t, db, vpa)
	secret := []byte("test-webhook-secret")
	body := []byte(`{"payload":{"payment":{"entity":{"id":"pay_redeliver","amount":25000}}}}`)
	sig := signBody(body, secret)
	payload := DepositWebhookPayload{PaymentID: "pay_redeliver", VPA: vpa, AmountPaise: 25000}

	if err := HandleDepositCaptured(context.Background(), db, secret, body, sig, payload); err != nil {
		t.Fatalf("HandleDepositCaptured (first delivery): %v", err)
	}
	if err := HandleDepositCaptured(context.Background(), db, secret, body, sig, payload); err != nil {
		t.Fatalf("HandleDepositCaptured (redelivery): %v", err)
	}

	var rows int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM owner_escrow_events WHERE owner_id = $1`, ownerID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("owner_escrow_events rows after 2 identical deliveries = %d, want 1", rows)
	}
}

func TestDepositWebhookRejectsBadSignature(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)

	vpa := randVPA()
	ownerID := insertTestOwner(t, db, vpa)
	secret := []byte("test-webhook-secret")
	body := []byte(`{"payload":{"payment":{"entity":{"id":"pay_badsig","amount":10000}}}}`)
	payload := DepositWebhookPayload{PaymentID: "pay_badsig", VPA: vpa, AmountPaise: 10000}

	err := HandleDepositCaptured(context.Background(), db, secret, body, "not-a-real-signature", payload)
	if !errors.Is(err, ErrInvalidWebhookSignature) {
		t.Fatalf("HandleDepositCaptured(bad signature): got %v, want ErrInvalidWebhookSignature", err)
	}

	var rows int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM owner_escrow_events WHERE owner_id = $1`, ownerID).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 0 {
		t.Errorf("owner_escrow_events rows after a rejected signature = %d, want 0 (no DB write before verification)", rows)
	}
}

// ── Reversal webhook (payout.reversed) ─────────────────────────────────────────

func TestReversalWebhookInsertsReversalEvent(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)

	providerID := insertTestProvider(t, db, testProviderSpec{linkedAccountID: "acc_test123"})
	auditPeriodID := insertTestAuditPeriod(t, db, providerID)

	originalIdempotencyKey := releaseIdempotencyKeyForTest(providerID, auditPeriodID)
	if err := InsertEscrowEvent(context.Background(), db, providerID, EscrowRelease, 30000, originalIdempotencyKey, &auditPeriodID); err != nil {
		t.Fatalf("seed original RELEASE: %v", err)
	}

	secret := []byte("test-webhook-secret")
	body := []byte(`{"payload":{"payout":{"entity":{"amount":30000}}}}`)
	sig := signBody(body, secret)
	payload := PayoutReversedWebhookPayload{AmountPaise: 30000, ReferenceID: originalIdempotencyKey}

	if err := HandlePayoutReversed(context.Background(), db, secret, body, sig, payload); err != nil {
		t.Fatalf("HandlePayoutReversed: %v", err)
	}

	var reversalRows int
	if err := verify.QueryRow(`SELECT COUNT(*) FROM escrow_events WHERE provider_id = $1 AND event_type = 'REVERSAL'`,
		providerID).Scan(&reversalRows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if reversalRows != 1 {
		t.Errorf("REVERSAL rows = %d, want 1", reversalRows)
	}
}

// releaseIdempotencyKeyForTest delegates to the real releaseIdempotencyKey
// (release.go) so this fixture can never drift from the actual
// implementation it is standing in for.
func releaseIdempotencyKeyForTest(providerID, auditPeriodID uuid.UUID) string {
	return releaseIdempotencyKey(providerID, auditPeriodID)
}

func insertTestAuditPeriod(t *testing.T, db *sql.DB, providerID uuid.UUID) uuid.UUID {
	t.Helper()
	id := uuid.New()
	now := time.Now().UTC()
	_, err := db.Exec(`
		INSERT INTO audit_periods (id, provider_id, period_start, period_end)
		VALUES ($1, $2, $3, $4)`,
		id, providerID, now.AddDate(0, 0, -30), now)
	if err != nil {
		t.Fatalf("insertTestAuditPeriod: %v", err)
	}
	return id
}

// ── account.created webhook ─────────────────────────────────────────────────────

func TestAccountCreatedSetsProfileDrivenCoolingPeriod(t *testing.T) {
	for _, tc := range []struct {
		name    string
		profile config.NetworkProfile
	}{
		{"demo", config.DemoProfile},
		{"production", config.ProductionProfile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			verify := openVerifyDB(t)
			providerID := insertTestProvider(t, db, testProviderSpec{})

			secret := []byte("test-webhook-secret")
			body := []byte(fmt.Sprintf(`{"payload":{"account":{"entity":{"id":"acc_%s"}}}}`, tc.name))
			sig := signBody(body, secret)
			payload := AccountCreatedWebhookPayload{AccountID: "acc_" + tc.name, ProviderID: providerID}

			before := time.Now().UTC()
			if err := HandleAccountCreated(context.Background(), db, tc.profile, secret, body, sig, payload); err != nil {
				t.Fatalf("HandleAccountCreated: %v", err)
			}
			after := time.Now().UTC()

			var linkedAccountID string
			var coolingUntil time.Time
			if err := verify.QueryRow(`SELECT razorpay_linked_account_id, razorpay_cooling_until FROM providers WHERE provider_id = $1`,
				providerID).Scan(&linkedAccountID, &coolingUntil); err != nil {
				t.Fatalf("query provider: %v", err)
			}
			if linkedAccountID != "acc_"+tc.name {
				t.Errorf("razorpay_linked_account_id = %q, want %q", linkedAccountID, "acc_"+tc.name)
			}

			wantMin := before.Add(tc.profile.RazorpayCoolingPeriod).Add(-time.Second)
			wantMax := after.Add(tc.profile.RazorpayCoolingPeriod).Add(time.Second)
			if coolingUntil.Before(wantMin) || coolingUntil.After(wantMax) {
				t.Errorf("razorpay_cooling_until = %v, want within [%v, %v] (NOW() + profile.RazorpayCoolingPeriod = %v)",
					coolingUntil, wantMin, wantMax, tc.profile.RazorpayCoolingPeriod)
			}
		})
	}
}

func TestAccountCreatedIdempotentOnRedelivery(t *testing.T) {
	db := openTestDB(t)
	verify := openVerifyDB(t)
	providerID := insertTestProvider(t, db, testProviderSpec{})
	profile := config.ProductionProfile

	secret := []byte("test-webhook-secret")
	body := []byte(`{"payload":{"account":{"entity":{"id":"acc_redeliver"}}}}`)
	sig := signBody(body, secret)
	payload := AccountCreatedWebhookPayload{AccountID: "acc_redeliver", ProviderID: providerID}

	if err := HandleAccountCreated(context.Background(), db, profile, secret, body, sig, payload); err != nil {
		t.Fatalf("HandleAccountCreated (first delivery): %v", err)
	}
	var firstCoolingUntil time.Time
	if err := verify.QueryRow(`SELECT razorpay_cooling_until FROM providers WHERE provider_id = $1`, providerID).
		Scan(&firstCoolingUntil); err != nil {
		t.Fatalf("query after first delivery: %v", err)
	}

	// Redeliver with a DIFFERENT account ID — the IS NULL guard must make
	// this a no-op, since razorpay_linked_account_id is already set.
	body2 := []byte(`{"payload":{"account":{"entity":{"id":"acc_should_not_apply"}}}}`)
	sig2 := signBody(body2, secret)
	payload2 := AccountCreatedWebhookPayload{AccountID: "acc_should_not_apply", ProviderID: providerID}
	if err := HandleAccountCreated(context.Background(), db, profile, secret, body2, sig2, payload2); err != nil {
		t.Fatalf("HandleAccountCreated (redelivery): %v", err)
	}

	var linkedAccountID string
	var secondCoolingUntil time.Time
	if err := verify.QueryRow(`SELECT razorpay_linked_account_id, razorpay_cooling_until FROM providers WHERE provider_id = $1`,
		providerID).Scan(&linkedAccountID, &secondCoolingUntil); err != nil {
		t.Fatalf("query after redelivery: %v", err)
	}
	if linkedAccountID != "acc_redeliver" {
		t.Errorf("razorpay_linked_account_id = %q after redelivery, want unchanged %q", linkedAccountID, "acc_redeliver")
	}
	if !secondCoolingUntil.Equal(firstCoolingUntil) {
		t.Errorf("razorpay_cooling_until changed from %v to %v on redelivery, want unchanged", firstCoolingUntil, secondCoolingUntil)
	}
}
