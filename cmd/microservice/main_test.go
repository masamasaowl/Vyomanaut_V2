package main

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/masamasaowl/Vyomanaut_V2/internal/audit"
	"github.com/masamasaowl/Vyomanaut_V2/internal/cluster"
	"github.com/masamasaowl/Vyomanaut_V2/internal/config"
	"github.com/masamasaowl/Vyomanaut_V2/internal/repair"
)

// openTestDB opens a connection to the local dev Postgres instance
// (deployments/dev/docker-compose.yml), mirroring internal/api's own test
// helper convention (readiness_test.go's openTestDB/testDSN). Skips the
// calling test if no database is reachable, rather than failing — this
// sandbox has no Postgres available, and CI environments that do should not
// need any special setup beyond the existing docker-compose.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		envOr("PGHOST", "localhost"), envOr("PGPORT", "5432"), envOr("PGUSER", "vyomanaut_app"),
		envOr("PGPASSWORD", "devpass"), envOr("PGDATABASE", "vyomanaut_dev"), envOr("PGSSLMODE", "disable"))
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Skipf("openTestDB: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("openTestDB: database unreachable: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// failingSecretsClient always reports the secrets manager as unreachable.
type failingSecretsClient struct{}

func (failingSecretsClient) GetSecret(context.Context, string) ([]byte, error) {
	return nil, audit.ErrSecretManagerUnavailable
}

func TestStartupFailsClosedOnUnreachableSecretsManager(t *testing.T) {
	cache := audit.NewClusterSecretCache(failingSecretsClient{})
	if err := loadClusterSecret(context.Background(), cache); err == nil {
		t.Fatal("loadClusterSecret: expected an error when the secrets manager is unreachable, got nil")
	}
}

func TestStartupDemoModeSkipsGossipWait(t *testing.T) {
	start := time.Now()
	membership, err := waitForGossipQuorum(context.Background(), config.DemoProfile, "", "")
	if err != nil {
		t.Fatalf("waitForGossipQuorum: unexpected error in demo mode: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("waitForGossipQuorum: expected an immediate return in demo mode, took %s", elapsed)
	}
	if _, ok := membership.(cluster.SoloMembership); !ok {
		t.Fatalf("waitForGossipQuorum: expected cluster.SoloMembership in demo mode, got %T", membership)
	}
	if got := membership.HealthyCount(); got != 1 {
		t.Fatalf("waitForGossipQuorum: expected HealthyCount()==1 in demo mode, got %d", got)
	}
}

func TestStartupProdModeBlocksUntilTwoPeerAck(t *testing.T) {
	membership, err := waitForGossipQuorum(context.Background(), config.ProductionProfile, "seed1.example:4001", "seed2.example:4001")
	if err != nil {
		t.Fatalf("waitForGossipQuorum: unexpected error: %v", err)
	}
	gc, ok := membership.(*cluster.GossipCluster)
	if !ok {
		t.Fatalf("waitForGossipQuorum: expected *cluster.GossipCluster in prod mode, got %T", membership)
	}
	if got := gc.HealthyCount(); got < gossipMinPeerAcks {
		t.Fatalf("waitForGossipQuorum: expected HealthyCount() >= %d (this session's own >=2 peer-ack requirement), got %d",
			gossipMinPeerAcks, got)
	}
}

func TestStartupProdModeRequiresSeedNodes(t *testing.T) {
	if _, err := waitForGossipQuorum(context.Background(), config.ProductionProfile, "", ""); err == nil {
		t.Fatal("waitForGossipQuorum: expected an error when seed nodes are unset in prod mode")
	}
}

// fakePenaliser records whether Penalise was invoked, standing in for a
// payment.PaymentProvider's Penalise method for wiring verification.
type fakePenaliser struct {
	called bool
}

func (f *fakePenaliser) Penalise(_ context.Context, _ uuid.UUID, _ int64, _ string) error {
	f.called = true
	return nil
}

func TestStartupWiresDepartureDetectorPenaliseCallback(t *testing.T) {
	db := openTestDB(t)
	fake := &fakePenaliser{}
	detector := repair.NewDepartureDetector(db, config.DemoProfile, fake.Penalise)
	if detector == nil {
		t.Fatal("repair.NewDepartureDetector returned nil")
	}
	// Exercises the exact construction this session's step 13 performs
	// (repair.NewDepartureDetector(db, profile, paymentProvider.Penalise));
	// with no departed providers present this should complete without
	// error and without invoking Penalise.
	if err := detector.DetectOnce(context.Background()); err != nil {
		t.Fatalf("DetectOnce: %v", err)
	}
}

func TestStartupRejectsProdModeWithEnvSecretPresent(t *testing.T) {
	t.Setenv("VYOMANAUT_CLUSTER_MASTER_SEED", "should-not-be-set-in-prod")
	profile := config.SelectProfile("prod")
	if err := config.ValidateStartupGuards(profile); err == nil {
		t.Fatal("config.ValidateStartupGuards: expected an error for prod mode with VYOMANAUT_CLUSTER_MASTER_SEED set (M1 Session 1.3.2's PROD_MODE_ENV_SECRET guard)")
	}
}

// openTestMigratorDB opens a connection as vyomanaut_migrator (BYPASSRLS) —
// required for regenerateProviderScoresView's DROP/CREATE MATERIALIZED VIEW
// (see config_env.go's MigratorDBDSN doc comment). Skips if unreachable,
// same convention as openTestDB.
func openTestMigratorDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		envOr("PGHOST", "localhost"), envOr("PGPORT", "5432"), envOr("PGMIGRATORUSER", "vyomanaut_migrator"),
		envOr("PGMIGRATORPASSWORD", envOr("PGPASSWORD", "devpass")), envOr("PGDATABASE", "vyomanaut_dev"), envOr("PGSSLMODE", "disable"))
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Skipf("openTestMigratorDB: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("openTestMigratorDB: database unreachable: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestRegenerateProviderScoresView(t *testing.T) {
	db := openTestMigratorDB(t)
	if err := regenerateProviderScoresView(context.Background(), db, config.DemoProfile); err != nil {
		t.Fatalf("regenerateProviderScoresView: %v", err)
	}
	// The view must exist and be queryable (zero rows is fine — no audit
	// receipts exist in a fresh test database).
	rows, err := db.QueryContext(context.Background(), `SELECT provider_id, score_composite FROM mv_provider_scores`)
	if err != nil {
		t.Fatalf("query mv_provider_scores: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var composite float64
		if err := rows.Scan(&id, &composite); err != nil {
			t.Fatalf("scan mv_provider_scores row: %v", err)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate mv_provider_scores: %v", err)
	}

	// Regenerating again (as every startup does) must also succeed —
	// DROP MATERIALIZED VIEW IF EXISTS followed by CREATE must be safely
	// repeatable.
	if err := regenerateProviderScoresView(context.Background(), db, config.DemoProfile); err != nil {
		t.Fatalf("regenerateProviderScoresView (second run): %v", err)
	}
}
