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
// Guard 1 (PROD_MODE_ENV_SECRET): production mode must not read the cluster master
// seed from an environment variable — a secrets manager is required.
//
// Guard 2 (DEMO_MODE_REAL_PAYMENT): demo mode must not connect to the live Razorpay
// endpoint — real money must not move during a demo run.
//
// Caller pattern for cmd/ wiring (M12/M13):
//
//	if err := config.ValidateStartupGuards(profile); err != nil {
//	    log.Fatalf("[STARTUP] FATAL guard rail: %v", err)
//	}
//
// [REF: MVP §2.3, IC §3.3 PROD_MODE_ENV_SECRET / DEMO_MODE_REAL_PAYMENT]
func ValidateStartupGuards(profile NetworkProfile) error {
	// Guard 1: production seed must come from a secrets manager, never an env var.
	if profile.Mode == "prod" && os.Getenv("VYOMANAUT_CLUSTER_MASTER_SEED") != "" {
		return &StartupError{
			Code:    "PROD_MODE_ENV_SECRET",
			Message: "VYOMANAUT_CLUSTER_MASTER_SEED must not be set in production; use secrets manager",
		}
	}

	// Guard 2: live Razorpay endpoint is forbidden in demo mode.
	if profile.Mode == "demo" && profile.PaymentMode == "razorpay_live" {
		return &StartupError{
			Code:    "DEMO_MODE_REAL_PAYMENT",
			Message: "live Razorpay endpoint must not be used in demo mode",
		}
	}

	return nil
}
