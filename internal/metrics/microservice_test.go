package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// TestMetricsRegistry is the OBS.1.1 VERIFY block's umbrella test. Its
// subtests are named exactly as the session text lists them so `go test
// -run TestMetricsRegistry` exercises all three.
func TestMetricsRegistry(t *testing.T) {
	t.Run("TestAllNFR025NamesRegistered", func(t *testing.T) {
		// CounterVec/GaugeVec collectors only emit a child series in
		// Gather() once a label combination has actually been touched —
		// an untouched Vec has a registered Desc but zero Metric samples,
		// so its family is absent from Gather() output entirely. Touch
		// each Vec with a representative label combination (mirroring
		// what OBS.1.2's real call sites do) so this test verifies the
		// same "is it registered" question for every metric uniformly,
		// via the same Gather() path.
		AuditResultsTotal.WithLabelValues("PASS").Add(0)
		PaymentEscrowEventsTotal.WithLabelValues("DEPOSIT").Add(0)
		ClusterReplicaCount.WithLabelValues("healthy").Set(0)

		mfs, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatalf("gather: %v", err)
		}
		names := make(map[string]bool, len(mfs))
		for _, mf := range mfs {
			names[mf.GetName()] = true
		}
		want := []string{
			"vyomanaut_audit_challenges_issued_total",
			"vyomanaut_audit_results_total",
			"vyomanaut_scoring_provider_score",
			"vyomanaut_repair_queue_depth",
			"vyomanaut_repair_jobs_completed_total",
			"vyomanaut_payment_escrow_events_total",
			"vyomanaut_cluster_replica_count",
			"vyomanaut_db_read_latency_seconds",
		}
		for _, n := range want {
			if !names[n] {
				t.Errorf("metric %s not registered with the default gatherer", n)
			}
		}
	})

	t.Run("TestDBReadHistogramHasThrottleBuckets", func(t *testing.T) {
		mfs, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatalf("gather: %v", err)
		}
		var found *dto.MetricFamily
		for _, mf := range mfs {
			if mf.GetName() == "vyomanaut_db_read_latency_seconds" {
				found = mf
			}
		}
		if found == nil {
			t.Fatal("vyomanaut_db_read_latency_seconds not registered")
		}
		if len(found.GetMetric()) == 0 {
			t.Fatal("vyomanaut_db_read_latency_seconds has no metric samples")
		}
		buckets := found.GetMetric()[0].GetHistogram().GetBucket()
		haveFour, haveFive := false, false
		for _, b := range buckets {
			switch b.GetUpperBound() {
			case 0.04:
				haveFour = true
			case 0.05:
				haveFive = true
			}
		}
		if !haveFour {
			t.Error("expected a 0.04s bucket (ADR-033 early-warning boundary)")
		}
		if !haveFive {
			t.Error("expected a 0.05s bucket (NFR-028 throttle threshold)")
		}
	})

	t.Run("TestForegroundP99WindowReflectsRecentBurstNotLifetime", func(t *testing.T) {
		// A short-window estimator isolated from the package-level
		// singleton, so this test can't be polluted by (or pollute)
		// anything else observing ForegroundReadLatency.
		w := newSlidingWindowP99(80 * time.Millisecond)

		// Build a long "lifetime" baseline of fast reads.
		for i := 0; i < 50; i++ {
			w.Observe(1 * time.Millisecond)
		}

		// Let the baseline age out of the trailing window entirely.
		time.Sleep(100 * time.Millisecond)

		// A recent burst of slow reads — this is what ADR-033 says the
		// throttle must see, even though it's a tiny fraction of all
		// samples ever observed.
		for i := 0; i < 5; i++ {
			w.Observe(200 * time.Millisecond)
		}

		p99 := w.P99()
		if p99 < 150*time.Millisecond {
			t.Errorf("windowed p99 = %s, want ~200ms — the aged-out 1ms baseline "+
				"should have been pruned rather than pulling the estimate down "+
				"toward a lifetime average", p99)
		}
	})
}