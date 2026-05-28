# Vyomanaut V2 — System Architecture Document

**Version:** 1.0  
**Status:** Pre-Build phase.  
**Authors:** https://github.com/masamasaowl  
**Last updated:** May 2026  
**Repository:** https://github.com/masamasaowl/Vyomanaut_Research  
**Decisions index:** `docs/decisions/README.md`  
**Research index:** `docs/research/reading-list.md`

> **Note:** This document is not a build plan. It does not say what to build first or how long things take. It describes what the finished system looks like.

---

## Terminology and Glossary

| Term | Definition |
|---|---|
| **AONT** | All-or-Nothing Transform. An encryption scheme where the key K is embedded in the ciphertext and can only be recovered by assembling all codewords. Used before erasure coding so that possessing fewer than k=16 fragments reveals nothing. |
| **ASN** | Autonomous System Number. Identifies an ISP or network operator. The 20% ASN cap ensures no correlated provider group holds more than ~11 of 56 fragments of any file. |
| **BWavg** | Steady-state repair bandwidth per provider (Kbps). Computed via Giroire Formula 1. Target: <= 100 Kbps. At MTTF=300d, ~39 Kbps. |
| **Canary** | A fixed 16-byte value appended to plaintext before AONT encoding. Verified on decode to detect corruption. If the canary fails, the segment is corrupt. |
| **Chunk** | A 256 KB encrypted fragment. The atomic unit of storage, audit, and repair. Each file segment produces 56 chunks. |
| **Circuit Relay v2** | A libp2p protocol for routing traffic through an intermediary when direct connections are impossible (symmetric NAT). |
| **DCUtR** | Direct Connection Upgrade through Relay. A libp2p NAT hole-punching protocol with 97.6% first-attempt success rate for cone NAT. |
| **DHT** | Distributed Hash Table. Specifically, Kademlia. Used for chunk-address lookup on the data plane. Provider discovery goes through the microservice, not the DHT. |
| **Ed25519** | A digital signature scheme. Used for audit receipts (both provider and microservice sign), pointer file integrity, and peer identity. |
| **Escrow** | Funds held by the system on behalf of providers. Balance is computed, never stored: `SUM(DEPOSIT) - SUM(RELEASE + SEIZURE)`. Integer paise only. |
| **Giroire Formula** | A family of analytical formulas for computing data loss rate, repair bandwidth, and burst transfer volume in lazy-repair erasure-coded systems. From [Paper 10](../research/paper-10-giroire-lazy.md). |
| **HKDF** | HMAC-based Key Derivation Function (SHA-256). Derives file keys, pointer keys, and keystore keys from the master secret. |
| **JIT flag** | Just-In-Time retrieval detection. Set when a provider responds faster than `0.3x` the expected transfer time, suggesting they did not read from local disk. |
| **K (AONT key)** | A fresh 256-bit random key generated per segment. Embedded in the erasure-coded data via `c_{s+1} = K XOR SHA-256(all codewords)`. Never stored or transmitted separately. |
| **lf** | Fragment (chunk) size. Fixed at 256 KB (262,144 bytes) in V2. |
| **Master secret** | `Argon2id(passphrase, owner_id)`. The root of the data owner's key hierarchy. Never written to disk or transmitted. |
| **MTTF** | Mean Time To Failure. For V2 desktop providers: target 300 days, minimum acceptable 180 days. |
| **NetworkProfile** | The `internal/config.NetworkProfile` struct that contains all parameters differing between demo and production mode (erasure coding parameters, time windows, Argon2id cost, payment mode, infrastructure thresholds). Constructed once at startup from either `config.ProductionProfile` or `config.DemoProfile` based on `VYOMANAUT_MODE`. (ADR-031) |
| **Pointer file** | A per-file metadata structure containing provider IDs, chunk content addresses, and erasure parameters. Encrypted with AEAD_CHACHA20_POLY1305. Stored as ciphertext by the microservice (which cannot decrypt it). |
| **PN-counter CRDT** | A conflict-free replicated data type for counters that support both increment and decrement. The escrow ledger uses this pattern. |
| **Qpeek** | Burst repair bandwidth: total network transfer required when one provider fails. At N=1,000, 50 GB/provider: ~793 GB. |
| **r** | Number of parity fragments. Fixed at 40 in V2. Analytically optimal per Giroire Formula 4. |
| **r0** | Lazy repair trigger buffer. Fixed at 8. Repair fires when available fragments drop to s+r0=24, not at every loss. Reduces bandwidth by ~38x vs eager repair. |
| **RS(s, n)** | Reed-Solomon erasure code with s data shards and n total shards. V2: RS(16, 56). Systematic form: first 16 output shards are identity-mapped from the AONT package. |
| **RTO** | Retransmission Timeout. Per-provider: `AVG + 4 x VAR` of recent audit response latencies. New providers use pool median. |
| **s** | Number of data (reconstruction threshold) shards. Fixed at 16. |
| **Segment** | A 4 MB unit of file data (56 x 256 KB). Files larger than 4 MB are split into multiple segments, each processed independently. |
| **Silent departure** | A provider absent >= 72 hours without announcement. Triggers escrow seizure and immediate repair. |
| **Synthetic vetting chunk** | A random 256 KB block generated by the microservice and assigned to a vetting provider in place of a real file shard. Stored and audited identically to real chunks at the daemon level; flagged `is_vetting_chunk = TRUE` in `chunk_assignments`; never associated with a data owner file; discarded with zero repair cost if the vetting provider departs. (ADR-030) |
| **vLog** | The append-only Value Log in the WiscKey storage engine. Fixed-size entries of 262,212 bytes. All reads verify `SHA-256(chunk_data) == content_hash`. It serialises all appends through a single writer goroutine. Concurrent upload goroutines do not write to the vLog file handle directly. |
| **Vetting period** | 4-6 months after provider registration. 60-day escrow hold, 50% release cap. Ends after 80 consecutive audit passes. |
| **WiscKey** | Key-value separation architecture: small index in RocksDB, large values in an append-only log. Reduces write amplification from 10-14x to ~1.0 at 256 KB values. |

---

## Table of Contents

1. [What Vyomanaut Does](#1-what-vyomanaut-does)
2. [What V2 Does Not Do](#2-what-v2-does-not-do)
3. [Quality Attributes](#3-quality-attributes)
4. [Technology Stack](#4-technology-stack)
5. [System Context](#5-system-context)
6. [Component Overview](#6-component-overview)
7. [Trust Boundaries](#7-trust-boundaries)
8. [Minimum Viable Network](#8-minimum-viable-network)
9. [Core Design Principles](#9-core-design-principles)
10. [Data Encoding Pipeline](#10-data-encoding-pipeline)
11. [Key Hierarchy](#11-key-hierarchy)
12. [Provider Lifecycle](#12-provider-lifecycle)
13. [P2P Transfer Layer](#13-p2p-transfer-layer)
14. [Audit System](#14-audit-system)
15. [Repair System](#15-repair-system)
16. [Provider Storage Engine](#16-provider-storage-engine)
17. [Payment System](#17-payment-system)
18. [Coordination Microservice](#18-coordination-microservice)
19. [Reliability Scoring](#19-reliability-scoring)
20. [Adversarial Defences](#20-adversarial-defences)
21. [Consistency Model](#21-consistency-model)
22. [Error Handling](#22-error-handling)
23. [Observability](#23-observability)
24. [Deployment Topology](#24-deployment-topology)
25. [Accepted Trade-offs](#25-accepted-trade-offs)
26. [Known Limitations and V3 Scope](#26-known-limitations-and-v3-scope)
27. [Capacity planning](#27-capacity-planning)
28. [ADR Reference Index](#28-adr-reference-index)

---

## 1. What Vyomanaut Does

Vyomanaut is a paid distributed cold storage network. Data owners pay to store files. Storage providers — people running the Vyomanaut daemon on their home desktop or NAS — earn money by keeping those files available and passing regular storage verification challenges.

Three properties define the system:

**The service never sees the data.** Files are encrypted on the data owner's device before anything leaves it. Providers store encrypted pieces. The coordination microservice never touches file contents. No party in the middle can read anything stored.

**Data is split across 56 independent providers.** A file is broken into 56 encrypted fragments. Any 16 of those 56 are enough to reconstruct the original. A provider can go offline, depart, or fail — the file is unaffected as long as enough fragments survive.

**Payment is tied to proof, not trust.** Providers are paid only when they pass a daily audit: a cryptographic challenge where they prove they still hold the data they were assigned. The correct answer cannot be computed without the actual data. You cannot fake a pass.

---

## 2. What V2 Does Not Do

These are explicit design decisions, each with a documented reason. Building around them wastes time.

- **No mobile providers.** Home desktops and NAS devices only. Operating systems on phones kill background processes unpredictably. Mobile introduces repair bandwidth costs the network cannot absorb at the current erasure parameters. Mobile is explicitly deferred to V3. ([ADR-010](../decisions/ADR-010-desktop-only-v2.md))
- **No blockchain.** The three things blockchains provide in competing systems — an immutable audit log, automatic payment triggers on verified proof, and public dispute resolution — are each replicated by specific microservice components without on-chain writes.
- **No deduplication.** Every file upload uses a fresh random encryption key. Files are not deduplicated across data owners. This is a property of the AONT encoding design.
- **No file versioning or in-place updates.** Files are immutable once stored. Updating a file means deleting the old one and uploading a new one.
- **No retrieval market.** Payment is for proving storage presence, not for serving downloads. A provider earns the same whether or not their data is retrieved.
- **India-only at launch.** All payment processing uses Razorpay and UPI, which require an Indian bank account. The payment abstraction layer is designed to add international gateways later without rewriting the payment logic.

---

## 3. Quality Attributes

These are the non-functional requirements that drove architectural decisions. They reveal how research improved the project.

### Durability
**Target:** Data loss rate < 10⁻¹⁵ per year per file.  
**How achieved:** Reed-Solomon RS(16, 56) with lazy repair at r0=8. At the target MTTF of 300 days per provider, the Giroire formula gives an actual loss rate of approximately 10⁻²⁵ per year — ten orders of magnitude below the target. The 20% ASN cap is a co-requisite: without it, correlated failures invalidate the calculation. ([ADR-003](../decisions/ADR-003-erasure-coding.md), [ADR-014](../decisions/ADR-014-adversarial-defences.md))

### Availability
**Target:** File retrievable at any time as long as 16 of 56 fragment holders are reachable.  
**How achieved:** 40 parity fragments above the reconstruction threshold. A file is accessible even if 40 providers simultaneously fail. At the target MTTF, the probability of 40 simultaneous failures is negligible.

### Repair bandwidth
**Target:** ≤ 100 Kbps per provider of background upload bandwidth.  
**Actual:** ~39 Kbps per provider at MTTF = 300 days.  
**How achieved:** Lazy repair defers reconstruction until redundancy drops to r0=8 above the reconstruction floor. This produces approximately 38× lower bandwidth than reacting to every failure immediately. ([ADR-004](../decisions/ADR-004-repair-protocol.md))

### Audit response latency
**Target:** Each provider responds to an audit challenge within `(chunk_size / p95_measured_upload_speed) × 1.5`.  
**Typical value:** ~614 ms for a provider with 5 Mbps declared upload throughput.  
**Why it matters:** This deadline makes just-in-time retrieval attacks infeasible — a provider cannot fetch the data from somewhere else and respond in time. ([ADR-014](../decisions/ADR-014-adversarial-defences.md))

### Microservice availability
**Target:** No single microservice replica failure interrupts service.  
**How achieved:** Three replicas with a (3, 2, 2) quorum. One replica can fail; reads and writes continue with the remaining two. ([ADR-025](../decisions/ADR-025-microservice-consistency-mechanism.md))

### Background CPU usage
**Target:** ≤ 5% of CPU and background I/O from the data owner device and provider daemon.  
**How achieved:** WiscKey storage engine keeps write amplification at ~1.0 for 256 KB chunks for the Provider. And ChaCha20 AONT encoding completes in ~186 ms per 14 MB segment for Data Owners on hardware without AES acceleration. ([ADR-009](../decisions/ADR-009-background-execution.md), [ADR-023](../decisions/ADR-023-provider-storage-engine.md))

### Zero-knowledge storage
**Target:** The service and its operators must never be able to read any stored data.  
**How achieved:** All encryption happens on the data owner's device before upload. The microservice stores only the encrypted ciphertext of pointer files. Providers store only encrypted fragments. No party in the coordination or audit path holds a decryption key. ([ADR-019](../decisions/ADR-019-client-side-encryption.md), [ADR-022](../decisions/ADR-022-encryption-erasure-order.md))

---

## 4. Technology Stack

| Layer | Technology | Why |
|---|---|---|
| Microservice language | Go | Concurrent, low-overhead, good libp2p support |
| Provider daemon language | Go | Same binary can support future cross-platform builds |
| Microservice database | PostgreSQL | Append-only INSERT-only audit log; CRDT-compatible escrow ledger; row security policy enforcement |
| Provider storage engine | RocksDB (index) + custom append-only vLog (values) | WiscKey key-value separation; write amplification ~1.0 at 256 KB |
| P2P networking | libp2p | Production-deployed in IPFS and Filecoin; full NAT traversal stack; Kademlia DHT included |
| Primary transport | QUIC v1 (RFC 9000) | Connection migration survives IP changes; independent stream delivery; built-in TLS 1.3 |
| Fallback transport | TCP + Noise XX + yamux | Identical hole-punch success rate to QUIC; activates when UDP is blocked |
| Client-side encryption | ChaCha20-256 (no AES-NI) / AES-256-CTR (AES-NI) | ChaCha20 is constant-time without hardware; 3× faster than AES on low-end desktops |
| Authenticated encryption | AEAD_CHACHA20_POLY1305 | For pointer file encryption; RFC 8439 standard |
| Erasure coding | Reed-Solomon RS(16, 56) | Only MDS code family that supports arbitrary (n, k) at our parameters |
| Key derivation | HKDF-SHA256 and Argon2id | HKDF for operational key derivation; Argon2id for master secret from passphrase |
| Digital signatures | Ed25519 | Sub-millisecond key generation; used for audit receipts and pointer file integrity |
| Payment gateway | Razorpay (Route + Smart Collect 2.0 + RazorpayX Payouts) | India-first; UPI; no per-transaction fee for P2P transfers |
| Secrets management | HashiCorp Vault / AWS SSM / GCP Secret Manager | For cluster audit secret distribution across microservice replicas |
| Network Profile | `internal/config.NetworkProfile` | Single source of truth for all parameters that differ between demo and production mode. Constructed once at startup, passed via dependency injection to every subsystem. No subsystem reads `VYOMANAUT_MODE` directly. (ADR-031) |
| Repository layout | Single Go module (`github.com/masamasaowl/vyomanaut`) | Three binaries share `internal/crypto` and `internal/erasure`; split repos would duplicate security-critical code or require publishing internal packages as external modules, reintroducing version-skew across consumers |
| Build system | `go build ./cmd/<binary>` per binary; no custom build tags | Conditional cipher selection (ChaCha20 vs AES-CTR) is a runtime CPUID check, not a compile-time flag. No Makefile magic needed. |
| Dependency management | Module proxy + pinned `go.sum`; `go-libp2p` vendored on validator failure | `TestDHTKeyValidatorPersists` is the gate: if a `go-libp2p` upgrade resets the custom DHT key namespace, the dependency is vendored until the upstream fix lands. Vendoring decision is recorded in `repo-structure.md §1.3`. |
| CI enforcement | `golangci-lint` strict mode + four mandatory grep-fail checks | Four patterns fail the pipeline unconditionally: `challenge_nonce BYTEA(32)`, `float64\|\float32` in payment context, `ADR-039` (non-existent ADR reference), UPI Collect API calls |

---

### 4.1 Technology Rationale

The table above lists selections; this subsection records *why* each was chosen over the
alternatives considered, with a cited performance number, and a one-sentence lock-in risk
statement. Every choice traces to an ADR. Version pins marked `TBD` must be resolved before
the milestone noted.

---

#### Go ≥ 1.22

| Rejected alternative | Reason |
|---|---|
| Rust | goroutine model maps more naturally to the concurrent challenge-dispatch and single-writer vLog workload; existing team fluency |
| Python / JavaScript | Cannot satisfy NFR-009 (AONT encode ≤ 200 ms per 14 MB segment without AES-NI) or NFR-008 (audit lookup p99 ≤ 100 ms) |
| Java / JVM | GC pauses can cause audit RTO violations; JVM warm-up incompatible with daemon auto-start (ADR-009) |

**Performance contract.** `crypto/chacha20poly1305` achieves ≥ 75 MB/s on OMAP-class
hardware without AES-NI (RFC 8439 Table B.1), encoding a 14 MB segment in ≤ 186 ms.  
**Lock-in risk.** Low — well-defined HTTP and libp2p interfaces; language rewrite does not
require protocol or data-model changes.

---

#### PostgreSQL ≥ 15

`NULLS NOT DISTINCT` syntax (required in `chunk_assignments`) is only available from PG 15.

| Rejected alternative | Reason |
|---|---|
| MySQL / MariaDB | Row-level security policies (enforcing INSERT-only on `audit_receipts` and `escrow_events`) are PostgreSQL-specific; without them Invariants 1–2 cannot be enforced at the DB layer |
| CockroachDB | Operational complexity with no benefit at V2 scale; the 6 coordinated operations route through a single payment service, not distributed SQL |
| MongoDB | Append-only CRDT ledger requires reliable INTEGER arithmetic and UNIQUE constraint enforcement — mismatched to MongoDB's document model |
| SQLite | Insufficient concurrency for a multi-replica cluster issuing thousands of audit receipts per second at scale |

**Performance contract.** Postgres single-instance INSERT ceiling: approximately 5,000–10,000
rows/sec under standard workload. At V2 launch (hundreds of providers), the audit receipt
INSERT rate is tens of rows/sec — three orders of magnitude below the ceiling.
Monthly partitioning of `audit_receipts` is mandatory from day one to manage ~1.8 TB/year
growth at 56 providers × 50 GB/provider. See §28.4.  
**Lock-in risk.** Medium — row security policies, CRDT ledger, and `EXCLUDE USING GIST` encode
business logic at the DB layer; migration requires re-implementing these invariants at the
application layer.

---

#### RocksDB + custom vLog (WiscKey) — provider daemon

| Rejected alternative | Reason |
|---|---|
| Standard RocksDB (values in LSM) | Write amplification 10–14× at 256 KB values (Paper 26); storing 50 GB would write 500–700 GB to disk, breaching NFR-011 |
| Flat object store (one file per chunk) | No Bloom filter; audit TIMEOUT result for unassigned chunks requires disk I/O; no GC primitive |
| BoltDB / badger | Both store large values in the LSM tree; same write amplification problem |
| LevelDB | Same write amplification; no per-key Bloom filter config; no `fallocate(FALLOC_FL_PUNCH_HOLE)` in Go wrappers for GC |

**Performance contract:** WiscKey write amplification ≈ 1.0 at 256 KB (Paper 27 Figure 10),
satisfying NFR-013. Bloom filter (10 bits/key, ~1% FP rate) eliminates disk I/O for all
audit challenges on unassigned chunks. One random disk read per lookup: ~1 ms SSD,
~12–15 ms HDD — both within NFR-008 p99 thresholds.  
**Lock-in risk.** Medium — vLog format is documented in ADR-023; migrating the index library
requires translating crash-recovery tail-scan and GC logic.

**RocksDB CGo build note:**RocksDB (`linxGnu/grocksdb`) is a CGo dependency requiring a pre-built shared library. The CI pipeline uses a pinned Docker image with the correct `librocksdb` version pre-installed. The exact image tag and RocksDB version are specified in `.github/workflows/ci.yml` and must be updated together when the Go binding version changes. A mismatch produces a link-time failure, not a runtime failure — this is detectable in CI.

---

#### libp2p / go-libp2p

Version pin: TBD — pin before M3 closes. The custom DHT key validator
(`/vyomanaut/dht-key/1.0.0`) must survive every `go-libp2p` upgrade; `TestDHTKeyValidatorPersists`
is a CI required check (interface-contracts.md §12).

| Rejected alternative | Reason |
|---|---|
| Raw QUIC + custom peer discovery | NAT traversal, hole-punching, relay coordination, and cryptographic peer identity must be built from scratch; libp2p is production-proven at IPFS and Filecoin scale |
| gRPC over HTTP/2 | No connection migration; TCP head-of-line blocking; no built-in NAT traversal; DHCP lease rotation on Indian ISPs makes connection migration a first-class requirement |
| Custom DHT | S/Kademlia parameters (k=16, α=3, disjoint lookups) are already implemented in `go-libp2p-kad-dht`; re-implementing adds correctness risk with no gain |

**Performance contract.** DCUtR hole-punching achieves 70% success across 4.4 M traversal
attempts; 97.6% of successful connections succeed on the first attempt (Paper 30), justifying
`max_hole_punch_retries = 1`. IPFS post-v0.5 median DHT lookup latency: 622 ms (Paper 20).  
**Lock-in risk.** High — custom HMAC DHT key validator and three-tier NAT traversal are tightly
coupled to libp2p internals; replacement requires re-implementing NAT traversal and Kademlia
from scratch.

---

#### QUIC v1 (RFC 9000) — primary transport

| Rejected alternative | Reason |
|---|---|
| TCP as primary | Head-of-line blocking stalls all 56 parallel shard streams on a single lost packet; no connection migration when provider IP changes due to DHCP rotation |
| HTTP/3 framing on QUIC | Unnecessary header overhead; libp2p's binary chunk protocol is used directly over QUIC streams |

**Performance contract.** QUIC Connection ID migration allows in-flight 256 KB shard transfers
to survive DHCP lease rotation with zero application-layer retry. Independent stream delivery
eliminates HOL blocking across 56 parallel upload streams per segment.  
**Lock-in risk.** Low — IETF standard (RFC 9000); TCP fallback already provides an alternative.

---

#### TCP + Noise XX + yamux — fallback transport

| Rejected alternative | Reason |
|---|---|
| TLS over TCP | Noise XX provides identical cryptographic properties (mutual auth, forward secrecy) with simpler P2P semantics; libp2p has a production-hardened implementation |
| Plain TCP (no multiplexing) | 56 parallel streams would require 56 simultaneous TCP connections per segment upload |

**Performance contract.** TCP and QUIC achieve statistically identical NAT hole-punch success
rates (~70%) in production measurements (Paper 30). The fallback carries no reliability penalty.  
**Lock-in risk.** Low — TCP is universal; Noise XX and yamux are replaceable.

---

#### ChaCha20-256 (no AES-NI) / AES-256-CTR (AES-NI) — AONT internal cipher

Cipher is detected at daemon startup via CPUID and stored as a package-level constant; never
re-checked at runtime. Version pin for `golang.org/x/crypto`: TBD — pin before M1 closes.

| Rejected alternative | Reason |
|---|---|
| AES-only on all hardware | Software AES on no-AES-NI hardware: 24–42 MB/s vs ChaCha20's 75–131 MB/s; table-lookup AES has cache-timing vulnerability without hardware |
| RC4-128 + MD5 ("fast" AONT-RS config) | RC4 is cryptographically broken (RFC 7465, 2015); this is the explicitly insecure configuration in Paper 16; must never be adopted |
| Single cipher everywhere (ChaCha20 only) | AES-256-CTR on AES-NI hardware achieves ~900 MB/s vs ~75–131 MB/s; the CPUID check at startup costs nothing |

**Performance contract.** ChaCha20-256 at 75 MB/s (no-AES-NI, OMAP-class hardware, Paper 17
Table B.1) encodes a 14 MB segment in ≈ 186 ms — within NFR-009 p50 ≤ 200 ms target.
AES-256-CTR on AES-NI hardware achieves ≈ 900 MB/s (same segment in ≈ 16 ms).  
**Lock-in risk.** Low — both are IETF-standardised; any RFC 8439-compliant ChaCha20 implementation
produces identical keystreams.

---

#### AEAD_CHACHA20_POLY1305 (RFC 8439) — pointer file encryption

Version pin for `golang.org/x/crypto/chacha20poly1305`: TBD — pin before M1 closes.

| Rejected alternative | Reason |
|---|---|
| AES-256-GCM | 3–4× slower on no-AES-NI hardware; GHASH has known constant-time issues in some software implementations; Poly1305 is constant-time on all hardware with no hardware dependency |
| NaCl secretbox (XSalsa20-Poly1305) | RFC 8439 is the current standard; `golang.org/x/crypto/chacha20poly1305` is a direct standard library; NaCl adds indirection |

**Performance contract.** Poly1305 tag verification uses `crypto/subtle.ConstantTimeCompare`,
satisfying NFR-019. A tampered pointer file is rejected before any decryption attempt.  
**Lock-in risk.** Low — RFC 8439; nonce and AAD structure documented in ADR-019.

---

#### `klauspost/reedsolomon` — erasure coding

Version pin: TBD — pin before M1 closes. Must be vendored if API breaking changes are
detected in a minor release.

| Rejected alternative | Reason |
|---|---|
| Clay codes (MSR) | Sub-packetisation at (n=56, k=16): α ≥ 40^16 — computationally intractable (Paper 22, Q19-2); removed as V3 candidate |
| LRC (Azure-style) | Non-MDS; local group co-locality cannot be guaranteed in a consumer P2P network; repair benefit collapses to RS-level in the worst case |
| Custom GF(2^8) implementation | `klauspost/reedsolomon` is production-hardened with SIMD (AVX-512, AVX2, NEON); re-implementing GF arithmetic introduces correctness risk |
| Intel ISA-L via CGo | Additional CGo dependency; `klauspost/reedsolomon` achieves comparable throughput in pure Go with SIMD intrinsics |

**Performance contract.** `klauspost/reedsolomon` encodes n=56 shards of 256 KB well within
the 200 ms encoding budget. r=40 is the analytically optimal redundancy level per Giroire
Formula 4 (∂BWavg/∂r = 0 at r=40 for s=16, r0=8).  
**Lock-in risk.** Low — standard GF(2^8) RS; any compliant library implementing systematic
coding at (n=56, k=16) can replace it.

---

#### HKDF-SHA256 + Argon2id — key derivation

Version pin for `golang.org/x/crypto` (covers both hkdf and argon2): TBD — pin before M1
closes.

| Rejected alternative | Reason |
|---|---|
| PBKDF2 for operational keys | PBKDF2 is designed for password-based derivation (high cost); HKDF is the correct primitive for fast deterministic domain-separated derivation from an already-strong secret (RFC 5869) |
| Raw SHA-256 for key derivation | No domain separation; vulnerable to length-extension attacks; HKDF `info` parameter provides cryptographic domain separation between file keys and pointer keys |
| scrypt for master secret | Argon2id won the Password Hashing Competition (2015); OWASP recommends Argon2id over scrypt for new systems |
| bcrypt for master secret | 72-byte password input limit; insufficient for high-entropy passphrases |

**Performance contract.** Argon2id at t=3, m=64 MB, p=4 completes in ≈ 200 ms on
minimum-spec hardware (benchmarking protocol Q18-1; NFR-010 target: p50 ≤ 500 ms).
HKDF-SHA256 is computationally negligible (microseconds per derivation).  
**Lock-in risk.** Low — RFC 5869 (HKDF) and RFC 9106 (Argon2id); domain separation strings
documented in ADR-020.

---

#### Ed25519 — digital signatures (`crypto/ed25519`, stdlib)

| Rejected alternative | Reason |
|---|---|
| ECDSA (P-256) | Requires a random nonce per signature; nonce reuse is catastrophic (private key recovery); Ed25519 uses a deterministic nonce — no RNG in the signing path |
| RSA-2048/4096 | Signatures are 256–512 bytes vs Ed25519's 64 bytes; at millions of audit receipts per year the storage difference is significant; RSA key generation is orders of magnitude slower |
| BLS signatures | BLS aggregation complexity is not justified at V2 scale; Ed25519 is directly available in the Go standard library |

**Performance contract.** Ed25519 key generation, signing, and verification are all
sub-millisecond. Signature size (64 bytes) contributes 128 bytes per audit receipt row
(provider_sig + service_sig), negligible relative to the ~450-byte on-disk row size.  
**Lock-in risk.** Low — RFC 8032; keys and signatures are portable across implementations.

---

#### Razorpay (Route + Smart Collect 2.0 + RazorpayX)

Go SDK version: pin before M6 closes. All payment logic is isolated behind the
`PaymentProvider` interface (ADR-011) so the gateway can be replaced without rewriting
business logic.

| Rejected alternative | Reason |
|---|---|
| Stripe Connect | Per-transaction fee; settlement takes days vs UPI's seconds; requires Stripe account (friction for Indian providers); planned as future international-gateway implementation of `PaymentProvider` |
| Cryptocurrency | Price volatility; high onboarding friction; Swarm SWAP structural failure confirmed (Paper 07); bandwidth-as-currency is structurally incompatible with Vyomanaut's asymmetric model |
| Razorpay Escrow+ | Requires NBFC registration and trustee approval Vyomanaut does not qualify for; Route `on_hold_until` provides the equivalent primitive (Paper 35) |
| Manual bank transfers | No programmatic payout API; no idempotency support; cannot satisfy FR-047 or FR-048 |

**Mandatory compliance notes (non-optional).**
- UPI Collect deprecated by NPCI 28 Feb 2026 — all deposit flows must use UPI Intent or QR (NFR-029).
- `X-Payout-Idempotency` header mandatory since 15 Mar 2025 (NFR-030, ADR-012).
- RBI bank holiday table updated in every December deployment (NFR-031).

**Performance contract.** UPI has zero per-transaction merchant fee and settles in seconds.
Razorpay Route API rate limit: 500 requests/minute — sufficient for monthly payout computation
up to ~120,000 providers in a 4-hour release window.  
**Lock-in risk.** High — Smart Collect VPA assignment, Route transfer IDs, and `on_hold_until`
semantics are embedded in payment flow; `PaymentProvider` interface confines gateway-specific
code to one implementation; migration adds a new implementation plus data migration of Route
transfer IDs.

---

#### Secrets management — HashiCorp Vault / AWS SSM / GCP Secret Manager

Any of the three is acceptable, selected at deployment time based on cloud-provider choice.
All three support per-path versioning (`/vyomanaut/audit-secret/v{N}`) and IAM-gated access.
The microservice accesses via the `SecretsManagerClient` interface (interface-contracts.md §8).

| Rejected alternative | Reason |
|---|---|
| Environment variables | Not versioned; cannot model the 24-hour rotation overlap window (ADR-027 §4); visible in process listings |
| Per-replica local secret files | Cannot be updated across all three replicas atomically; no audit trail for access |
| `VYOMANAUT_CLUSTER_MASTER_SEED` env var | Permitted in development and simulation mode only; presence in production is a critical misconfiguration caught by the startup check |

**Performance contract.** Not on the hot path — microservice caches `server_secret_vN` in
memory with a 5-minute TTL. If the secrets manager is unreachable at replica startup the
replica fails to start (fail-closed, ADR-027).  
**Lock-in risk.** Low — `SecretsManagerClient` interface abstracts over all three providers;
switching requires ~50 lines of Go, not application logic changes.

---

#### Prometheus + Grafana — observability

Prometheus exporter and `prometheus/client_golang` versions: pin before M8 closes.

| Rejected alternative | Reason |
|---|---|
| Datadog / New Relic | Per-host per-month cost scales with provider count; SaaS APM agents cannot be installed on provider machines without violating the minimal-footprint requirement |
| OpenTelemetry only | Collection standard, not a storage backend; adds indirection at launch without gain |
| Logging only | Operational alerts (repair queue depth, TIMEOUT rate, content hash failures, replica count) require time-series data with threshold evaluation |

**Performance contract.** Pull-based scraping adds negligible overhead at V2 scale.
`repair_queue_depth` gauge is the primary early-warning signal for repair window exceedance.  
**Lock-in risk.** Low — Prometheus format is an open standard; migrating to Victoria Metrics
or Grafana Cloud requires re-pointing the scrape endpoint only.

---

### 4.2 Stack Lock-in Risk Summary

| Technology | Lock-in | Migration cost | Notes |
|---|---|---|---|
| Go | Low | Medium | Interfaces are stable; language rewrite does not require protocol or data-model changes |
| PostgreSQL | Medium | High | Row security policies and CRDT ledger encode business logic at DB layer |
| RocksDB + vLog | Medium | Medium | vLog format documented; GC and crash-recovery code is bounded in scope |
| libp2p | **High** | High | Custom HMAC DHT validator and three-tier NAT traversal tightly coupled to libp2p internals |
| QUIC v1 | Low | Low | IETF RFC 9000; TCP fallback path already exists |
| TCP + Noise XX + yamux | Low | Low | All three are individually replaceable |
| ChaCha20 / AES-256-CTR | Low | Low | RFC 8439 and NIST; identical output for same inputs and counter convention |
| AEAD_CHACHA20_POLY1305 | Low | Low | RFC 8439; nonce and AAD structure documented in ADR-019 |
| `klauspost/reedsolomon` | Low | Low | Standard GF(2^8) RS; any systematic-(n=56,k=16)-capable library is a drop-in |
| HKDF-SHA256 + Argon2id | Low | Low | RFC 5869 and RFC 9106; domain strings documented in ADR-020 |
| Ed25519 | Low | Low | RFC 8032; standard format portable across languages |
| Razorpay | **High** | Medium | `PaymentProvider` interface insulates logic; migration adds one implementation + Route transfer ID data migration |
| Secrets manager | Low | Low | `SecretsManagerClient` interface; switching is ~50 lines of Go |
| Prometheus + Grafana | Low | Low | Open standard; dashboards are configuration files, not application code |

---

## 5. System Context

Vyomanaut sits between data owners who need storage and providers who have idle disk space. The coordination microservice is the control plane. The P2P network is the data plane. These two planes are deliberately separate — the microservice is never in the path of actual file data.

```
External parties:
  DATA OWNER ──────────────────────────────────────────────── PROVIDER (×56+)
  (desktop app / web UI)                                      (daemon on home desktop / NAS)
       │                                                              │
       │ HTTPS: deposit, upload metadata, retrieve pointer            │ libp2p/QUIC: chunk upload/download; audit challenge + response
       │ QUIC: direct P2P chunk upload (to providers directly)        │ HTTPS: heartbeat (4-hour address update only)
       │                                                              │
       └────────────────── COORDINATION MICROSERVICE ────────────────┘
                           (control plane only — never data)
                                       │
                            ┌──────────┴──────────┐
                            │                     │
                       RAZORPAY               SECRETS MANAGER
                    (payment rails)         (cluster audit secret)
```

Provider side labels:
  libp2p/QUIC: chunk upload/download; audit challenge + response
  HTTPS:       heartbeat (4-hour address update only)
  
What the microservice knows: which chunk is on which provider, who passed their last audit, who should be paid, and the encrypted ciphertext of pointer files (which it cannot decrypt).

What the microservice never touches: file plaintext, AONT keys, decryption keys of any kind.

---

## 6. Component Overview

The system has six first-class components and three external dependencies.

**First-class components:**

| Component | Runs on | Primary responsibility |
|---|---|---|
| Coordination microservice | Cloud VMs (3 replicas) | Registration, audit scheduling, payment, repair orchestration |
| Provider daemon | Provider desktop / NAS | Chunk storage, audit response, heartbeat |
| Data owner client | Data owner's device | Encryption, encoding, upload, retrieval |
| Relay nodes | Cloud VMs (3 at launch) | NAT traversal fallback for symmetric-NAT providers |
| Kademlia DHT | Distributed across all provider daemons | Chunk-address lookup (data plane routing) |
| PostgreSQL cluster | Co-located with microservice | Audit log, escrow ledger, chunk assignment table, provider registry |

**External dependencies:**

| Dependency | Purpose | Failure impact |
|---|---|---|
| Razorpay Route + Smart Collect 2.0 | Provider payment releases and data owner deposits | Payment releases pause; audits and storage continue unaffected |
| Secrets manager | Cluster audit secret distribution | Microservice replicas cannot start; existing instances continue running with cached secret |
| Indian ISP infrastructure | Connectivity between providers and data owners | Covered by 40-fragment parity and 3-region relay deployment |

---

## 7. Trust Boundaries

This table defines what each component is and is not trusted to do. It determines where verification must happen rather than trust.

| Component | Trusted for | Not trusted for |
|---|---|---|
| Coordination microservice | Audit scheduling, payment computation, chunk assignment, audit result recording | Reading file contents; holding decryption keys; self-reporting its own correctness (V3 Merkle Log addresses this) |
| Provider daemon | Storing the chunk it was assigned and responding to challenges | Reporting its own reliability; self-certifying throughput; knowing what other providers hold |
| Data owner client | Encrypting correctly before upload; holding the pointer file | Providing correct audit results; knowing which providers hold which fragments |
| Relay nodes | Forwarding QUIC/TCP traffic for NAT traversal | Reading packet contents (all traffic is TLS 1.3 encrypted end-to-end) |
| Razorpay | Processing payment transfers per API calls | Computing payout amounts (microservice does this and sends exact paise amounts) |
| PostgreSQL | Enforcing INSERT-only policy on audit_receipts (row security policy) | Validating audit response correctness (microservice verifies before inserting) |

**Critical trust boundary: the AONT threshold.** Security depends on no single party holding k=16 or more fragments of the same file. The 20% ASN cap keeps any correlated group below 12 of 56 fragments. If the cap is not enforced at assignment time, the zero-knowledge property degrades.

---

## 8. Minimum Viable Network

The system refuses upload requests until all of the following conditions are simultaneously true. The assignment service re-evaluates every 60 seconds and exposes the state at `GET /api/v1/admin/readiness`. ([ADR-029](../decisions/ADR-029-bootstrap-minimum-viable-network.md))

| Condition | Threshold | Reason |
|---|---|---|
| Active vetted providers | ≥ 56 | RS(16, 56) requires exactly 56 distinct shard holders per file |
| Distinct ASNs in active pool | ≥ 5 | With fewer than 5 ASNs, one ASN necessarily holds > 20% — the cap is unenforceable |
| Distinct Indian metro regions | ≥ 3 | Geographic baseline: Delhi NCR, Mumbai, and one southern metro |
| Microservice cluster state | Full (3, 2, 2) quorum | Degraded quorum is operational but not a safe launch baseline |
| Razorpay Linked Accounts | ≥ 56 with 24h cooling complete | No provider can receive payment until cooling passes |
| Relay infrastructure | ≥ 3 relay nodes deployed | Required for symmetric-NAT providers (~30% of expected population) |
| Cluster audit secret | Loaded on all replicas | All replicas must share the secret before any challenge is issued |

Upload requests return HTTP 503 ("Network not ready") until all conditions are met.

**Mode-variable thresholds.** All numeric thresholds in the readiness gate (≥ 56 providers, ≥ 5 ASNs, ≥ 3 regions, ≥ 3 relays, etc.) are read from the active `NetworkProfile` rather than being hardcoded. In `VYOMANAUT_MODE=demo` the thresholds drop to the demo values specified in ADR-031. The gate logic is identical in both modes; only the threshold values differ.

**Capacity at the exact floor (56 providers).**
The assignment service requires exactly 56 distinct providers per file segment. At N=56,
only one file segment can be in-flight at a time — a second upload must queue until the first
completes at the provider level. Simultaneous upload concurrency is `floor(N / 56)` and scales
linearly: at N=112, 2 simultaneous segments; at N=560, up to 10.

**The minimum storable file is one complete segment:** 4 MB of plaintext (16 × 256 KB systematic
shards), producing 14 MB of wire data (56 × 256 KB). Files larger than 4 MB require multiple
independent segments.

**Usable network storage at the floor (50 GB declared per provider, 28.57% efficiency):**
~800 GB of data accessible to owners; ~2.8 TB of raw provider capacity. The burst repair
window at N=56 is approximately 22 hours — exceeding the 12-hour safety target. Operators
must monitor `repair_queue_depth` and manually verify no repair job is outstanding before
accepting any new provider departure within a 24-hour window until N grows past ~500. See §28.

**Scale validation at 1,000,000 providers (analytical):** The Giroire BWavg formula scales as D/N (total data / provider count). For fixed per-provider storage, BWavg stays at ~39 Kbps regardless of provider count. LossRate is similarly scale-invariant for fixed D/N. The erasure parameters are valid at any scale with consistent D/N ratios.

---

## 9. Core Design Principles

These eight principles governed every architectural decision. When a new engineering choice comes up during the build, check it against these before deciding.

**Lazy everything.** Work is deferred until necessary. Repair fires only when fragment count drops to a threshold, not immediately on every departure. This single decision reduces repair bandwidth by ~38× compared to eager repair. ([ADR-004](../decisions/ADR-004-repair-protocol.md))

**Prove, don't trust.** No component self-reports its own state. Storage is verified by cryptographic challenge-response. Throughput is measured during audits, not taken from declarations. The microservice issues all challenges; providers cannot influence when they are tested. ([ADR-002](../decisions/ADR-002-proof-of-storage.md))

**Coordinate only where necessary.** 14 of 20 core operations scale horizontally without any coordination. Only 6 require a single authoritative source. The system is designed to push as many operations as possible into the coordination-free category. ([ADR-013](../decisions/ADR-013-consistency-model.md))

**Bound correlated failures structurally.** When providers share an ISP, city, or power grid, they fail together. The system prevents this from causing data loss by ensuring no single correlated group holds more than 20% of any file's fragments at assignment time. This cap is both a security requirement and a durability requirement. ([ADR-014](../decisions/ADR-014-adversarial-defences.md))

**Pay for presence, not transfer.** Providers earn per audit passed, not per gigabyte transferred. This keeps the payment system and the P2P transfer layer independent — a payment outage cannot accumulate credit liability, and a transfer outage does not stop audits. ([ADR-012](../decisions/ADR-012-payment-basis.md))

**Fail closed on cryptographic operations.** If the cluster audit secret cannot be loaded, a microservice replica does not start. If a chunk's content hash fails verification at read time, the audit result is FAIL — never a wrong hash. Unknown is always worse than a known failure.

**Data plane and control plane are separate.** File data never flows through the microservice. The microservice knows about data (which chunk is where, who holds it) but never holds the data itself. This separation is not a performance optimisation — it is the mechanism by which zero-knowledge storage holds even if the microservice is compromised.

**Profile-driven configuration.** All parameters that differ between a live demo and production (erasure coding parameters, time windows, infrastructure thresholds, Argon2id cost, payment mode) live exclusively in the `NetworkProfile` struct (ADR-031). Business logic never branches on the mode string directly. Switching from demo to production is a change to the active profile instance and the addition of three infrastructure dependencies (secrets manager, Razorpay live, relay nodes) — no logic changes, no Go function modifications, no schema changes beyond two parameterised CHECK constraint values.

The following parameters are **not** in `NetworkProfile` because they must be identical in both modes: `ShardSize` (262,144 bytes — a compile-time constant), the 33-byte challenge nonce length, all cipher identities, Poly1305 constant-time tag comparison, row security policies on `audit_receipts` and `escrow_events`, and the single-writer vLog goroutine requirement.

---

## 9.5 Security Boundary Summary

The table below answers "can Vyomanaut staff read my files?" for every plausible attack path. It is the single reference for security reasoning during code review.

| Threat | What limits it | ADR |
|---|---|---|
| Operator or microservice reads stored files | The AONT key K is never transmitted to or stored by the microservice. K is recoverable only by assembling k=16 fragments from providers. The microservice holds only encrypted pointer file ciphertext it cannot decrypt. | [ADR-019](../decisions/ADR-019-client-side-encryption.md), [ADR-022](../decisions/ADR-022-encryption-erasure-order.md) |
| Compromised provider reads files it does not hold | A provider holds one of 56 fragments. Decrypting any word requires K. K requires all s+1 AONT codewords. A single fragment reveals nothing. | [ADR-022](../decisions/ADR-022-encryption-erasure-order.md) |
| Compromised provider reads files it does hold (k=16 threshold breach) | The 20% ASN cap ensures no correlated group holds more than ~11 of 56 fragments. Collusion at or below 11 providers cannot reach the k=16 threshold. | [ADR-014](../decisions/ADR-014-adversarial-defences.md) |
| DHT observer correlates lookup traffic to file identity | DHT lookup keys are `HMAC-SHA256(chunk_hash, file_owner_key)`. The file_owner_key is derived from the data owner's master secret. No observer can map a DHT key to a file without the owner's credentials. | [ADR-001](../decisions/ADR-001-coordination-architecture.md) |
| Replay of a valid audit response from a previous challenge | The challenge nonce is `HMAC(server_secret, chunk_id + server_ts)`. A nonce is used exactly once (unique index on `challenge_nonce` in `audit_receipts`). Replaying an old nonce collides with the unique constraint. | [ADR-017](../decisions/ADR-017-audit-receipt-schema.md), [ADR-027](../decisions/ADR-027-cluster-audit-secret.md) |
| Silent disk corruption producing a wrong but accepted audit response | The provider daemon verifies `SHA-256(chunk_data) == content_hash` from the vLog entry before computing any response. If verification fails, the result is `FAIL` — a wrong hash is never returned to the microservice. | [ADR-023](../decisions/ADR-023-provider-storage-engine.md) |
| Provider outsources data retrieval just before a challenge | The audit deadline `(chunk_size / p95_throughput) × 1.5` is shorter than the round-trip time to fetch 256 KB from another provider over a residential connection. JIT retrieval is physically infeasible within the window. | [ADR-014](../decisions/ADR-014-adversarial-defences.md) |
| Cluster secret leaks and nonces become predictable | Nonces include a version byte; rotation retires old secrets after 24 hours. The master seed lives only in the secrets manager, never on disk in any replica. | [ADR-027](../decisions/ADR-027-cluster-audit-secret.md) |

---

## 10. Data Encoding Pipeline

Before any data leaves the data owner's device, it passes through a four-stage pipeline that transforms it into 56 encrypted, independent fragments.

### Segmentation

Each plaintext segment is at most 4 MB (16 data shards × 256 KB). After AONT encoding and RS coding, each segment produces 56 × 256 KB = 14 MB of encoded fragments. Every segment is processed independently through Stages 1–4 below. The pointer file contains one entry per segment, including the provider list and chunk IDs for that segment's 56 fragments. Retrieval reconstructs each segment independently and concatenates them in order to rebuild the original file. There is no cross-segment state — losing all 56 fragments of one segment does not affect any other segment's recoverability.

### Stage 1 — AONT encryption

A fresh random 256-bit key K is generated for the segment. The AONT (All-or-Nothing Transform) processes the segment as follows:

1. Append a fixed-value 16-byte canary word to the segment.
2. Generate K = SecureRandom(256 bits).
3. For each 16-byte word d_i in the segment: 
      - ChaCha20 path:  `c_i = d_i XOR ChaCha20_keystream_word(K, block=⌊i/16⌋, offset=i%16)`
      - AES-CTR path:  `c_i = d_i XOR AES-256-ECB(K, i+1)    // counter starts at i+1 per AONT-RS spec.`
4. Compute the commitment hash: `h = SHA-256(c_0 || c_1 || ... || c_s)`.
5. Append the key-embedding block: `c_{s+1} = K XOR h`.

The security property: recovering K requires computing h, which requires all s+1 codewords. An attacker with fewer than k=16 fragments cannot assemble the full package, cannot compute h, cannot recover K, and cannot decrypt any word. The key is embedded in the data and protected by the erasure threshold. ([ADR-022](../decisions/ADR-022-encryption-erasure-order.md))

The cipher is ChaCha20-256 on hardware without AES-NI (the common case for Indian desktops), and AES-256-CTR on hardware with AES-NI. The daemon detects this at startup via CPUID and sets a global constant. Both paths produce identical outputs from the AONT's perspective — only the performance differs. ([ADR-019](../decisions/ADR-019-client-side-encryption.md))

### Stage 2 — Reed-Solomon erasure coding

The AONT package feeds into systematic Reed-Solomon coding with parameters s=16, r=40, producing 56 fragments of 256 KB each. The first 16 fragments are the direct AONT package words (the systematic property). The remaining 40 are parity.

The parameters come from the Giroire optimality condition: r=40 is the unique value that minimises repair bandwidth at s=16, r0=8. Choosing any other r increases bandwidth. ([ADR-003](../decisions/ADR-003-erasure-coding.md))

### Stage 3 — Upload and pointer file creation

The 56 fragments upload directly to 56 providers selected by the microservice's assignment service. On completion, the data owner's client creates the pointer file containing the 56 provider IDs, 56 chunk content addresses (SHA-256 of each fragment), and the erasure parameters. The pointer file is encrypted with AEAD_CHACHA20_POLY1305 before storage. The microservice stores three fields from the AEAD operation: the ciphertext body (pointer_ciphertext BYTEA), the 96-bit counter nonce (pointer_nonce BYTEA(12)), and the 16-byte Poly1305 authentication tag (pointer_tag BYTEA(16)). These map directly to the files table schema in ADR-020. The microservice cannot decrypt the ciphertext — it holds no key ([ADR-020](../decisions/ADR-020-key-management.md), [ADR-022](../decisions/ADR-022-encryption-erasure-order.md))

### Stage 4 - Decoding

To retrieve a file, the data owner contacts any 16 of the 56 providers, downloads their fragments, runs Reed-Solomon decode to recover the AONT package, recovers K from the package (h is computable from the full package, K = c_{s+1} XOR h), decrypts each word, and verifies the canary. If the canary is wrong, the segment is corrupt — escalate to the audit subsystem.

---

## 11. Key Hierarchy

The data owner's credentials derive from a single master secret that is never written to disk.

```
passphrase + owner_id
      │
      ▼  Argon2id (t=3, m=64 MB, p=4)
  master_secret  ─────────────────────────────── BIP-39 mnemonic (offline backup)
      │
      ├─ HKDF("vyomanaut-file-v1" || file_id)       ──► file_key  (per file, for future use)
      │
      ├─ HKDF("vyomanaut-pointer-v1" || file_id)    ──► pointer file encryption key
      │                                                       │
      │                                             AEAD_CHACHA20_POLY1305 encrypt
      │                                                       │
      │                                             encrypted pointer file
      │                                             (stored by microservice, unreadable to it)
      │
      └─ HKDF("vyomanaut-keystore-v1")              ──► key store encryption key
                                                            │
                                                    encrypts: Ed25519 signing key
                                                              + pointer file nonce counter
```

The AONT key K is not in this hierarchy. It is embedded in the erasure-coded fragments and recovered automatically when k=16 fragments are assembled. The data owner never manages K directly.

Recovery paths:
- **Device loss, passphrase known:** Re-derive master_secret; download and decrypt pointer file ciphertext from microservice. Full recovery.
- **Device loss, no passphrase, BIP-39 mnemonic available:** Reconstruct master_secret from mnemonic. Same as above.
- **All credentials lost:** Permanent, unrecoverable data loss. This must be clearly disclosed at onboarding.

([ADR-020](../decisions/ADR-020-key-management.md))

---

## 12. Provider Lifecycle

### Joining

**Registration.** The provider installs the daemon, which generates an Ed25519 key pair on first run. The provider registers via the app with their phone number (OTP-verified) and public key. The microservice creates a provider record (`status = PENDING_VERIFICATION`) and initiates a Razorpay Route Linked Account creation. A 24-hour cooling period must pass before the first payment transfer. ([ADR-001](../decisions/ADR-001-coordination-architecture.md))

**First heartbeat.** Once running, the daemon sends a signed heartbeat to the microservice control plane every 4 hours, reporting the provider's current libp2p multiaddresses. Indian residential ISPs frequently rotate DHCP leases — the heartbeat ensures the microservice always has a fresh address for audit challenge dispatch. Status advances to `VETTING`. ([ADR-028](../decisions/ADR-028-provider-heartbeat.md))

**Vetting period (4–6 months).** The provider receives **synthetic vetting chunks** — random 256 KB blocks generated by the microservice — rather than real file shards. These chunks are stored and audited through the identical daemon code paths as production chunks; the provider daemon cannot distinguish them. The microservice caps synthetic assignment at 10% of the provider's `declared_storage_gb` (roughly `declared_storage_gb × 400` chunks). Earnings accumulate under a 60-day hold window with a 50% release cap. Synthetic chunks are never associated with any data owner file; if the vetting provider departs, no repair job is enqueued and the dummy data is discarded. This eliminates the ~793 GB burst repair transfer per departure that would otherwise burden the network during its smallest and most fragile phase. After 80 consecutive audit passes AND 120 days since first chunk assignment, the provider advances to `ACTIVE`. One drawback: the approach does not test the real data-owner-to-provider chunk upload path during vetting. The first real shard upload a provider handles is post-ACTIVE. It is acceptable because daily audit challenges test the same retrieval mechanics.(ADR-030, ADR-005, ADR-024)

**Full operation.** On the ACTIVE transition the microservice sends a GC instruction (via the `/vyomanaut/vetting-gc/1.0.0` libp2p protocol) listing all synthetic chunk IDs held by that provider. The daemon deletes these from the vLog; the assignment service marks their `chunk_assignments` rows `PENDING_DELETION`. Real shard assignments begin flowing immediately — GC runs in parallel. The hold window shortens to 30 days; the release cap is removed. The provider competes for real chunk assignments alongside all other ACTIVE providers. (ADR-030, ADR-023)

### During operation

The provider daemon continuously:
- Sends a heartbeat every 4 hours to the microservice with its current network addresses
- Receives audit challenge messages daily for each stored chunk
- Responds to challenges within the per-provider deadline
- Receives new chunk assignments via the assignment service
- Stores new chunks in the WiscKey engine on local disk
- Accumulates earnings in the microservice's internal escrow ledger

### Exiting

Four exit states with distinct triggers and financial consequences. ([ADR-007](../decisions/ADR-007-provider-exit-states.md))

| State | Condition | Repair triggered | Escrow outcome |
|---|---|---|---|
| Temporary absence | Absent < 72 h, no notice | No | Score decremented per polling cycle |
| Promised downtime | Absence declared in advance | No — wait for promised period | Fine deducted if promise broken |
| Silent departure (VETTING)| Absent ≥ 72 h, no announcement, `status = VETTING` | No — all assignments are synthetic vetting chunks; no repair jobs enqueued; chunks discarded | All held escrow seized; provider marked `DEPARTED` |
| Silent departure (ACTIVE) | Absent ≥ 72 h, no announcement, `status = ACTIVE` | Immediately | All held escrow seized for repair fund |
| Announced departure | Provider explicitly notified | Immediately (real chunks only; synthetic chunks discarded) | Held escrow released proportionally |

The 72-hour threshold is derived from Bolosky's measured desktop absence distribution: 99.7% of weekend absences resolve within 70 hours. The threshold is set above the peak normal absence to avoid false-positive repair triggers.

**Vetting departure path.** When a provider with `status = VETTING` crosses the 72-hour departure threshold, the departure handler marks the provider `DEPARTED` and seizes escrow identically to an ACTIVE silent departure. The difference is in the repair scheduler: the handler queries `chunk_assignments WHERE provider_id = $1 AND is_vetting_chunk = FALSE` and finds zero rows (vetting providers only hold synthetic chunks). No repair jobs are enqueued. The synthetic chunk rows are soft-deleted (`status = 'DELETED'`, `deleted_at = NOW()`). The total repair bandwidth cost of a vetting departure is zero. (ADR-030)

**Returning after silent departure.** When a provider is declared silently departed, the microservice sets `status = DEPARTED`, removes all their chunk assignments from the assignment table (stopping further challenge issuance), seizes their escrow, and queues repair for all affected chunks. If the provider later comes back online, the microservice returns HTTP 403 on all requests. Re-joining requires a full registration with a new provider_id. Prior chunk data on disk is not re-integrated — it has already been replaced by repair.

---

## 13. P2P Transfer Layer

File data moves directly between data owner and providers, and between providers during repair. The microservice is never in this path. ([ADR-021](../decisions/ADR-021-p2p-transfer-protocol.md))

### Framework

libp2p handles peer identity, connection establishment, NAT traversal, stream multiplexing, and the Kademlia DHT. It is the same stack used in production by IPFS and Filecoin.

### Transports

**Primary: QUIC v1 (RFC 9000).** Each chunk transfer is one independent QUIC stream on an existing connection. A lost packet on one stream does not stall others. QUIC connections are identified by a Connection ID rather than an IP:port pair — when a provider's IP changes due to DHCP rotation, in-flight transfers survive automatically. TLS 1.3 is built into QUIC; no separate security handshake is needed.

**Fallback: TCP + Noise XX + yamux.** Activates automatically when a provider is behind a UDP-blocking middlebox. Empirically, TCP and QUIC achieve statistically identical NAT hole-punch success rates (~70%) — the fallback carries no reliability penalty. yamux provides the same independent stream semantics as QUIC over the TCP connection.

### NAT traversal — three tiers

| Tier | Protocol | Applies to | Notes |
|---|---|---|---|
| 1 | AutoNAT | All providers at connect | Classifies: publicly reachable / cone NAT (punchable) / symmetric NAT (relay required) |
| 2 | DCUtR (Direct Connection Upgrade through Relay) | Cone NAT — common on Indian home routers | Relay coordinates simultaneous dial from both sides. 97.6% of successes happen on the first attempt. Retry count set to 1 (not the libp2p default of 3). |
| 3 | Circuit Relay v2 | Symmetric NAT — approximately 30% of providers | All traffic routed through a Vyomanaut-operated relay. Relay overhead < 50 ms for Indian cloud-hosted relays; fits within audit deadline. |

**Relay infrastructure at launch:** Three relay nodes, one per Indian cloud availability zone. Each handles 128 concurrent relay reservations — 384 total slots, 4.3× headroom at 300 initial providers. ([Q30-1, answered](../research/answered-questions.md))

### Peer identity

Each provider daemon generates an Ed25519 key pair at installation. The libp2p Peer ID is `multihash(public_key)`. Every transport connection authenticates the remote Peer ID cryptographically during the handshake — providers cannot impersonate each other.

### DHT configuration

The Kademlia DHT handles chunk-address lookup (finding which provider holds which fragment). It does not handle provider discovery — that goes through the microservice.

| Parameter | Value | Reason |
|---|---|---|
| k-bucket size | 16 | S/Kademlia disjoint-path design: k = 2×d where d=8 |
| Parallel lookups (alpha) | 3 | Produces O(log n / 3) round trips; sub-second median lookup |
| DHT mode for providers | Server (full participant) | Desktop providers must be reachable for challenge dispatch |
| Key namespace | Custom HMAC validator | Prevents file identity leakage in DHT |

DHT lookup keys use: `HMAC-SHA256(chunk_hash, file_owner_key)` where `file_owner_key = HKDF(master_secret, "vyomanaut-dht-v1", file_id)`. Only the file owner can reverse-map a DHT key to its chunk. The DHT never sees real chunk hashes or file IDs.

DHT records are republished by provider daemons at every NetworkProfile.DHTRepublishInterval (12 hours in production, 2 minutes in demo). The daemon's heartbeat goroutine triggers `PutProviderRecord` for all active chunk assignments. `The dht_key` for each chunk is cached locally in the provider's RocksDB instance alongside the vlog_offset so republication does not require the data owner to be online. See interface-contracts.md §12 for the complete republication contract. ([ADR-006](../decisions/ADR-006-polling-interval.md))

### Session resumption policy

0-RTT session resumption (zero round-trip reconnect) is disabled for all connections carrying an audit challenge or signed receipt. 0-RTT data can be replayed by an attacker — unacceptable for operations with payment or audit consequences. 0-RTT may be enabled for pure chunk data transfers where replay has no security consequence.

---

## 14. Audit System

The audit system is how the microservice verifies that providers hold the chunks they are supposed to hold.

### Challenge issuance

Once per day per assigned chunk, the microservice issues an audit challenge containing the chunk ID and a nonce:

```
challenge_nonce = HMAC-SHA256(server_secret, chunk_id + server_timestamp)
```

`server_timestamp` is generated by the microservice — providers cannot influence it. The nonce is versioned (1 prefix byte) so any replica can validate any challenge across replica boundaries. ([ADR-027](../decisions/ADR-027-cluster-audit-secret.md))

The microservice dispatches challenges using the address from `providers.last_known_multiaddrs` (updated by the 4-hour heartbeat), not from the DHT. DHT is used only as a fallback if no heartbeat has been received within 8 hours.

The per-challenge response timeout uses a TCP-style per-provider RTO:

```
RTO = AVG + 4 × VAR
```

where AVG and VAR are the exponentially weighted mean and variance of that provider's recent audit response times. New providers use the pool median. This distinguishes a slow provider (high variance, wait longer) from one that has departed (consistently unresponsive). ([ADR-006](../decisions/ADR-006-polling-interval.md))

### Provider response

The provider receives the challenge and:
1. Looks up the chunk_id in RocksDB. If absent, returns FAIL immediately (no disk I/O needed).
2. Reads the chunk from the vLog using the stored offset.
3. Verifies `SHA-256(chunk_data) == stored_content_hash`. If wrong, returns FAIL — disk corruption detected.
4. Computes `response_hash = SHA-256(chunk_data || challenge_nonce)`.
5. Returns the signed audit receipt.

### Receipt recording

The microservice verifies the provider's Ed25519 signature over the receipt fields, then writes one row to `audit_receipts`. The table is INSERT-only — no row is ever updated or deleted, enforced by Postgres row security policy. But (audit_result, service_sig, service_countersign_ts) fields are treated as an exeption. Both the provider and microservice sign the receipt with Ed25519. ([ADR-017](../decisions/ADR-017-audit-receipt-schema.md), [ADR-015](../decisions/ADR-015-audit-trail.md))

>**NOTE:** The correctness of response_hash — that it was computed over the actual 256 KB chunk and not fabricated — is guaranteed by the computational hardness of SHA-256 preimage inversion, not by independent microservice verification. Please note the microservice never verifies the response_hash, it knows the chunk_id but not the 256 KB chunk data making the SHA-256(chunk_data || challenge_nonce) unverifiable. It only verifies the Ed25519 signature.

### The Audit receipt schema

| Field | Purpose |
|---|---|
| receipt_id (UUIDv7) | Time-ordered primary key; no coordinator needed |
| schema_version | Forward compatibility |
| chunk_id | Content address of the chunk |
| file_id | Pseudonymous file handle |
| provider_id | Which provider responded |
| challenge_nonce | BYTEA(33) — 1-byte version prefix OR HMAC-SHA256(server_secret_vN, chunk_id + server_ts). |
| server_challenge_ts | Set by server; prevents backdating |
| response_hash | The proof: SHA-256(chunk_data ‖ nonce) |
| response_latency_ms | Just-in-time retrieval detector |
| audit_result | PASS / FAIL / TIMEOUT |
| provider_sig | Ed25519 over all fields above |
| service_sig | Ed25519 over provider_sig + countersign timestamp |
| service_countersign_ts | Timestamp of microservice countersignature |
| jit_flag | Set when response_latency_ms < (chunk_size / p95_throughput) × 0.3 — JIT anomaly |
| abandoned_at | Set by GC for PENDING rows older than 48h; not counted in any score window |

### Crash-safe receipt writing

The microservice uses a two-phase write to survive crashes between challenge validation and receipt countersignature:
1. Write a PENDING row (audit_result = NULL) with the full provider_sig. This is durable before any further processing.
2. Validate the response_hash.
3. Update the row: set audit_result = PASS|FAIL, service_sig, service_countersign_ts.
4. Return the countersignature to the provider.

>**Note:** The UPDATE from PENDING to PASS/FAIL/TIMEOUT is the only permitted mutation on audit_receipts. The Postgres row security policy must explicitly carve out this single transition (WHERE audit_result IS NULL) while blocking all other UPDATE and DELETE operations. See ADR-015.

If the microservice crashes between step 1 and step 3: the provider receives no countersignature. On the next audit cycle (24 hours), a new challenge is issued. The orphaned PENDING row is garbage-collected after 48 hours (marked ABANDONED, not counted in any score window). If the provider retries the same response before the next cycle, the microservice detects the duplicate challenge_nonce and returns the existing receipt.

---

## 15. Repair System

### When repair fires

Repair fires in two situations: a provider is declared silently departed (absent ≥ 72 hours), or the fragment count for a chunk drops to s + r0 = 24 (the lazy repair threshold, 8 above the reconstruction floor of 16).

The 72-hour threshold is calibrated to Bolosky's bimodal desktop absence distribution: nightly absences peak at µ=14h (99.7% return within 20h), weekend absences peak at µ=64h (99.7% return within 70h). 72 hours safely clears the weekend peak without triggering false-positive repair. ([ADR-006](../decisions/ADR-006-polling-interval.md))

**Vetting chunk exclusion.** Before enqueueing any repair job, the repair scheduler checks `chunk_assignments.is_vetting_chunk`. Rows where `is_vetting_chunk = TRUE` are unconditionally excluded from repair job creation. A vetting provider's departure produces zero repair jobs regardless of how many synthetic chunks they held. The `EnqueueJob` function in `internal/repair`enforces this as a pre-condition: callers must not pass a chunk_id where `is_vetting_chunk = TRUE`. (ADR-030)

### What repair does

The repair scheduler contacts k=16 surviving fragment holders, downloads their fragments, runs Reed-Solomon decode to reconstruct the AONT package, re-encodes the missing fragments, and uploads them to newly selected providers. New providers are selected with the same ASN diversity constraints as original assignments.

The scheduler prioritises confirmed permanent departures (providers past the 72-hour threshold) over pre-warning jobs (chunks approaching r0 but not yet there). This prevents permanently degraded chunks from accumulating behind lower-priority work. ([ADR-004](../decisions/ADR-004-repair-protocol.md))

### Bandwidth budget

For N=1,000 providers, a single provider failure produces approximately 793 GB of total repair network transfer. At 100 Kbps per provider (aggregate 100 Mbps), repair completes in about 8 hours — inside the 12-hour safety window before a second failure becomes likely. The steady-state per-provider repair bandwidth is ~39 Kbps, well inside the 100 Kbps background budget. ([ADR-003](../decisions/ADR-003-erasure-coding.md))

### Emergency floor

Regardless of the 72-hour threshold, if fragment count for any chunk drops to s=16 (the reconstruction floor), repair triggers immediately. No file ever sits at the minimum threshold waiting for a timeout to expire.

---

## 16. Provider Storage Engine

Each provider daemon stores chunks using WiscKey-style key-value separation: the chunk index lives in RocksDB; chunk data lives in a separate append-only value log (vLog). ([ADR-023](../decisions/ADR-023-provider-storage-engine.md))

The vLog and RocksDB index are identical for synthetic vetting chunks and real production shards. The provider daemon stores and retrieves both using the same `AppendChunk` and `LookupChunk` code paths. The `is_vetting_chunk` flag exists only in the microservice's `chunk_assignments` table; the daemon has no visibility into this distinction. This is intentional: the vetting period must test authentic daemon behaviour under production-identical storage mechanics.

### Why not standard RocksDB

Standard RocksDB stores keys and values together and rewrites both during compaction. At 256 KB value sizes, this produces 10–14× write amplification. With WiscKey separation, compaction moves only the small 44-byte index entries — values never move. Write amplification drops to approximately 1.0 at 256 KB.

### The chunk index (RocksDB)

Maps `chunk_id (32 bytes) → (vlog_offset uint64, chunk_size uint32) (12 bytes)`. Total entry size: 44 bytes. The index is small enough to stay entirely in RocksDB's block cache after warm-up — audit challenge lookups typically require no disk I/O for the index.

Bloom filters are enabled (10 bits per key, ~1% false-positive rate). An audit challenge for a chunk not assigned to this provider hits only the Bloom filter in memory and returns FAIL instantly.

### The value log (vLog)

A single append-only file on the provider's storage device. Fixed-size entries of 262,212 bytes each:

```
chunk_id        (32 bytes)  — copy for GC validation
chunk_size      (4 bytes)   — always 262,144 in V2
chunk_data      (262,144 bytes) — raw 256 KB fragment
content_hash    (32 bytes)  — SHA-256(chunk_data), verified on every read
```

The `content_hash` is verified on every read before computing the audit response. If disk corruption has altered the chunk, the verification fails and the audit result is FAIL — the microservice is informed, and repair is triggered. Silent disk corruption cannot produce a wrong-but-plausible PoR response.

### Single writer goroutine

All vLog appends are serialised through one writer goroutine. Multiple upload goroutines submit write requests via a buffered channel and block until the writer confirms the vlog_offset. POSIX O_APPEND atomicity does not hold for writes above ~4 KB; for 262 KB entries, serialisation is required.

### Crash recovery

On restart, the daemon reads the last vLog head pointer from RocksDB and scans forward, re-inserting any index entries that were appended to the vLog but not yet flushed to RocksDB. Maximum scan: one memtable flush interval worth of entries, typically a few hundred chunks.

### Audit lookup path

1. Receive `(chunk_id, challenge_nonce)` from the microservice.
2. Check Bloom filter — absent → return FAIL (no disk I/O).
3. Read `(vlog_offset, chunk_size)` from RocksDB block cache (no disk I/O).
4. Read 262,212 bytes from vLog at offset — one random read: ~1 ms SSD / ~12–15 ms HDD.
5. Verify `SHA-256(chunk_data) == content_hash`. Fail immediately on mismatch.
6. Compute `response_hash = SHA-256(chunk_data || challenge_nonce)`.
7. Return signed audit receipt.

Both SSD and HDD times are well inside the audit deadline.

---

## 17. Payment System

### How Razorpay is used

Three Razorpay products, each serving a distinct purpose:

**Smart Collect 2.0** — each data owner is assigned a virtual UPI ID (Customer Identifier). When they deposit funds, Razorpay automates reconciliation and notifies the microservice via webhook. The microservice credits the data owner's escrow account.

**Razorpay Route** — each provider is a Linked Account. After a 24-hour cooling period from account creation, the microservice can create transfers with `on_hold: true`. The Modify Settlement Hold API updates `on_hold_until` on each monthly computation, controlling when earnings are released.

**RazorpayX Payouts** — monthly payment transfers from the master account to providers' registered bank accounts via IMPS or UPI.

Note: **Razorpay Escrow+ is not used.** It requires NBFC registration and a trustee arrangement. Vyomanaut does not qualify. The Route `on_hold` mechanism provides the required partial-hold-and-release behaviour without regulatory preconditions.

**UPI Collect is deprecated as of February 2026.** All data owner deposit flows must use UPI Intent (payer selects their UPI app from a displayed list) or QR code. The Smart Collect 2.0 receiving side is unaffected.

**Idempotency is mandatory.** Every payout API call includes the `X-Payout-Idempotency` header, whose value is `SHA-256(provider_id + audit_period)` taken directly from the `idempotency_key` column in `escrow_events`. Razorpay rejects payout calls without this header as of March 2025.

### Internal escrow ledger

The microservice maintains an append-only `escrow_events` table. Balance is never stored as a column — it is always computed:

```sql
balance =
  SUM(amount_paise) WHERE event_type = 'DEPOSIT'
  - SUM(amount_paise) WHERE event_type IN ('RELEASE', 'SEIZURE')
  for a given provider_id
```

All amounts are stored as integer paise (₹1 = 100 paise). No floating-point arithmetic anywhere in the payment path. ([ADR-016](../decisions/ADR-016-payment-db-schema.md))

### Monthly release computation

On the 23rd of each month, the microservice computes each provider's releasable balance. Razorpay releases on the next business day after `on_hold_until` — the target window for providers is "within the first 3 business days of the following month." The microservice sets ``on_hold_until` to the last working day of the current month (computed from a static rbi_bank_holidays_YYYY table updated each December), so that Razorpay's release on the next business day after that timestamp lands within the first three business days of the following month

The release multiplier is determined by the provider's 30-day reliability score:

| 30-day score | Release multiplier |
|---|---|
| ≥ 0.95 | 1.00 — full release |
| 0.80–0.94 | 0.75 — partial release |
| 0.65–0.79 | 0.50 — partial release |
| < 0.65 | 0.00 — hold in full |

Withheld portions from partial releases are not seized. They roll into the next month's escrow window. Seizure only occurs on silent departure.

If the 7-day score drops more than 0.20 below the 30-day score (the deterioration signal), the next release uses the lower multiplier of the two score windows — even before the 72-hour departure threshold is crossed. This catches providers who are degrading before they disappear. ([ADR-024](../decisions/ADR-024-economic-mechanism.md))

### Escrow seizure on departure

When a provider is declared silently departed (72 hours without contact):
1. The microservice freezes the provider's account.
2. All earnings in the 30-day rolling window are transferred to the repair reserve fund (SEIZURE event appended to `escrow_events`).
3. A Razorpay Route reversal is issued if any transfer has not yet settled.
4. Repair is triggered for all affected chunks. The seized escrow funds the cost of onboarding replacement providers.

### Vetting period economics

During the 4–6 month vetting period, the hold window is 60 days and the release cap is 50%. A provider in vetting can earn but can access at most 50% of any month's earnings until they pass vetting. This acts as a temporal entry cost without requiring a financial pre-commitment.

---

## 18. Coordination Microservice

### What it does

The microservice is the system's control plane. It handles: provider registration and KYC verification, chunk assignment, audit challenge scheduling and dispatch, audit receipt recording, reliability score maintenance, repair job queuing, payment release computation, DHT record republication, and heartbeat address updates.

It never touches file contents.

### Cluster configuration

Three replicas with a (3, 2, 2) quorum: reads require 2 replicas to respond; writes require 2 replicas to acknowledge. One replica can fail without interrupting service. ([ADR-025](../decisions/ADR-025-microservice-consistency-mechanism.md))

**Gossip membership:** each replica contacts one randomly chosen peer per second to reconcile membership histories. Two seed node addresses (stable, pre-configured) prevent the cluster from partitioning on restart.

**Client-driven routing:** for latency-sensitive paths (audit challenge dispatch, chunk assignment decisions), the service client caches cluster membership and routes directly to the responsible replica, bypassing the load balancer. This reduces 99.9th-percentile latency by 30+ ms compared to load-balancer indirection.

**Background task throttling:** background work (view refresh, repair queuing, Merkle log compaction) monitors the 99th-percentile of foreground DB read latency over the last 60 seconds. If foreground latency approaches 50 ms, background task allocation is reduced. This keeps audit challenge SLAs intact under maintenance load.

### Key API endpoints

| Endpoint | Method | Purpose |
|---|---|---|
| `/api/v1/provider/register` | POST | Provider registration; initiates Razorpay Linked Account creation |
| `/api/v1/provider/heartbeat` | POST | 4-hourly signed address update |
| `/api/v1/upload/assign` | POST | Returns 56 provider assignments for a new file segment |
| `/api/v1/audit/challenge` | POST | Dispatch audit challenge to a provider |
| `/api/v1/audit/receipt` | POST | Record audit receipt returned by a provider |
| `/api/v1/admin/readiness` | GET | Returns network readiness gate status |
| `/api/v1/admin/repair/queue` | GET | Repair queue depth and status |

### The cluster audit secret

Every challenge nonce is `HMAC-SHA256(server_secret, chunk_id + server_ts)`. The `server_secret` must be identical across all three replicas so that any replica can validate any challenge, including during a failover.

The secret is derived from a cluster master seed stored in the secrets manager:

```
server_secret_v{N} = HKDF-SHA256(
  ikm  = cluster_master_seed,
  salt = cluster_id,
  info = "vyomanaut-audit-secret-v" + N,
  len  = 32
)
```

Nonces are prefixed with a 1-byte version so the correct secret version is always identifiable. On rotation, the previous version is accepted for 24 hours (one full audit cycle) before being retired. ([ADR-027](../decisions/ADR-027-cluster-audit-secret.md))

---

## 19. Reliability Scoring

Each provider has a reliability score computed from three rolling audit windows: last 24 hours (highest weight), last 7 days, and last 30 days. The score is a weighted average audit pass rate across the three windows. ([ADR-008](../decisions/ADR-008-reliability-scoring.md))

The score drives two outcomes:

**Assignment priority.** The assignment service uses Power of Two Choices: two provider candidates are drawn randomly from the vetted pool, and the higher-scored one receives the assignment. No provider is guaranteed all assignments; any vetted provider has non-zero selection probability. This prevents one provider from accumulating all new chunks (Matthew effect).

**Payment release.** The 30-day score determines the monthly release multiplier. The 7-day score triggers the deterioration signal if it drops more than 0.20 below the 30-day score.

**Scoring must be centralised.** If providers rated each other instead of being rated by the microservice, the game-theoretically dominant strategy is universal dishonesty — every provider rates every other as passing regardless of actual behaviour. The Shelby proof (Research Paper 37) formally establishes this. The microservice as sole auditor is not a convenience; it is the only architecture that produces honest outcomes.

**Bootstrapping.** New providers start with an optimistic neutral score (equivalent to BitTorrent's optimistic unchoking) and accumulate audit history through the vetting period.

---

## 20. Adversarial Defences

Five classes of adversarial provider behaviour are explicitly defended against. ([ADR-014](../decisions/ADR-014-adversarial-defences.md))

### Correlated shard placement (Honest Geppetto)

**Attack:** A group of colluding providers accumulates months of genuine trust, then deletes all their data simultaneously.

**Defence:** At assignment time, no single ASN may hold more than 20% of any file's fragments (~11 of 56). Even if the entire correlated group vanishes simultaneously, 45 fragments survive — 29 above the reconstruction threshold of 16. This cap is both a security requirement and a durability requirement independently.

### Outsourcing

**Attack:** A provider does not store the data but retrieves it just-in-time from another provider during a challenge.

**Defence:** The audit deadline is `(chunk_size / p95_measured_upload_throughput) × 1.5`. The p95 throughput is measured empirically during vetting and updated from audit responses — a provider cannot extend their deadline by self-declaring a low speed. Fetching 256 KB from another provider over the internet takes longer than the deadline allows.
A lower floor also applies: if response_latency_ms < `(chunk_size / p95_throughput_kbps) × 0.3`, the response is anomalously fast, suggesting JIT retrieval from a co-located source. The microservice sets jit_flag = true on the receipt. Three or more JIT flags from the same provider within a rolling 7-day window triggers a 0.5× weight penalty on that provider's audit passes in the 24h scoring window for 30 days. Three JIT flags with identical response_latency_ms within ±5ms escalates to the manual review queue as a collusion signal.

### Just-in-time retrieval

**Attack:** A provider pre-caches data only when a challenge is imminent by monitoring the challenge schedule.

**Defence:** Challenge timing is randomised. The nonce is `HMAC-SHA256(server_secret, chunk_id + server_ts)` where `server_ts` is generated by the microservice at challenge time. Providers cannot predict when the next challenge arrives.

### False audit responses

**Attack:** A provider sends a plausible-looking response hash without holding the data.

**Defence:** The correct response is `SHA-256(chunk_data || challenge_nonce)`. Without the actual 256 KB chunk data, this cannot be computed correctly. The microservice independently has the expected hash and verifies it.

### Service denial

**Attack:** A provider stores the data, passes all audits, but refuses to serve retrieval requests from data owners.

**Defence:** RS(16, 56) requires more than 40 of 56 providers to simultaneously refuse retrieval before a data owner is blocked. The 20% ASN cap limits any single correlated group to ~11 providers — categorically insufficient for an effective denial. At the individual provider level, the reliability scorer should be extended to track data owner retrieval failure reports (see Q41-1 in `open-questions.md`).

---

## 21. Consistency Model

Of approximately 20 core database operations in the system, 14 are coordination-free (any replica can execute them without talking to the others) and 6 require a single authoritative executor. ([ADR-013](../decisions/ADR-013-consistency-model.md))

**Coordination-free (14 operations):** Insert audit receipt (pass or fail), increment reliability score on pass, insert provider registration (UUIDv7), insert file record, insert chunk record, all reads, increment escrow balance on deposit, soft-delete provider (status flag), trigger repair, record repair completion, issue audit challenge, validate capability token read, record repair scheduling.

**Coordinated (6 operations) — all go through the single payment microservice:**

| Operation | Why it cannot be distributed |
|---|---|
| Decrement reliability score on audit fail | Score floor ≥ 0 cannot be maintained across independent replicas |
| Decrement escrow balance on payment release | Balance floor ≥ 0 (paise) |
| Seize escrow on departure | Same floor invariant |
| Assign chunk to provider (new file upload) | Uniqueness: no two providers assigned the same slot |
| Validate capability token | Token expiry is time-dependent and cannot be checked by a replica with a stale view |

>**NOTE:** Physical row deletion from the providers table is unconditionally prohibited (ADR-013). There is no code path that executes it. status = DEPARTED is the only legal exit from the provider table. This constraint is enforced by the Postgres row security policy.

Any new operation added to the codebase must be evaluated against this framework before choosing an implementation path. The five-step protocol: (1) state the invariant, (2) ask the merge question, (3) check Bailis Table 2, (4) scope coordination to minimum, (5) document in PR.

---

## 22. Error Handling

### Audit challenge timeout

If a provider does not respond within the per-provider RTO, the microservice records `audit_result = TIMEOUT`. The reliability score decrements. The challenge is not retried until the next audit cycle (24 hours). Three consecutive TIMEOUT results in the 24-hour window do not trigger departure — only the 72-hour absence threshold does.

### Disk corruption detected by provider

If `SHA-256(chunk_data) ≠ content_hash` at read time, the provider returns `audit_result = FAIL` with a corruption-specific error code in the receipt. The microservice queues an accelerated re-audit of all chunks on that provider (the Schroeder finding: a provider with one uncorrectable error has 30× elevated probability of further errors). If multiple chunks fail within a 7-day window, the provider is treated as in rapid decline and their chunks are re-replicated on priority.

### Microservice replica failure

The (3, 2, 2) quorum absorbs single-replica failures transparently. Reads and writes continue with the two healthy replicas. Gossip membership detects the failure within seconds. The failed replica's re-integration is driven by anti-entropy: on return, it reconciles its state with gossip peers and receives any missing data via the availability service.

### Razorpay API failure

Payout failures result in a `reversed` payout state. Razorpay automatically refunds the amount to the master account. The microservice handles the `payout.reversed` webhook by leaving the provider's escrow_events row in an ATTEMPTED state and retrying on the next monthly cycle. No escrow is lost; the idempotency key prevents double-payment on retry.

### Secrets manager unavailable at startup

A microservice replica that cannot reach the secrets manager cannot load `server_secret` and will not start. This is a fail-closed design: unverifiable challenges are worse than no challenges. Existing running replicas continue serving with their cached (5-minute TTL) secret value.

### vLog write failure

If the vLog `fsync()` fails during a chunk store operation, the write is considered failed. The provider returns an error to the upload client. The upload client retries to a different provider. The incomplete vLog entry (if any) will be detected and skipped during crash recovery by validating the content_hash of the partially-written entry.

### DHT record expiry

If the availability service fails to republish a provider's DHT record within the 24-hour expiry window (12-hour republish interval, 12-hour buffer), the provider's chunk addresses expire from the DHT. Retrieval falls back to the microservice's `chunk_assignments` table. The DHT is the fast path; the microservice is the authoritative source.

---

## 23. Observability

### Key metrics (microservice)

| Metric | What it indicates |
|---|---|
| `audit_challenges_issued_total` | Challenge throughput |
| `audit_results_total{result="PASS|FAIL|TIMEOUT"}` | Network reliability |
| `provider_score_histogram` | Distribution of reliability scores |
| `repair_queue_depth` | Repair backlog |
| `repair_jobs_completed_total` | Repair throughput |
| `escrow_events_total{type="DEPOSIT|RELEASE|SEIZURE"}` | Payment volume |
| `microservice_replica_count{state="healthy"}` | Quorum health |
| `db_read_p99_latency_ms` | Foreground latency (triggers background throttle at 50 ms) |

### Key metrics (provider daemon)

| Metric | What it indicates |
|---|---|
| `chunks_stored_total` | Stored chunk count |
| `audit_responses_sent_total` | Challenge volume |
| `audit_response_latency_ms_p99` | Response time distribution |
| `vlog_append_latency_ms_p99` | Write performance |
| `content_hash_failures_total` | Silent disk corruption events |
| `heartbeat_sent_total` | Connectivity to microservice |

### Operational alerts

| Alert | Threshold | Action |
|---|---|---|
| Repair queue depth | > 1,000 jobs | Investigate departure rate; check bandwidth budget |
| TIMEOUT rate | > 5% of challenges | Check relay infrastructure; check heartbeat address freshness |
| Content hash failures | > 0 on any provider in 7-day window | Trigger accelerated re-audit of all that provider's chunks |
| Microservice healthy replicas | < 3 | Investigate replica failure |
| Release multiplier 0.00 rate | > 10% of active providers | Investigate systemic score degradation |

---

## 24. Deployment Topology

**Mode selection.** The deployment topology described in this section is the production topology. In `VYOMANAUT_MODE=demo`, the entire system runs on a single machine or a local area network: five provider daemon instances (physical laptops or `--sim-count=5` on one machine), one microservice replica with quorum checks disabled, no relay nodes, and a mock payment provider. The binary is identical; only the `NetworkProfile` values differ. See ADR-031 for the complete demo topology specification.

**Cloud provider.** AWS or GCP — operator's choice at deployment time. Both are acceptable. The architecture has no dependency on cloud-provider-specific features; managed Postgres (RDS or Cloud SQL) and standard VM instances are the only cloud primitives used. The remainder of this section gives AWS names in parentheses as a concrete reference; substitute GCP equivalents if deploying there.

**Coordination microservice.** Three VM instances (e.g. AWS EC2 `t3.medium` or equivalent: **2 vCPU, 4 GB RAM**), one per availability zone in `ap-south-1` (Mumbai). Each runs the microservice binary. They share a managed Postgres primary in the same region with two read replicas across AZs. Gossip membership connects all three replicas directly. External administrative traffic routes through a load balancer; audit challenge dispatch and assignment calls bypass the load balancer via client-driven direct routing.

**Postgres.** Managed Postgres with Multi-AZ replication enabled (e.g. AWS RDS `db.t3.medium`: **2 vCPU, 4 GB RAM**, 100 GB GP3 SSD to start, auto-scaling enabled). The microservice writes to the primary; read-heavy paths (score queries, assignment lookups) use a read replica. Monthly partition archival of `audit_receipts` is mandatory — partitions older than 90 days move to object storage (S3 / GCS). This preserves the full 30-day scoring window plus the 60-day dual-window analysis horizon and supports the 90-day provider cohort telemetry required by Q08-1 and Q29-1.

**Relay nodes.** Three instances (**1 vCPU, 1 GB RAM, minimum 1 Gbps network**), one per availability zone. At launch: Mumbai AZ1, Mumbai AZ2, Chennai/Hyderabad. Each runs libp2p with Circuit Relay v2 enabled, sized for 128 concurrent relay reservations — 384 total slots, 4.3× headroom at 300 initial providers. Scale to a fourth node when provider count exceeds 570 (the relay saturation point at 45% CGNAT fraction).

**Provider daemon.** Runs on provider hardware — home desktop or NAS. Cross-platform binary for Windows 10+, macOS 12+, and Ubuntu 22.04+. No cloud dependency. Connects outbound to the microservice via HTTPS and to other providers via libp2p.

**Data owner client.** Desktop app or web application. Runs the encoding pipeline locally (ChaCha20 AONT + Reed-Solomon) before any data leaves the device.

**Secrets manager.** A managed secrets service (HashiCorp Vault, AWS SSM Parameter Store, or GCP Secret Manager) accessible by all three microservice replicas. Stores the cluster master seed under versioned paths (`/vyomanaut/audit-secret/v{N}`). Not in the hot path — loaded at replica startup and cached for 5 minutes. If the secrets manager is unreachable at startup, the replica does not start (fail-closed).

---

## 25. Accepted Trade-offs

These trade-offs are settled. Every entry records what was chosen, what was rejected, what was gained, and what was consciously accepted as a cost. To revisit one, open a new ADR and cite this section. Do not quietly build around a trade-off you dislike.

Every entry follows the pattern: **We chose X over Y. We gain A. We accept B.** The gain is the reason the decision exists. The acceptance is the cost that must be managed, not eliminated.

### Storage and Durability

**Reed-Solomon RS(16,56) over three-way replication.** We gain 2.5× storage overhead instead of 3×, and approximately 38× less aggregate repair bandwidth under steady-state lazy repair (Blake & Rodrigues). We accept that reconstruction requires contacting k=16 fragment holders and that the lazy window leaves a file at reduced redundancy between the trigger and repair completion.

**Wide stripe n=56 over standard n≤20.** We gain 40 parity fragments of fault tolerance — the file survives simultaneous loss of any 40 of 56 providers. We accept that MSR regenerating codes are computationally intractable at this stripe width, leaving RS as the only feasible code family, and that the ASN cap is a non-optional co-requisite (Nath et al., Paper 38 — large-m erasure schemes can be strictly worse than simpler schemes under unbounded correlated failures).

**r=40 as the fixed redundancy level.** We gain the unique r that minimises repair bandwidth at s=16, r0=8 per Giroire Formula 4 — any other r increases bandwidth. We accept 3.5× storage overhead (56/16). This is not an engineering preference; it is a mathematical saddle point.

**The 20% ASN cap as both a security and a durability constraint.** We gain two independent guarantees from one mechanism: the Honest Geppetto adversarial attack is bounded at ~11 shards per correlated group, and real-world correlated failure amplification (Nath et al.) is structurally prevented. We accept that the assignment service must track ASN per provider and enforce the cap at write time.

**WiscKey key-value separation over standard RocksDB.** We gain write amplification of approximately 1.0 at 256 KB values (vs 10–14× in standard RocksDB). We accept the single-writer vLog goroutine requirement, vLog GC on chunk deletion, and a crash recovery tail-scan at startup.

### Network and Peer Discovery

**Hybrid microservice + Kademlia over pure DHT.** We gain admission control, reliable audit scheduling, and a stable authority for payment computation. Pure Kademlia DHT discovery fails operationally below ~100 peers and is vulnerable to Eclipse attacks without an admission gate. Storj v3.1 removed their DHT entirely — we retain it for data-plane chunk lookup only. We accept that the microservice is a dependency and that its failure halts new uploads and pauses audit scheduling, though the data plane continues.

**Centralised audit scoring over distributed reputation (EigenTrust, PeerTrust).** We gain correctness: the Shelby proof (Paper 37, Proposition 1) formally demonstrates that peer-to-peer audit reports without a trusted backstop collapse to universal dishonesty as the unique Nash equilibrium. Centralising the scorer is the only architecture that produces honest outcomes. We accept that this is a non-I-confluent operation that cannot be distributed.

**(3,2,2) microservice quorum over a single-node microservice.** We gain single-replica fault tolerance. We accept gossip membership overhead and the operational complexity of a three-node cluster.

**libp2p + QUIC over raw QUIC or gRPC.** We gain production-proven three-tier NAT traversal (AutoNAT → DCUtR → Circuit Relay v2), connection migration, independent stream delivery, and Kademlia DHT — tested at IPFS and Filecoin scale. We accept a significant external dependency on the libp2p release cycle and the need to maintain a custom DHT key validator across upgrades.

**Circuit Relay v2 fallback for symmetric NAT over excluding symmetric-NAT providers.** We gain access to approximately 30% of Indian home desktop operators behind CGNAT. We accept ~50 ms relay overhead per audit interaction and the operational cost of maintaining at minimum 3 relay nodes.

**HMAC-pseudonymised DHT keys over plaintext content IDs.** We gain DHT privacy: a monitoring node cannot correlate lookup requests with file identity without the owner's master secret (closes DSN Challenge 3 from the SoK survey, Paper 07). We accept a custom Kademlia key validator that must survive every `go-libp2p` upgrade — `TestDHTKeyValidatorPersists` is a CI required check precisely because a silent namespace reset would break all chunk lookups.

**Provider heartbeat every 4 hours over relying on DHT republication.** We gain reliable audit challenge delivery despite DHCP lease rotations. Indian residential ISPs commonly rotate addresses on 24-hour cycles; the DHT record can be stale by up to 12 hours. We accept that providers accumulate stale-address TIMEOUT audit results if they shut their machine down without the daemon running.

### Encryption and Key Management

**AONT-RS over encrypt-then-code or code-then-encrypt.** We gain elimination of external key management: AONT key K is embedded in the erasure-coded data and recoverable only when k=16 shards are assembled. No key server, no per-file AES key to store or rotate. We accept that the pointer file is the sole retrieval credential — its loss combined with loss of the master secret means permanent, unrecoverable data loss. This must be disclosed clearly at onboarding.

**ChaCha20-256 as the default AONT cipher over AES-256.** We gain a constant-time cipher requiring no hardware acceleration — on Indian desktops without AES-NI, ChaCha20 is 3× faster than software AES and free of cache-timing vulnerabilities. We accept two code paths in the daemon and CPUID detection at startup.

**Master-secret-derived key hierarchy over per-file random keys.** We gain a single backup surface: one passphrase recovers all files on any device. We accept that the master secret is a single point of failure — loss of both the passphrase and the mnemonic means permanent loss of all files with no support path.

**0-RTT disabled for audit interactions over enabling it for all reconnects.** We gain protection against replay attacks on audit responses. We accept one additional round trip per audit reconnect after a provider's nightly absence.

### Payment and Incentives

**Held-earnings escrow over pre-committed collateral.** We gain zero entry barrier — any Indian home desktop owner with a UPI-linked bank account can join without staking capital. We accept that new providers have limited escrow at risk in the first few days. The vetting period (60-day hold, 50% release cap) partially compensates.

**Payment per audit passed over payment per GB stored or per GB transferred.** We gain decoupling of the payment layer from the P2P transfer layer — a microservice outage accumulates no credit liability, and transfer failures do not interrupt audit scoring. We eliminate the delete-and-restore attack and the bandwidth-as-currency failure mode that destroyed Swarm's SWAP protocol. We accept that providers earn equally for storing popular data and data never retrieved.

**Fiat escrow via Razorpay over cryptocurrency.** We gain zero crypto-wallet friction for Indian providers and data owners, instant UPI settlement, and zero per-transaction merchant fee. We accept India-only operation at launch. The `PaymentProvider` interface is designed so that Stripe Connect or another international gateway can be added without rewriting payment logic.

**Razorpay Route `on_hold` over Razorpay Escrow+.** We gain full programmatic hold/release/seizure via standard REST APIs with no NBFC registration required. We accept that Route is a settlement hold on a payment transfer, not a legally regulated tri-party escrow account. All escrow logic lives in the microservice's internal ledger. The legal exposure of this distinction is a product-level open question.

**Deterministic pricing over auction pricing.** We gain predictability for both providers and data owners — contracted cold storage with month-long SLAs is incompatible with fluctuating pricing. We accept that the storage rate may diverge from market rate over time and must be revisited empirically.

**Non-transferable escrow over transferable earnings.** We gain deterrence against identity-cycling attacks. We accept this must be enforced as a hard rule at the payment service level with no exceptions.

### Proof of Storage and Auditing

**PoR Merkle challenges over PoRep + PoSt.** We gain compatibility with commodity desktop hardware — Filecoin's PoRep requires 256 GB RAM and a GPU with at least 11 GB VRAM, eliminating every provider in Vyomanaut's target demographic. We accept a weaker cryptographic guarantee. Three co-requisite mitigations close the gaps: registration gating (Sybil), response deadline (outsourcing), and randomised challenge timing (JIT retrieval).

**Timing-based outsourcing prevention over cryptographic sealing.** We gain a mechanism that works on any hardware. We accept a weaker guarantee — the deadline is timing-based, not cryptographically enforced. The 20% ASN cap limits co-located provider attacks.

**INSERT-only audit receipts over mutable audit records.** We gain tamper-evidence at the database row security policy level, not merely in application code. Both provider and microservice sign every receipt. We accept monotonically growing table size requiring periodic archival.

**V2 signed receipt exchange over immediate public audit verifiability.** We gain operational simplicity at launch. We accept that data owners must trust the microservice's countersignature in V2. The V3 Transparent Merkle Log is the explicit upgrade path, not an afterthought.

### Operational Constraints

**72-hour departure threshold over shorter or longer windows.** We gain safety against false-positive repair triggers from normal weekend absences — Bolosky's bimodal distribution (Paper 09) shows 99.7% of weekend absences resolve within 70 hours; 72 hours is the first safe value above this peak. We accept up to 72 hours of reduced redundancy before a permanently departed provider's chunks begin repair.

**24-hour polling interval over shorter or adaptive intervals.** We gain approximately 30× bandwidth savings over an instant timeout (Blake & Rodrigues). Daily challenges are sufficient for a write-once cold storage workload. We accept that a provider can delete their chunks and go undetected for up to 24 hours.

**Desktop-only providers in V2 over including mobile.** We gain a single parameter set that works for all V2 providers. Mobile at MTTF ~30–90 days pushes per-provider BWavg to ~130 Kbps, exceeding the 100 Kbps budget and burdening every desktop provider. We accept a smaller launch-day provider pool.

**Minimum viable network gate over allowing early uploads.** We gain the guarantee that no file is ever stored with insufficient redundancy from the moment the network opens. A file stored with only 30 providers fails RS(16,56) — an impossible state to recover from cleanly. We accept that launch day is gated on provider recruitment.

### What this architecture is not trying to do

It is not trying to eliminate server trust in V2. Public verifiability is a V3 goal. It is not trying to use blockchain — the three functions blockchain provides (immutable log, payment trigger, dispute resolution) are replicated by specific non-blockchain mechanisms, and Indian regulatory uncertainty around cryptocurrency makes blockchain a liability for the India-first market. It is not trying to make storage cheap by accepting lower durability — 10⁻¹⁵ per year is the target, r=40 is its analytical consequence, and reducing r to save storage would push the loss rate into observable territory. It is not trying to abstract away correlated failure — the 20% ASN cap is the structural bound; there are no exceptions for well-known or trusted providers. It is not trying to serve hot data in V2.

### Trade-off decision registry

| Trade-off | ADR | Research source |
| --- | --- | --- |
| RS over replication | ADR-003 | Papers 06, 10 |
| Wide stripe (n=56) | ADR-003 | Papers 19, 22, 38 |
| Lazy repair over eager | ADR-004 | Papers 06, 10, 39 |
| 20% ASN cap (dual purpose) | ADR-014 | Papers 07, 20, 38 |
| WiscKey over standard RocksDB | ADR-023 | Papers 25, 26, 27, 32 |
| Hybrid microservice + DHT | ADR-001 | Papers 02, 03, 05, 07 |
| Centralised audit scoring | ADR-008 | Papers 24, 31, 37, 40 |
| (3,2,2) quorum | ADR-025 | Paper 12 |
| libp2p + QUIC | ADR-021 | Papers 13, 14, 30 |
| Circuit Relay v2 fallback | ADR-021 | Paper 30 |
| HMAC-pseudonymised DHT keys | ADR-001 | Papers 04, 07 |
| 4-hour heartbeat | ADR-028 | Papers 20, 28, 30 |
| AONT-RS encoding | ADR-022 | Papers 15, 16 |
| ChaCha20 as default cipher | ADR-019 | Paper 17 |
| Master-secret key hierarchy | ADR-020 | Papers 17, 18 |
| 0-RTT disabled for audit | ADR-021 | Paper 14 |
| Held-earnings escrow | ADR-024 | Papers 05, 29, 33 |
| Payment per audit passed | ADR-012 | Papers 05, 07, 33 |
| Fiat over cryptocurrency | ADR-011 | Papers 07, 33, 35 |
| Route over Escrow+ | ADR-011 | Paper 35 |
| Deterministic pricing | ADR-024 | Paper 33 |
| Non-transferable escrow | ADR-024 | Papers 33, 37 |
| PoR over PoRep | ADR-002 | Papers 05, 07, 29 |
| Timing-based outsourcing prevention | ADR-014 | Papers 29, 37 |
| INSERT-only audit receipts | ADR-015 | Papers 07, 11 |
| V2 signed receipts over public verifiability | ADR-015 | Paper 07 |
| 72-hour departure threshold | ADR-006, ADR-007 | Papers 08, 09, 12 |
| 24-hour polling interval | ADR-006 | Papers 06, 08 |
| Desktop-only V2 | ADR-010 | Papers 05, 06, 08, 10 |
| Minimum viable network gate | ADR-029 | Papers 10, 36, 38 |
| India-only at launch | ADR-011 | Papers 33, 35 |

---

### 25.1 Technologies Explicitly Rejected

The following were seriously considered and rejected. Any engineer proposing to re-introduce
a technology on this list must open a new ADR explaining why the original rejection reason
no longer applies before any PR can be merged.

| Technology | Considered for | Rejection reason | ADR / Source |
|---|---|---|---|
| Blockchain / smart contracts | Payment trigger, immutable audit log, public dispute | NBFC registration required for Razorpay Escrow+; token price volatility; Swarm SWAP structural failure in production | ADR-011, Paper 07 |
| Razorpay Escrow+ | Native escrow product | Requires NBFC registration and trustee approval Vyomanaut does not qualify for | ADR-011, Paper 35 |
| UPI Collect | Data owner deposit flow | Deprecated by NPCI 28 Feb 2026 — all flows must use UPI Intent or QR | NFR-029, Paper 35 |
| gRPC over HTTP/2 | P2P chunk transfer | No QUIC connection migration; TCP HOL blocking; no built-in NAT traversal | ADR-021 |
| EigenTrust | Distributed provider reputation | Peer-to-peer audit reports without a trusted backstop collapse to universal dishonesty as the unique Nash equilibrium (SHELBY Proposition 1) | ADR-008, Papers 24, 37 |
| Filecoin PoRep + PoSt | Continuous proof of storage | 256 GB RAM + GPU ≥ 11 GB VRAM required; eliminates every Indian home desktop provider | ADR-002, Papers 07, 29 |
| Clay codes (MSR regenerating codes) | Repair bandwidth optimisation (V3) | Sub-packetisation at (n=56, k=16): α ≥ 40^16 — computationally intractable (Paper 22, Q19-2 definitively answered) | ADR-026, Paper 22 |
| LRC (Azure-style locally repairable codes) | Erasure coding | Non-MDS; local group co-locality cannot be guaranteed; repair benefit collapses to RS-level under Indian ISP conditions | ADR-026, Paper 19 |
| Managed consensus (etcd / Consul) | Microservice cluster coordination | External operational dependency; gossip-based membership is sufficient for a 3-node cluster | ADR-025, Paper 12 |
| BitSwap / Swarm SWAP | P2P chunk transfer protocol | Bandwidth-as-currency is structurally incompatible with Vyomanaut's asymmetric model; structural failure confirmed | ADR-012, Paper 07 |
| AES-only ciphers (no ChaCha20 path) | AONT word encryption | Software AES: 24–42 MB/s on no-AES-NI hardware; cache-timing vulnerability in table-lookup AES; ~10–15% of target Indian desktops lack AES-NI | ADR-019, Paper 17 |
| RC4-128 + MD5 ("fast" AONT-RS config) | AONT internal cipher | RC4 cryptographically broken (RFC 7465, 2015); this is the explicitly insecure AONT-RS configuration from Paper 16 | ADR-022, Paper 16 |
| Mobile providers (iOS / Android) in V2 | Storage providers | MTTF ≈ 90d → BWavg ≈ 130 Kbps/peer, exceeding the 100 Kbps budget; background execution kills daemon unpredictably | ADR-010, Paper 10 |
| Convergent encryption / deduplication | Cross-owner deduplication | Comparing ciphertexts/plaintexts creates privacy risk; each AONT key K is fresh random per segment by design | ADR-022 |
| File versioning / in-place update | Data owner file management | Files are immutable once stored; update = delete + re-upload; versioning adds pointer file complexity with no research-validated benefit at V2 | requirements.md §3 |
| Coral DSHT | Geographic proximity DHT routing | Deferred to V3; India-only provider network has sufficiently homogeneous inter-node latency (5–40 ms between Indian cities) | ADR-001 |

---

## 26. Known Limitations and V3 Scope

These are explicit design decisions, each with a documented reason and a V3 path.

**No mobile providers.** At MTTF ~30–90 days, repair bandwidth exceeds the 100 Kbps background budget. Including mobile at V2 parameters would make repair consume more bandwidth than steady-state storage. Research required before V3: iOS/Android background execution limits, MTTF under financial incentives, mobile-specific erasure parameters. ([ADR-010](../decisions/ADR-010-desktop-only-v2.md))

**No public audit verification.** In V2, a data owner must trust the microservice's countersignature on audit receipts. The V3 Transparent Merkle Log will publish a daily Merkle root over all receipts, allowing independent verification without trusting the operator. ([ADR-015](../decisions/ADR-015-audit-trail.md))

**No repair bandwidth optimisation.** At V2 scale (~hundreds of providers), the 39 Kbps/peer bandwidth is comfortably inside the budget. The adoption gate for Hitchhiker codes (25–45% bandwidth reduction, V3 candidate) is: if observed V2 bandwidth exceeds 60 Kbps/peer over the first 6 months, implement. ([ADR-026](../decisions/ADR-026-repair-bw-optimisation.md))

**No upload optimality threshold.** Storj uses a parameter `o` to cancel the slowest uploads once `o` of 56 fragments confirm, reducing P99 upload latency. This is a V3 optimisation — the microservice would need to issue cancel signals to slow providers.

**Audit scalability ceiling.** Full daily audit of 100,000 providers × 10,000 chunks each approaches the ceiling of a single optimised Postgres instance (~10,000 inserts/second). V2 launches at hundreds of providers — far below this limit. If probabilistic sampling is introduced at V3 scale, the SHELBY incentive-compatibility conditions (Theorem 1, Research Paper 37) must be re-verified at the new audit frequency before deploying.

**India-only payments.** Razorpay and UPI require Indian bank accounts. The `PaymentProvider` interface in ADR-011 is designed for international gateways (e.g., Stripe Connect) to be added as implementations without rewriting payment logic.

---

## 27. Capacity Planning

This section contains all capacity estimates, sizing tables, and the hard architectural
ceilings. All values are derived from the constants in the Dimensions Table below and
trace to cited papers or ADRs. Recompute whenever any value in that table changes.

**Value status labels:**

| Label | Meaning |
|---|---|
| `DESIGN` | Deliberate parameter set in an ADR. Changing it requires a new ADR. |
| `DERIVED` | Computed from other values by formula. Changes when inputs change. |
| `MEASURED` | Taken from empirical data in a cited paper or benchmark. |
| `ASSUMED` | Estimate with no cited source. Flag for empirical validation. Treat conservatively. |

---

### 27.1 Dimensions Table

#### Erasure coding and storage geometry

| Name | Symbol | Value | Unit | Status | Source |
|---|---|---|---|---|---|
| Reconstruction threshold | s | 16 | shards | DESIGN | ADR-003 |
| Redundancy fragments | r | 40 | shards | DESIGN | ADR-003 |
| Total shards per segment | n | 56 | shards | DERIVED (s+r) | ADR-003 |
| Lazy repair trigger | s + r0 | 24 | shards | DESIGN | ADR-003 |
| Lazy repair buffer | r0 | 8 | shards | DESIGN | ADR-003 |
| Fragment (chunk) size | lf | 256 KB | bytes | DESIGN | ADR-003 |
| Block size (one full segment) | lb | 4 MB | bytes | DERIVED (s × lf) | ADR-003 |
| Maximum segment size on wire | — | 14 MB | bytes | DERIVED (n × lf) | ADR-003 |
| Storage efficiency | s/n | 28.57% | — | DERIVED (16/56) | ADR-003 |
| Target annual file loss rate | LossRate | < 10⁻¹⁵ | per file/year | DESIGN | ADR-003 |
| Computed LossRate at V2 params | — | ~10⁻²⁵ | per file/year | DERIVED (Giroire Formula 3) | Paper 10 |

#### Provider MTTF and bandwidth

| Name | Value | Unit | Status | Source |
|---|---|---|---|---|
| Minimum acceptable provider MTTF | 180 | days | DESIGN | ADR-010 |
| Target provider MTTF (planning) | 300 | days | DESIGN | ADR-003 |
| Maximum observed MTTF (NAS) | 380 | days | DESIGN | ADR-010 |
| Background bandwidth budget per provider | 100 | Kbps | DESIGN | ADR-009, Paper 06 |
| Background CPU budget | ≤ 5 | % | DESIGN | ADR-009 |
| BWavg at MTTF=300d (N=1,000, 50 GB/peer) | ~39 | Kbps/peer | DERIVED (Giroire Formula 1) | Paper 10 |
| BWavg at MTTF=180d | ~65 | Kbps/peer | DERIVED | Paper 10 |
| BWavg at MTTF=90d (mobile — out of scope) | ~130 | Kbps/peer | DERIVED | Paper 10 |
| Qpeek at N=1,000, 50 GB/peer | ~793 | GB per failure | DERIVED (Giroire Formula 2) | Paper 10 |
| Repair window at N=1,000 | ~8 | hours | DERIVED | Paper 10 |
| Repair safety budget | 12 | hours | DESIGN | ADR-004 |
| Real repair BW std deviation | 22× | × independent model σ | MEASURED (simulation, 5,000 peers) | Paper 36 |

#### Audit system

| Name | Value | Unit | Status | Source |
|---|---|---|---|---|
| Audit frequency per chunk | 1 | per 24 h | DESIGN | ADR-006 |
| Audit receipt row size (on-disk est.) | ~450 | bytes | ASSUMED (raw ~297 B + Postgres overhead ~50%) | — |
| Postgres single-instance INSERT ceiling | 5,000–10,000 | rows/sec | ASSUMED — **must be benchmarked on actual schema before V2 GA; see NFR-043** | — |

#### Provider storage engine

| Name | Value | Unit | Status | Source |
|---|---|---|---|---|
| vLog entry size | 262,212 | bytes | DERIVED (32+4+262,144+32) | ADR-023 |
| RocksDB index entry size | 44 | bytes | DERIVED (32+8+4) | ADR-023 |
| Write amplification at lf=256 KB | ~1.0 | × | MEASURED (WiscKey Figure 10) | Paper 27 |
| Audit lookup on SSD | ~1 | ms | MEASURED | Paper 27 |
| Audit lookup on 7,200 RPM HDD | ~12–15 | ms | DERIVED | Paper 27 |
| Bloom filter false-positive rate | ~1 | % | DESIGN (10 bits/key) | ADR-023 |

#### Network and connectivity

| Name | Value | Unit | Status | Source |
|---|---|---|---|---|
| Hole-punch success rate (DCUtR) | 70 | % | MEASURED (4.4M attempts) | Paper 30 |
| Relay-dependent fraction (global) | ~30 | % | MEASURED | Paper 30 |
| Relay-dependent fraction (Indian CGNAT est.) | 30–45 | % | ASSUMED — tracked in Q20-1 | Paper 30 |
| Relay slots per relay node | 128 | concurrent | DESIGN | ADR-021 |
| Relay nodes at launch | 3 | nodes | DESIGN | ADR-021 |
| Total relay slots at launch | 384 | concurrent | DERIVED (3 × 128) | ADR-021 |
| Relay overhead (Indian cloud-hosted) | < 50 | ms RTT | DESIGN | ADR-021, Paper 30 |
| Median DHT lookup latency (IPFS post-v0.5) | 622 | ms | MEASURED | Paper 20 |

---

### 27.2 Storage Capacity

#### Network-wide usable storage

Storage efficiency is 28.57% (s/n = 16/56): for every 3.5 GB a provider contributes, ~1 GB
is accessible file data. The remainder is the cost of 40-fragment fault tolerance.

| Active providers | Storage/provider | Raw network storage | Usable file data |
|---|---|---|---|
| 56 (floor) | 50 GB | 2.8 TB | 800 GB |
| 56 (floor) | 500 GB | 28 TB | 8 TB |
| 500 | 50 GB | 25 TB | 7.1 TB |
| 1,000 | 50 GB | 50 TB | 14.3 TB |
| 1,000 | 500 GB | 500 TB | 143 TB |
| 10,000 | 50 GB | 500 TB | 143 TB |

#### Per-provider storage engine sizing

At 50 GB declared storage:
Chunks stored = 50 GB / 262,212 bytes ≈ 200,700 chunks RocksDB index = 200,700 × 44 bytes ≈ 8.8 MB (trivially fits in RAM) vLog on disk = 200,700 × 262,212 ≈ 52.7 GB Bloom filter = 200,700 × 10 bits ≈ 2.5 MB

At 500 GB declared storage:
Chunks stored = ≈ 2,007,000 chunks RocksDB index = ≈ 88.3 MB (fits fully in RocksDB block cache) vLog on disk = ≈ 527 GB Bloom filter = ≈ 25.1 MB

The RocksDB index at any realistic provider allocation (up to several TB) stays fully cached
in memory — audit lookup is a memory operation plus one vLog read.

#### Audit receipt table growth

The `audit_receipts` table is INSERT-only and grows permanently. Monthly partitioning is
mandatory from day one.

| Providers | Chunks/provider | Rows/day | Storage/year (@ 450 B/row) |
|---|---|---|---|
| 56 | 200,000 | 11.2 M | 1.8 TB |
| 500 | 200,000 | 100 M | 16.4 TB |
| 1,000 | 10,000 | 10 M | 1.6 TB |
| 1,000 | 200,000 | 200 M | 32.9 TB |
| 10,000 | 10,000 | 100 M | 16.4 TB |

Archive partitions older than 30 days to cold object storage — the reliability scorer only
queries the trailing 30-day window (ADR-008) and the 90-day provider cohort analysis (Q08-1,
Q29-1) is the maximum operational lookback.

---

### 27.3 Bandwidth Budget

#### Steady-state repair bandwidth (BWavg)

Giroire Formula 1 (Paper 10). Key result: BWavg scales as D/N — it is proportional to
per-provider data load, not total network size. Increasing N while holding per-provider
storage constant does not increase BWavg.

| MTTF | BWavg (N=1,000, 50 GB/peer) | Within 100 Kbps budget? |
|---|---|---|
| 90 days (mobile — out of scope) | ~130 Kbps/peer | No — exceeds budget |
| 180 days (desktop minimum) | ~65 Kbps/peer | Yes — 35 Kbps headroom |
| 300 days (planning target) | ~39 Kbps/peer | Yes — 61 Kbps headroom |
| 380 days (NAS tier) | ~31 Kbps/peer | Yes — 69 Kbps headroom |

**Per-provider storage ceiling at 100 Kbps budget (N=1,000):**

| MTTF | Max chunk data per provider |
|---|---|
| 180 days (worst case V2) | ~70 GB |
| 300 days (planning target) | ~130 GB |

This ceiling must be enforced in provider onboarding limits (see NFR-044). It is not
currently stated in an ADR and must be addressed as an operational constraint.

#### Burst repair bandwidth (Qpeek)

Giroire Formula 2 (Paper 10). At N=1,000, 50 GB/provider:
Qpeek ≈ 793 GB total network transfer per failure event θ = Qpeek / (N × 100 Kbps) ≈ 8 hours

Repair window at multiple scales:

| N providers | Storage/provider | Qpeek | θ (@ 100 Kbps/peer) | Within 12h? |
|---|---|---|---|---|
| 56 | 50 GB | 44 GB | ~22 hours | **No** |
| 200 | 50 GB | 222 GB | ~25 hours | **No** |
| 500 | 50 GB | 396 GB | ~18 hours | **Borderline** |
| 1,000 | 50 GB | 793 GB | ~8 hours | Yes |
| 5,000 | 50 GB | 793 GB | ~1.6 hours | Yes |

At N < 500: repair window exceeds 12 hours. `repair_queue_depth` (alerts at > 1,000) is the
primary monitoring signal, but at this scale operators must manually verify no repair job is
outstanding before accepting a new provider departure within a 24-hour window. The automated
alert alone is insufficient because individual failures can keep the queue below threshold
while still exceeding the repair window.

The real-world standard deviation of repair bandwidth is 22× larger than the independent
model predicts (Paper 36, Dalle et al., 5,000-peer simulation). The 20% ASN cap (ADR-014)
limits the maximum correlated failure to ~11 simultaneous departures from any single group.

---

### 27.4 Audit System Throughput

#### Challenge issuance rate

Challenges/sec = (N_providers × chunks_per_provider) / 86,400


| Providers | Chunks/provider | Challenges/day | Challenges/sec |
|---|---|---|---|
| 56 | 10,000 | 560,000 | 6.5 /sec |
| 56 | 200,000 | 11.2 M | 130 /sec |
| 1,000 | 10,000 | 10 M | 116 /sec |
| 1,000 | 200,000 | 200 M | 2,315 /sec |
| 10,000 | 10,000 | 100 M | 1,157 /sec |
| 100,000 | 10,000 | 1 B | ~11,574 /sec — approaches Postgres ceiling |

#### Postgres INSERT ceiling (audit_receipts)

The general Postgres benchmark ceiling of 5,000–10,000 rows/sec is the planning estimate.
The actual ceiling on the `audit_receipts` schema — which contains two 64-byte Ed25519
signatures per row — is expected to be lower. **This value must be benchmarked on the real
schema with row security policy enabled before V2 GA (NFR-043).** Measure the point at which
INSERT latency exceeds 50 ms at p99.

Based on the planning estimate, Postgres INSERT is the first architectural ceiling in the
audit pipeline. It binds at approximately **100,000 providers × 10,000 chunks** (or 5,000
providers × 200,000 chunks) — orders of magnitude above V2 launch scale.

If probabilistic sampling is introduced to extend this ceiling, the SHELBY
incentive-compatibility conditions (Theorem 1, Paper 37) must be re-verified at the new
per-chunk audit frequency before deployment.

---

### 27.5 Network and DHT Scaling

#### DHT hop count

Kademlia lookup takes O(log₂ N / α) round trips with α = 3:

| N providers | Approx. hops | Est. lookup latency |
|---|---|---|
| 56 | ~2 | ~1.2 s |
| 500 | ~3 | ~1.9 s |
| 1,000 | ~3–4 | ~2.5 s |
| 10,000 | ~5 | ~3.1 s |

DHT lookup latency is logarithmic in N and is never a scaling bottleneck.

#### Provider RAM requirement for DHT records

Each assigned chunk generates one DHT provider record. At ~200 bytes per record:

| Provider storage | Chunks | DHT record memory |
|---|---|---|
| 10 GB | ~40,000 | ~8 MB |
| 50 GB | ~200,000 | ~40 MB |
| 200 GB | ~800,000 | ~160 MB |
| 500 GB | ~2,000,000 | ~400 MB |

A provider running the daemon with less free RAM than their chunk allocation requires will
cause DHT record eviction, producing audit TIMEOUT results indistinguishable from provider
absence. This minimum RAM figure must appear in the daemon's hardware requirements
specification (NFR-045).

#### Relay infrastructure scaling

Required relay slots = N × relay_fraction × 1.5; add a relay node when required slots exceed
80% of available slots (relay_nodes × 128 × 0.80).

| N providers | Relay-dependent (30%) | Slots needed | Slots available | Headroom |
|---|---|---|---|---|
| 56 | 17 | 26 | 384 | 93% spare |
| 300 | 90 | 135 | 384 | 65% spare |
| 570 (45% CGNAT) | 257 | 386 | 384 | **Oversubscribed — add 4th relay node** |
| 850 (30% CGNAT) | 255 | 383 | 384 | ~0% spare — add 4th relay node |
| 1,000 (30% CGNAT) | 300 | 450 | 384 | Oversubscribed |

Under the pessimistic Indian CGNAT assumption (45%), relay becomes the binding constraint at
approximately **570 providers**. As a conservative operational rule: provision the 4th relay
node before reaching 400 providers. Q20-1 telemetry at private beta resolves the actual CGNAT
fraction. Each additional relay node adds capacity for ~280 providers.

---

### 27.6 Payment System Throughput

Monthly payout release (23rd of each month) processes ~5 operations per active provider:
1 SELECT (30-day score), 1 SELECT (7-day score), 1 SELECT (escrow balance), 1 Razorpay
PATCH, 1 INSERT (`RELEASE` event).

| N providers | Operations total | Sustained rate (4-hour window) | Within Razorpay rate limit (500 req/min)? |
|---|---|---|---|
| 1,000 | 5,000 | ~0.35/sec | Yes |
| 10,000 | 50,000 | ~3.5/sec | Yes |
| 120,000 | 600,000 | ~41.7/sec | At limit — distribute release window |

The `escrow_events` table grows at ~1 event per provider per month plus seizures and deposits;
it is never a scaling concern at any realistic V2 scale.

---

### 27.7 Hard Ceilings — What Breaks First

| Constraint | Binds at | Symptom | Action |
|---|---|---|---|
| Relay slot exhaustion (45% Indian CGNAT) | ~570 providers | TIMEOUT rate rises for relay-dependent providers | Add relay nodes; each buys ~280 more providers |
| Relay slot exhaustion (30% global baseline) | ~850 providers | Same | Same |
| Repair window exceeds 12 h | < 500 providers | Qpeek > 12 h; second failure risk during repair | Monitor `repair_queue_depth`; manual oversight per departure until N > 500 |
| Per-provider bandwidth ceiling (MTTF=180d) | ~70 GB/provider | BWavg → 100 Kbps; repair traffic impairs provider foreground I/O | Enforce chunk count ceiling in onboarding (NFR-044) |
| Provider RAM for DHT records | ~400 MB free at 500 GB allocation | DHT eviction causes false TIMEOUT results | Enforce minimum RAM in daemon hardware spec (NFR-045) |
| Postgres audit INSERT rate | ~100k providers × 10k chunks (planning estimate — must be measured, NFR-043) | INSERT latency > 50 ms p99; audit queue grows | Monthly partitioning from day 1; probabilistic sampling when approaching ceiling (re-verify SHELBY conditions first) |
| Audit receipt storage | ~1.6 TB/year at V2 launch | Disk fills; query latency rises | Monthly partition archiving to cold object storage |
| Razorpay Route payout release | ~120,000 providers in 4-hour window | Release window exceeds midnight | Extend release window or upgrade Razorpay rate limit tier |
| Microservice gossip (3-node) | > 5 replicas | Operational complexity exceeds value of hand-rolled solution | Migrate to etcd or Consul (ADR-025 open constraint) |

**The most likely first constraint in the V2 growth trajectory is relay slot exhaustion.**
Relay infrastructure is cheap to add incrementally but must be provisioned ahead of the
trigger — not in response to TIMEOUT alerts.

The second constraint to bind is the per-provider bandwidth ceiling at MTTF=180d. This
must be enforced in provider onboarding, not discovered operationally.

---

### 27.8 Scale Milestones

| Milestone | Condition | Key actions |
|---|---|---|
| M-cap-1: Network open | 56 providers | 1 simultaneous upload segment; repair window ~22 h — manual monitoring required; relay 93% spare |
| M-cap-2: 300 providers | Growth | 5 simultaneous segments; relay 65% spare (30% CGNAT); watch Q20-1 telemetry |
| M-cap-3: 570 providers | Indian CGNAT pressure | **Add 4th relay node** (if Q20-1 confirms ≥ 40% CGNAT fraction) |
| M-cap-4: 850 providers | Global baseline pressure | **Add 4th relay node** if not already done |
| M-cap-5: 1,000 providers | Giroire target | Repair window ~8 h — comfortable on all Giroire metrics; first time all parameters hold simultaneously |
| M-cap-6: 5,000 providers | Scale growth | Audit INSERT rate approaching ceiling at 200k chunks/provider; storage allocation ceiling per provider is now binding; document in provider hardware spec |
| M-cap-7: 10,000 providers | Operational scale | 16.4 TB/year audit receipt storage at 10k chunks/provider; partitioning and archiving mandatory; monthly payout computation well within Razorpay rate limits |
| M-cap-8: 100,000 providers | Architecture ceiling | Postgres INSERT rate is the binding constraint; evaluate probabilistic audit sampling (re-verify SHELBY conditions before deploying) |

---

## 28. ADR Reference Index

| Component | ADRs |
|---|---|
| Coordination architecture (hybrid microservice + Kademlia DHT) | [ADR-001](../decisions/ADR-001-coordination-architecture.md) |
| Proof of storage / audit challenge design | [ADR-002](../decisions/ADR-002-proof-of-storage.md) |
| Erasure coding parameters (RS s=16, r=40, r0=8, lf=256 KB) | [ADR-003](../decisions/ADR-003-erasure-coding.md) |
| Repair protocol (lazy, 72h threshold, priority ordering) | [ADR-004](../decisions/ADR-004-repair-protocol.md) |
| Peer selection and vetting pipeline | [ADR-005](../decisions/ADR-005-peer-selection.md) |
| Polling interval (24h) and departure threshold (72h) | [ADR-006](../decisions/ADR-006-polling-interval.md) |
| Provider exit states (four states, seizure mechanics) | [ADR-007](../decisions/ADR-007-provider-exit-states.md) |
| Reliability scoring (three rolling windows) | [ADR-008](../decisions/ADR-008-reliability-scoring.md) |
| Background CPU budget (≤5%) | [ADR-009](../decisions/ADR-009-background-execution.md) |
| No mobile providers in V2 | [ADR-010](../decisions/ADR-010-desktop-only-v2.md) |
| Fiat escrow via Razorpay/UPI | [ADR-011](../decisions/ADR-011-escrow-payments.md) |
| Payment per audit passed | [ADR-012](../decisions/ADR-012-payment-basis.md) |
| Consistency model (14 coordination-free, 6 coordinated) | [ADR-013](../decisions/ADR-013-consistency-model.md) |
| Adversarial defences (five classes) | [ADR-014](../decisions/ADR-014-adversarial-defences.md) |
| Audit trail (signed receipts + V3 Merkle Log) | [ADR-015](../decisions/ADR-015-audit-trail.md) |
| Payment DB schema (PN-counter CRDT, escrow_events) | [ADR-016](../decisions/ADR-016-payment-db-schema.md) |
| Audit receipt schema (12 fields, Ed25519 dual signatures) | [ADR-017](../decisions/ADR-017-audit-receipt-schema.md) |
| Hot/cold storage bands | [ADR-018](../decisions/ADR-018-hot-cold-storage-bands.md) |
| Client-side cipher (ChaCha20-256 / AES-256-CTR) | [ADR-019](../decisions/ADR-019-client-side-encryption.md) |
| Key management (HKDF hierarchy, pointer file, BIP-39) | [ADR-020](../decisions/ADR-020-key-management.md) |
| P2P transfer (libp2p + QUIC v1, three-tier NAT traversal) | [ADR-021](../decisions/ADR-021-p2p-transfer-protocol.md) |
| Encoding pipeline (AONT-RS: transform then code) | [ADR-022](../decisions/ADR-022-encryption-erasure-order.md) |
| Provider storage engine (WiscKey: RocksDB index + vLog) | [ADR-023](../decisions/ADR-023-provider-storage-engine.md) |
| Economic mechanism (deterministic escrow, graduated penalty) | [ADR-024](../decisions/ADR-024-economic-mechanism.md) |
| Microservice cluster quorum ((3,2,2) + gossip) | [ADR-025](../decisions/ADR-025-microservice-consistency-mechanism.md) |
| Repair bandwidth optimisation (V3 — Hitchhiker candidate) | [ADR-026](../decisions/ADR-026-repair-bw-optimisation.md) |
| Cluster audit secret (HKDF derivation, versioned rotation) | [ADR-027](../decisions/ADR-027-cluster-audit-secret.md) |
| Provider heartbeat (4-hour address update) | [ADR-028](../decisions/ADR-028-provider-heartbeat.md) |
| Bootstrap minimum viable network (7 conditions) | [ADR-029](../decisions/ADR-029-bootstrap-minimum-viable-network.md) |
| Synthetic vetting chunks (repair-safe provider assessment) | [ADR-030](../decisions/ADR-030-synthetic-vetting-chunks.md) |
| Demo / production mode: NetworkProfile, mode flag, demo specifications | ADR-031 |