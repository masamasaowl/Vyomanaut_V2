// Package scoring is declared in doc.go.
// This file implements GetScore and GetScoreFromPrimary, both of which query
// the mv_provider_scores materialised view (DM §7).
//
// [REF: IC §5.6, DM §7, ADR-008, ADR-024, FR-050, build.md Phase 8.1 Session 8.1.1]

package scoring

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
)

// dualWindowDropThreshold mirrors config.NetworkProfile.DualWindowDrop, which
// is documented as "always 0.20; never mode-variable" (network_profile.go —
// unlike VettingMinPasses/VettingMinDuration, this value never differs
// between demo and prod). GetScore's declared signature (IC §5.6) takes no
// profile parameter — unlike IncrementConsecutivePasses, which the Milestone
// 8 review flagged as needing one precisely because VettingMinPasses IS
// mode-variable — so there is no parameter this constant could be threaded
// through even if it varied. Declared as a named constant, not an inline
// literal, per this codebase's "no magic numbers" standard. If DualWindowDrop
// is ever made mode-variable, GetScore's signature will need revisiting
// alongside it.
const dualWindowDropThreshold = 0.20

// ProviderScore holds the three-window scores and the weighted composite
// (ADR-008). ScoresAsOf carries the view's own NOW()-at-refresh-time column
// (DM §7) so callers — especially payment release computation (Milestone 10)
// — can enforce the "< 60 minutes old" staleness rule before using a score
// for a payment decision (DM §7 CRITICAL note, ADR-024).
type ProviderScore struct {
	Score24h       float64   // window weight 0.50 in the composite
	Score7d        float64   // window weight 0.30
	Score30d       float64   // window weight 0.20
	Composite      float64   // 0.50*24h + 0.30*7d + 0.20*30d
	DualWindowFlag bool      // true when score30d - score7d > 0.20 (FR-050, ADR-024 §3)
	ScoresAsOf     time.Time // from mv_provider_scores.scores_as_of (DM §7)
}

// GetScore queries mv_provider_scores for providerID. The view may be up to
// 60 seconds stale — acceptable for general scoring queries (IC §5.6). Reads
// via the connection pool, which may route to a read replica (ARCH §24).
//
// DualWindowFlag is computed in application code, not SQL, per IC §5.6 (easier
// to unit-test in isolation from the database).
//
// Error semantics:
//   - ErrProviderNotFound: no row for providerID in the view yet.
//
// Goroutine-safe: yes.
func GetScore(ctx context.Context, db *sql.DB, providerID uuid.UUID) (ProviderScore, error) {
	score, err := queryProviderScore(ctx, db, providerID)
	if err != nil {
		return ProviderScore{}, fmt.Errorf("scoring.GetScore: %w", err)
	}
	return score, nil
}

// GetScoreFromPrimary is identical to GetScore except it forces the read to
// the primary replica (pass a *sql.DB handle pointing at the primary, not the
// pooled/replica-routed one used elsewhere). Required whenever a score is
// about to be used for a payment decision — the monthly release multiplier
// computation (Milestone 10) MUST call this, not GetScore, per DM §7's
// staleness requirement.
//
// [TODO M10: internal/payment's depguard rule and IC §9's import table do not
// yet list internal/scoring among internal/payment's permitted imports, even
// though this doc comment (and DM §7's own staleness rule) requires Milestone
// 10's release computation to call GetScoreFromPrimary directly. Every other
// row in the import-constraint table that names a real cross-package caller
// spells out the allowed import explicitly; this one gap is most likely an
// oversight rather than a deliberate omission, since nothing else in the
// governing docs describes an alternative path for payment to reach a score.
// Flagged here rather than silently worked around — whoever picks up
// Milestone 10 will need to add internal/scoring to internal/payment's
// depguard allow-list and to IC §9's table in the same PR, the same way this
// session's own README note added google/uuid and lib/pq to scoring's own
// entry.]
//
// Goroutine-safe: yes.
func GetScoreFromPrimary(ctx context.Context, primaryDB *sql.DB, providerID uuid.UUID) (ProviderScore, error) {
	score, err := queryProviderScore(ctx, primaryDB, providerID)
	if err != nil {
		return ProviderScore{}, fmt.Errorf("scoring.GetScoreFromPrimary: %w", err)
	}
	return score, nil
}

// queryProviderScore is the shared implementation behind GetScore and
// GetScoreFromPrimary — the two differ only in which *sql.DB handle the
// caller supplies (pooled/replica-routed vs. forced-primary), a wiring
// concern the doc comments above already push onto the caller (see the
// "Flagged" note on GetScoreFromPrimary's own declaration in build.md Phase
// 8.1). Keeping one query implementation here avoids drift between the two
// exported wrappers.
func queryProviderScore(ctx context.Context, db *sql.DB, providerID uuid.UUID) (ProviderScore, error) {
	const query = `
SELECT score_24h, score_7d, score_30d, score_composite, scores_as_of
FROM mv_provider_scores
WHERE provider_id = $1`

	var (
		score24h, score7d, score30d sql.NullFloat64
		composite                   float64
		scoresAsOf                  time.Time
	)
	err := db.QueryRowContext(ctx, query, providerID).
		Scan(&score24h, &score7d, &score30d, &composite, &scoresAsOf)
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderScore{}, ErrProviderNotFound
	}
	if err != nil {
		return ProviderScore{}, fmt.Errorf("query mv_provider_scores: %w", err)
	}

	// A provider with zero terminal audit_result rows in a given window scores
	// NULL for that window's ratio (the view's own NULLIF, DM §7) even though
	// the provider itself has a row in the view (some audit history exists,
	// just not within this specific window). The view's own score_composite
	// column already treats a missing window as 0 via COALESCE; the three
	// window fields returned here follow that same convention so Composite
	// and (Score24h, Score7d, Score30d) never disagree with each other.
	s24 := nullFloatOrZero(score24h)
	s7 := nullFloatOrZero(score7d)
	s30 := nullFloatOrZero(score30d)

	return ProviderScore{
		Score24h:       s24,
		Score7d:        s7,
		Score30d:       s30,
		Composite:      composite,
		DualWindowFlag: (s30 - s7) > dualWindowDropThreshold,
		ScoresAsOf:     scoresAsOf,
	}, nil
}

// nullFloatOrZero returns nf.Float64 if nf is valid, else 0. See the
// COALESCE-to-0 note in queryProviderScore for why 0 is the correct fallback
// here (matching the view's own treatment of a missing window), as opposed
// to the DM §9 checklist's "default to NULL, not 0/2000" rule, which governs
// providers.avg_rtt_ms / p95_throughput_kbps (an unestablished-measurement
// case, handled in rto.go) and is not about this composite-score fallback.
func nullFloatOrZero(nf sql.NullFloat64) float64 {
	if !nf.Valid {
		return 0
	}
	return nf.Float64
}

// Score30dBasisPoints and Score7dBasisPoints scale their corresponding score
// to basis points (10000 = 1.00, 9500 = 0.95, ...), rounded to the nearest
// integer.
//
// [Decision, build.md Milestone 10] internal/payment's release computation
// (Phase 10.4) needs Score30d/Score7d to apply FR-049's release-multiplier
// table, but IC §5.8/IC §11 place an absolute, no-exceptions ban on any
// fractional-point numeric type appearing anywhere in internal/payment/ —
// enforced by TWO independent mechanisms (a go/ast-based test and a grep
// scan over internal/payment/*.go and *.sql) that make no exception for
// merely reading a value of that type from another package. Since
// ProviderScore's window-score fields are that same type by construction
// (Session 8.1.1, predating and unrelated to Milestone 10's ban), there is
// no way for internal/payment to consume Score30d/Score7d directly without
// violating its own package's rule the moment it holds or compares such a
// value — even without ever spelling out the type's name in payment's own
// source.
//
// These two methods are the fix: the one unavoidable scale-and-round
// operation happens HERE, once, inside internal/scoring's own domain where
// that numeric type is already used freely and is entirely unrestricted.
// internal/payment calls these instead of touching Score30d/Score7d
// directly, and from that point on works ONLY with plain integers —
// resolving the conflict without weakening either package's own rule.
// basisPointsScale converts a [0,1] score ratio to basis points (10000 = 1.00).
const basisPointsScale = 10000

func (p ProviderScore) Score30dBasisPoints() int64 {
	return int64(math.Round(p.Score30d * basisPointsScale))
}

func (p ProviderScore) Score7dBasisPoints() int64 {
	return int64(math.Round(p.Score7d * basisPointsScale))
}
