# Vyomanaut V2 — MVP Specification: Demo / Production Mode

**Status:** Authoritative — read alongside ADR-031, architecture.md, and the pre-ADR analysis  
**Version:** 1.0  
**Date:** May 2026  
**Repository:** https://github.com/masamasaowl/Vyomanaut_Research  
**Supersedes:** —  
**Companion documents:**
- [`docs/system-design/architecture.md`](./architecture.md) — system overview
- [`docs/system-design/requirements.md`](./requirements.md) — functional and non-functional requirements
- [`docs/system-design/data-model.md`](./data-model.md) — canonical database schema
- [`docs/system-design/interface-contracts.md`](./interface-contracts.md) — wire-format contracts
- [`docs/decisions/ADR-031-demo-mode-network-profile.md`](../decisions/ADR-031-demo-mode-network-profile.md) — authoritative ADR for the mode flag

---

## Table of Contents

1. [Why This Document Exists](#1-why-this-document-exists)
2. [The Mode Flag](#2-the-mode-flag)
3. [Exact Demo Network Specifications](#3-exact-demo-network-specifications)
4. [Feature Gap Table: Demo vs Production](#4-feature-gap-table-demo-vs-production)
5. [How to Toggle Each Feature](#5-how-to-toggle-each-feature)
6. [Switching Mode Requirements and Repercussions](#6-switching-mode-requirements-and-repercussions)
7. [Viability Fact-Check: Every Demo Value Verified](#7-viability-fact-check-every-demo-value-verified)
8. [Repository Foundation](#8-repository-foundation)

---

## 1. Why This Document Exists

The pre-ADR analysis identified the full parameter space that separates a live demo from
production. This MVP file translates that analysis into three actionable artefacts: a
precise demo spec (what the built system looks like in a room), a decision record (what
cannot exist in demo), and a build plan (what order to build things so neither mode is a
throwaway).

The central principle, inherited from ADR-029's simulation mode design, is:

> **A demo on a forked codebase proves nothing. The demo must run the same binary as
> production, configured with different parameters.**

Everything that follows enforces this principle. No logic is mocked. No cryptographic
primitive is weakened in structure. No data integrity invariant is relaxed. The demo is
production with faster clocks and fewer providers.

---

## 2. The Mode Flag

### 2.1 Definition

```
Environment variable:  VYOMANAUT_MODE=demo | prod
CLI override:          --mode=demo | --mode=prod
```

Default: `prod`. Any process that does not explicitly set `VYOMANAUT_MODE=demo` behaves
as production. The active mode is printed as the first stdout line and first log line at
startup.

```
[STARTUP] Vyomanaut daemon v0.1.0 — mode=DEMO — do not use for real data
[STARTUP] Vyomanaut daemon v0.1.0 — mode=PRODUCTION
```

### 2.2 Immutability

The mode is read once at process startup from the environment, stored as a single
package-level constant, and passed to all subsystems via the `NetworkProfile` struct
(defined in Section 5.1). It cannot be changed without restarting the process. There is
no API endpoint or signal that changes the mode at runtime.

### 2.3 Guard rails

The following startup checks are mandatory:

- If `VYOMANAUT_MODE=prod` **and** `VYOMANAUT_CLUSTER_MASTER_SEED` is present in the
  environment → **fatal error, refuse to start.** A secrets manager is required in production.
- If `VYOMANAUT_MODE=demo` **and** the process connects to the live Razorpay API endpoint
  (not the test environment or mock) → **fatal error, refuse to start.** Real money must
  not be moved during a demo.
- If `VYOMANAUT_MODE` is absent → default to `prod` and log a warning that mode was not
  explicitly set.

---

## 3. Exact Demo Network Specifications

This section answers the question: *what does the system look like when you run it at a
hackathon or investor meeting?*

### 3.1 Physical topology

| Resource | Minimum for demo | Notes |
|---|---|---|
| Provider machines | 5 laptops or 1 laptop with `--sim-count=5` | Each runs the provider daemon binary |
| Microservice instance | 1 (single replica, quorum checks disabled) | Runs on any laptop or a separate machine |
| Relay nodes | 0 | All traffic is local; NAT traversal is not exercised |
| Secrets manager | None | `VYOMANAUT_CLUSTER_MASTER_SEED` env var replaces it |
| Payment gateway | Mock in-memory implementation | Implements the `PaymentProvider` interface; no HTTP to Razorpay |
| PostgreSQL | Single instance, local | Managed Postgres is overkill for demo |

### 3.2 Erasure coding parameters

| Parameter | Production | **Demo** |
|---|---|---|
| `DataShards` (s) | 16 | **3** |
| `ParityShards` (r) | 40 | **2** |
| `TotalShards` (n) | 56 | **5** |
| `ShardSize` (lf) | 262,144 B (256 KB) | **262,144 B (unchanged)** |
| `LazyRepairR0` | 8 | **1** |
| Lazy repair trigger | s + r0 = 24 | **s + r0 = 4** |
| Emergency floor | s = 16 | **s = 3** |
| Max segment size | 14 MB (56 × 256 KB) | **1.25 MB (5 × 256 KB)** |
| Fault tolerance | 40-of-56 simultaneous failures | **2-of-5 simultaneous failures** |

### 3.3 Readiness gate thresholds

| Condition | Production | **Demo** |
|---|---|---|
| Active vetted providers | ≥ 56 | **≥ 5** |
| Distinct ASNs | ≥ 5 | **≥ 5** (see §7.1 — corrected from pre-ADR analysis) |
| Distinct metro regions | ≥ 3 | **≥ 1** |
| Microservice quorum | Full (3,2,2) | **Single instance** |
| Razorpay accounts with cooling | ≥ 56 | **≥ 5 (mock; cooling = 0 s)** |
| Relay nodes deployed | ≥ 3 | **0** |
| Cluster audit secret | Loaded on all replicas | **`VYOMANAUT_CLUSTER_MASTER_SEED` env var** |

### 3.4 Time windows

| Parameter | Production | **Demo** | Observable in |
|---|---|---|---|
| Provider heartbeat interval | 4 h | **30 s** | ~2 min |
| Heartbeat jitter | ±5 min | **±5 s** | — |
| Polling / audit interval | 24 h | **2 min** | ~2 min |
| DHT republication interval | 12 h | **2 min** | ~2 min |
| DHT record expiry | 24 h | **4 min** | ~4 min |
| Departure threshold | 72 h | **10 min** | ~10 min |
| Promised downtime maximum | 72 h | **10 min** | — |
| Vetting: consecutive passes required | 80 | **5** | ~10 min |
| Vetting: minimum duration | 120 days | **5 min** | ~10 min |
| Vetting escrow hold window | 60 days | **2 min** | ~2 min |
| Post-vetting escrow hold window | 30 days | **1 min** | ~1 min |
| Monthly release computation cycle | Monthly (23rd) | **Every 2 min** | ~2 min |
| Razorpay cooling period | 24 h | **0 s (instant)** | Instant |
| Audit period duration | 1 calendar month | **2 min** | ~2 min |
| Pending receipt GC timeout | 48 h | **5 min** | ~5 min |
| Scoring window: short | 24 h | **2 min** | — |
| Scoring window: medium | 7 days | **6 min** | — |
| Scoring window: long | 30 days | **20 min** | — |
| Dual-window trigger lookback | 7d vs 30d | **6 min vs 20 min** | — |
| Repair pre-warning promotion timeout | 6 h | **3 min** | ~3 min |
| GC retry backoff (vetting GC) | 5m → 15m → 60m | **10s → 30s → 2min** | — |

### 3.5 Cryptographic parameters

| Parameter | Production | **Demo** | What changes |
|---|---|---|---|
| Argon2id time cost (t) | 3 | **1** | Faster session start (~20–50 ms vs ~200–500 ms) |
| Argon2id memory (m) | 65,536 KiB (64 MB) | **4,096 KiB (4 MB)** | Weaker brute-force resistance |
| Argon2id parallelism (p) | 4 | **1** | Fewer threads |
| BIP-39 mnemonic confirmation | Required (2 of 24 words) | **Skipped (mnemonic displayed, not confirmed)** | UX only |
| All ciphers (ChaCha20, AES-CTR, AEAD) | Unchanged | **Unchanged** | — |
| Ed25519 key generation | Unchanged | **Unchanged** | — |
| SHA-256, HMAC-SHA256, HKDF | Unchanged | **Unchanged** | — |
| Challenge nonce length | 33 bytes | **33 bytes (unchanged)** | — |
| Poly1305 constant-time tag check | Enabled | **Enabled (unchanged)** | — |

### 3.6 What a demo session looks like on a timeline

```
T+00:00  — 5 provider daemons start, register, Ed25519 keys generated
T+00:30  — Heartbeats arrive at microservice; all 5 providers in VETTING
T+00:30  — Readiness gate passes (5 providers, 5 synthetic ASNs, instant cooling)
T+01:00  — Data owner registers; master secret derived (< 50 ms); mnemonic displayed
T+01:00  — Data owner uploads a test file (< 1.25 MB per segment; 5 shards placed)
T+03:00  — First audit cycle fires; all 5 providers respond; first PASS logged
T+05:00  — Vetting minimum duration (5 min) reached
T+10:00  — 5th consecutive audit PASS; providers transition VETTING → ACTIVE
T+10:30  — Vetting GC instruction delivered; synthetic chunks deleted
T+10:30  — Real data owner shard assignments begin
T+12:00  — Escrow hold window (1 min post-vetting) elapses; release computation fires
T+12:00  — Mock payment provider logs a successful "payout"
T+20:00  — Simulate a provider departure (kill one daemon)
T+30:00  — Departure threshold (10 min) crossed; silent departure declared
T+30:00  — Escrow seized; repair job queued; 4 remaining shards contacted; repair fires
T+32:00  — Repair completes; fragment count restored to 5; file still retrievable
```

This is the entire end-to-end lifecycle, observable by a live audience in ~30–35 minutes.

---

## 4. Feature Gap Table: Demo vs Production

The following table enumerates every capability present in PROD but absent, reduced, or
simulated in DEMO. Features not listed are identical in both modes.

| # | Feature | PROD | DEMO | Why absent in DEMO |
|---|---|---|---|---|
| F-01 | File size per segment | Up to 14 MB | Up to 1.25 MB | RS n=5 × 256 KB = 1.25 MB max per segment |
| F-02 | Fault tolerance | Any 40 of 56 providers can fail | Any 2 of 5 providers can fail | Direct consequence of RS(3,5) |
| F-03 | Provider network size | ≥ 56 active providers | ≥ 5 active providers | Demo n = 5 |
| F-04 | Geographic diversity | ≥ 3 distinct Indian metro regions | ≥ 1 region | Demo can run on a single LAN |
| F-05 | Microservice HA | (3,2,2) quorum, 3 replicas, gossip | Single instance, no quorum | Demo infrastructure constraint |
| F-06 | NAT traversal (relay) | Circuit Relay v2, 3+ relay nodes | None (all local) | Demo runs on LAN/localhost |
| F-07 | Secrets manager | Vault / AWS SSM / GCP Secret Manager | `VYOMANAUT_CLUSTER_MASTER_SEED` env var | No cloud dependency in demo |
| F-08 | Payment gateway | Razorpay Route + RazorpayX + Smart Collect 2.0 | Mock in-memory `PaymentProvider` implementation | No real money in demo |
| F-09 | UPI deposit flow | UPI Intent / QR (NPCI-compliant) | CLI command deposits into mock ledger | No Razorpay in demo |
| F-10 | Razorpay cooling period | 24 h before first payout | 0 s (instant) | Demo time compression |
| F-11 | Provider vetting duration | 80 passes AND 120 days | 5 passes AND 5 minutes | Demo time compression |
| F-12 | Escrow hold window | 30 days post-vetting | 1 minute post-vetting | Demo time compression |
| F-13 | Argon2id strength | t=3, m=64 MB (production-grade) | t=1, m=4 MB (demo-grade) | Demo session start must be fast |
| F-14 | BIP-39 mnemonic confirmation | Two words confirmed before proceeding | Mnemonic displayed; confirmation skipped | Demo UX (no awkward typing) |
| F-15 | Monthly payment computation | On the 23rd of each calendar month | Every 2 minutes | Demo time compression |
| F-16 | DHT record republication | 12-hour interval | 2-minute interval | Demo time compression |
| F-17 | Audit period duration | 1 calendar month | 2 minutes | Demo time compression |
| F-18 | Departure threshold | 72 hours | 10 minutes | Demo time compression |
| F-19 | ASN diversity enforcement | 20% cap, 5+ real ASNs | 20% cap, 5 synthetic ASNs (`SIM-AS1` … `SIM-AS5`) | Demo providers share the same LAN |
| F-20 | Repair bandwidth (BWavg) | ~39 Kbps/peer at MTTF=300d | Not computed (demo departures are deliberate) | No MTTF modelling needed for demo |
| F-21 | Hot storage band | Cold only (V2 scope) | Cold only | Unchanged; not a demo limitation |
| F-22 | Transparent Merkle Log | V3 deferred | V3 deferred | Unchanged; not a demo limitation |
| F-23 | V3 Hitchhiker repair codes | V3 deferred | V3 deferred | Unchanged; not a demo limitation |
| F-24 | Provider web dashboard | V3 deferred | V3 deferred | Unchanged; not a demo limitation |
| F-25 | International payments | Razorpay abstraction layer ready | Abstraction layer ready | Unchanged; not a demo limitation |

**Features that are identical in both modes (not listed above):**

All cryptographic operations (AONT-RS encoding, ChaCha20/AES-CTR, AEAD_CHACHA20_POLY1305,
Ed25519 signing, HMAC-SHA256 nonce generation, HKDF key derivation, Poly1305 constant-time
tag comparison), the two-phase PENDING→final audit receipt write, all row security policies
on `audit_receipts` and `escrow_events`, the single-writer vLog goroutine, crash recovery,
the vLog `content_hash` verification before every audit response, the ASN cap enforcement
logic, the JIT detection mechanism, the synthetic vetting chunk path, the 33-byte nonce
format, the idempotency key requirement, integer paise enforcement, and all libp2p protocol
wire formats.

---

## 5. How to Toggle Each Feature

### 5.1 The NetworkProfile struct (single source of truth)

All mode-variable values live in one struct. No scattered `if mode == "demo"` conditionals
exist anywhere except the two places that construct these profiles at startup.

```go
// internal/config/network_profile.go

// NetworkProfile is the single authoritative container for all parameters that
// differ between DEMO and PROD mode. It is constructed once at startup and passed
// via dependency injection to every subsystem. Package-level reads of this struct
// are prohibited — callers receive it as a constructor argument.
//
// INVARIANT: Every field that affects wire format, cryptographic output, or database
// schema (ShardSize, challenge nonce length, amount_paise type) must be identical in
// both profiles. Only performance thresholds, time windows, and infrastructure scale
// parameters are mode-variable.
type NetworkProfile struct {
    Mode string // "demo" or "prod" — printed at startup, never used for branching

    // ── Erasure coding (ADR-003) ─────────────────────────────────────────────
    DataShards   int // s
    ParityShards int // r
    TotalShards  int // n = s + r
    ShardSize    int // lf — FIXED at 262144 in both modes; must not change
    LazyRepairR0 int // r0

    // ── Readiness gate (ADR-029) ─────────────────────────────────────────────
    MinActiveProviders int
    MinDistinctASNs    int
    MinMetroRegions    int
    MinRelayNodes      int
    MinCooledAccounts  int

    // ── ASN cap (ADR-014) ────────────────────────────────────────────────────
    // MaxShardsPerASN = floor(TotalShards * ASNCapFraction)
    // Production: 0.20 (20%) → floor(56*0.20) = 11 shards per ASN
    // Demo:       0.20 (20%) → floor(5*0.20)  = 1 shard per ASN
    // With 5 distinct synthetic ASNs in demo (SIM-AS1…SIM-AS5), this is satisfied.
    ASNCapFraction float64

    // ── Time windows ─────────────────────────────────────────────────────────
    HeartbeatInterval       time.Duration
    HeartbeatJitter         time.Duration
    PollingInterval         time.Duration
    DHTRepublishInterval    time.Duration
    DHTExpiryDuration       time.Duration
    DepartureThreshold      time.Duration
    PromisedDowntimeMaximum time.Duration
    AuditPeriodDuration     time.Duration
    EscrowHoldWindow        time.Duration // post-vetting
    VettingHoldWindow       time.Duration
    PendingReceiptGCAge     time.Duration
    RepairPromotionTimeout  time.Duration // §FR-043

    // ── Scoring windows (ADR-008) ─────────────────────────────────────────────
    ScoreWindowShort  time.Duration // "24h" in prod, "2min" in demo
    ScoreWindowMedium time.Duration // "7d"  in prod, "6min" in demo
    ScoreWindowLong   time.Duration // "30d" in prod, "20min" in demo
    DualWindowDrop    float64       // always 0.20; never mode-variable

    // ── Vetting (ADR-005) ─────────────────────────────────────────────────────
    VettingMinPasses   int
    VettingMinDuration time.Duration
    VettingCapFraction float64 // always 0.10 (10% of declared_storage_gb); never mode-variable

    // ── Cryptographic cost (ADR-020) ─────────────────────────────────────────
    Argon2Time    uint32
    Argon2Memory  uint32 // in KiB
    Argon2Threads uint8

    // ── Infrastructure ───────────────────────────────────────────────────────
    RequireSecretsManager bool
    RequireQuorum         bool
    PaymentMode           string        // "razorpay_live" | "razorpay_test" | "mock"
    SkipMnemonicConfirm   bool
    RazorpayCoolingPeriod time.Duration

    // ── Release computation cycle ─────────────────────────────────────────────
    // Production: computed once per calendar month on the 23rd
    // Demo:       computed on this ticker interval
    ReleaseComputationInterval time.Duration

    // ── GC retry backoff (§4.5 interface-contracts) ──────────────────────────
    GCRetryBackoff []time.Duration // e.g. [5m, 15m, 60m] or [10s, 30s, 2m]
}
```

### 5.2 The two profile instances

```go
// internal/config/profiles.go

var ProductionProfile = NetworkProfile{
    Mode:         "prod",
    DataShards:   16, ParityShards: 40, TotalShards: 56,
    ShardSize:    262144, LazyRepairR0: 8,
    MinActiveProviders: 56, MinDistinctASNs: 5, MinMetroRegions: 3,
    MinRelayNodes: 3, MinCooledAccounts: 56,
    ASNCapFraction: 0.20,
    HeartbeatInterval: 4 * time.Hour, HeartbeatJitter: 5 * time.Minute,
    PollingInterval: 24 * time.Hour, DHTRepublishInterval: 12 * time.Hour,
    DHTExpiryDuration: 24 * time.Hour, DepartureThreshold: 72 * time.Hour,
    PromisedDowntimeMaximum: 72 * time.Hour, AuditPeriodDuration: 30 * 24 * time.Hour,
    EscrowHoldWindow: 30 * 24 * time.Hour, VettingHoldWindow: 60 * 24 * time.Hour,
    PendingReceiptGCAge: 48 * time.Hour, RepairPromotionTimeout: 6 * time.Hour,
    ScoreWindowShort: 24 * time.Hour, ScoreWindowMedium: 7 * 24 * time.Hour,
    ScoreWindowLong: 30 * 24 * time.Hour, DualWindowDrop: 0.20,
    VettingMinPasses: 80, VettingMinDuration: 120 * 24 * time.Hour,
    VettingCapFraction: 0.10,
    Argon2Time: 3, Argon2Memory: 65536, Argon2Threads: 4,
    RequireSecretsManager: true, RequireQuorum: true,
    PaymentMode: "razorpay_live", SkipMnemonicConfirm: false,
    RazorpayCoolingPeriod: 24 * time.Hour,
    ReleaseComputationInterval: 0, // driven by calendar date, not ticker
    GCRetryBackoff: []time.Duration{5 * time.Minute, 15 * time.Minute, 60 * time.Minute},
}

var DemoProfile = NetworkProfile{
    Mode:         "demo",
    DataShards:   3, ParityShards: 2, TotalShards: 5,
    ShardSize:    262144, LazyRepairR0: 1,
    MinActiveProviders: 5, MinDistinctASNs: 5, MinMetroRegions: 1,
    MinRelayNodes: 0, MinCooledAccounts: 5,
    ASNCapFraction: 0.20, // 20% of 5 = 1 shard/ASN; enforced via 5 synthetic ASNs
    HeartbeatInterval: 30 * time.Second, HeartbeatJitter: 5 * time.Second,
    PollingInterval: 2 * time.Minute, DHTRepublishInterval: 2 * time.Minute,
    DHTExpiryDuration: 4 * time.Minute, DepartureThreshold: 10 * time.Minute,
    PromisedDowntimeMaximum: 10 * time.Minute, AuditPeriodDuration: 2 * time.Minute,
    EscrowHoldWindow: 1 * time.Minute, VettingHoldWindow: 2 * time.Minute,
    PendingReceiptGCAge: 5 * time.Minute, RepairPromotionTimeout: 3 * time.Minute,
    ScoreWindowShort: 2 * time.Minute, ScoreWindowMedium: 6 * time.Minute,
    ScoreWindowLong: 20 * time.Minute, DualWindowDrop: 0.20,
    VettingMinPasses: 5, VettingMinDuration: 5 * time.Minute,
    VettingCapFraction: 0.10,
    Argon2Time: 1, Argon2Memory: 4096, Argon2Threads: 1,
    RequireSecretsManager: false, RequireQuorum: false,
    PaymentMode: "mock", SkipMnemonicConfirm: true,
    RazorpayCoolingPeriod: 0,
    ReleaseComputationInterval: 2 * time.Minute,
    GCRetryBackoff: []time.Duration{10 * time.Second, 30 * time.Second, 2 * time.Minute},
}
```

### 5.3 Profile selection and injection

```go
// cmd/microservice/main.go  and  cmd/provider/main.go

func selectProfile() config.NetworkProfile {
    mode := os.Getenv("VYOMANAUT_MODE")
    if flag.Lookup("mode") != nil {
        mode = *modeFlag // CLI flag overrides env
    }
    switch mode {
    case "demo":
        log.Printf("[STARTUP] Vyomanaut — mode=DEMO — do not use for real data")
        return config.DemoProfile
    case "prod", "":
        if mode == "" {
            log.Printf("[STARTUP] WARNING: VYOMANAUT_MODE not set; defaulting to prod")
        }
        log.Printf("[STARTUP] Vyomanaut — mode=PRODUCTION")
        return config.ProductionProfile
    default:
        log.Fatalf("[STARTUP] FATAL: unknown VYOMANAUT_MODE=%q; must be 'demo' or 'prod'", mode)
    }
    panic("unreachable")
}
```

The returned profile is passed to every subsystem constructor. No subsystem reads
`VYOMANAUT_MODE` directly.

### 5.4 Toggle map: one reference per feature

| Feature / parameter | Toggle location | How it toggles |
|---|---|---|
| RS parameters (s, r, n, r0) | `NetworkProfile.DataShards` / `ParityShards` / `TotalShards` / `LazyRepairR0` | `internal/erasure` reads these from the profile passed to `NewEngine(profile)` |
| Readiness gate thresholds | `NetworkProfile.MinActiveProviders` etc. | Assignment service `ReadinessChecker` reads all `Min*` fields from profile |
| ASN cap | `NetworkProfile.ASNCapFraction` | `MaxShardsPerASN = floor(profile.TotalShards * profile.ASNCapFraction)` |
| Heartbeat interval | `NetworkProfile.HeartbeatInterval` | Daemon timer `time.NewTicker(profile.HeartbeatInterval)` |
| Audit polling | `NetworkProfile.PollingInterval` | Scheduler ticker |
| DHT republication | `NetworkProfile.DHTRepublishInterval` | Availability service ticker |
| Departure threshold | `NetworkProfile.DepartureThreshold` | Departure detector query `WHERE last_heartbeat_ts < NOW() - profile.DepartureThreshold` |
| Audit period duration | `NetworkProfile.AuditPeriodDuration` | Period-creation logic in audit scheduler |
| Scoring windows | `NetworkProfile.ScoreWindow{Short,Medium,Long}` | `mv_provider_scores` view SQL generated at startup with these intervals |
| Vetting passes required | `NetworkProfile.VettingMinPasses` | Scoring package `IncrementConsecutivePasses` checks against this value |
| Vetting minimum duration | `NetworkProfile.VettingMinDuration` | ACTIVE transition checks `first_chunk_assignment_at + profile.VettingMinDuration <= NOW()` |
| Escrow hold window | `NetworkProfile.EscrowHoldWindow` | Release computation queries events in `[NOW()-EscrowHoldWindow, NOW()]` |
| Release computation cycle | `NetworkProfile.ReleaseComputationInterval` | In demo: ticker. In prod: calendar date (23rd of month). Branch on `profile.ReleaseComputationInterval == 0` |
| Razorpay cooling | `NetworkProfile.RazorpayCoolingPeriod` | `razorpay_cooling_until = NOW() + profile.RazorpayCoolingPeriod` |
| Payment gateway | `NetworkProfile.PaymentMode` | `PaymentProvider` factory selects `MockProvider`, `RazorpayTestProvider`, or `RazorpayLiveProvider` |
| Argon2id parameters | `NetworkProfile.Argon2{Time,Memory,Threads}` | `internal/crypto.DeriveMasterSecret` receives these as arguments |
| Mnemonic confirmation | `NetworkProfile.SkipMnemonicConfirm` | Client UX gate: `if !profile.SkipMnemonicConfirm { requireTwoWords() }` |
| Secrets manager | `NetworkProfile.RequireSecretsManager` | Startup: if false, read `VYOMANAUT_CLUSTER_MASTER_SEED` env var instead |
| Quorum requirement | `NetworkProfile.RequireQuorum` | Gossip cluster: if false, allow N=1 with quorum checks disabled |
| Relay nodes | `NetworkProfile.MinRelayNodes` | Readiness gate only; libp2p relay stack compiled in both modes |
| GC retry backoff | `NetworkProfile.GCRetryBackoff` | Vetting GC delivery uses `profile.GCRetryBackoff[attempt]` |
| Pending receipt GC | `NetworkProfile.PendingReceiptGCAge` | GC query: `WHERE audit_result IS NULL AND server_challenge_ts < NOW() - profile.PendingReceiptGCAge` |
| Repair promotion timeout | `NetworkProfile.RepairPromotionTimeout` | Scheduler: PRE_WARNING jobs older than this are promoted to PERMANENT_DEPARTURE |

### 5.5 Database schema parameterisation

Two CHECK constraints differ between modes. Both are emitted during the initial migration
from the active profile:

```go
// migrations/generator.go

func GenerateInitialSchema(profile config.NetworkProfile) string {
    return fmt.Sprintf(`
-- chunk_assignments.shard_index constraint
CONSTRAINT chunk_assignments_shard_index_range
    CHECK (shard_index BETWEEN 0 AND %d OR shard_index IS NULL),

-- repair_jobs.available_shard_count constraint
CONSTRAINT repair_jobs_shard_count_range
    CHECK (available_shard_count BETWEEN %d AND %d),
`,
        profile.TotalShards-1,
        profile.DataShards,
        profile.TotalShards,
    )
}
```

The `mv_provider_scores` view is dropped and recreated at microservice startup using the
profile's scoring window values:

```sql
-- Generated at startup from profile.ScoreWindow{Short,Medium,Long}
CREATE MATERIALIZED VIEW mv_provider_scores AS
SELECT provider_id,
    SUM(...) FILTER (WHERE server_challenge_ts >= NOW() - INTERVAL '{{ScoreWindowShort}}')
    ...
```

This is an application-layer step, not a migration — the view is regenerated every restart.

### 5.6 Synthetic ASN assignment in demo mode

In demo mode (whether `--sim-count=N` or physical laptops), providers must declare distinct
synthetic ASNs to satisfy the 20% cap. This is enforced at registration:

```go
// Demo mode: provider registration handler
if profile.Mode == "demo" {
    // If the provider does not supply an ASN, assign the next available
    // synthetic ASN from the pool SIM-AS1…SIM-AS{N}
    if req.ASN == "" {
        req.ASN = nextSyntheticASN(db, profile)
    }
}
```

With `--sim-count=5` and `--sim-asn-count=5` (from ADR-029 simulation mode), this is
already handled: each simulated instance gets `SIM-AS{1..5}`. For physical demo machines,
each provider daemon must be started with `--demo-asn=SIM-AS{N}` or the microservice
auto-assigns one.

---

## 6. Switching Mode Requirements and Repercussions

This section enumerates every requirement that must be satisfied for the mode-switching
mechanism to exist safely, and the repercussion if any requirement is violated.

### 6.1 Code requirements

| # | Requirement | Repercussion if violated |
|---|---|---|
| CR-01 | `NetworkProfile` is the only place mode-variable values are defined. No `if mode == "demo"` in business logic. | Scattered conditionals create untested code paths; demo behavior diverges from production without detection |
| CR-02 | `ShardSize` (262,144) is a constant in both profiles. It must not appear in `NetworkProfile` as a variable field. | A changed shard size breaks vLog entry sizing, audit challenge framing, and RocksDB index assumptions simultaneously — silent data corruption |
| CR-03 | The 33-byte `challenge_nonce` CHECK constraint and all wire-format fixed sizes are hardcoded constants, not profile fields. | A shorter nonce in demo breaks cross-replica validation for all future production deployments where demo receipts exist in the same audit log |
| CR-04 | All amounts in `escrow_events` and `owner_escrow_events` are `BIGINT` paise in both modes. The mock `PaymentProvider` must enforce this. | Floating-point in demo hides payment arithmetic bugs that surface in production |
| CR-05 | The `audit_receipts` row security policy (INSERT-only, single PENDING→final UPDATE) applies in demo mode. | Relaxing the policy in demo means demo testing does not validate the invariant the RSP is supposed to guarantee |
| CR-06 | `internal/repair.EnqueueJob` must check `IsVettingChunk` before enqueueing in both modes. | A demo departure of a vetting provider that triggers repair would cause a visible crash or incorrect behavior during the presentation |
| CR-07 | The single-writer vLog goroutine requirement applies in demo mode. | Concurrent writes in demo produce vLog corruption; the bug surfaces in production after demo code is merged |
| CR-08 | `RecoverFromCrash()` runs at daemon startup in both modes. | A demo crash mid-presentation cannot be recovered by restarting the daemon |
| CR-09 | Ed25519 signing and verification occur on every audit receipt in both modes. | Skipping signing in demo means the audit signature path is never tested before production |
| CR-10 | The `PaymentProvider` interface must be used for all payment operations; the mock must implement it fully, not bypass it. | Direct DB writes from a mock break the interface abstraction; switching to Razorpay then requires finding every bypass |

### 6.2 Infrastructure requirements

| # | Requirement | Repercussion if violated |
|---|---|---|
| IR-01 | Demo and production databases are completely separate instances (separate connection strings, separate Postgres data directories). | Demo data (meaningless synthetic chunks, test escrow events) in the production DB corrupts audit logs and payment history |
| IR-02 | Demo and production Razorpay credentials are separate. Demo uses mock or Razorpay test environment; it must never touch the live Razorpay account. | Real money could be moved; real provider bank accounts could be credited or seized |
| IR-03 | Demo and production `VYOMANAUT_CLUSTER_MASTER_SEED` values are separate. | A demo secret leaked in env vars (e.g. in container logs) that matches the production seed would allow audit nonce prediction |
| IR-04 | The readiness gate in demo mode must still pass before uploads are accepted. `VYOMANAUT_MODE=demo` does not bypass the gate; it changes the thresholds. | Skipping the gate in demo means demo testing does not validate the gate logic |
| IR-05 | A deployment check at CI must verify that `VYOMANAUT_MODE=prod` and `VYOMANAUT_CLUSTER_MASTER_SEED` cannot coexist. | Secrets manager is bypassed in production; audit nonces are predictable |

### 6.3 Operational requirements

| # | Requirement | Repercussion if violated |
|---|---|---|
| OR-01 | The `NetworkProfile` struct must be printed in full at startup (values only, not secrets). | Mode drift between replicas is undetectable; audit scoring uses different windows per replica |
| OR-02 | The demo `DemoProfile` struct has a compiler-enforced test that verifies `ShardSize == ProductionProfile.ShardSize`. | A future engineer changes ShardSize in DemoProfile without realising the wire-format implication |
| OR-03 | Every time a new mode-variable parameter is added to `NetworkProfile`, a corresponding value must be added to both `ProductionProfile` and `DemoProfile`. The Go compiler enforces this (struct literal with all fields). | A zero-value default silently uses Go's zero value (0, false, "", nil) which may be wrong for production |
| OR-04 | The `mv_provider_scores` view is dropped and recreated at microservice startup. A migration that changes the view must also update the view-generation code. | A stale view with production intervals runs in demo mode; providers never reach ACTIVE within a 30-minute session |

### 6.4 Schema migration requirements

| # | Requirement | Repercussion if violated |
|---|---|---|
| MR-01 | Schema migrations are never applied between demo and production databases in either direction. | A demo schema (with `shard_index BETWEEN 0 AND 4`) applied to production breaks all 56-shard file uploads |
| MR-02 | The migration generator (`migrations/generator.go`) must be given the active `NetworkProfile` at generation time, not at apply time. | Generating with the wrong profile produces a schema that is structurally correct but has wrong CHECK bounds |
| MR-03 | The migration checklist in `data-model.md §9` must be run against both profiles in CI (two separate migration runs against two separate databases). | A constraint that works for demo (5 shards) may fail to create correctly for production (56 shards) without this check |

---

## 7. Viability Fact-Check: Every Demo Value Verified

This section works through every parameter change and confirms it does not create a logical
bottleneck when the full demo network runs together.

### 7.1 CORRECTED: ASN Cap vs MinDistinctASNs (bottleneck found in pre-ADR analysis)

**The pre-ADR analysis contains an internal contradiction.** It states both:
- "20% of 5 = 1 shard per ASN" (correct)
- "with 2 required ASNs and 5 shards it is enforceable (2–3 shards per ASN)" (contradicts the 20% cap)

**Analysis:** With n=5 and a 20% cap, `floor(5 × 0.20) = 1 shard per ASN`. Placing 5 shards
under a 1-shard-per-ASN cap requires exactly 5 distinct ASNs. Setting `MinDistinctASNs=2`
as proposed in the analysis makes the cap literally impossible to satisfy — 5 shards across
2 ASNs means at least one ASN holds 3 shards (60%), violating the 20% cap.

**Resolution (applied in this document):**

`MinDistinctASNs` is set to **5** in `DemoProfile`, not 2. With simulation mode
`--sim-asn-count=5` (already specified in ADR-029), this is automatically satisfied.
For physical laptops in demo mode, each provider must declare a distinct synthetic ASN
(`SIM-AS1` through `SIM-AS5`) at startup. The microservice auto-assigns if not provided.

The 20% cap percentage (0.20) is unchanged. The enforcement works as intended: each provider
holds exactly 1 of 5 shards; no correlated group can lose the file by departing. This is
actually a stricter diversity constraint in demo than the 11-of-56 allowed in production,
which is the correct direction — a 5-provider network is more fragile and benefits from
strict diversity.

**Verdict: ✅ Viable with MinDistinctASNs=5 (corrected). The pre-ADR analysis value of 2 was incorrect.**

### 7.2 RS(3,5) reconstruction math

- Reconstruction requires any 3 of 5 shards. ✓
- With 5 providers, losing any 2 simultaneously still leaves 3 (the floor). ✓
- The lazy repair trigger at s+r0=4 fires when only 4 remain (after 1 departure). ✓
- The emergency floor at s=3 fires when exactly 3 remain. ✓
- Repair requires contacting 3 surviving shard holders to RS-decode — always available until
  the 3rd departure triggers the emergency floor. ✓
- Repair places 1 new shard on a replacement provider — restores count to 4 (still above floor). ✓

**Verdict: ✅ Fully consistent.**

### 7.3 Vetting timing consistency

- Polling interval: 2 minutes → one audit challenge per chunk per 2 minutes.
- `VettingMinPasses=5` → 5 consecutive passes required → minimum ~10 minutes of polling.
- `VettingMinDuration=5 minutes` → the 5-minute minimum is always satisfied before 5 passes
  are achieved at 2-minute polling. It is never the binding constraint. This is intentional
  — the pass count is the binding condition, matching the production design where 80 passes
  at 24-hour polling = 80 days minimum, always before the 120-day minimum.
- Conclusion: VETTING → ACTIVE transition happens at approximately T+10 minutes. ✓

**Verdict: ✅ Timing is consistent. VettingMinDuration is never violated.**

### 7.4 DHT republication buffer

- Production: republish every 12h, expire after 24h → 12h buffer.
- Demo: republish every 2min, expire after 4min → 2min buffer.
- The buffer ratio is maintained (2× the republication interval). ✓
- A single delayed republication does not cause record expiry as long as the next fires
  within the buffer window. ✓

**Verdict: ✅ DHT record availability is maintained in demo.**

### 7.5 Scoring window ratios

- Production: short=24h (1×), medium=7d (7×), long=30d (30×)
- Demo: short=2min (1×), medium=6min (3×), long=20min (10×)

The ratios are not proportional (7× vs 3×, 30× vs 10×). This is intentional and acceptable
for demo: the windows just need to be distinct enough to capture different behavioural
patterns within a 30-minute session. A provider that passes 5 audits will have identical
scores across all three windows (no data for the medium/long windows to show degradation
separately). The dual-window trigger fires if the 6-minute score drops 0.20 below the
20-minute score, which requires deliberate inconsistency (3 of 3 audits in the short window
failing while the long window still shows prior passes). This is observable within the demo.

**Verdict: ✅ Window ratios are not proportional but are functionally correct for demo.**

### 7.6 Escrow hold window vs audit period duration

- Audit period: 2 minutes. Release hold window: 1 minute (post-vetting).
- At ACTIVE transition (T+10 min), the first audit period is approximately 5 periods old
  (5 × 2-minute periods = 10 minutes).
- The 1-minute hold window means earnings older than 1 minute are immediately releasable
  at the next release computation (fires every 2 minutes).
- Result: the first payout fires approximately 2 minutes after ACTIVE transition. ✓
- The dual-window trigger checks if the 6-minute score dropped 0.20 below the 20-minute
  score. With only ~5 audit data points, a single FAIL in a 6-minute window would drop the
  short score to 0.75 (3/4 passes), while the long window would still show ~0.80 (4/5 passes).
  Difference: 0.05 — would not trigger the dual-window hold. To trigger it in demo, the
  presenter would need to kill a provider for at least 3 consecutive audit cycles (~6 minutes).
  This is achievable and makes the feature demonstrable. ✓

**Verdict: ✅ Payment lifecycle is coherent and observable in demo.**

### 7.7 Mock PaymentProvider idempotency

The mock payment provider must enforce `idempotency_key` uniqueness even without Razorpay.
The `escrow_events.idempotency_key` column has a `UNIQUE` constraint at the database level.
Any INSERT with a duplicate key will fail at the DB layer. The mock does not need additional
logic — the DB constraint is the enforcement. ✓

**Verdict: ✅ Idempotency is DB-enforced regardless of payment provider implementation.**

### 7.8 Repair in demo: does it complete in time?

- With n=5 providers on a LAN, all shard holders are reachable in < 1 ms (localhost) or
  < 5 ms (LAN).
- RS decode requires 3 shards; each is 256 KB. Download: 3 × 256 KB at LAN speed
  (~100 Mbps) = 3 × ~20 ms = ~60 ms total.
- RS encode of missing shards: < 1 ms on modern hardware.
- Upload of 1 replacement shard to a new provider: ~20 ms.
- Total repair time: < 100 ms.
- The repair queue dequeues and executes within 1 polling cycle (2 minutes).
- A departure at T+20:00 results in a visible, completed repair by T+22:00. ✓

**Verdict: ✅ Repair is fast and demonstrable in real time.**

### 7.9 Argon2id performance at demo parameters

- Demo: t=1, m=4096 KiB (4 MB), p=1 → approximately 20–50 ms on any modern laptop.
- This is imperceptible to a live audience. ✓
- Security note: the reduced parameters weaken the master secret against offline brute-force.
  For demo use with no real data, this is an accepted trade-off. The code path (Argon2id →
  32-byte output → HKDF chain) is identical. ✓

**Verdict: ✅ Demo Argon2id is fast and structurally identical to production.**

### 7.10 Segment size limitation (max 1.25 MB per segment)

- Files larger than 1.25 MB require multiple segments.
- A 5 MB demo file → 4 segments × 5 shards each → 20 total shard uploads.
- The pointer file schema stores one entry per segment, each with 5 (not 56) provider IDs
  and chunk IDs. The pointer file is smaller but structurally identical.
- Upload time for 5 MB on LAN at 100 Mbps: 4 segments × 5 × 256 KB = 5 MB → ~400 ms. ✓
- Retrieval: download 3 shards per segment × 4 segments = 12 shard downloads → ~240 ms. ✓

**Verdict: ✅ Multi-segment files work in demo. File size is limited but not crippling.**

### 7.11 PENDING receipt GC at 5 minutes vs polling at 2 minutes

- Audit challenge fires every 2 minutes. Provider has RTO seconds (pool median ~2000 ms).
- A PENDING row that is never completed (microservice crash between Phase 1 and Phase 2)
  is GC'd after 5 minutes.
- A provider that is online and responding will complete Phase 2 within one RTO (~2 seconds).
- 5 minutes gives 2.5× the audit cycle time as headroom for crash recovery. ✓
- In production, 48 hours = 2× the 24-hour audit cycle. The ratio is the same. ✓

**Verdict: ✅ GC timing is proportionally correct relative to the audit cycle.**

### 7.12 Release computation every 2 minutes vs escrow hold of 1 minute

- Hold window: 1 minute. Release computation: every 2 minutes.
- It is possible that earnings released from the hold window (older than 1 minute) sit for
  up to 2 minutes before the next release computation runs.
- Maximum delay between earnings becoming releasable and the payout firing: 2 minutes.
- This is observable and acceptable. ✓
- Note: in production, the analogous delay is up to 1 month (hold window = 30 days;
  release computation fires once on the 23rd). The demo makes this delay visible in
  real time, which is a better demonstration of the mechanism. ✓

**Verdict: ✅ Release timing is coherent. No earnings are permanently withheld.**

### 7.13 Synthetic ASNs and the readiness gate

- With `--sim-count=5 --sim-asn-count=5`, providers are assigned `SIM-AS1` through `SIM-AS5`.
- Readiness gate requires `MinDistinctASNs=5` (corrected from pre-ADR analysis).
- COUNT(DISTINCT asn) WHERE status = 'VETTING' or 'ACTIVE' = 5 → gate passes. ✓
- ASN cap check at upload: MAX shards per ASN = floor(5 × 0.20) = 1. Assignment service
  must assign one shard per ASN. With 5 ASNs and 5 shards, this is exactly satisfied. ✓

**Verdict: ✅ ASN enforcement is fully consistent once MinDistinctASNs is corrected to 5.**

---

## 8. Repository Foundation

This section is the authoritative reference for the repository structure, package layout, file inventories, ownership boundaries, and the list of code patterns that must never be introduced. It was previously maintained as a standalone `repo-structure.md` — that document is deprecated; this section is its canonical successor.

### 8.1 Top-level directory map

```tree

├── cmd/
│   ├── microservice/          # Coordination microservice entrypoint — wiring only, no business logic
│   ├── provider/              # Provider daemon; supports --sim-count and --sim-asn-count
│   └── client/                # Data owner CLI
│
├── internal/
│   ├── config/                # NetworkProfile struct, ProductionProfile, DemoProfile (§5.2)
│   ├── crypto/                # AONT cipher, HKDF, Argon2id, pointer file AEAD, BIP-39, Ed25519 helpers
│   ├── erasure/               # Reed-Solomon RS encode/decode via klauspost/reedsolomon
│   ├── storage/               # WiscKey: RocksDB chunk index + append-only vLog
│   ├── p2p/                   # libp2p host, QUIC/TCP transports, DHT, NAT traversal, heartbeat
│   ├── audit/                 # Challenge generation, dispatch, receipt two-phase write, cluster secret
│   ├── scoring/               # Three-window reliability score, consecutive-pass counter, EWMA RTO
│   ├── repair/                # Departure detector, repair job queue, repair executor
│   ├── payment/               # PaymentProvider interface, Razorpay implementation, escrow ledger
│   └── client/
│       ├── account/           # Registration, BIP-39 mnemonic, session key derivation, keystore
│       ├── upload/            # Upload orchestrator: encode + assignment + parallel transfer
│       ├── retrieve/          # Retrieval orchestrator: pointer file + shard download + decode
│       └── manage/            # File list, delete, escrow balance view
│
├── migrations/                # Numbered SQL DDL; one .sql per forward migration, one .down.sql per destructive migration
│
├── deployments/
│   ├── production/            # Terraform / Kubernetes manifests for production
│   ├── staging/               # Mirrors production at reduced scale
│   └── dev/                   # docker-compose.yml for local development
│
├── scripts/                   # Developer tooling: lint, test, simulation runner, benchmarks
│
├── runbooks/                  # Operational runbooks (must all exist before M8 closes)
│
├── docs/
│   ├── decisions/             # ADR-001 through ADR-031+
│   ├── research/              # Paper notes, reading list, open/answered questions, benchmarking protocol
│   └── system-design/         # The six canonical documents + sequence-diagrams/
│       └── api/               # openapi.yaml
│
├── .github/
│   ├── workflows/             # ci.yml, release.yml
│   └── CODEOWNERS
│
├── go.mod                     # module github.com/masamasaowl/vyomanaut
└── go.sum
```

### 8.2 Package file inventories

Each package's file list is the contract between the repository structure and the interface definitions in `interface-contracts.md §5`. Adding a file is additive and safe. Renaming or splitting a file that exports a frozen symbol (listed in `interface-contracts.md §12`) requires updating §12 in the same PR.

- `internal/config/**network_profile.go` — `NetworkProfile` struct (§5.2 of this document), `ProductionProfile` and `DemoProfile` vars. `profiles_test.go` — `TestProfileShardSizeIsConstant`, `TestProfileBothFullySpecified` (Go struct-literal compiler enforcement).

- `internal/crypto/**hkdf.go`, `argon2.go`, `aont.go`, `aont_canary.go` (fixed `[16]byte` const, never a var), `bip39.go` (MasterSecretToMnemonic, MnemonicToMasterSecret, SelectConfirmationWords (see interface-contracts.md §5.1 for full signatures)), `chacha20poly1305.go`, `aesni.go` (CPUID, `//go:build amd64`), `aesni_other.go` (stub, returns false), `errors.go` (`ErrTagMismatch`, `ErrCanaryMismatch`), `*_test.go` including cross-platform known-answer vectors and fuzz targets.

- `internal/erasure/**params.go` (exported constants `DataShards`, `ParityShards`, `TotalShards`, `ShardSize`), `engine.go` (`NewEngine(profile)`, `EncodeSegment`, `DecodeSegment`), `errors.go`, `engine_test.go` (round-trip, any-k-shards, shard-size).

- `internal/storage/**store.go` (`ChunkStore` interface, `NewChunkStore`), `vlog.go` (append, read, GC, crash-recovery tail-scan), `index.go` (RocksDB wrapper: Bloom filter, lookup, insert, delete), `rotational.go` (HDD/SSD detection, `//go:build linux`), `rotational_other.go` (stub, assume SSD), `errors.go`, `store_test.go`, `single_writer_test.go` (`TestSingleWriterGoroutine` with 100-goroutine contention — deadlock, not data corruption, is the detectable failure mode).

- `internal/p2p/**host.go` (0-RTT policy enforcement per protocol ID suffix), `dht.go` (custom HMAC key validator), `dht_namespace.go` (`const dhtKeyNamespace = "/vyomanaut/dht-key/1.0.0"` — sole definition in the entire repo), `nat.go` (`maxHolePunchRetries = 1`), `heartbeat.go` (4-hour signed heartbeat), `identity.go` (Ed25519 key pair generation and keystore persistence), `errors.go`, `dht_test.go` (`TestDHTKeyValidator`, `TestDHTKeyValidatorPersists` — CI required check).

- `internal/audit/**challenge.go` (`ChallengeNonce` — always 33 bytes), `validate.go` (`ValidateResponse`), `receipt.go` (`WriteReceiptPhase1`, `WriteReceiptPhase2`), `secret.go` (`ClusterSecretCache`, 5-minute TTL, fail-closed on expiry), `secrets_iface.go` (`SecretsManagerClient` interface), `jit.go` (JIT threshold computation and `jit_flag` evaluation), `errors.go`, `audit_test.go` (two-phase crash safety, idempotent retry, cross-replica nonce validation).

- `internal/scoring/**score.go` (`GetScore`, `GetScoreFromPrimary` — monthly release multiplier must use primary), `passes.go` (`IncrementConsecutivePasses`, `ResetConsecutivePasses`), `rto.go` (EWMA for `avg_rtt_ms`, `var_rtt_ms`, `p95_throughput_kbps`), `errors.go`, `score_test.go` (dual-window flag, VETTING→ACTIVE transition race guard).

- `internal/repair/**departure.go` (departure detector loop, seizure + repair enqueue in one transaction), `queue.go` (`EnqueueJob`, `DequeueNextJob`, `MarkJobComplete`, `IsVettingChunk`, `DeleteVettingChunksOnDeparture`), `executor.go` (download 16 shards → RS decode → re-encode → upload to replacements), `assignment.go` (Power of Two Choices + ASN cap enforcement for replacement selection), `errors.go`, `repair_test.go` (priority ordering, ASN cap, emergency floor, vetting exclusion).

- `internal/payment/**provider.go` (`PaymentProvider` interface), `razorpay.go` (Razorpay implementation), `mock.go` (mock implementation for demo mode), `ledger.go` (`InsertEscrowEvent`, `EscrowEventType` constants including `EscrowReversal`), `balance.go` (`GetBalance`), `release.go` (monthly release computation, release multiplier table, dual-window flag check), `seizure.go` (escrow seizure on departure, Razorpay Route reversal), `paise.go` (`PaiseAmount int64` type with custom JSON unmarshaller that rejects fractional values), `rbi_holidays.go` (`LastWorkingDayOfMonth`), `errors.go`, `payment_test.go` (`TestNoFloatArithmetic` — CI required check), `razorpay_test.go` (webhook handler tests for all three events).

- `internal/client/account/**register.go`, `master_secret.go` (UI gate before upload), `mnemonic.go` (BIP-39 generation and `TwoWordConfirmationGate` — skipped in demo when `profile.SkipMnemonicConfirm = true`), `keystore.go` (encrypted keystore: Ed25519 key + pointer file nonce counter), `recover.go` (passphrase and mnemonic recovery paths), `account_test.go`.

- `internal/client/upload/**orchestrator.go`, `session.go` (FR-060 crash recovery: `file_id`, `chunk_ids`, `ack_status[TotalShards]`, persisted to disk), `assign.go` (assignment request, HTTP 503 handling), `transfer.go` (parallel libp2p shard upload, receipt collection, progress callback), `pointer.go` (pointer file construction, AEAD encryption, microservice registration), `upload_test.go`.

- `internal/client/retrieve/**orchestrator.go`, `pointer.go` (fetch, derive key, constant-time tag verify, decrypt), `download.go` (parallel 56-provider dial, cancel after 16 valid shards, content-address verification per shard), `decode.go` (RS decode, AONT decode, K recovery, canary check, padding strip, buffer zeroing on canary fail), `retrieve_test.go`.

- `internal/client/manage/**files.go`, `delete.go`, `escrow.go`.

### 8.3 `cmd/` entrypoint flags

**`cmd/provider/main.go` flags:**

| Flag | Default | Description |
| --- | --- | --- |
| `--microservice-url` | — | Required. HTTPS base URL of the coordination microservice. |
| `--data-dir` | `$HOME/.vyomanaut` | Persistent data directory. |
| `--sim-count` | 0 | Simulation instances in a single process. 0 = normal mode. |
| `--sim-base-port` | 4001 | Base libp2p listen port for simulation instances. |
| `--sim-data-dir` | `/tmp/vyomanaut-sim` | Root directory for simulation instance data. |
| `--sim-asn-count` | 5 | Synthetic ASN count for simulation mode. |
| `--relay-addrs` | — | Comma-separated relay node multiaddrs. |
| `--declared-storage-gb` | — | Required in normal mode. |

**`cmd/client/main.go` subcommands:**

| Subcommand | Description | Package |
| --- | --- | --- |
| `register` | Account creation, Argon2id derivation, mnemonic display | `internal/client/account` |
| `recover` | New-device recovery via passphrase or mnemonic | `internal/client/account` |
| `upload <path>` | Encode and upload a file | `internal/client/upload` |
| `retrieve <file_id>` | Download and decode a file | `internal/client/retrieve` |
| `ls` | List files with availability status | `internal/client/manage` |
| `rm <file_id>` | Delete a file | `internal/client/manage` |
| `balance` | Show escrow balance | `internal/client/manage` |
| `deposit` | Initiate UPI Intent deposit | `internal/client/manage` |

### 8.4 CI pipeline structure

All checks in this section are required — a PR may not be merged if any of them fail. CI changes must not remove any required check; such a PR is automatically rejected by CODEOWNERS.

```go
.github/workflows/ci.yml triggers on every PR:
  1. go build ./...           — zero warnings, strict mode
  2. go vet ./...
  3. golangci-lint run        — .golangci.yml configured with exhaustive, errcheck, godot, gomnd
  4. go test ./... -race      — race detector enabled
  5. TestDHTKeyValidatorPersists        — separate required check; blocks merge if failing
  6. TestNoFloatArithmetic              — blocks merge if internal/payment/ contains any float type
  7. Migration apply + rollback         — against CI Postgres instance with btree_gist installed
  8. Grep fail: challenge_nonce BYTEA(32) in any file
  9. Grep fail: float64|float32|FLOAT|DECIMAL|NUMERIC in internal/payment/ context
  10. Grep fail: ADR-039 or any non-existent ADR reference
  11. Grep fail: UPI Collect API endpoint string
  12. Mermaid render check (all .md files in docs/system-design/)
  13. Hyperlink check (markdown-link-check)
  14. TestProfileShardSizeIsConstant
  15. TestProfileBothFullySpecified`
```

`.golangci.yml` mandatory linters: `gofmt`, `govet`, `errcheck` (every error handled or explicitly ignored with a comment), `exhaustive` (every switch on `AuditResult`, `ProviderStatus`, `EscrowEventType`, `RepairPriority` must handle all cases), `godot` (all exported doc comments end with a period), `gomnd` (catches magic numbers that should be `NetworkProfile` fields).

### 8.5 Infrastructure directory conventions

**`migrations/`** — see `data-model.md §9` for the complete naming convention and ordering requirements.

**`deployments/dev/docker-compose.yml`** — must include: one microservice replica, one Postgres instance with `btree_gist` pre-installed, one relay node (can be the microservice itself in relay mode for dev), and one provider daemon in `--sim-count=5 --sim-asn-count=5` mode. The dev compose file should reach a passing readiness gate automatically within ~10 seconds of startup.

**`runbooks/`** — these eight files must all exist before M8 (private beta) closes: `microservice-failover.md`, `postgres-failover.md`, `relay-node-replacement.md`, `secrets-manager-outage.md`, `razorpay-api-outage.md`, `provider-mass-departure.md`, `rbi-holiday-table-update.md`, `audit-secret-rotation.md`. Grafana alert runbook links point to these files by name.

**`scripts/benchmarks/`** — four benchmark scripts must produce a passing result on minimum-spec hardware (dual-core, no AES-NI, 2 GB RAM, 7200 RPM HDD) before any launch milestone closes: `aont_encode.sh` (Q16-1), `argon2id.sh` (Q18-1), `rocksdb_ssd.sh` (Q27-1 SSD variant), `rocksdb_hdd.sh` (Q27-1 HDD variant). These are the launch gates defined in `requirements.md §7.4`.

---

*Repository: https://github.com/masamasaowl/Vyomanaut_Research*  
*Authoritative companion: `docs/decisions/ADR-031-demo-mode-network-profile.md`*  
