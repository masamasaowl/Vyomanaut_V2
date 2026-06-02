package config

import "testing"

// TestSelectProfile verifies that SelectProfile returns the correct NetworkProfile
// for each recognised mode string, including fallback to VYOMANAUT_MODE when the
// flag is empty.
//
// The fatal default case (unknown mode) is not tested here because log.Fatalf
// calls os.Exit(1) — subprocess execution would be required. The three non-fatal
// branches satisfy the build.md VERIFY requirement.
//
// [REF: build.md Phase 1.3 Session 1.3.1, MVP §5.3]
func TestSelectProfile(t *testing.T) {
	t.Run("demo_flag_returns_demo_profile", func(t *testing.T) {
		profile := SelectProfile("demo")
		if profile.Mode != "demo" {
			t.Errorf("SelectProfile(\"demo\"): Mode = %q, want \"demo\"", profile.Mode)
		}
		if profile.DataShards != DemoProfile.DataShards {
			t.Errorf("SelectProfile(\"demo\"): DataShards = %d, want %d",
				profile.DataShards, DemoProfile.DataShards)
		}
	})

	t.Run("prod_flag_returns_production_profile", func(t *testing.T) {
		profile := SelectProfile("prod")
		if profile.Mode != "prod" {
			t.Errorf("SelectProfile(\"prod\"): Mode = %q, want \"prod\"", profile.Mode)
		}
		if profile.DataShards != ProductionProfile.DataShards {
			t.Errorf("SelectProfile(\"prod\"): DataShards = %d, want %d",
				profile.DataShards, ProductionProfile.DataShards)
		}
	})

	t.Run("empty_flag_no_env_defaults_to_prod", func(t *testing.T) {
		// Clear the env var so the empty-flag path reaches the "" switch case.
		t.Setenv("VYOMANAUT_MODE", "")
		profile := SelectProfile("")
		if profile.Mode != "prod" {
			t.Errorf("SelectProfile(\"\") with VYOMANAUT_MODE unset: Mode = %q, want \"prod\"",
				profile.Mode)
		}
	})

	t.Run("empty_flag_env_demo_returns_demo_profile", func(t *testing.T) {
		// Env var overrides the empty flag argument.
		t.Setenv("VYOMANAUT_MODE", "demo")
		profile := SelectProfile("")
		if profile.Mode != "demo" {
			t.Errorf("SelectProfile(\"\") with VYOMANAUT_MODE=demo: Mode = %q, want \"demo\"",
				profile.Mode)
		}
	})
}
