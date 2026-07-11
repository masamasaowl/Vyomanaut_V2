package config

import "testing"

// TestGuardRails verifies all four startup guard rail combinations.
//
// Each sub-test uses t.Setenv for full environment isolation — the original value
// is restored automatically after the sub-test regardless of pass/fail.
//
// [REF: build.md Phase 1.3 Session 1.3.3, MVP §2.3, IC §3.3]
func TestGuardRails(t *testing.T) {
	// ── Guard 1: PROD_MODE_ENV_SECRET ─────────────────────────────────────────

	t.Run("prod+seed_present → error PROD_MODE_ENV_SECRET", func(t *testing.T) {
		t.Setenv("VYOMANAUT_CLUSTER_MASTER_SEED", "devonlysecret00000000000000000000")
		err := ValidateStartupGuards(ProductionProfile)
		if err == nil {
			t.Fatal("expected *StartupError, got nil")
		}
		se, ok := err.(*StartupError) //nolint:errorlint // direct type assertion is intentional; StartupError is the only error type returned by ValidateStartupGuards
		if !ok {
			t.Fatalf("expected *StartupError, got %T: %v", err, err)
		}
		if se.Code != "PROD_MODE_ENV_SECRET" {
			t.Errorf("Code = %q, want \"PROD_MODE_ENV_SECRET\"", se.Code)
		}
		if se.Message == "" {
			t.Error("Message must not be empty")
		}
	})

	t.Run("prod+seed_absent → nil", func(t *testing.T) {
		// Set to empty string: os.Getenv returns "" → guard condition is false.
		t.Setenv("VYOMANAUT_CLUSTER_MASTER_SEED", "")
		err := ValidateStartupGuards(ProductionProfile)
		if err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})

	// ── Guard 2: DEMO_MODE_REAL_PAYMENT ──────────────────────────────────────

	t.Run("demo+live_payment → error DEMO_MODE_REAL_PAYMENT", func(t *testing.T) {
		// Construct a demo profile with PaymentMode overridden to live.
		// DemoProfile is a package-level var; copy it to avoid mutation.
		profile := DemoProfile
		profile.PaymentMode = "razorpay_live"

		err := ValidateStartupGuards(profile)
		if err == nil {
			t.Fatal("expected *StartupError, got nil")
		}
		se, ok := err.(*StartupError) //nolint:errorlint // direct type assertion is intentional; StartupError is the only error type returned by ValidateStartupGuards
		if !ok {
			t.Fatalf("expected *StartupError, got %T: %v", err, err)
		}
		if se.Code != "DEMO_MODE_REAL_PAYMENT" {
			t.Errorf("Code = %q, want \"DEMO_MODE_REAL_PAYMENT\"", se.Code)
		}
		if se.Message == "" {
			t.Error("Message must not be empty")
		}
	})

	t.Run("demo+mock_payment → nil", func(t *testing.T) {
		// DemoProfile.PaymentMode is "mock" — guard must not fire.
		t.Setenv("VYOMANAUT_CLUSTER_MASTER_SEED", "") // ensure guard 1 is also clear
		err := ValidateStartupGuards(DemoProfile)
		if err != nil {
			t.Errorf("expected nil, got %v", err)
		}
	})
}

// TestGuardRailsIgnoreModeString verifies both guards key off their typed
// fields (RequireSecretsManager, AllowLivePayments), not profile.Mode. A
// synthetic non-"prod"/"demo" Mode value is used to prove neither guard
// special-cases those two literal strings.
//
// [REF: MVP CR-01, ARCH §9]
func TestGuardRailsIgnoreModeString(t *testing.T) {
	t.Run("RequireSecretsManager=true guards even when Mode != \"prod\"", func(t *testing.T) {
		t.Setenv("VYOMANAUT_CLUSTER_MASTER_SEED", "devonlysecret00000000000000000000")
		profile := DemoProfile
		profile.Mode = "staging"
		profile.RequireSecretsManager = true
		err := ValidateStartupGuards(profile)
		se, ok := err.(*StartupError) //nolint:errorlint // see TestGuardRails above for rationale
		if !ok {
			t.Fatalf("expected *StartupError even though Mode=%q, got %T: %v", profile.Mode, err, err)
		}
		if se.Code != "PROD_MODE_ENV_SECRET" {
			t.Errorf("Code = %q, want \"PROD_MODE_ENV_SECRET\"", se.Code)
		}
	})

	t.Run("AllowLivePayments=false guards even when Mode != \"demo\"", func(t *testing.T) {
		t.Setenv("VYOMANAUT_CLUSTER_MASTER_SEED", "") // keep guard 1 clear
		profile := ProductionProfile
		profile.Mode = "staging"
		profile.AllowLivePayments = false
		err := ValidateStartupGuards(profile)
		se, ok := err.(*StartupError) //nolint:errorlint
		if !ok {
			t.Fatalf("expected *StartupError even though Mode=%q, got %T: %v", profile.Mode, err, err)
		}
		if se.Code != "DEMO_MODE_REAL_PAYMENT" {
			t.Errorf("Code = %q, want \"DEMO_MODE_REAL_PAYMENT\"", se.Code)
		}
	})
}
