package metrics

import (
	"math"
	"sort"
	"sync"
	"time"
)

// foregroundWindowDuration is the ADR-033 trailing window length used to
// estimate the 99th-percentile of foreground DB read latency. NFR-028's
// background-task throttle reads this window — never the cumulative
// Prometheus histogram — because a lifetime histogram cannot reflect a
// latency spike that started ten seconds ago; it would take hours of
// steady-state traffic to move the quantile at all.
const foregroundWindowDuration = 60 * time.Second

// windowSample is one latency observation, timestamped so it can be pruned
// once it falls outside the trailing window.
type windowSample struct {
	at      time.Time
	latency time.Duration
}

// slidingWindowP99 is a mutex-protected, pure-stdlib estimator of the 99th
// percentile over a trailing time window. [Decision — OBS.1.1] It has zero
// dependency on Prometheus or any internal/ package by design (ADR-033,
// A2): internal/metrics must remain an importable leaf, and Prometheus
// histograms/summaries cannot answer "what was p99 over just the last 60
// seconds" on their own — a summary's quantile is computed over its own
// (much longer) decay window, and a histogram only supports
// histogram_quantile() over whatever range the query specifies, not a
// process-local rolling window read synchronously by the throttle loop.
// This type is therefore the ADR-033 estimator itself; the Prometheus
// histogram registered in microservice.go remains the system of record for
// dashboards/alerts (NFR-025), and this estimator is the system of record
// for the NFR-028 throttle signal. ForegroundReadLatency.Observe (in
// microservice.go) updates both from the same call site.
type slidingWindowP99 struct {
	mu      sync.Mutex
	window  time.Duration
	samples []windowSample
}

// newSlidingWindowP99 constructs an estimator over the given trailing
// window duration.
func newSlidingWindowP99(window time.Duration) *slidingWindowP99 {
	return &slidingWindowP99{window: window}
}

// Observe records a single latency sample at the current time and prunes
// any samples that have aged out of the trailing window. Safe for
// concurrent use.
func (w *slidingWindowP99) Observe(d time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	w.samples = append(w.samples, windowSample{at: now, latency: d})
	w.pruneLocked(now)
}

// pruneLocked drops samples older than the trailing window. Callers must
// hold w.mu.
func (w *slidingWindowP99) pruneLocked(now time.Time) {
	cutoff := now.Add(-w.window)
	i := 0
	for i < len(w.samples) && w.samples[i].at.Before(cutoff) {
		i++
	}
	if i > 0 {
		w.samples = w.samples[i:]
	}
}

const p99Quantile = 0.99

// P99 returns the 99th-percentile latency across samples currently inside
// the trailing window, using the nearest-rank method. It returns 0 if no
// samples have landed inside the window yet — e.g. during network
// bootstrap, before any foreground reads have occurred (mirrors the
// computeRTO fallback precedent from Phase 12.1: no signal yet is not an
// error, it's "nothing to throttle").
func (w *slidingWindowP99) P99() time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pruneLocked(time.Now())

	n := len(w.samples)
	if n == 0 {
		return 0
	}

	latencies := make([]time.Duration, n)
	for i, s := range w.samples {
		latencies[i] = s.latency
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })

	rank := int(math.Ceil(p99Quantile*float64(n))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= n {
		rank = n - 1
	}
	return latencies[rank]
}

// foregroundWindow is the package-level ADR-033 estimator for foreground DB
// read latency. A single process-wide instance is correct here: the
// microservice has exactly one foreground read path to throttle against
// (IC §9 forbids internal/metrics from knowing about internal/audit or any
// other caller — it just records whatever latencies it's given).
var foregroundWindow = newSlidingWindowP99(foregroundWindowDuration)

// ForegroundReadP99Window returns the ADR-033 99th-percentile estimate of
// foreground DB read latency over the trailing 60-second window. NFR-028's
// background-task throttle loop calls this — never
// ForegroundReadLatency's underlying Prometheus histogram — to decide
// whether to reduce background task allocation.
func ForegroundReadP99Window() time.Duration {
	return foregroundWindow.P99()
}
