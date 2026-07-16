// Package audit is declared in doc.go.
// Unit tests for ClusterSecretCache, using an in-memory fake
// SecretsManagerClient — no live secrets manager is needed for any of these.
//
// Two tests reach into unexported ClusterSecretCache fields directly
// (same-package access): TestClusterSecretCacheExpiresAfterTTL manipulates
// lastLoadedAt, and TestClusterSecretCacheRejectsRetiredVersion manipulates
// overlapExpiresAt. Neither NewClusterSecretCache nor any other exported
// entry point accepts an injectable clock or a shorter TTL — IC §8 fixes
// the TTL at exactly 5 minutes and the overlap at exactly 24 hours — so
// directly aging the relevant timestamp is the only way to exercise these
// paths without a real 5-minute or 24-hour wait.
//
// Tests:
//   - TestClusterSecretCacheFailsClosedOnLoadError     Load errors when unreachable; no panic
//   - TestClusterSecretCacheServesFromCacheDuringOutage outage mid-TTL -> CurrentSecret still succeeds
//   - TestClusterSecretCacheExpiresAfterTTL             TTL elapsed + unreachable -> ErrSecretExpired
//   - TestClusterSecretCacheRotationOverlap             both vN and vN+1 loaded -> IsVersionValid true for both
//   - TestClusterSecretCacheRejectsRetiredVersion       version outside the 24h overlap -> IsVersionValid false
//
// [REF: IC §8, MVP §8.2, build.md Phase 7.4 Session 7.4.1]

package audit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ── fake SecretsManagerClient ─────────────────────────────────────────────────

// fakeSecretsManager is a fully in-memory, fully-controllable
// SecretsManagerClient test double.
type fakeSecretsManager struct {
	mu       sync.Mutex
	secrets  map[string][]byte
	forceErr error // if non-nil, every GetSecret call returns this instead
}

func newFakeSecretsManager() *fakeSecretsManager {
	return &fakeSecretsManager{secrets: make(map[string][]byte)}
}

// set makes secret available at the given version's path.
func (f *fakeSecretsManager) set(version uint8, secret []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets[secretPath(version)] = secret
}

// setUnreachable makes every subsequent GetSecret call fail with err,
// simulating an outage.
func (f *fakeSecretsManager) setUnreachable(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forceErr = err
}

func (f *fakeSecretsManager) GetSecret(_ context.Context, path string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.forceErr != nil {
		return nil, f.forceErr
	}
	secret, ok := f.secrets[path]
	if !ok {
		return nil, ErrSecretNotFound
	}
	return secret, nil
}

// testSecret returns a deterministic 32-byte fixture filled with fill.
func testSecret(fill byte) []byte {
	s := make([]byte, 32)
	for i := range s {
		s[i] = fill
	}
	return s
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestClusterSecretCacheFailsClosedOnLoadError verifies Load returns an
// error (never panics) when the secrets manager is unreachable, and that a
// cache which has never had a successful Load is not usable via
// CurrentSecret.
//
// [REF: IC §8 "fail-closed", build.md Phase 7.4 Session 7.4.1]
func TestClusterSecretCacheFailsClosedOnLoadError(t *testing.T) {
	fake := newFakeSecretsManager()
	fake.setUnreachable(ErrSecretManagerUnavailable)
	cache := NewClusterSecretCache(fake)

	if err := cache.Load(context.Background()); err == nil {
		t.Fatal("Load: expected an error when the secrets manager is unreachable, got nil")
	}

	if _, _, err := cache.CurrentSecret(); err == nil {
		t.Error("CurrentSecret: expected an error after Load never succeeded, got nil")
	}
}

// TestClusterSecretCacheServesFromCacheDuringOutage verifies that once a
// secret has been loaded, a subsequent outage does not prevent
// CurrentSecret from serving the last good value while still within TTL.
//
// [REF: IC §8, build.md Phase 7.4 Session 7.4.1]
func TestClusterSecretCacheServesFromCacheDuringOutage(t *testing.T) {
	fake := newFakeSecretsManager()
	fake.set(1, testSecret(0x01))
	cache := NewClusterSecretCache(fake)

	if err := cache.Load(context.Background()); err != nil {
		t.Fatalf("initial Load: %v", err)
	}

	fake.setUnreachable(ErrSecretManagerUnavailable)
	if err := cache.Load(context.Background()); err == nil {
		t.Fatal("Load during outage: expected an error, got nil")
	}

	secret, version, err := cache.CurrentSecret()
	if err != nil {
		t.Fatalf("CurrentSecret during outage (still within TTL): %v", err)
	}
	if version != 1 {
		t.Errorf("version = %d, want 1", version)
	}
	if string(secret) != string(testSecret(0x01)) {
		t.Errorf("secret = %x, want %x", secret, testSecret(0x01))
	}
}

// TestClusterSecretCacheExpiresAfterTTL verifies that once the 5-minute TTL
// has elapsed since the last successful Load, and the manager remains
// unreachable, CurrentSecret returns ErrSecretExpired rather than
// continuing to serve stale data indefinitely.
//
// [REF: IC §8, build.md Phase 7.4 Session 7.4.1]
func TestClusterSecretCacheExpiresAfterTTL(t *testing.T) {
	fake := newFakeSecretsManager()
	fake.set(1, testSecret(0x02))
	cache := NewClusterSecretCache(fake)

	if err := cache.Load(context.Background()); err != nil {
		t.Fatalf("initial Load: %v", err)
	}

	cache.mu.Lock()
	cache.lastLoadedAt = time.Now().Add(-cache.ttl - time.Minute)
	cache.mu.Unlock()

	fake.setUnreachable(ErrSecretManagerUnavailable)

	if _, _, err := cache.CurrentSecret(); !errors.Is(err, ErrSecretExpired) {
		t.Errorf("CurrentSecret after TTL expiry: got %v, want ErrSecretExpired", err)
	}
}

// TestClusterSecretCacheRotationOverlap verifies that when both vN and
// vN+1 exist in the secrets manager, Load picks up both, and both are
// valid — vN via the 24-hour overlap, vN+1 as the new current version.
//
// [REF: IC §8 rotation contract, build.md Phase 7.4 Session 7.4.1]
func TestClusterSecretCacheRotationOverlap(t *testing.T) {
	fake := newFakeSecretsManager()
	fake.set(1, testSecret(0x03))
	fake.set(2, testSecret(0x04))
	cache := NewClusterSecretCache(fake)

	if err := cache.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !cache.IsVersionValid(1) {
		t.Error("IsVersionValid(1) = false, want true (still within the 24h overlap window)")
	}
	if !cache.IsVersionValid(2) {
		t.Error("IsVersionValid(2) = false, want true (the new current version)")
	}

	secret1, err := cache.SecretForVersion(1)
	if err != nil {
		t.Errorf("SecretForVersion(1): %v", err)
	} else if string(secret1) != string(testSecret(0x03)) {
		t.Errorf("SecretForVersion(1) = %x, want %x", secret1, testSecret(0x03))
	}

	current, version, err := cache.CurrentSecret()
	if err != nil {
		t.Fatalf("CurrentSecret: %v", err)
	}
	if version != 2 {
		t.Errorf("CurrentSecret version = %d, want 2", version)
	}
	if string(current) != string(testSecret(0x04)) {
		t.Errorf("CurrentSecret secret = %x, want %x", current, testSecret(0x04))
	}
}

// TestClusterSecretCacheRejectsRetiredVersion verifies that once the
// 24-hour overlap window has elapsed, the superseded version stops being
// valid — while the current version remains unaffected.
//
// [REF: IC §8 "any nonce with version byte N received after 24 hours is
// rejected as expired", build.md Phase 7.4 Session 7.4.1]
func TestClusterSecretCacheRejectsRetiredVersion(t *testing.T) {
	fake := newFakeSecretsManager()
	fake.set(1, testSecret(0x05))
	fake.set(2, testSecret(0x06))
	cache := NewClusterSecretCache(fake)

	if err := cache.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !cache.IsVersionValid(1) {
		t.Fatal("IsVersionValid(1) = false immediately after rotation was detected, want true")
	}

	cache.mu.Lock()
	cache.overlapExpiresAt = time.Now().Add(-time.Hour)
	cache.mu.Unlock()

	if cache.IsVersionValid(1) {
		t.Error("IsVersionValid(1) = true more than 24h after v2 was observed, want false")
	}
	if !cache.IsVersionValid(2) {
		t.Error("IsVersionValid(2) = false, want true — retiring v1 must not affect the current version")
	}

	if _, err := cache.SecretForVersion(1); !errors.Is(err, ErrSecretNotFound) {
		t.Errorf("SecretForVersion(1) after retirement: got %v, want ErrSecretNotFound", err)
	}
}
