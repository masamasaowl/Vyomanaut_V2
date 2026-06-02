// Package config is declared in doc.go.
// This file implements profile selection at process startup.
// The CLI flag overrides the env var; env var overrides the default.
//
// [REF: MVP §5.3, build.md Phase 1.3 Session 1.3.1]

package config

import (
	"log"
	"os"
)

// SelectProfile returns the canonical NetworkProfile for the given mode string.
// If modeFlag is empty, VYOMANAUT_MODE is read from the environment.
// Absent or empty mode defaults to ProductionProfile with a logged warning.
// An unrecognised mode is fatal — the process refuses to start.
//
// Callers (cmd/microservice, cmd/provider) pass the parsed --mode CLI flag value;
// wiring is deferred to M12/M13. [REF: MVP §5.3]
func SelectProfile(modeFlag string) NetworkProfile {
	if modeFlag == "" {
		modeFlag = os.Getenv("VYOMANAUT_MODE")
	}

	switch modeFlag {
	case "demo":
		log.Printf("[STARTUP] Vyomanaut — mode=DEMO — do not use for real data")
		return DemoProfile
	case "prod":
		log.Printf("[STARTUP] Vyomanaut — mode=PRODUCTION")
		return ProductionProfile
	case "":
		log.Printf("[STARTUP] WARNING: VYOMANAUT_MODE not set; defaulting to prod")
		return ProductionProfile
	default:
		log.Fatalf("[STARTUP] FATAL: unknown VYOMANAUT_MODE=%q; must be 'demo' or 'prod'",
			modeFlag)
		return ProductionProfile // unreachable; satisfies compiler
	}
}
