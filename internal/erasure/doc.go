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
// # Import Constraint (IC §9)
//
// This package imports zero other internal/ packages. It receives config.NetworkProfile by value at construction time only. The config package may NOT import erasure.
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

[REF: ADR-003]
*/
package erasure
