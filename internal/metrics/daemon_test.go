package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// TestDaemonMetricsRegistry mirrors TestMetricsRegistry's approach
// (microservice_test.go) for the seven provider-daemon metrics registered
// in daemon.go.
func TestDaemonMetricsRegistry(t *testing.T) {
	t.Run("TestAllNFR026NamesRegistered", func(t *testing.T) {
		mfs, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatalf("gather: %v", err)
		}
		names := make(map[string]bool, len(mfs))
		for _, mf := range mfs {
			names[mf.GetName()] = true
		}
		want := []string{
			"vyomanaut_daemon_chunks_stored_total",
			"vyomanaut_daemon_audit_responses_sent_total",
			"vyomanaut_daemon_audit_response_latency_milliseconds",
			"vyomanaut_daemon_vlog_append_latency_milliseconds",
			"vyomanaut_daemon_content_hash_failures_total",
			"vyomanaut_daemon_heartbeat_sent_total",
			"vyomanaut_daemon_ram_constrained",
		}
		for _, n := range want {
			if !names[n] {
				t.Errorf("metric %s not registered with the default gatherer", n)
			}
		}
	})

	t.Run("TestRAMConstrainedGaugeIsBoolean", func(t *testing.T) {
		DaemonRAMConstrained.Set(1)
		mfs, err := prometheus.DefaultGatherer.Gather()
		if err != nil {
			t.Fatalf("gather: %v", err)
		}
		for _, mf := range mfs {
			if mf.GetName() != "vyomanaut_daemon_ram_constrained" {
				continue
			}
			if len(mf.GetMetric()) != 1 {
				t.Fatalf("expected exactly one series, got %d", len(mf.GetMetric()))
			}
			if got := mf.GetMetric()[0].GetGauge().GetValue(); got != 1 {
				t.Errorf("expected gauge value 1, got %v", got)
			}
		}
		DaemonRAMConstrained.Set(0)
	})
}