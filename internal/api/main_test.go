// Package api is declared in doc.go.
//
// TestMain gives this package's live-DB tests a clean slate before each
// `go test` invocation.
//
// [Root cause] Dozens of tests across this package (owner_test.go,
// provider_test.go, upload_test.go, file_test.go, ...) each insert their
// own providers, many with distinct ASN values, into the single shared
// database openTestDB/openVerifyDB always connect to — there is no
// per-test isolation (no transaction rollback, no per-test schema). Within
// one test binary run, this accumulates without bound: by the time later
// tests run, the ACTIVE provider pool can span 50+ ASNs left behind by
// earlier tests in the same run, none of them relevant to the test
// currently executing.
//
// repair.SelectReplacementProvider draws candidates via a bounded random
// sample (a fixed number of retries, two random candidates per attempt) —
// a reasonable, statistically sound design against a real, gradually-grown
// production provider pool, but not against a same-run accumulation of
// hundreds of unrelated rows. As the accumulated pool grows, the bounded
// sample increasingly draws from providers left behind by other tests
// rather than the handful a specific test actually seeded, occasionally
// exhausting the retry budget and returning ErrNoEligibleReplacement for a
// scenario that is not actually infeasible — observed directly:
// TestUploadAssignShardIndexRangeDemo failing with "Current distinct ASNs:
// 62" despite seeding a perfectly adequate 5-ASN pool itself.
//
// Some tests already defend against this individually (e.g.
// otherActiveProviderIDs / TestUploadAssignRejectsASNDiversityDemo's
// exclude-everything-already-present snapshot), but that pattern doesn't
// generalize to every test that relies on SelectReplacementProvider
// succeeding (as opposed to deliberately failing) — TestMain fixes the
// underlying accumulation directly instead, so no individual test needs to
// defend against it.
//
// [REF: internal/repair/assignment_test.go's own allActiveProviderIDs(t,
// verify) helper — the same accumulation concern, addressed there at the
// single-test-function level, which was and remains sufficient for that
// package because it doesn't insert nearly as much test data per run.]
package api

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"
)

// truncateOrder lists every table this package's tests write to, in an
// order that would satisfy FK dependencies even without CASCADE (CASCADE
// is used regardless, for robustness against any table this list misses).
var truncateOrder = []string{
	"audit_receipts",
	"escrow_events",
	"owner_escrow_events",
	"repair_jobs",
	"audit_periods",
	"chunk_assignments",
	"segments",
	"files",
	"pending_registrations",
	"providers",
	"owners",
}

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

// runTestMain is a plain function (not a method needing *testing.T) so it
// can report a real process failure via its return code — TestMain runs
// before any *testing.T exists to call t.Fatalf on.
func runTestMain(m *testing.M) int {
	// testDSN/envOr are defined in readiness_test.go, same package.
	db, err := sql.Open("postgres", testDSN("PGVERIFY_USER", "postgres", "PGVERIFY_PASSWORD"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: sql.Open failed (%v) — running the suite anyway; individual tests will skip via their own t.Skipf if Postgres is unreachable\n", err)
		return m.Run()
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: live Postgres not reachable (%v) — running the suite anyway; individual tests will skip via their own t.Skipf\n", err)
		return m.Run()
	}

	for _, table := range truncateOrder {
		if _, err := db.ExecContext(ctx, "TRUNCATE TABLE "+table+" CASCADE"); err != nil {
			fmt.Fprintf(os.Stderr, "TestMain: truncate %s failed: %v\n", table, err)
			return 1
		}
	}

	return m.Run()
}