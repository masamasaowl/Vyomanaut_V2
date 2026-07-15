/*
// Package erasure implements Reed-Solomon erasure coding parameterised by NetworkProfile.
//
// # Design Role (architecture.md §10 Stage 2)
//
// Receives an AONT package from internal/crypto.AONTEncodeSegment and produces
// TotalShards independent fragments via Engine.EncodeSegment. On retrieval, Engine.DecodeSegment reconstructs the AONT package, which is then passed to internal/crypto.AONTDecodePackage. The two packages are always used in sequence; neither is standalone (ADR-022).
//
// # Key Invariant (DM §3 Invariant 7, ADR-031)
//
// ShardSize = 262144 (256 KB) is a compile-time constant. It is identical in demo and production modes. No profile field changes this value. Any code that receives a shard of different size has a bug in the caller, not this package.
//
// # Profile Parameterisation
//
//   Production: DataShards=16, ParityShards=40, TotalShards=56  (RS(16,56))
//   Demo:       DataShards=3,  ParityShards=2,  TotalShards=5   (RS(3,5))
//
// The active profile is injected at construction time via NewEngine(profile). No package-level state exists; multiple Engine instances with different profiles may coexist safely.
//
# Import Constraint (IC §9)

This package imports zero other internal/ packages. It receives config.NetworkProfile by value at construction time only. internal/config's PRODUCTION code may NOT import erasure (that would cycle, since erasure imports config) — but internal/config/profiles_test.go correctly imports erasure from the external `package config_test`, specifically to work around this for cross-package test assertions. internal/storage/shard_size_test.go uses the identical trick for the analogous storage↔erasure check. (M3 review §5)
//
// # Goroutine Safety
//
// Engine is goroutine-safe after construction. EncodeSegment and DecodeSegment are stateless over the Engine struct.
//
// # Files
//
//   params.go     — const ShardSize = 262144; Engine struct definition
//   engine.go     — NewEngine, EncodeSegment, DecodeSegment, ErrTooFewShards, ErrShardSize
//   engine_test.go — round-trip (prod+demo), any-k-shards (demo), ShardSize assertion

// Design notes after Phase 3.1 - Engine Construction

// The build.md says to use github.com/klauspost/reedsolomon but we can't fetch it in this environment
// The go module cache format requires zips to be named with the module path prefix inside the zip. GitHub releases/codeload zips use the format repo-version/ not module@version/. This is why the unzip fails.
// Decision: We use the pure-Go GF(2^8) implementation; GONOSUMDB and write the logic in rs_internal.go. This is actually the cleanest approach for this codebase since it:

 1. Eliminates the external dependency
 2. Keeps the code self-contained
 3. The API matches exactly what the build spec requires

// Remove the reedsolomon external dep from params.go and engine.go. Use the pure-Go RS implementation in rs_internal.go. The go.mod stays clean (no external reedsolomon dep). The Engine struct uses the pure-Go *rsEncoder instead of reedsolomon.Encoder.

[REF: ADR-003]
*/
package erasure
