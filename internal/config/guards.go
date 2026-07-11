// Package config is declared in doc.go.
// This file implements mandatory startup guard rails (MVP §2.3, IC §3.3).
//
// [REF: MVP §2.3, IC §3.3, build.md Phase 1.3 Session 1.3.2].

package config

import "os"

// StartupError carries the IC §3.3 error code for fatal startup conditions.
// It is returned by ValidateStartupGuards and must be handled via log.Fatalf
// in every cmd/ entry point before proceeding with subsystem wiring.
type StartupError struct {
	// Code is the machine-readable IC §3.3 error_code; never localised.
	Code string
	// Message is the human-readable explanation; may change between releases.
	Message string
}

// Error implements the error interface.
func (e *StartupError) Error() string { return e.Code + ": " + e.Message }

// ValidateStartupGuards checks fatal startup invariants that cannot be caught at
// compile time. It returns a *StartupError with the IC §3.3 error_code on violation,
// or nil when all checks pass.
//
// Guard 1 (PROD_MODE_ENV_SECRET): a profile with RequireSecretsManager=true (only
// ProductionProfile today) must not read the cluster master seed from an
// environment variable — a secrets manager is required.
//
// Guard 2 (DEMO_MODE_REAL_PAYMENT): a profile with AllowLivePayments=false (only
// DemoProfile today) must not connect to the live Razorpay endpoint — real money
// must not move during a demo run.
//
// Both guards key off typed NetworkProfile fields rather than profile.Mode, per
// the "no runtime branching on the mode string" rule (MVP CR-01, ARCH §9, and
// NetworkProfile.Mode's own field comment). A future profile that isn't named
// "prod" or "demo" (e.g. a staging profile) is still guarded correctly as long
// as its RequireSecretsManager / AllowLivePayments fields are set correctly —
// see TestGuardRailsIgnoreModeString in guards_test.go.
//
// Caller pattern for cmd/ wiring (M12/M13):
//
//	if err := config.ValidateStartupGuards(profile); err != nil {
//	    log.Fatalf("[STARTUP] FATAL guard rail: %v", err)
//	}
//
// [REF: MVP §2.3, MVP CR-01, ARCH §9, IC §3.3 PROD_MODE_ENV_SECRET / DEMO_MODE_REAL_PAYMENT]
func ValidateStartupGuards(profile NetworkProfile) error {
	// Guard 1: a profile that requires a secrets manager must not fall back to
	// reading the cluster master seed from an environment variable.
	if profile.RequireSecretsManager && os.Getenv("VYOMANAUT_CLUSTER_MASTER_SEED") != "" {
		return &StartupError{
			Code:    "PROD_MODE_ENV_SECRET",
			Message: "VYOMANAUT_CLUSTER_MASTER_SEED must not be set when RequireSecretsManager is true; use secrets manager",
		}
	}

	// Guard 2: a profile that does not allow live payments must not be
	// configured to hit the live Razorpay endpoint.
	if !profile.AllowLivePayments && profile.PaymentMode == "razorpay_live" {
		return &StartupError{
			Code:    "DEMO_MODE_REAL_PAYMENT",
			Message: "live Razorpay endpoint must not be used when AllowLivePayments is false",
		}
	}

	return nil
}
