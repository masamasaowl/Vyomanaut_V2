// Package audit is declared in doc.go.
// This file implements ClusterSecretCache per IC §8: a 5-minute TTL cache
// over SecretsManagerClient that holds one or two cluster audit secret
// versions simultaneously during the 24-hour rotation overlap window.
//
// METHOD SET NOTE: mvp.md §8.2 names ClusterSecretCache but no document
// gives it a method set; the four methods below are shaped to satisfy (a)
// Session 7.2.1's ValidateResponse caller needing IsVersionValid, (b) IC
// §8's fail-closed-at-startup and TTL-expiry-during-operation requirements,
// and (c) the 24-hour rotation overlap. There is no periodic-refresh method
// here (no "Run"/"StartLoop") — Load may be called repeatedly (once at
// startup, and again on whatever cadence a caller chooses; IC §8 suggests
// every 5 minutes) and CurrentSecret/IsVersionValid/SecretForVersion are
// pure, lock-protected reads of whatever Load last observed. Wiring an
// actual periodic ticker around Load is Milestone 12's job, the same
// division of labour Session 6.3.1's RunHeartbeat used relative to its own
// underlying doHeartbeat.
//
// [REF: IC §8, MVP §8.2, NFR-018, ADR-027]

package audit

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// ── Constants ──────────────────────────────────────────────────────────────

const (
	// clusterSecretCacheTTL is the maximum time CurrentSecret will keep
	// serving a cached value after the secrets manager becomes unreachable
	// (IC §8). Always 5 minutes; never profile-variable.
	clusterSecretCacheTTL = 5 * time.Minute

	// rotationOverlapWindow is how long a superseded secret version remains
	// valid for IsVersionValid/SecretForVersion after a newer version is
	// first observed (IC §8's 24-hour rotation overlap).
	rotationOverlapWindow = 24 * time.Hour

	// secretPathPrefix is the IC §8 path convention; secretPath appends the
	// version integer. N starts at 1 at cluster bootstrap.
	secretPathPrefix = "/vyomanaut/audit-secret/v"

	// secretScanUpperBound bounds Load's upward-scan recovery (see
	// scanForwardFromPrunedVersion) for when the base version it expected to
	// find has been pruned out from under it. At the default 24-hour
	// rotation cadence, 32 versions covers roughly a month of missed
	// rotations — far more generous than any expected outage — while still
	// bounding the number of network calls a single Load call can make.
	// Milestone 7 corrections session addition.
	secretScanUpperBound = 32
)

// secretPath returns the IC §8 secrets-manager path for versionByte, e.g.
// secretPath(3) == "/vyomanaut/audit-secret/v3".
func secretPath(versionByte uint8) string {
	return fmt.Sprintf("%s%d", secretPathPrefix, versionByte)
}

// ── ClusterSecretCache ────────────────────────────────────────────────────

// ClusterSecretCache holds the cluster audit secret(s) with a 5-minute TTL
// (IC §8). During the 24-hour rotation overlap window it holds both
// server_secret_vN and server_secret_v{N+1} simultaneously.
type ClusterSecretCache struct {
	client SecretsManagerClient
	ttl    time.Duration // always 5 minutes (IC §8) — not profile-variable

	mu sync.RWMutex

	// currentVersion/currentSecret are the highest version Load has
	// observed. currentVersion == 0 means Load has never succeeded.
	currentVersion uint8
	currentSecret  []byte

	// previousVersion/previousSecret hold the immediately-superseded
	// version during the 24-hour rotation overlap (IC §8).
	// previousVersion == 0 means no overlap is currently in effect — either
	// no rotation has ever been observed, or overlapExpiresAt has passed.
	previousVersion  uint8
	previousSecret   []byte
	overlapExpiresAt time.Time

	// lastLoadedAt is the timestamp of the last SUCCESSFUL Load call; it is
	// what CurrentSecret measures the TTL against. Zero means never loaded.
	lastLoadedAt time.Time
}

// NewClusterSecretCache constructs a cache with the fixed 5-minute TTL.
//
// In demo mode (profile.RequireSecretsManager == false), client should be an
// env-var-backed adapter that reads VYOMANAUT_CLUSTER_MASTER_SEED (IC §8)
// instead of a real Vault/SSM/GCP client. The PROD_MODE_ENV_SECRET guard (M1
// Session 1.3.2) is what prevents this env var from being used in
// production — this constructor does not re-check that, and never reads
// NetworkProfile itself (the caller decides which client to construct and
// passes the result in here).
//
// [REF: IC §8, MVP §5.4]
func NewClusterSecretCache(client SecretsManagerClient) *ClusterSecretCache {
	return &ClusterSecretCache{
		client: client,
		ttl:    clusterSecretCacheTTL,
	}
}

// Load performs a fetch against the secrets manager: the version currently
// held (or version 1, on the very first call) plus a speculative check for
// version+1, to detect whether a rotation has started. Must be called once
// at microservice startup, before any challenge is issued, with the caller
// (Milestone 12 Session 12.1.1) refusing to start the replica if this first
// call errors (IC §8: "fail-closed" — better zero challenges than
// unvalidatable ones). May be called again on whatever periodic cadence the
// caller chooses (IC §8 suggests every 5 minutes) to keep the cache fresh
// and to pick up a newly-started rotation.
//
// COLD-START / LONG-OUTAGE RECOVERY (Milestone 7 corrections session): if
// the base version (currentVersion, or 1 on a genuinely first-ever call) has
// been pruned from the secrets manager — e.g. a replica cold-starts, or
// reconnects after an outage, well behind several rotations — the base fetch
// fails with ErrSecretNotFound and Load now scans forward (see
// scanForwardFromPrunedVersion) to find the lowest surviving version, rather
// than failing outright. Before this fix, that scenario made Load
// unrecoverable: assuming base=1 on cold start (or clinging to a
// long-retired currentVersion on a stale warm cache) and probing only base
// and base+1 could never reach a cluster that had since rotated past both.
//
// Pre-conditions:
//   - ctx is a live context for the network calls this makes
//
// Post-conditions (on nil error):
//   - CurrentSecret reflects the highest version found
//   - if a version one higher than what was previously known now exists,
//     the previous version becomes valid under the 24-hour rotation overlap
//     rather than being dropped immediately
//
// Error semantics: any GetSecret failure other than "the speculative
// version+1 probe returned ErrSecretNotFound" is returned to the caller,
// and the cache's existing (possibly now-stale) state is left untouched —
// CurrentSecret's own TTL grace period governs how long that stale state
// remains usable, not this function. This includes exhausting
// secretScanUpperBound during cold-start/long-outage recovery: that surfaces
// as an error asking for operator attention, rather than silently retrying
// forever.
//
// Goroutine-safe: yes. Network calls happen without holding the lock; only
// the brief in-memory state update at the end is lock-protected.
//
// [REF: IC §8, ADR-027]
func (c *ClusterSecretCache) Load(ctx context.Context) error {
	c.mu.RLock()
	base := c.currentVersion
	c.mu.RUnlock()
	if base == 0 {
		base = 1
	}

	baseSecret, err := c.client.GetSecret(ctx, secretPath(base))
	if errors.Is(err, ErrSecretNotFound) {
		var scanErr error
		base, baseSecret, scanErr = c.scanForwardFromPrunedVersion(ctx, base)
		if scanErr != nil {
			return fmt.Errorf("audit.ClusterSecretCache.Load: %w", scanErr)
		}
	} else if err != nil {
		return fmt.Errorf("audit.ClusterSecretCache.Load: fetch %s: %w", secretPath(base), err)
	}

	next := base + 1
	nextSecret, nextErr := c.client.GetSecret(ctx, secretPath(next))
	switch {
	case nextErr == nil:
		// A rotation to `next` is in progress (or was already known).
	case errors.Is(nextErr, ErrSecretNotFound):
		// Normal steady state: no newer version yet.
	default:
		// The base version was reachable, but the speculative next-version
		// probe hit a real error rather than a clean "not found" — treat
		// this refresh attempt as failed rather than guessing. The caller
		// still has whatever was cached from the last successful Load.
		return fmt.Errorf("audit.ClusterSecretCache.Load: probe %s: %w", secretPath(next), nextErr)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if nextErr == nil {
		if c.currentVersion != next {
			// Newly observed rotation (by this replica, at least — see
			// receipt.go-style notes elsewhere in this package for the same
			// "first observed by us" simplification pattern): start the
			// 24-hour overlap clock for the version being superseded now.
			c.previousVersion = base
			c.previousSecret = baseSecret
			c.overlapExpiresAt = time.Now().Add(rotationOverlapWindow)
		}
		c.currentVersion = next
		c.currentSecret = nextSecret
	} else {
		c.currentVersion = base
		c.currentSecret = baseSecret
	}

	c.lastLoadedAt = time.Now()
	return nil
}

// scanForwardFromPrunedVersion is called only when GetSecret(prunedBase)
// returns ErrSecretNotFound — see the COLD-START / LONG-OUTAGE RECOVERY note
// on Load. It walks forward, one version at a time, up to
// secretScanUpperBound versions past prunedBase, until it finds the lowest
// surviving version, and returns that version and its secret. versionByte is
// a uint8, so the scan also stops cleanly at 255 rather than wrapping around.
//
// [REF: IC §8, ADR-027; Milestone 7 corrections session]
func (c *ClusterSecretCache) scanForwardFromPrunedVersion(ctx context.Context, prunedBase uint8) (found uint8, secret []byte, err error) {
	v := prunedBase
	for i := 0; i < secretScanUpperBound; i++ {
		if v == math.MaxUint8 {
			break
		}
		v++

		s, getErr := c.client.GetSecret(ctx, secretPath(v))
		if getErr == nil {
			return v, s, nil
		}
		if !errors.Is(getErr, ErrSecretNotFound) {
			return 0, nil, fmt.Errorf("scan forward from v%d: probe %s: %w", prunedBase, secretPath(v), getErr)
		}
	}
	return 0, nil, fmt.Errorf(
		"no valid secret version found scanning forward from v%d (pruned) up to %d versions ahead — "+
			"operator intervention likely needed", prunedBase, secretScanUpperBound)
}

// CurrentSecret returns the highest currently-valid server_secret and its
// version byte, for use when issuing new challenges (ChallengeNonce).
//
// Returns ErrSecretExpired if more than the 5-minute TTL has elapsed since
// the last successful Load — IC §8: "the microservice continues serving
// using the cached value for up to 5 minutes, then returns ErrSecretExpired
// ... it must not issue challenges with an expired secret."
//
// Goroutine-safe: yes.
//
// [REF: IC §8]
func (c *ClusterSecretCache) CurrentSecret() (secret []byte, versionByte uint8, err error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.currentVersion == 0 {
		return nil, 0, fmt.Errorf("audit.ClusterSecretCache.CurrentSecret: Load has not succeeded yet")
	}
	if time.Since(c.lastLoadedAt) > c.ttl {
		return nil, 0, ErrSecretExpired
	}
	return c.currentSecret, c.currentVersion, nil
}

// IsLoaded reports whether Load has completed successfully at least once —
// a trivial accessor, unlike CurrentSecret, that does not consult the
// 5-minute TTL: it answers "has this cache ever been populated," not "is it
// still fresh."
//
// [Retroactive addition, build.md Milestone 11 Session 11.2.1] Not part of
// the original Milestones 7–8 ClusterSecretCache proposal; added because the
// readiness gate's cluster_audit_secret_loaded condition (IC §3.4, OAS
// ReadinessResponse) needs exactly this boolean and nothing more — it must
// not fail (as CurrentSecret would) merely because the cache's TTL has
// lapsed between refresh cycles, since that is not what "is the secret
// loaded" is asking.
//
// Goroutine-safe: yes.
func (c *ClusterSecretCache) IsLoaded() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.currentVersion != 0
}

// IsVersionValid reports whether versionByte corresponds to a
// currently-accepted secret version — vN or vN+1 during the 24-hour
// rotation overlap window (IC §8). Session 7.2.1's ValidateResponse caller
// uses this; it is NOT called from inside ValidateResponse itself (that
// function has no reference to this cache — see validate.go's NOTE).
//
// Unlike CurrentSecret, this does not consult the 5-minute TTL: a nonce
// issued under a version that is still within its 24-hour overlap remains
// validatable even if this specific replica's cache happens to be stale,
// since the version/secret pairing itself doesn't change during that
// window — only CurrentSecret's "safe to issue NEW challenges" answer is
// TTL-gated.
//
// Goroutine-safe: yes.
//
// [REF: IC §8, ADR-027]
func (c *ClusterSecretCache) IsVersionValid(versionByte uint8) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if versionByte != 0 && versionByte == c.currentVersion {
		return true
	}
	if versionByte != 0 && versionByte == c.previousVersion && time.Now().Before(c.overlapExpiresAt) {
		return true
	}
	return false
}

// SecretForVersion returns the raw secret for a specific (possibly older,
// but still-valid) version byte — needed to validate a challenge nonce
// issued under a version other than the current one.
//
// Error semantics:
//   - ErrSecretNotFound: versionByte is not currently valid — either it was
//     never observed, or (for a previously-superseded version) more than 24
//     hours have passed since the version that replaced it first appeared.
//
// Goroutine-safe: yes.
//
// [REF: IC §8, ADR-027]
func (c *ClusterSecretCache) SecretForVersion(versionByte uint8) (secret []byte, err error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if versionByte != 0 && versionByte == c.currentVersion {
		return c.currentSecret, nil
	}
	if versionByte != 0 && versionByte == c.previousVersion && time.Now().Before(c.overlapExpiresAt) {
		return c.previousSecret, nil
	}
	return nil, ErrSecretNotFound
}
