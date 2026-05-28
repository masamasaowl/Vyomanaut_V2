# Vyomanaut V2 — Product Requirements Document

**Status:** Draft  
**Author:** Vyomanaut Engineering  
**Date:** April 2026  
**Version:** 1.0  
**Repository:** https://github.com/masamasaowl/Vyomanaut_Research  
**Architecture reference:** `docs/system-design/architecture.md`  
**Decisions index:** `docs/decisions/README.md`

---

## Table of Contents

1. [Overview](#1-overview)
2. [Problem Statement](#2-problem-statement)
3. [Goals and Non-Goals](#3-goals-and-non-goals)
4. [Functional Requirements](#4-functional-requirements)
   - [4.1 Data Owner — Registration and Onboarding](#41-data-owner--registration-and-onboarding)
   - [4.2 Data Owner — File Upload](#42-data-owner--file-upload)
   - [4.3 Data Owner — File Retrieval](#43-data-owner--file-retrieval)
   - [4.4 Data Owner — File Management](#44-data-owner--file-management)
   - [4.5 Provider — Installation and Registration](#45-provider--installation-and-registration)
   - [4.6 Provider — Operation](#46-provider--operation)
   - [4.7 Provider — Exit and Departure](#47-provider--exit-and-departure)
   - [4.8 Audit System](#48-audit-system)
   - [4.9 Repair System](#49-repair-system)
   - [4.10 Payment System](#410-payment-system)
   - [4.11 Network Readiness Gate](#411-network-readiness-gate)
   - [4.12 Provider Daemon — Simulation Mode](#412-provider-daemon--simulation-mode)
   - [4.13 Provider — Pre-Registration Earnings Calculator](#413-provider--pre-registration-earnings-calculator)
   - [4.14 Data Owner — Escrow Management and Upload Resume](#414-data-owner--escrow-management-and-upload-resume)
5. [Non-Functional Requirements](#5-non-functional-requirements)
   - [5.1 Durability](#51-durability)
   - [5.2 Availability](#52-availability)
   - [5.3 Performance](#53-performance)
   - [5.4 Security and Privacy](#54-security-and-privacy)
   - [5.5 Reliability and Correctness](#55-reliability-and-correctness)
   - [5.6 Observability and Operability](#56-observability-and-operability)
   - [5.7 Compliance and Payments](#57-compliance-and-payments)
   - [5.8 Privacy](#58-privacy)
   - [5.9 End-to-End Latency](#59-end-to-end-latency)
6. [UX Considerations](#6-ux-considerations)
   - [6.1 Critical UX Moments](#61-critical-ux-moments)
   - [6.2 Edge States](#62-edge-states)
7. [Technical Considerations](#7-technical-considerations)
   - [7.1 Hard Constraints](#71-hard-constraints)
   - [7.2 Key External Dependencies](#72-key-external-dependencies)
   - [7.3 Data Model Implications](#73-data-model-implications)
   - [7.4 Benchmark Requirements Before Shipping](#74-benchmark-requirements-before-shipping)
   - [Benchmark Procedures](#75-benchmark-procedures)
8. [Analytics and Instrumentation](#8-analytics-and-instrumentation)
   - [8.1 Primary Metric](#81-primary-metric)
   - [8.2 Provider Primary Metric](#82-provider-primary-metric)
   - [8.3 Key Events](#83-key-events)
   - [8.4 Guardrail Metrics (must not worsen)](#84-guardrail-metrics-must-not-worsen)
9. [Launch Plan](#9-launch-plan)
   - [9.1 Phases](#91-phases)
   - [9.2 Feature Flags](#92-feature-flags)
   - [9.3 Risk Factors and Mitigations](#93-risk-factors-and-mitigations)
   - [9.4 Rollback Plan](#94-rollback-plan)
10. [Research Questions](#10-research-questions) 
   - [10.1 Product Open Questions](#101-product-open-questions)
   - [10.2 Engineering Open Questions - Telemetry](#102-engineering-open-questions--telemetry)
   - [10.3 V3 Deferred Questions](#103-v3-deferred-questions)
11. [Appendix](#11-appendix)
   - [11.1 Research Basis](#111-research-basis)
   - [11.2 Rejected Requirements](#112-rejected-requirements)
   - [11.3 Answered Questions](#113-answered-questions)

---

## 1. Overview

This document formalises what Vyomanaut V2 must do and how well it must do it, in a form
that product, engineering, and operations teams can align on before a single line of
production code is committed. V1 failed because it was built without this alignment — the
architecture document describes what V2 is; this PRD describes what V2 must do for the
people who use it. Every requirement here has a traceable root in either a user story or
an accepted ADR. Where the two conflict, the ADR wins.

---

## 2. Problem Statement

**User problem — data owners:** Cloud storage (AWS S3, GCS, iCloud) is either expensive at
scale, proprietary, or geographically centralised. Data owners — individuals, small businesses,
creators — pay for storage they do not always trust, at prices set by monopolies, knowing
their unencrypted files sit on servers they do not control.

**User problem — storage providers:** Millions of Indian home desktop owners and NAS operators
have idle terabytes of disk space and symmetrical 100 Mbps connections at ₹600/month. No
product lets them monetise this idle capacity reliably, transparently, and without technical
expertise.

**Business problem:** Vyomanaut V1 confirmed both markets exist but was too unreliable (slow
peer discovery, no audit enforcement, no payment guarantees) to retain either user type past
the first week. V2 is a research-first rebuild that solves the reliability and trust problems
structurally before building any user-facing surface.

**Evidence:**

- V1 post-mortem: lack of research-backed architecture drove structural compromises within
  the first sprint. Three failure modes identified: inefficient peer discovery, no audit
  enforcement, and no payment guarantees.
- IPFS production measurement (Paper 20): 87.6% of unincentivised P2P sessions last under
  8 hours. Financial friction is necessary to sustain provider uptime.
- Razorpay API availability: UPI has zero merchant per-transaction fee and settles in
  seconds — the payment rail exists and is accessible to any Indian bank account holder.

---

## 3. Goals and Non-Goals

### Goals

- A data owner can upload, store, and retrieve files knowing no party in the system can read
  them — not Vyomanaut, not the providers, not a network observer.
- A provider can earn a predictable monthly income in Indian rupees for keeping a daemon
  running and passing storage audits, without understanding cryptography.
- The network sustains data durability above 10⁻¹⁵ annual loss probability per file even
  as providers join and leave daily.
- Every provider interaction — registration, audit response, payment, departure — has a
  defined outcome that both parties can verify independently.

### Non-Goals

- **Mobile providers in V2.** Operating system background execution limits on iOS and Android
  make provider-grade uptime impossible without special permissions that most users will not
  grant. Mobile providers are deferred to V3. (ADR-010)
- **File versioning or in-place updates.** Files are immutable once stored. Updating a file
  means deleting and re-uploading. Version history is out of scope.
- **Content deduplication across data owners.** Each upload uses a fresh AONT key. Two data
  owners storing the same file consume 2× the network capacity. This is the privacy price.
- **A retrieval marketplace.** Providers are not paid for serving downloads. Payment is for
  sustained storage presence proved by daily audits, not for bandwidth consumed.
- **International payments at launch.** V2 is India-only. Razorpay and UPI require Indian
  bank accounts. International payment support is deferred to a later version. (ADR-011)
- **A web dashboard for providers.** V2 ships a desktop daemon and a CLI. Provider UI is
  deferred to V3.

---

## 4. Functional Requirements

Requirements are grouped by domain. Each carries a priority label: **P0** (must ship for
V2 launch), **P1** (must ship before first paying customer), **P2** (should ship within the
first quarter post-launch). Every P0 requirement is a launch blocker.

### 4.1 Data Owner — Registration and Onboarding

| ID | Requirement | Priority | ADR / Notes |
|----|-------------|----------|-------------|
| FR-001 | The system must allow a data owner to create an account using a phone number verified by OTP, with no other personal information required. | P0 | Registration gate is the Sybil defence |
| FR-002 | At account creation, the system must derive a 32-byte master secret from the owner's passphrase using Argon2id (t=3, m=65536 KiB (64 MB), p=4) and must never write the master secret to disk or transmit it to the microservice. | P0 | ADR-020 |
| FR-003 | At account creation, the system must generate a BIP-39 24-word mnemonic from the master secret and display it exactly once, requiring the owner to confirm two randomly chosen words before proceeding. | P0 | ADR-020 — only recovery path on passphrase loss |
| FR-004 | The system must allow a data owner to restore access on a new device using either their passphrase or their 24-word BIP-39 mnemonic. | P0 | ADR-020 |
| FR-005 | The system must warn the data owner, in plain language, that loss of both the passphrase and the mnemonic results in permanent, unrecoverable data loss with no support path. | P0 | UX requirement; legal protection |
| FR-006 | The system must allow a data owner to deposit escrow funds using UPI Intent flow (see NFR-029 for the canonical compliance statement), with Smart Collect 2.0 reconciliation. | P0 | ADR-011, Paper 35 |

### 4.2 Data Owner — File Upload

| ID | Requirement | Priority | ADR / Notes |
|----|-------------|----------|-------------|
| FR-007 | The client must perform all encryption and erasure encoding on the data owner's device before any data is transmitted to any provider. | P0 | ADR-019, ADR-022 |
| FR-008 | The encoding pipeline must use AONT-RS. (Refer: [architecture.md Section 10](./architecture.md#10-data-encoding-pipeline)) | P0 | ADR-022, ADR-019, ADR-003 |
| FR-009 | The system must select 56 distinct providers for each file segment, ensuring no single ASN holds more than 20% of fragments (~11 of 56) for that segment. If the constraint is unsatisfied, the microservice must return HTTP 503 with error code INSUFFICIENT_ASN_DIVERSITY and a human-readable explanation. The client must surface this as: "Upload paused — not enough provider diversity. Retry will happen automatically when the network recovers." | P0 | ADR-014, ADR-005 |
| FR-010 | The system must upload all 56 fragments via direct libp2p/QUIC P2P connections to providers, without routing any file data through the microservice. | P0 | ADR-021; data plane / control plane separation |
| FR-011 | The system must create a pointer file containing the 56 provider IDs, 56 chunk content addresses, and erasure parameters, encrypt it with AEAD_CHACHA20_POLY1305 (key derived via HKDF from the master secret), and store with the microservice the three components as separate fields: the ciphertext body, the 96-bit nonce (monotone counter), and the 16-byte Poly1305 authentication tag, per the files table schema in ADR-020. | P0 | ADR-020, ADR-022 |
| FR-012 | The system must display upload progress per fragment (not just total), allowing the data owner to see when the upload is complete even if individual providers respond slowly. | P1 | UX requirement |
| FR-013 | The system must display the expected monthly storage cost for the file before the upload begins, calculated from the current storage rate and file size. | P1 | DO-04, FR-006 alignment |
| FR-014 | The system must refuse to begin an upload if the data owner's escrow balance is insufficient to cover 30 days of storage for the file. If the escrow balance covers fewer than 30 days, the upload must be blocked and the client must surface a UPI Intent deposit link pre-populated with the shortfall amount (rounded up to the nearest ₹10) | P0 | Prevents free-riding on provider capacity |

### 4.3 Data Owner — File Retrieval

| ID | Requirement | Priority | ADR / Notes |
|----|-------------|----------|-------------|
| FR-015 | The client must retrieve the encrypted pointer file from the microservice, decrypt it locally using the HKDF-derived key, and use the provider list and chunk IDs inside to initiate retrieval — without the microservice being involved in the data transfer. | P0 | ADR-020, zero-knowledge |
| FR-016 | The client must attempt retrieval from the 16 fastest-responding providers (not necessarily the first 16 in the pointer file), cancelling remaining connections once k=16 responses are received. "Fastest-responding" is measured by initiating parallel libp2p dials to all 56 providers simultaneously. The first 16 to complete the connection handshake are used. This is the standard parallel-fetch-with-cancel pattern. No pre-probe is required. | P1 | Reduces P99 retrieval latency |
| FR-017 | The client must verify each retrieved fragment against its SHA-256 content address before decoding. Any fragment that fails must be replaced by requesting from an alternate provider. | P0 | Integrity check; ADR-022 canary verification |
| FR-018 | After RS decoding and AONT decryption, the client must verify the canary word. If it fails, the client must surface an error and must not hand corrupted plaintext to the data owner. | P0 | ADR-022 |

### 4.4 Data Owner — File Management

| ID | Requirement | Priority | ADR / Notes |
|----|-------------|----------|-------------|
| FR-019 | The system must provide a file list view showing each file's name, size, upload date, current storage cost per month, and retrieval status (all fragments available / degraded / unavailable). | P1 | DO-04 |
| FR-020 | The system must allow a data owner to delete a file, which must trigger removal of all 56 chunk assignments from the microservice and notify each holding provider to delete their fragment. If a provider is unreachable at deletion time, the microservice must flag the chunk assignment as `pending_deletion` and retry the deletion notification at each subsequent heartbeat cycle. The provider daemon must check for `pending_deletion` assignments on startup and act on them before accepting new audit challenges. The provider daemon must run the vLog GC procedure for the affected chunk_id, as specified in ADR-023 §Garbage collection. | P1 | ADR-007; GC on vLog |
| FR-021 | The system must provide an escrow balance view showing: current balance, amount reserved for active files (next 30 days), amount available for withdrawal, and transaction history. | P1 | DO-04, ADR-016 |

### 4.5 Provider — Installation and Registration

| ID | Requirement | Priority | ADR / Notes |
|----|-------------|----------|-------------|
| FR-022 | The provider daemon must be installable on Windows 10+, macOS 12+, and Ubuntu 22.04+ via a signed single-file installer with no manual dependency installation. | P0 | ADR-009; desktop-only V2 |
| FR-023 | At first launch, the daemon must generate an Ed25519 key pair, persist it encrypted under a daemon-local passphrase, and display the public key fingerprint to the provider for their records. | P0 | ADR-021; peer identity |
| FR-024 | The daemon must register with the microservice by submitting the provider's phone number (OTP-verified), Ed25519 public key, declared storage allocation in GB, and declared city/region. | P0 | ADR-001, ADR-005 |
| FR-025 | The system must initiate Razorpay Route Linked Account creation asynchronously after registration and must not begin chunk assignment until the 24-hour cooling period has elapsed and the account is confirmed active. | P0 | ADR-024, Paper 35 |
| FR-026 | The daemon must set `providers.status = PENDING_ONBOARDING` at registration, advance to `VETTING` on first successful heartbeat, and advance to `ACTIVE` automatically once both conditions are satisfied: (a) a minimum of 120 days have elapsed since first chunk assignment, AND (b) 80 consecutive audit passes have been recorded without any DEPARTED or SILENT_DEPARTURE event in between. | P0 | ADR-005, ADR-007 |
| FR-061 | During the vetting period (`status = VETTING`), the assignment service must assign only synthetic vetting chunks to the provider. Synthetic chunks are random 256 KB blocks generated by the microservice using `crypto/rand`. The provider daemon must store, serve audit challenges for, and earn credits from synthetic chunks through the identical code paths as real shards. No real data owner file shard may be assigned to a provider in `VETTING` status. | P0 | ADR-030 |
| FR-062 | The assignment service must cap synthetic vetting chunk allocation at `floor(declared_storage_gb × 400)` active chunks per provider (equivalent to 10% of declared storage). Once the cap is reached, no further synthetic assignments are made until existing ones are retired. | P0 | ADR-030 — prevents monopolising provider disk with dummy data before trust is established |
| FR-063 | On the ACTIVE transition (80 consecutive audit passes AND ≥ 120 days since first chunk assignment), the microservice must send a GC instruction to the provider daemon via the `/vyomanaut/vetting-gc/1.0.0` libp2p protocol containing the list of all synthetic chunk IDs (`chunk_id`values where `is_vetting_chunk = TRUE AND provider_id = $1`). The daemon must delete these chunks from the vLog using the standard `DeleteChunk` + GC path. The assignment service must set those `chunk_assignments.status = 'PENDING_DELETION'` at transition time and `'DELETED'` after the daemon confirms deletion. Real shard assignments begin flowing immediately after the status transition; GC runs in parallel. | P0 | ADR-030, ADR-023 |
| FR-064 | If the provider is offline at the time of the ACTIVE transition, the GC instruction must be queued and delivered on the provider's next successful heartbeat connection. Until the GC instruction is delivered and acknowledged, the synthetic chunk rows must remain in `status = 'PENDING_DELETION'` and must not be issued further audit challenges. The audit scheduler must skip `PENDING_DELETION` rows. | P0 | ADR-030 — prevents auditing chunks the provider is in the process of discarding |
| FR-065 | When a provider with `status = VETTING` crosses the 72-hour departure threshold, the departure handler must enqueue **zero** repair jobs. All synthetic chunk assignments for that provider must be soft-deleted (`status = 'DELETED'`, `deleted_at = NOW()`). The standard escrow seizure and DEPARTED status transition still apply. | P0 | ADR-030 — the entire point of synthetic chunks is to eliminate repair bandwidth for vetting departures |
| FR-066 | The repair scheduler must check `chunk_assignments.is_vetting_chunk` before enqueueing any repair job. A departure handler or threshold monitor that identifies a chunk where `is_vetting_chunk = TRUE` must not call `EnqueueJob` for that chunk. This check is enforced at the application layer AND as a pre-condition in the `internal/repair` package interface. | P0 | ADR-030 |
| FR-068 | In `VYOMANAUT_MODE=demo`, if a registering provider does not supply an ASN, the microservice must auto-assign the next available synthetic ASN from the pool `SIM-AS1` … `SIM-AS{N}`, where N = `NetworkProfile.MinDistinctASNs`. This ensures the 20% ASN cap is satisfiable from the first upload. In production, ASN is resolved from the provider's IP address via a GeoIP/ASN database and must not be auto-assigned. | P0 | ADR-031, ADR-014 |
| FR-069 | The microservice must refuse to start if `VYOMANAUT_MODE=prod` AND the `VYOMANAUT_CLUSTER_MASTER_SEED` environment variable is present. The microservice must refuse to start if `VYOMANAUT_MODE=demo` AND the process is configured to connect to the live Razorpay API endpoint (not mock or test). These are fatal startup guard rails. | P0 | ADR-031 |

### 4.6 Provider — Operation

| ID | Requirement | Priority | ADR / Notes |
|----|-------------|----------|-------------|
| FR-027 | The daemon must send a signed heartbeat to the microservice control plane every 4 hours containing the provider's current libp2p multiaddresses, so that the microservice always has a fresh address for challenge dispatch. | P0 | ADR-028; DHCP rotation problem |
| FR-028 | The daemon must respond to audit challenges by reading the specified chunk from the vLog, verifying its SHA-256 content hash, computing SHA-256(chunk_data ‖ challenge_nonce), and returning a signed receipt — all within the per-provider deadline of (256 KB / p95_throughput_kbps) × 1.5. | P0 | ADR-002, ADR-014 |
| FR-029 | The daemon must expose a local status interface (CLI or tray icon) showing: daemon health, number of stored chunks, last audit result, current reliability score, and pending earnings. | P1 | PR-02 |
| FR-030 | The daemon must honour a provider-set storage cap: if accepting a new chunk assignment would exceed the cap, the daemon must decline the assignment and inform the microservice. | P1 | PR-05 |
| FR-031 | The daemon must auto-start on OS boot using the platform-appropriate mechanism (Windows Service, macOS LaunchDaemon, Linux systemd unit) with no manual configuration by the provider. | P0 | ADR-009; reliability of provider uptime |

### 4.7 Provider — Exit and Departure

| ID | Requirement | Priority | ADR / Notes |
|----|-------------|----------|-------------|
| FR-032 | The system must allow a provider to announce a planned offline period (0–72 hours) through the app. During a promised offline period, audit FAILs must not decrement the reliability score, and no escrow penalty must be applied unless the period is overrun. | P0 | ADR-007 |
| FR-033 | If a provider overruns a promised offline period, the system must reclassify the state to Permanent Silent Departure and apply the 72-hour departure consequences. | P0 | ADR-007 |
| FR-034 | The system must allow a provider to announce a permanent departure. On announcement, the system must immediately queue repair for all affected chunks, release the provider's pending escrow proportional to the fraction of the current 30-day window completed, and set `providers.status = DEPARTED`. | P0 | ADR-007, ADR-024 |
| FR-035 | When a provider's last heartbeat exceeds 72 hours, the system must automatically declare a silent departure, seize the 30-day rolling escrow window into the repair reserve fund, trigger repair for all chunks, and block the provider's Peer ID from further interactions. | P0 | ADR-007, ADR-024 |
| FR-036 | A provider declared as silently departed who attempts to reconnect must receive HTTP 403. Re-joining the network requires a full new re-registration. | P0 | ADR-007; prevents gaming the departure threshold |

### 4.8 Audit System

| ID | Requirement | Priority | ADR / Notes |
|----|-------------|----------|-------------|
| FR-037 | The microservice must issue exactly one audit challenge per assigned chunk per 24-hour period, with challenge timing randomised within the window to prevent providers from anticipating it. An exception is when the accelerated re-audit protocol of FR-041 is active for that provider, in which case the challenge cadence is controlled by the repair scheduler. | P0 | ADR-002, ADR-014 |
| FR-038 | Every challenge nonce must be 33 bytes: a 1-byte version prefix identifying the server_secret version, followed by HMAC-SHA256(server_secret_vN, chunk_id + server_ts) where server_ts is set by the microservice at challenge time. The version byte enables any replica to validate any nonce across failover. Providers must not be able to compute nonces in advance. | P0 | ADR-017, ADR-027 |
| FR-039 | Every audit event must produce an INSERT-only row in `audit_receipts` containing the 14 schema fields defined in ADR-017 (BYTEA(33)) and subsequent amendments in ADR-014 jit_flag and ADR-015's abandoned_at and a UNIQUE index on challenge_nonce, signed by both provider (Ed25519) and microservice (Ed25519). No row in this table may ever be updated or deleted except the audit_result column which allows UPDATE from NULL (PENDING) to PASS/FAIL as part of the idempotent retry protocol. | P0 | ADR-015, ADR-017 |
| FR-040 | The microservice must use a per-provider TCP-style RTO (RTO = AVG + 4 × VAR of recent audit response times) as the challenge timeout, not a fixed value. New providers must default to the pool-median RTO. | P0 | ADR-006, Paper 28 |
| FR-041 | If a provider's content_hash verification fails when reading a chunk for an audit response, the provider daemon must immediately report `audit_result = FAIL` with a corruption error code, and the microservice must queue accelerated re-audit of all chunks on that provider within the next polling cycle. | P0 | ADR-023, Paper 32 |

### 4.9 Repair System

| ID | Requirement | Priority | ADR / Notes |
|----|-------------|----------|-------------|
| FR-042 | The repair scheduler must trigger a repair job when the available fragment count for any chunk drops to s + r0 = 24 (8 above the reconstruction floor of s=16). | P0 | ADR-004 |
| FR-043 | The repair scheduler must prioritise repair jobs triggered by confirmed permanent departures (72-hour threshold) over jobs triggered by the r0 pre-warning threshold. Transient-absence jobs must wait behind permanent-departure jobs and must be promoted to permanent-departure priority if they have been queued for more than 6 hours without service | P0 | ADR-004, Paper 39 |
| FR-044 | Repair must be triggered immediately (regardless of the 72-hour threshold) if the fragment count for any chunk drops to s=16 (the reconstruction floor). | P0 | ADR-004 emergency floor |
| FR-045 | During repair, replacement providers must be selected with the same 20% ASN cap constraint that governs original chunk assignment. | P0 | ADR-014 |
| FR-067 | The repair scheduler must treat is_vetting_chunk = TRUE as a hard exclusion from all repair job creation. This applies to all trigger types (SILENT_DEPARTURE, ANNOUNCED_DEPARTURE, THRESHOLD_WARNING, EMERGENCY_FLOOR). A vetting chunk that falls below any redundancy threshold is simply a vetting provider departing; it requires no repair. | P0 | ADR-030, ADR-004 |

### 4.10 Payment System

| ID | Requirement | Priority | ADR / Notes |
|----|-------------|----------|-------------|
| FR-046 | All payment amounts must be computed and stored as integer paise (₹1 = 100 paise). Floating-point arithmetic must not appear anywhere in the payment calculation or storage path. | P0 | ADR-016 |
| FR-047 | Every payout API call to Razorpay must include the `X-Payout-Idempotency` header set to SHA-256(provider_id + audit_period). Duplicate payout calls with the same key must return the existing payout state without creating a second transfer. | P0 | ADR-012, Paper 35 |
| FR-048 | Monthly earnings release must run on the 23rd of each month. Razorpay's `on_hold_until` must be set to the last working day of the current month (accounting for RBI bank holidays via a static lookup table updated each December), targeting release within the first 3 business days of the following month. | P0 | ADR-024, Paper 35 |
| FR-049 | The release multiplier must be applied per provider based on their 30-day reliability score: 1.00 (score ≥ 0.95), 0.75 (0.80–0.94), 0.50 (0.65–0.79), 0.00 (< 0.65). Withheld portions must roll into the next month's escrow window, not be seized. However withheld amounts that have been rolled forward for more than 90 days without release must be reviewed and either released or seized by the operations team. | P0 | ADR-024 |
| FR-050 | If the 7-day reliability score drops more than 0.20 below the 30-day score, the dual-window flag must be set and the next release must use the lower multiplier of the two score windows. | P0 | ADR-024, Paper 31 |
| FR-051 | During the vetting period (first 4–6 months), the hold window must be 60 days and the release cap must be 50%. After vetting is complete, the hold window must revert to 30 days with no release cap. | P0 | ADR-024 |
| FR-052 | The microservice must refuse any request to transfer a provider's escrow balance to a different provider_id. Escrow is identity-bound and non-transferable. | P0 | ADR-024, Paper 33 |

### 4.11 Network Readiness Gate

| ID | Requirement | Priority | ADR / Notes |
|----|-------------|----------|-------------|
| FR-053 | The assignment service must return HTTP 503 for all data owner upload requests until all seven readiness conditions are simultaneously satisfied: ≥ 56 active vetted providers, ≥ 5 distinct ASNs, ≥ 3 distinct Indian metro regions, full (3,2,2) microservice quorum, ≥ 56 Razorpay Linked Accounts with 24-hour cooling complete, ≥ 3 relay nodes deployed, and the cluster audit secret loaded on all replicas. The GET /api/v1/admin/readiness endpoint (FR-054) must query the payment microservice for the count of providers whose Razorpay Linked Accounts have a cooling_complete_at timestamp that has passed. The assignment service must not cache this count — it must be a live query per evaluation cycle | P0 | ADR-029 |
| FR-054 | The microservice must expose a `GET /api/v1/admin/readiness` endpoint that returns the current pass/fail status of each of the seven readiness conditions, re-evaluated every 60 seconds. | P0 | ADR-029; operational monitoring |

### 4.12 Provider Daemon — Simulation Mode

| ID | Requirement | Priority | ADR / Notes |
|----|-------------|----------|-------------|
| FR-055 | The provider daemon must support a `--sim-count=N` flag that launches N simulated provider instances in a single process, each with isolated key pairs, RocksDB instances, and vLog files, for local integration testing without physical machines. | P0 | ADR-029; cannot build without testability |
| FR-056 | Simulation mode must not bypass the network readiness gate; a simulation with `--sim-count=56` and `--sim-asn-count=5` must be required to satisfy the same readiness conditions as production before uploads are permitted. | P0 | ADR-029; simulation must proxy production behaviour |

### 4.13 Provider — Pre-Registration Earnings Calculator

| ID | Requirement | Priority | ADR / Notes |
|----|-------------|----------|-------------|
| FR-057 | The system must provide a storage earnings calculator accessible before registration (i.e. without an account) that accepts as input: declared storage in GB, declared uptime target as a percentage (e.g. 95%), and the current storage rate in paise per GB per month. The calculator must output: gross monthly earnings (storage_gb × rate), estimated escrow hold (30% of gross during vetting; 0% after), and estimated net monthly payout. This calculator must be available on the marketing site and within the installer welcome screen. | P1 | PR-06; ADR-024 |

### 4.14 Data Owner — Escrow Management and Upload Resume

| ID | Requirement | Priority | ADR / Notes |
|----|-------------|----------|-------------|
| FR-058 | The system must provide an authenticated endpoint (`GET /api/v1/provider/receipts`) allowing a provider to retrieve all audit receipts where they are the responding provider, filterable by `chunk_id` and date range. This endpoint is the provider's primary dispute evidence path and must be available even after a provider's status is set to DEPARTED. | P1 | SY-02; ADR-015, ADR-017 |
| FR-059 | The system must allow a data owner to withdraw their available escrow balance — defined as the total balance minus the amount reserved to cover active file storage for the next 30 days — to their UPI-linked bank account. Withdrawal must use the Razorpay payout path with its own idempotency key (SHA-256(owner_id + withdrawal_request_id)) and must be blocked while any file upload is in-flight. | P1 | DO-04; ADR-011, ADR-016 |
| FR-060 | If the data owner client crashes or loses connectivity after some but not all 56 shard uploads have completed for a given segment, the client must be able to resume the upload on next launch without re-transmitting already-acknowledged shards. Upload session state (segment ID, list of provider IDs with acknowledgement status, and pointer file draft) must be persisted locally keyed by a session ID generated at upload start, and must be cleaned up only after the pointer file has been successfully stored with the microservice. | P0 | DO-01; crash safety |

---

## 5. Non-Functional Requirements

### 5.1 Durability

| ID | Requirement | Type | Target | ADR |
|----|-------------|------|--------|-----|
| NFR-001 | The system must sustain an annual file loss rate of less than 10⁻¹⁵ per stored file at the target provider MTTF of 300 days. | Durability | < 10⁻¹⁵/year | ADR-003 |
| NFR-002 | The 20% ASN cap (FR-009) is a co-requisite for NFR-001. Disabling the cap invalidates the durability guarantee regardless of erasure parameters. | Durability | Non-negotiable | ADR-003, ADR-014 |
| NFR-003 | The system must tolerate the simultaneous departure of any 40 of 56 fragment holders for any file without data loss or reconstruction failure. | Durability | 40-fault tolerance | ADR-003 |
| NFR-034 | No real data owner file shard may ever be assigned to a provider in VETTING status. The assignment service must enforce this at INSERT time for chunk_assignments. A vetting provider departure must produce zero repair jobs and zero impact on any data owner's file durability. | Durability | Zero data owner impact from vetting departures | ADR-030 |
| NFR-036 | In `VYOMANAUT_MODE=demo`, all durability guarantees scale proportionally to the demo RS(3,5) parameters. The fault tolerance is 2-of-5 simultaneous provider failures (not 40-of-56). The annual loss rate formula still applies; the result is lower because n and r are smaller. Demo data must not be treated as durably stored for any real use case. | Durability | Proportional to demo parameters | ADR-031 |

### 5.2 Availability

| ID | Requirement | Type | Target | ADR |
|----|-------------|------|--------|-----|
| NFR-004 | A file must be retrievable as long as any 16 of its 56 fragment holders are reachable. The client must retry across alternate providers without user intervention. | Availability | k=16 of n=56 | ADR-003, FR-016 |
| NFR-005 | The coordination microservice must absorb the failure of any single replica without interrupting audit scheduling, challenge dispatch, or payment processing. | Availability | Single-replica fault tolerance | ADR-025 |
| NFR-006 | A provider behind symmetric NAT must remain fully auditable via Circuit Relay v2, with relay one-way RTT below 50 ms from Indian cloud-hosted relay nodes. Total round-trip overhead (two relay legs) is therefore bounded at 100 ms — within the 614 ms audit deadline at standard throughput. | Availability | Relay RTT < 50 ms | ADR-021, Paper 30 |

### 5.3 Performance

| ID | Requirement | Type | Target | ADR |
|----|-------------|------|--------|-----|
| NFR-007 | Each provider must respond to an audit challenge within (256 KB / p95_measured_upload_throughput_kbps) × 1.5. For a provider with p95 throughput of 500 KB/s this is 768 ms. | Performance | Per-provider deadline | ADR-014 |
| NFR-008 | The audit challenge lookup path on the provider daemon (Bloom filter check + RocksDB lookup + vLog read + hash verification) must complete within 100 ms at p99 on SSD hardware and 200 ms at p99 on HDD hardware, under concurrent upload load. | Performance | p99 ≤ 100 ms SSD / 200 ms HDD | ADR-023 |
| NFR-009 | The AONT encoding pass for a full 14 MB segment must complete within 200 ms at p50 and a p99 target will be set after the Q16-1 benchmark protocol on hardware without AES-NI (minimum-spec Indian desktop: dual-core, no AES-NI, 2 GB RAM, 7200 RPM HDD). | Performance | p50 ≤ 200 ms | ADR-019, benchmarking-protocol.md |
| NFR-010 | The Argon2id master secret derivation at session start (t=3, m=64 MB, p=4) must complete within 500 ms at p50 on the minimum-spec target hardware. If it does not, parameters must be reduced per the fallback protocol in benchmarking-protocol.md. | Performance | p50 ≤ 500 ms | ADR-020 |
| NFR-011 | The provider daemon must consume no more than 5% of CPU and remain within normal desktop I/O load during steady-state operation, defined as fewer than 5 concurrent chunk write operations and the standard daily audit cycle. Peak load during bulk onboarding (many simultaneous chunk assignments) is excluded from this constraint. | Performance | ≤ 5% CPU at steady state | ADR-009 |
| NFR-012 | Steady-state repair bandwidth per provider must not exceed 100 Kbps at the target MTTF of 300 days and a network of 1,000 providers each storing 50 GB. At these parameters, BWavg ≈ 39 Kbps/peer per the Giroire formula. | Performance | ≤ 100 Kbps/provider | ADR-003, ADR-004 |
| NFR-013 | Write amplification in the provider storage engine must not exceed 1.1× at 256 KB chunk size, meaning a provider storing 50 GB of chunks must write no more than 55 GB to their storage device in total. | Performance | Write amplification ≤ 1.1× | ADR-023 |
| NFR-035 | The total repair bandwidth attributable to vetting provider departures must be zero. At any N, a vetting provider departure must not increment the repair queue depth or consume BWavg budget. | Performance | 0 Kbps repair per vetting departure. | ADR-030 |
| NFR-037 | In `VYOMANAUT_MODE=demo`, the Argon2id master secret derivation uses t=1, m=4096 KiB, p=1. The session-start latency target is p50 ≤ 50 ms on any modern laptop. The reduced parameters weaken offline brute-force resistance; this is an accepted trade-off for demo sessions where no real data is stored. | Performance | p50 ≤ 50 ms in demo | ADR-031 

### 5.4 Security and Privacy

| ID | Requirement | Type | Target | ADR |
|----|-------------|------|--------|-----|
| NFR-014 | The service must never hold, derive, or transmit any key that could decrypt any stored file. The microservice stores pointer file ciphertext but must never hold the decryption key. | Security | Zero-knowledge | ADR-019, ADR-020 |
| NFR-015 | All audit challenge responses must be unforgeable without the actual chunk data. (1) The response hash (proving possession) is SHA-256(chunk_data ‖ challenge_nonce). (2) Receipt replay prevention uses SHA-256(chunk_id ‖ challenge_nonce ‖ response_hash ‖ timestamp) as specified in ADR-015. Both must be verified by the microservice before countersigning. | Security | Computational unforgability | ADR-002 |
| NFR-016 | All provider-to-provider and provider-to-client connections must be authenticated at the transport layer (TLS 1.3 via QUIC, or Noise XX via TCP). A provider cannot impersonate another provider's Peer ID. | Security | Transport authentication | ADR-021 |
| NFR-017 | DHT lookup keys must be pseudonymous: chunk lookup uses HMAC-SHA256(chunk_hash, file_owner_key) so that a DHT observer cannot correlate lookup traffic with file identity. | Security / Privacy | DHT privacy | ADR-001 |
| NFR-018 | The cluster audit secret (server_secret) must be derived via HKDF from a cluster master seed stored in a secrets manager (Vault, AWS SSM, or GCP Secret Manager). It must never be written to disk in plaintext and must never be transmitted between replicas in cleartext. | Security | Key management | ADR-027 |
| NFR-019 | Poly1305 tag comparison in the pointer file decryption path must use constant-time comparison. A timing oracle on tag verification must not be introduced. | Security | Timing attack prevention | ADR-019 |
| NFR-020 | All challenge nonces must include a 1-byte version prefix identifying which server_secret version was used, so that any replica can validate any nonce across failover without coordination. | Security | Cross-replica correctness | ADR-027 |

### 5.5 Reliability and Correctness

| ID | Requirement | Type | Target | ADR |
|----|-------------|------|--------|-----|
| NFR-021 | The audit receipt table must be append-only for all completed rows. The sole permitted exception is a single UPDATE from audit_result = NULL (PENDING state) to a terminal state (PASS, FAIL, TIMEOUT), as required by the idempotent retry protocol in ADR-015. All other UPDATE and DELETE operations must be prohibited via Postgres row security policy. | Correctness | Tamper-evident log | ADR-015 |
| NFR-022 | The escrow_events table must be INSERT-only. Account balance must always be computed from the event log (SUM of deposits minus releases and seizures) and must never be stored as a mutable column. | Correctness | CRDT-safe ledger | ADR-016 |
| NFR-023 | The vLog write path on the provider daemon must serialise all appends through a single writer goroutine. Concurrent upload goroutines must not write to the vLog file handle directly. | Correctness | Data integrity | ADR-023 |
| NFR-024 | On provider daemon crash and restart, the daemon must scan the vLog tail and re-insert any index entries missing from RocksDB before accepting new audit challenges or upload requests. | Correctness | Crash recovery | ADR-023 |
| NFR-043 | The Postgres INSERT throughput ceiling for the `audit_receipts` schema — with row security policy enabled and both Ed25519 signature columns populated — must be benchmarked on a production-equivalent Postgres instance before V2 GA. The benchmark must measure the sustained INSERT rate at which p99 write latency first exceeds 50 ms and that value must replace the 5,000–10,000 rows/sec planning estimate in §27.4 of `architecture.md`. This benchmark is a V2 launch blocker. | Correctness | Measured ceiling before GA | ADR-015, architecture.md §28.4 |
| NFR-044 | The assignment service must enforce a per-provider chunk count ceiling at onboarding time derived from the active `NetworkProfile` and the provider's declared MTTF tier: at MTTF=180 days (desktop minimum), the ceiling is approximately 70 GB of chunk data; at MTTF=300 days (planning target), approximately 130 GB. A provider must not be assigned chunks that would push their steady-state repair bandwidth (BWavg, Giroire Formula 1) above 100 Kbps. This ceiling must be surfaced in the provider onboarding UI as a declared storage limit advisory. | Performance / Durability | BWavg ≤ 100 Kbps at observed MTTF | ADR-009, architecture.md §28.3 |
| NFR-045 | The provider daemon installer must verify that sufficient free RAM is available for the DHT record cache before completing installation. The minimum free RAM required scales with declared storage allocation: approximately 40 MB at 50 GB declared, 160 MB at 200 GB, 400 MB at 500 GB (all at 200 bytes per DHT record and one record per chunk). If the check fails, the installer must surface a warning with the shortfall amount and must not allow chunk assignment to proceed until the hardware requirement is met. | Performance | DHT record memory ≤ available free RAM | architecture.md §28.5, ADR-023 |
| NFR-046 | All Prometheus metric names exposed by the microservice and provider daemon must follow the pattern `vyomanaut_{subsystem}_{name}_{unit}` where subsystem matches the `internal/` package name and unit uses OpenMetrics conventions: `_total` for counters, `_seconds` for histograms, `_bytes` for gauges. Grafana dashboard JSON and alert rules reference these names by exact string; renaming a metric without simultaneously updating all dashboards and alert rules in the same PR is a breaking change. The complete set of mandatory metric names is defined in NFR-025 and NFR-026. | Observability | Stable metric names | architecture.md §23 |

### 5.6 Observability and Operability

| ID | Requirement | Type | Target | ADR |
|----|-------------|------|--------|-----|
| NFR-025 | The microservice must expose the following metrics (at minimum) to a Prometheus-compatible scrape endpoint: audit challenges issued, audit results by outcome (PASS/FAIL/TIMEOUT), repair queue depth, repair jobs completed, escrow events by type, microservice replica count by health state, and foreground DB read p99 latency. | Observability | Prometheus | ADR-025 |
| NFR-026 | The provider daemon must expose the following metrics to the local status interface: stored chunk count, audit response p99 latency, content hash failure count, heartbeat success rate, and pending earnings in paise. | Observability | Local daemon | ADR-009 |
| NFR-027 | The system must fire an alert if any of the following thresholds are crossed: repair queue depth > 1,000 jobs, audit TIMEOUT rate > 5% of challenges in a 1-hour window, content hash failure count > 0 on any provider in a rolling 7-day window, microservice healthy replica count < 3. | Operability | Alert thresholds | architecture.md §24 |
| NFR-028 | Background tasks in the microservice (view refresh, repair queuing, Merkle log compaction) must monitor the 99th-percentile of foreground DB read latency over the last 60 seconds. If it approaches 50 ms, background task allocation must be reduced. | Operability | Background throttling | ADR-025 |
| ID | Requirement | Type | Target | ADR |
| --- | --- | --- | --- | --- |
| NFR-038 | The CI pipeline must fail on any file in `internal/payment/` containing the pattern `float64\|float32\|FLOAT\|DECIMAL\|NUMERIC`. This check (`TestNoFloatArithmetic`) runs as a mandatory CI gate and may not be disabled or weakened by any PR. | Correctness | Zero float in payment path | ADR-016, NFR-046 |
| NFR-039 | The CI pipeline must fail on any document or migration file containing the pattern `challenge_nonce BYTEA(32)`. The correct length is always `BYTEA(33)`. | Correctness | Nonce length enforcement | ADR-027 |
| NFR-040 | The CI pipeline must fail on any source file containing a reference to a non-existent ADR number. The check pattern `ADR-039` (and any number above the current highest ADR) must produce a lint failure. This prevents stale references from accumulating in design documents. | Correctness | ADR integrity | All ADRs |
| NFR-041 | The CI pipeline must fail on any source file or migration containing a call to the Razorpay UPI Collect API (deprecated NPCI 28 February 2026). All deposit flows must use UPI Intent. This is enforced by a grep on known UPI Collect endpoint path strings. | Compliance | NPCI mandate | ADR-011, NFR-029 |
| NFR-042 | All Prometheus metric names defined in NFR-025 and NFR-026 must use the naming pattern `vyomanaut_{subsystem}_{name}_{unit}` (e.g. `vyomanaut_audit_challenges_issued_total`, `vyomanaut_repair_queue_depth`, `vyomanaut_provider_score`). Grafana dashboard JSON and alert rules reference these names by exact string; renaming a metric without updating all dashboard and alert references simultaneously is a breaking change. | Observability | Metric naming | architecture.md §23 |

### 5.7 Compliance and Payments

| ID | Requirement | Type | Target | ADR |
|----|-------------|------|--------|-----|
| NFR-029 | All UPI deposit flows must use UPI Intent (app-based selection) or QR code. UPI Collect flow must not be used. (UPI Collect is deprecated by NPCI as of 28 February 2026.) | Compliance | NPCI mandate | ADR-011, Paper 35 |
| NFR-030 | Every Razorpay payout API call must include the `X-Payout-Idempotency` header (mandatory as of 15 March 2025). Payout calls without this header are rejected by Razorpay. | Compliance | Razorpay API | ADR-012, Paper 35 |
| NFR-031 | The RBI bank holiday lookup table used to compute `on_hold_until` dates must be updated as part of the December release deployment each year. | Compliance | RBI | ADR-024 |

### 5.8 Privacy

| ID | Requirement | Type | Target | ADR |
|----|-------------|------|--------|-----|
| NFR-032 | DHT lookup traffic must not allow a passive network observer to correlate lookup requests with file identity. This is implemented by HMAC-pseudonymised DHT keys (chunk lookup key = HMAC-SHA256(chunk_hash, file_owner_key)) as specified in FR-053 reference and ADR-001. A monitoring node that records all DHT traffic must not be able to link any lookup to a specific file or data owner without the file_owner_key. | Privacy | Traffic unlinkability | ADR-001, ADR-017 |

### 5.9 End-to-End Latency

| ID | Requirement | Type | Target | ADR |
|----|-------------|------|--------|-----|
| NFR-033 | The p50 time from a data owner initiating a file upload (encoding pipeline starts) to receiving confirmation that all 56 signed upload receipts have been collected and the pointer file has been stored with the microservice must not exceed 3 minutes for a 100 MB file, measured on a provider network where the p50 provider upload throughput is 10 Mbps. This target excludes Argon2id derivation time (session start, counted separately in NFR-010) and any time the data owner spends on the mnemonic confirmation step. | Performance | p50 ≤ 3 min for 100 MB | ADR-021 |

---

### 5.10 Mode Requirements

The following table maps each `NetworkProfile` field to the functional or non-functional requirement it governs. In demo mode (`VYOMANAUT_MODE=demo`) the threshold value is overridden; the requirement logic is unchanged.

| NetworkProfile field | Governs | Production value | Demo value |
| --- | --- | --- | --- |
| `DataShards`, `ParityShards`, `TotalShards` | NFR-001, NFR-003, NFR-034 | 16, 40, 56 | 3, 2, 5 |
| `ShardSize` | Wire format constant — **not in NetworkProfile** | 262,144 B | 262,144 B |
| `LazyRepairR0` | FR-042, ADR-004 | 8 | 1 |
| `MinActiveProviders` | FR-053, ADR-029 | 56 | 5 |
| `MinDistinctASNs` | FR-053, ADR-014, ADR-029 | 5 | 5 (see §7.1 note) |
| `MinMetroRegions` | FR-053, ADR-029 | 3 | 1 |
| `MinRelayNodes` | FR-053, ADR-029 | 3 | 0 |
| `HeartbeatInterval` | FR-027, ADR-028 | 4 h | 30 s |
| `PollingInterval` | FR-037, ADR-006 | 24 h | 2 min |
| `DepartureThreshold` | FR-035, ADR-006, ADR-007 | 72 h | 10 min |
| `VettingMinPasses` | FR-026, ADR-005 | 80 | 5 |
| `VettingMinDuration` | FR-026, ADR-005 | 120 days | 5 min |
| `EscrowHoldWindow` | FR-049, ADR-024 | 30 days | 1 min |
| `ReleaseComputationInterval` | FR-048, ADR-024 | Calendar (23rd) | 2 min ticker |
| `Argon2Time`, `Argon2Memory`, `Argon2Threads` | FR-002, NFR-010 | 3, 64 MB, 4 | 1, 4 MB, 1 |
| `RequireSecretsManager` | NFR-018, ADR-027 | true | false (env var substitute) |
| `RequireQuorum` | NFR-005, ADR-025 | true | false (single instance) |
| `PaymentMode` | FR-006, FR-047, ADR-011 | `razorpay_live` | `mock` |
| `SkipMnemonicConfirm` | FR-003 | false | true |
| `RazorpayCoolingPeriod` | FR-025, ADR-024 | 24 h | 0 s |
| `ScoreWindowShort/Medium/Long` | ADR-008 | 24 h / 7 d / 30 d | 2 / 6 / 20 min |

**Adding a new mode-variable parameter.** Any new parameter that differs between demo and production must be added to `NetworkProfile` with explicit values in both `ProductionProfile` and `DemoProfile`. The Go struct-literal syntax enforces this at compile time — an omitted field is a compile error, not a silent zero-value default.

---

## 6. UX Considerations

### 6.1 Critical UX Moments

**Mnemonic backup (FR-003):** This is the highest-stakes UX moment in the product. If a
data owner does not back up their mnemonic and later loses their passphrase, they lose their
data permanently. The UI must:

- Present the 24 words on a single screen with no surrounding UI noise.
- Explicitly state "These words are the ONLY way to recover your files if you forget your
  passphrase. Write them down now."
- Block progression until the owner correctly enters at least two randomly selected words.
- Never offer to store the mnemonic in the app, email it, or copy it to the clipboard
  automatically.
- **Demo mode exception.** When `VYOMANAUT_MODE=demo`, `NetworkProfile.SkipMnemonicConfirm = true`. The mnemonic is displayed but the two-word confirmation step is skipped. This is acceptable only because demo sessions store no real data. Production must always require confirmation.

**Storage cost transparency (FR-013):** Providers see a storage rate in paise per GB per
month. Data owners see a monthly cost in rupees. Both must be derived from the same
underlying rate with no hidden fees. The cost display must update in real time as the data
owner selects files.

**Provider local status interface (FR-029):** The provider's primary question every day is
"is my daemon working and am I earning?" The local CLI or system tray interface must answer
this without the provider needing to understand reliability scores, escrow windows, or audit
mechanics. There is no web dashboard in V2 — all provider status information is surfaced
through the daemon's local status interface only. Translate technical state to plain language:

- "Your daemon is healthy. You've earned ₹340 this month."
- "Your machine was offline for 26 hours. Your score dropped slightly but your earnings
  are not affected."
- "Warning: your machine has been offline for 60 hours. Earnings may be held if it does
  not reconnect within 12 hours."

### 6.2 Edge States

**Empty state — new data owner with no files:** Show estimated cost for a hypothetical 100 GB
upload, a link to add escrow funds, and an upload button. Do not show a blank screen.

**Degraded file state (some fragments unavailable):** Show which file is degraded, explain
in plain language that repair is in progress, and give an estimated completion time. Do not
surface the fragment count or erasure parameters.

**Escrow balance too low:** When a data owner's escrow balance will run out within 7 days,
show a non-blocking banner with the top-up amount and a UPI deep link. When it runs out,
block new uploads (not retrieval) and show a clear error.

**Provider daemon not running:** If the daemon has not sent a heartbeat in 4 hours, the
provider's local status interface (tray icon or CLI) must indicate this with a warning and
instructions to restart the daemon. The microservice also tracks `last_heartbeat_ts` and
can surface this state to any operator monitoring tool.

---

## 7. Technical Considerations

### 7.1 Hard Constraints

- The encoding pipeline runs entirely on the data owner's device. Any cloud-offloaded
  encoding path (e.g., Szabó et al. proxy model, Paper 15) is explicitly rejected for V2
  because Vyomanaut has no ISP operator relationships. (Paper 15 break)
- Razorpay Escrow+ is not available to Vyomanaut — it requires NBFC registration. Route
  with `on_hold` is the only available partial-hold primitive. (Paper 35)
- All amounts must be integer paise. Float arithmetic in the payment path is a correctness
  violation, not just a style concern. (ADR-016)
- **ShardSize is a compile-time constant in both modes.** `ShardSize = 262,144` (256 KB) must not appear in the `NetworkProfile` struct. It is the only erasure coding parameter that does not vary between demo and production, because changing it would simultaneously break vLog entry sizing, the audit challenge wire framing, and the RocksDB index assumptions. A compiler-enforced test (`TestProfileShardSizeIsConstant`) verifies this on every commit. (ADR-031)

### 7.2 Key External Dependencies

| Dependency | Failure mode | Impact |
|------------|-------------|--------|
| Razorpay Route API | Payment releases pause | Audits and storage continue; providers experience delayed payments |
| Secrets manager (Vault / SSM) | Replicas cannot start | Challenge issuance halts on startup; existing instances run with cached secret for 5 minutes |
| Indian ISP infrastructure | Connectivity degradation | Covered by 40-fragment parity and relay infrastructure |
| RBI bank holiday calendar | Wrong release dates | Static table updated annually in December deployment |

### 7.3 Data Model Implications

- `audit_receipts`: INSERT-only, row security policy enforced at DB. Unique index on
  `challenge_nonce` for idempotent retry. `audit_result` column must accept NULL (in-flight
  state). `abandoned_at` column for GC of stale PENDING rows. Schema version must be **33
  bytes** for `challenge_nonce` (32-byte HMAC + 1-byte version prefix per ADR-027).
- `providers`: `last_known_multiaddrs JSONB`, `last_heartbeat_ts TIMESTAMPTZ`,
  `multiaddr_stale BOOLEAN` added per ADR-028.
- `escrow_events`: INSERT-only, idempotency_key UNIQUE, amount_paise BIGINT only.
- The files table (from ADR-020 §What the microservice stores) requires a `file_status ENUM(ACTIVE, DELETION_PENDING, DELETED)` column. Active file count for analytics is `COUNT(*) WHERE file_status = 'ACTIVE'` AND `owner_id = $1`.
- `chunk_assignments`: add `is_vetting_chunk BOOLEAN NOT NULL DEFAULT FALSE`. Make `segment_id` nullable (was `NOT NULL`); add CHECK: `(is_vetting_chunk = FALSE AND segment_id IS NOT NULL) OR (is_vetting_chunk = TRUE AND segment_id IS NULL)`. Make `shard_index` nullable; add CHECK: `(is_vetting_chunk = FALSE AND shard_index IS NOT NULL) OR (is_vetting_chunk = TRUE AND shard_index IS NULL)`. Add partial unique index: `UNIQUE (segment_id, shard_index) WHERE is_vetting_chunk = FALSE AND status IN ('ACTIVE', 'REPAIRING')`.
- `audit_receipts`: make `file_id` nullable (was `NOT NULL REFERENCES files`). Add CHECK: `file_id IS NOT NULL OR (provider's chunk_assignment.is_vetting_chunk = TRUE)`. In practice enforced at application layer: the audit scheduler sets `file_id = NULL`when issuing challenges for synthetic chunks.
- `providers`: the 10% vetting storage cap is computed at assignment time from `declared_storage_gb`; no new stored column is required. The audit scheduler must JOIN `chunk_assignments WHERE is_vetting_chunk = TRUE AND status = 'ACTIVE'` to count current synthetic allocation.

### 7.4 Benchmark Requirements Before Shipping

The following benchmarks from `docs/research/benchmarking-protocol.md` must pass on a
minimum-spec machine before V2 launches:

- **Q16-1:** AONT encoding throughput — p50 ≤ 200 ms per 14 MB segment without AES-NI.
- **Q18-1:** Argon2id session start latency — p50 ≤ 500 ms at t=3, m=64 MB.
- **Q27-1:** RocksDB rate limiter calibration — p99 audit latency ≤ 100 ms (SSD) at the
  highest compaction rate that does not violate it.
- **HDD-specific audit latency** (ADR-023): p99 ≤ 200 ms under active compaction on a
  7200 RPM consumer HDD.
- **Postgres audit INSERT ceiling (NFR-043):** sustained INSERT rate on `audit_receipts` schema with row security policy enabled at which p99 write latency first exceeds 50 ms. Must replace the planning estimate in architecture.md §28.4 before any launch milestone closes.

The step-by-step execution procedures for each benchmark above are in **§7.5**.

---

### 7.5 Benchmark Procedures

Execute all protocols below on a **minimum-spec test machine** before the relevant
subsystem ships. Minimum spec: dual-core ≤ 1.8 GHz Intel Celeron / old Pentium
(confirmed no AES-NI), 2 GB RAM, consumer 7200 RPM HDD, Ubuntu 22.04 LTS.

Results must be recorded in the build log with machine specs and pass/fail verdict.
All three benchmarks are V2 launch blockers (NFR-043 adds a fourth — Postgres INSERT
ceiling — which must be measured on a production-equivalent schema instance, not on
minimum-spec desktop hardware).

---

#### Q16-1 — AONT Encoding Throughput (ChaCha20 path)

**Closes:** NFR-009 (p50 ≤ 200 ms per 14 MB segment without AES-NI)

**Pre-condition:** Verify AES-NI is absent before running.

```bash
grep -o 'aes' /proc/cpuinfo | head -1
# Must return nothing. If it returns 'aes', switch machines.
```

**Protocol:**

```python
import time, os, hashlib
from Crypto.Cipher import ChaCha20  # pip install pycryptodome

results = []
for _ in range(100):
    plaintext = os.urandom(14 * 1024 * 1024)   # 14 MB segment
    K = os.urandom(32)
    t0 = time.perf_counter()
    cipher = ChaCha20.new(key=K, nonce=b'\x00' * 8)
    ciphertext = cipher.encrypt(plaintext)
    h = hashlib.sha256(ciphertext).digest()
    c_last = bytes(a ^ b for a, b in zip(K, h[:32]))
    results.append(time.perf_counter() - t0)

results.sort()
print(f'median={results[50]*1000:.1f}ms  p95={results[95]*1000:.1f}ms  p99={results[99]*1000:.1f}ms')
```

**Pass criteria:**

- median ≤ 200 ms, p99 ≤ 400 ms → **PASS**
- median > 200 ms in Python but ≤ 200 ms in Go implementation → investigate overhead; repeat with the production Go binary
- median > 200 ms in Go → re-evaluate segment size; any change requires co-ordinated ADR-003 + ADR-004 update

---

#### Q18-1 — Argon2id Session Start Latency

**Closes:** NFR-010 (p50 ≤ 500 ms at t=3, m=64 MB, p=4)

```python
import time
from argon2 import PasswordHasher  # pip install argon2-cffi

ph = PasswordHasher(time_cost=3, memory_cost=65536, parallelism=4, hash_len=32, salt_len=16)
times = []
for _ in range(10):
    t0 = time.perf_counter()
    ph.hash('test_passphrase_representative_length_32ch')
    times.append(time.perf_counter() - t0)

times.sort()
print(f'median={times[5]*1000:.0f}ms  min={times[0]*1000:.0f}ms  max={times[-1]*1000:.0f}ms')
```

**Pass criteria and fallback ladder:**

| Result | Action |
|---|---|
| median ≤ 500 ms | **PASS** — use t=3, m=64 MB, p=4 |
| 500 ms–1000 ms | Acceptable with spinner UI; no parameter change |
| > 1000 ms, step 1 | Try t=2, m=64 MB, p=4 |
| Still > 1000 ms, step 2 | Try t=3, m=32 MB, p=4 |
| Still > 1000 ms, step 3 | Try t=2, m=32 MB, p=4 |
| Any step passes | Update ADR-020 with confirmed parameters and machine spec |

Do not go below m=32768 KiB (32 MB). OWASP 2023 minimum for interactive login.
Demo mode uses t=1, m=4096 KiB, p=1 per `NetworkProfile.Argon2*` — do not benchmark demo parameters on this protocol.

---

#### Q27-1 — RocksDB Rate Limiter Calibration

**Closes:** NFR-008 (p99 audit latency ≤ 100 ms SSD / 200 ms HDD under concurrent writes)

**Setup:** WiscKey prototype with RocksDB index (chunk_id 32 B → vlog_offset uint64 + size uint32 = 44 B) and append-only vLog at 256 KB entries. Populate with 200,000 synthetic chunks (~50 GB equivalent).

**Concurrent workload for 10 minutes:**

- **Thread A (writer):** continuously append new 256 KB chunks to vLog + RocksDB index
- **Thread B (auditor):** 1 random point lookup per second; measure time from RocksDB lookup to vLog read completion

**Rate limiter sweep:** 5 / 10 / 20 / 40 MB/s / unlimited. Record `(rate_limiter_MB_s, p99_audit_ms, write_throughput_MB_s)` at each level.

**Pass criteria:** Highest rate limiter where p99 audit latency ≤ 100 ms (SSD) or ≤ 200 ms (HDD). Write throughput at the chosen rate must be ≥ 2 MB/s (providers at 5 Mbps upload = 0.625 MB/s — this is well within budget). Record chosen value in ADR-023.

---

#### Q17-1 — AES-NI Hardware Prevalence (Provider Fleet Estimate)

**Not a timed benchmark — a fleet characterisation method.** Both cipher paths must be production-quality regardless of result (ADR-019). This protocol sizes the ChaCha20 testing investment.

**Method 1 — CPU model correlation (do first):**

Indian home desktop/laptop market at the V2 target price point (₹20,000–₹50,000) runs predominantly Intel Core i3 Sandy Bridge or later, or AMD Ryzen 3 — both with AES-NI. Planning estimate: **85–90% of providers have AES-NI**. Remaining 10–15% are Celeron N-series or Atom.

Reference: Intel ARK database (`ark.intel.com`) — search by CPU family. AES-NI present from: Intel Core i3/i5/i7 ≥ Sandy Bridge (2011), Pentium G ≥ G630, Celeron G ≥ G530. Absent: Intel Atom Bay Trail / Silvermont, pre-2011 AMD.

**Method 2 — Steam Hardware Survey (quantitative):**
Visit `store.steampowered.com/hwsurvey/`, filter India region, note CPU family distribution, cross-reference Intel AES-NI support matrix.

**Method 3 — Provider onboarding telemetry (definitive):**
The daemon already performs CPUID detection at startup (ADR-019). Report result to microservice at registration. After 100 beta registrations the fleet fraction is empirically known. This replaces the estimate above.

**Action:** CPUID detection is non-negotiable regardless of fleet estimate. Implement from day one.

---

## 8. Analytics and Instrumentation

### 8.1 Primary Metric

**Data owner 30-day retention** — the fraction of data owners who have at least one active
file stored 30 days after their first upload. Target: ≥ 70% in the first 90 days of launch.

### 8.2 Provider Primary Metric

**Provider 90-day survival rate** — the fraction of registered providers who are still ACTIVE
(not DEPARTED) 90 days after their first chunk assignment. Target: ≥ 60%. This is the proxy
for MTTF validation. (Q08-1)

### 8.3 Key Events

| Event | Trigger | Purpose |
|-------|---------|---------|
| `data_owner_registered` | OTP verified, account created | Funnel top |
| `mnemonic_confirmed` | Owner correctly enters 2 of 24 words | Safety gate completion rate |
| `escrow_deposit_initiated` | UPI Intent flow started | Payment funnel |
| `escrow_deposit_confirmed` | Razorpay webhook received | Revenue recognition |
| `file_upload_started` | Encoding pipeline begins | Upload funnel top |
| `file_upload_completed` | All 56 upload receipts received | Upload conversion |
| `file_upload_failed` | Any hard error (network, provider, escrow) | Error analysis |
| `file_retrieved` | Canary verified, plaintext delivered | Retrieval success |
| `provider_registered` | OTP verified, Ed25519 key submitted | Provider funnel top |
| `provider_vetting_completed` | 80th consecutive audit pass AND 120 elapsed since first assignment | Vetting conversion |
| `provider_departed_announced` | Announced departure received | Voluntary churn |
| `provider_departed_silent` | 72h threshold crossed | Involuntary churn |
| `audit_pass` | PASS receipt countersigned | Core reliability signal |
| `audit_fail` | FAIL receipt countersigned | Reliability degradation |
| `audit_timeout` | RTO exceeded | Address / connectivity issue |
| `repair_job_queued` | Fragment count at r0 threshold | Durability health |
| `repair_job_completed` | Replacement fragments uploaded | Durability health |
| `payment_released` | Payout processed | Revenue distributed |
| `payment_seized` | Silent departure escrow seized | Financial SLA enforcement |

### 8.4 Guardrail Metrics (must not worsen)

- **Content hash failure rate per provider per day** — must remain < 1% across the fleet.
  Spikes indicate a cohort of failing disks.
- **Relay-dependent provider fraction** — must not exceed 45%. If it does, add relay
  infrastructure before the constraint becomes a reliability risk. (ADR-021, Q20-1)
- **Repair queue depth** — must not exceed 5,000 jobs. Above this, repair bandwidth is
  likely to exceed the 100 Kbps/provider budget.

---

## 9. Launch Plan

### 9.1 Phases

These target quarters are planning references, not commitments.

| Phase | Condition to exit | Upload gate | Target Quarter |
|-------|-----------------|-------------|---------------|
| **Internal** | All P0 FRs and NFR benchmarks pass | Disabled (internal team only) | Q3 2026 |
| **Private beta** | Network readiness gate satisfied (FR-053); relay nodes deployed | Enabled for 20 invited data owners and 100 providers | Q4 2026 |
| **Public beta** | Provider 30-day survival rate ≥ 50%; no data loss events; audit TIMEOUT rate < 5% | Open registration, escrow deposits enabled | Q1 2027 |
| **V2 GA** | Provider 90-day survival rate ≥ 60%; data owner 30-day retention ≥ 70% | Full public | Q2 2027 |

### 9.2 Feature Flags

| Flag | Default (internal) | Default (beta) | Purpose |
|------|--------------------|---------------|---------|
| `upload_gate_enabled` | false | true | Enforces FR-053 readiness conditions |
| `payment_releases_enabled` | false | true | Enables actual Razorpay payouts |
| `sim_mode_allowed` | true | false | Allows `--sim-count` flag on daemon |
| `provider_ui_enabled` | false | false | Enables provider local status interface tray app (P1 feature, replaces CLI-only mode) |

### 9.3 Risk Factors and Mitigations

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|-----------|
| Indian ISP CGNAT blocks QUIC for > 30% of providers | Medium | High | Confirmed TCP fallback path; identical hole-punch success rate (Paper 30) |
| Razorpay Route API changes break `on_hold_until` semantics | Low | High | Abstract behind PaymentProvider interface (ADR-011); monitor API changelog |
| Razorpay Linked Account cooling period delays launch | High | Medium | Pre-register provider accounts at least 48 hours before the target launch date. Track cooling completion per provider in providers.razorpay_cooling_complete_at. |
| Minimum-spec hardware benchmark fails (NFR-009/010) | Medium | High | Benchmarking protocol documented; fallback parameters defined for Argon2id |
| Provider pool does not reach 56 before anticipated launch | High | Critical | (1) Recruit providers through private beta before enabling data owner uploads; FR-053 enforces the gate. (2) Simulation mode (FR-055) allows full system verification and data owner testing with 56 virtual providers before any real provider is recruited; the internal phase can complete entirely without real providers. |
| Data owner loses passphrase and mnemonic simultaneously | Low per user, certain at scale | High | Disclosed clearly at onboarding (FR-005); no support path exists by design |
| Vetting GC instruction not delivered before provider begins receiving real assignments | Low | Medium | FR-064 enforces PENDING_DELETION status blocks new real assignments; real assignment query filters `WHERE status = 'ACTIVE' AND is_vetting_chunk = FALSE` |
| Demo mode used with real data | Low | Critical | The `[STARTUP] mode=DEMO` log line and the live-Razorpay guard rail (refuse to start if DEMO + live endpoint) are the primary mitigations. Operators must treat any data stored under `VYOMANAUT_MODE=demo` as non-durable and not confidential (Argon2id parameters are weakened). |

### 9.4 Rollback Plan

**Microservice:** Replicas are stateless (Postgres is the source of truth). Rollback by
redeploying the previous container image. The audit log is immutable; no data is lost.

**Provider daemon:** The vLog and RocksDB index are on the provider's local disk. A daemon
version rollback does not affect stored data. The new daemon version reads the existing
vLog on startup via crash recovery (NFR-024, crash recovery path).

**Payment:** Razorpay payouts that fail return to the master account automatically
(`payout.reversed` webhook). No manual recovery is needed. Idempotency keys prevent
double-payment on retry.

---

## 10. Research Questions

Questions are in three tiers: **Product** (business decisions blocking private beta),
**Telemetry** (can only be answered by observing the live V2 system; none block the build),
and **V3 Deferred** (valid questions explicitly out of scope for V2).

All Tier 1 build-blocker questions are resolved. See §11.3 for the closed record.

---

### 10.1 Product Open Questions

These require a business or product decision before private beta opens.

| # | Question | Owner | Due | Linked |
| --- | --- | --- | --- | --- |
| OQ-001 | What is the storage rate (paise per GB per month) that makes participation economically viable for providers at MTTF ≥ 180 days while keeping data owner costs below cloud alternatives? | Product | Before private beta | ADR-024, FR-013, Paper 40 |
| OQ-002 | What fraction of Indian home routers are behind symmetric NAT, requiring Circuit Relay v2 permanently? Global baseline is ~30% (Paper 30); Indian CGNAT prevalence may be higher. Measure via AutoNAT classification at provider registration (Q20-1). | Engineering | Private beta — 30 days post-launch | ADR-021, NFR-006 |
| OQ-003 | Does the dual-window partial hold trigger (0.20 drop in 7d vs 30d score) correctly catch degrading providers before the 72h threshold without penalising providers with legitimate weekend absences? (Q31-1) | Engineering | Private beta — 90 days post-launch | ADR-024 |
| OQ-004 | What RocksDB rate limiter value keeps p99 audit latency ≤ 200 ms under concurrent compaction on a consumer 7200 RPM HDD? Run benchmark protocol §7.5 Q27-1 before GA. | Engineering | Before V2 GA | ADR-023 |
| OQ-005 | At what observed BWavg does Hitchhiker code adoption for V3 become justified? Decision gate: if V2 BWavg exceeds 60 Kbps/peer over the first 6 months, implement. (Q39-1) | Engineering | 6 months post-launch | ADR-026 |

---

### 10.2 Engineering Open Questions — Telemetry

None of the following block the build. All require observing the live V2 system.
Questions marked **[monitoring note]** have their design decision locked; only the
empirical validation remains open.

**Provider economics and survival**

- **Q05-4** — What held-earnings percentage and vetting period length empirically achieve MTTF 180–380 days? Starting values in ADR-024: 30-day rolling hold, 50% release cap during vetting. Measure: provider survival curves by cohort; adjust multipliers if median provider tenure < 6 months.
- **Q08-1** — What is the actual MTTF of a financially-incentivised Indian desktop provider? Measure: survival analysis on the first provider cohort; compare to Bhagwan's unincentivised floor (median session ~1–2h) and Bolosky's corporate desktop ceiling (MTTF 290–380 days). Feeds: ADR-003 MTTF assumption validation.
- **Q08-3** — At what provider count does a 20%/day turnover rate produce more repair bandwidth than idle upload capacity can absorb? Framework: `turnover_rate × Qpeek / (N × 100 KB/s)`. The repair window table in `architecture.md §27.3` shows the window exceeds 12h at N < 500; the actual turnover rate is unknown until V2. Measure: log per-departure transfer volume against provider count.
- **Q20-3** — What held-earnings percentage empirically achieves MTTF 180–380 days from an initially unincentivised population? Measure: survival curves for the first provider cohort; compare against Trautwein's unincentivised floor (87.6% of sessions under 8h).
- **Q21-1** — What fraction of registered providers pass the 4–6 month vetting period vs are rejected or depart? Measure: cohort analysis at 6 months. If reject rate > 40%, investigate registration gate or vetting criteria.
- **Q29-1** — Does graduated penalty (warn at 48h, partial hold at 60h) retain more providers near the 72h boundary than binary seizure? Measure: survival curves for providers in the 48–72h absence zone. Feeds: ADR-007 potential refinement.
- **Q33-1** — What holding percentage and holding period maximise provider retention while maintaining deterrence? ADR-024 starting values: 30-day rolling hold, 50% cap during vetting. Measure: cohort analysis.
- **Q40-1** *(method resolved)* — At what provider count N does the Nash equilibrium stability condition bav/bc − 1 ≥ 2.0 hold comfortably? **Analysis:** the condition is satisfied at any positive storage rate and N > 1. The question is the specific N at which the margin becomes operationally comfortable. Measure: compute bav/bc after OQ-001 storage rate is set; validate against first-cohort economics.
- **Q40-2** — What is the empirical distribution of marginal storage cost among Indian home desktop providers? Measure: provider survey at V2 beta registration; estimate marginal cost from declared free disk space and hardware tier. Feeds: OQ-001 rate-setting.

**Network and transport**

- **Q14-1** — What fraction of Indian home ISPs block UDP, forcing all QUIC connections to the TCP fallback? Measure: log transport type per provider connection at registration; report UDP-block rate at 30 days.
- **Q14-3** — What is the false-positive rate of the JIT detector (`response_latency_ms < deadline × 0.3`) during QUIC connection migration events? Measure: correlate QUIC migration signals with latency spikes over the first month. If false-positive rate > 1%, add a migration flag to the audit receipt schema.
- **Q30-2** *(design locked — validation pending)* — **The DCUtR retry count is confirmed at 1** in `architecture.md §13` (ADR-021), based on Paper 30's 97.6% first-attempt success rate. Validation: monitor audit challenge p99 latency under relay at private beta. If p99 exceeds 400 ms, re-evaluate to retry count = 2.

**Scoring and audit**

- **Q06-3** — Does t=24h need production tuning? Measure: false-positive departure declarations (providers declared departed who return within 72h) over the first 3 months. Adjust if false-positive rate > 5%.
- **Q31-1** — What threshold drop in the 7d vs 30d score should trigger the dual-window hold? ADR-024 starting value: 0.20 drop. Measure: examine provider score trajectories before silent departures; the threshold should catch > 80% of departing providers before the 72h boundary.
- **Q34-2** — What fraction of a provider's declared upload bandwidth is consumed by repair in steady state? Measure: log per-provider repair transfer volume over the first 3 months; compare to Giroire BWavg prediction.

**Storage and disk**

- **Q23-1** — Is the ~10–15% write throughput penalty at lf=256 KB vs lf=512 KB observable on Indian desktop hardware? Measure: run Q27-1 benchmark protocol (§7.5) at both entry sizes. If gap > 20%, re-evaluate lf — requires co-ordinated ADR-003 + ADR-004 update.
- **Q27-2** — What is the empirical rate of within-provider burst failures (multiple chunks failing simultaneously)? Measure: log the count of chunks simultaneously invalidated per provider failure event. If multi-chunk bursts > 1% of events, evaluate sparse vLog pre-allocation.

**Failure correlation**

- **Q19-3** — Does the > 98% single-chunk failure rate (Facebook warehouse observation) hold in a P2P consumer desktop network with correlated failures? Measure: log failure event sizes over the first 6 months. If multi-chunk bursts > 2% of events, re-evaluate whether MSR repair bandwidth targets the right case.
- **Q36-1** — What are the bi-exponential failure model parameters (α, ρ₁, ρ₂) for Indian ISP failure events? Measure: after 6 months, fit G(α, ρ₁, ρ₂) to observed provider failure events grouped by ASN. Use resulting σ_correlated to refine V3 provisioning target.
- **Q38-1** — Does the 20% ASN cap empirically keep RS(16,56) in the analytically superior region under real Indian ISP correlated failures? Measure: after 6 months, compute the observed maximum correlated failure event size; confirm it remains below the 11-shard analytical bound.

**Payment**

- **Q35-2** — Is the Razorpay per-transaction payout fee model sustainable at V2 scale? Measure: aggregate payout fee expense at 1,000, 3,000, and 10,000 providers; confirm with Razorpay account manager.

**Provider onboarding**

- **Q01-5** — Does reliability-proportional assignment create a runaway Matthew effect where the top 5% of providers receive > 50% of new chunk assignments? Power of Two Choices in ADR-005 is the structural mitigation. Measure: track cumulative chunk assignment distribution vs reliability score percentile over the first 90 days. Trigger: if the top decile holds > 40% of assignments, add a concentration cap.
- **Q05-1** — What practical challenges does the central microservice pose at launch scale, and which satellite functions can be decentralised in V3? Measure: microservice latency percentiles, failure incidents, and operational overhead over the first 6 months.
- **Q05-7** — At what file size does per-segment pointer file metadata overhead become user-visible? Measure: track upload distribution at launch; compute metadata-to-data ratio by file size decile; set an inline threshold if the smallest decile shows > 5% metadata overhead.

---

### 10.3 V3-Deferred Questions

Valid questions explicitly out of scope for V2. Each references the V3 milestone they feed.

| ID | Domain | Question summary | Feeds |
| --- | --- | --- | --- |
| Q01-4 | Peer selection | Geographic proximity as an assignment criterion | Coral DSHT implementation |
| Q05-3 | Mobile providers | At what mobile MTTF does the business model break? | V3 mobile provider tier design |
| Q07-4 | Reputation | Can correlated failure detection be distributed using EigenTrust on repair-event interactions? | V3 distributed reputation |
| Q08-4 | Scoring | Should polling interval t be adaptive based on score history? | V3 reliability scoring model |
| Q08-5 | Scoring | Can the scorer distinguish a diurnally-absent provider from a permanently-departed one before t expires? | V3 reliability scoring model |
| Q09-4–6 | Mobile | Free-space fraction on Indian smartphones; mobile departure threshold; max safe lazy-update window at MTTF ~30 days | V3 mobile provider tier |
| Q13-2 | Repair | Should GossipSub be used for repair event propagation? | V3 repair scheduler |
| Q15-1 | Mobile encryption | Can client-side RS erasure coding on mobile fit within battery and CPU budgets? | V3 mobile encoding pipeline |
| Q19-1 | Erasure coding | ECWide locality in a P2P network with no rack topology | V3 wide-stripe optimisation |
| Q24-1, Q24-2 | Reputation | Minimum repair interactions before EigenTrust score is meaningful; replacement for microservice pre-trusted anchor | V3 distributed reputation and coordination |
| Q29-2 | Erasure + pricing | Should hot-band uploads specify different erasure parameters at upload time? | V3 hot band pricing and ADR-026 multi-tier design |
| Q31-2, Q33-2 | Reputation / economics | PSM ratings from repair interactions; multi-task incentive weight function | V3 distributed reputation; V3 hot band economics |
| Q34-1 | Storage engine | Within-vLog hot/cold tiering for frequently-accessed vs archival chunks | V3 provider daemon storage tiering |
| Q36-2 | Repair | Periodic chunk rebalancing to homogenise provider disk fill ratios | V3 repair scheduler + Dalle variance reduction |
| Q37-1, Q37-2 | Trust / audit scalability | At what provider concentration does microservice capture become a realistic threat? Minimum audit frequency for probabilistic sampling under SHELBY Theorem 1 | V3 Transparent Merkle Log; V3 audit scheduler |
| Q39-1 (V3 path) | Repair BW | Hitchhiker code adoption after V2 telemetry gate (OQ-005) triggers | V3 ADR-026 implementation |

---

## 11. Appendix

### 11.1 Research Basis

The non-functional requirements in this PRD are not targets chosen arbitrarily. Each derives
from a specific formula, measurement, or proof in the research log:

- **NFR-001** (10⁻¹⁵ loss rate): Giroire Formula 3 applied to s=16, r=40, r0=8, MTTF=300 days
  gives LossRate ≈ 10⁻²⁵ — four orders of magnitude below target.
- **NFR-007** (audit deadline): Derived from the Filecoin Seal timing principle (Paper 29 §3.4.2),
  adapted for timing-based (not cryptographic) outsourcing prevention.
- **NFR-009** (AONT encoding): RFC 8439 (Paper 17) Table B.1 — ChaCha20 at 75 MB/s on OMAP-class
  hardware gives 186 ms per 14 MB segment.
- **NFR-012** (repair bandwidth): Giroire Formula 1 at N=1,000, MTTF=300 days,
  D=50 TB gives BWavg ≈ 39 Kbps/peer.
- **NFR-013** (write amplification): WiscKey (Paper 27) Figure 10 — write amplification ≈ 1.0
  at 256 KB values.
- **NFR-035** (zero vetting repair bandwidth): Giroire Formula 2 (Paper 10) at N=1,000 gives Qpeek ≈ 793 GB per departure. At launch with N=56–200, the burst is proportionally larger relative to BWavg. Synthetic chunks eliminate this entirely for the vetting cohort.

### 11.2 Rejected Requirements

The following were considered and deliberately excluded:

| Rejected requirement | Reason |
|---------------------|--------|
| File deduplication across data owners | Violates zero-knowledge: deduplication requires comparing ciphertexts or plaintexts; both create privacy risks |
| Per-retrieval payment to providers | Swarm SWAP failed structurally (Paper 07). Ties payment to transfer layer, creates liability during microservice outages (ADR-012) |
| Convergent encryption | Explicitly rejected (Paper 16, ADR-022). Each AONT key K is fresh random per segment |
| Blockchain for payment | NBFC registration required; token price volatility; high onboarding friction (ADR-011) |
| Mobile providers in V2 | BWavg at MTTF=90 days ≈ 130 Kbps/peer, exceeding the 100 Kbps budget (ADR-010) |
| Public audit verification in V2 | Requires Transparent Merkle Log — deferred to V3 (ADR-015) |

---

### 11.3 Answered Questions

The following questions were open during the research phase and are now closed.
Answers are locked in the referenced ADRs and must not be re-opened without a
superseding ADR. The full resolution record is in `docs/research/answered-questions.md`.

#### Coordination and DHT

| Question | Answer | ADR |
|---|---|---|
| How to avoid the tracker as a single point of failure? | Kademlia DHT replaces the tracker for all peer and chunk discovery. | ADR-001 |
| How to pseudonymise chunk IDs in the DHT without breaking FIND_VALUE? | DHT lookup key = `HMAC-SHA256(chunk_hash, file_owner_key)` where `file_owner_key = HKDF(master_secret, "vyomanaut-dht-v1", file_id)`. The DHT never sees chunk_hash or file_id. | ADR-001 |
| How to set DHT branching factor and concurrency? | k-bucket size k=16, α=3 parallel lookups. O(log n / 3) round trips. | ADR-001, ADR-021 |
| What replaces blockchain as the neutral audit trail? | Write-once append-only audit log. Both provider and microservice sign each receipt with Ed25519. INSERT-only Postgres (row security policy). V3 upgrade: Transparent Merkle Log. | ADR-015 |
| What practical attacks does DHT pseudonymisation close? | Closes DSN Challenge 3 from the SoK survey — a monitoring node recording all DHT traffic cannot correlate lookups to files without the owner's master secret. | ADR-001 |

#### Erasure Coding and Repair

| Question | Answer | ADR |
|---|---|---|
| What is Qpeek at N=1,000, 50 GB/provider? | ~793 GB per failure event. At 100 Kbps/peer aggregate, repair completes in ~8 hours — within the 12-hour safety window. | ADR-003, ADR-004 |
| What is BWavg at target parameters? | ~39 Kbps/peer at MTTF=300 days, N=1,000, 50 GB/peer (Giroire Formula 1). | ADR-003 |
| At what correlated failure rate does RS(16,56) become worse than a simpler scheme? | Never, under the 20% ASN cap. The reversal condition (Paper 38) requires correlated failure size to approach r=40. The ASN cap bounds maximum correlated failure at ~11 shards, leaving 44 survivors — 28 above the reconstruction floor. | ADR-003, ADR-014 |
| What is the optimal lazy repair strategy? | Single r0=8 (desktop-only V2 collapses the tier model). Reduces bandwidth ~38× vs eager repair. | ADR-004 |
| Is hinted handoff needed for the (3,2,2) quorum? | No. If replica A is down during a write, replicas B and C both ACK (W=2 satisfied). Anti-entropy gossip reconciles A's state within seconds of its return. | ADR-025 |

#### NAT Traversal and Transport

| Question | Answer | ADR |
|---|---|---|
| Does Circuit Relay v2 violate the audit response deadline? | No. Relay RTT < 50 ms from Indian cloud regions. Two relay legs = < 100 ms overhead, within the 614 ms deadline at 5 Mbps. | ADR-021 |
| What is the latency cost of forcing 1-RTT for audit reconnects? | 5–90 ms depending on NAT type (5 ms same-city, ~40 ms cross-city, +50 ms for relay-dependent). Worst case 90 ms, well within the 614 ms deadline. 0-RTT remains disabled for audit interactions. | ADR-021 |
| How many relay nodes are required at launch? | 3 nodes (Mumbai AZ1, Mumbai AZ2, Chennai/Hyderabad), 128 concurrent reservations each = 384 slots. 4.3× headroom at 300 initial providers. Scale to 4th node when provider count exceeds 570 (45% CGNAT assumption) or 850 (30% baseline). | ADR-021 |
| Q20-1 — What fraction of Indian home routers are behind symmetric NAT? | Design-time assumption: 45% (conservative upper bound). Sources: Paper 30 measures ~30% globally; Indian ISPs have broader CGNAT deployment. Relay infrastructure is sized for 45% to be conservative. Scale trigger: provision 4th relay node before provider count exceeds 570 (45% assumption) or 850 (30% baseline). Empirical validation via AutoNAT classification telemetry post-launch does not block the build; it informs the scale trigger. If observed relay-dependent fraction <30%, the 4th node can be deferred. If >45%, provision the 4th node immediately. | ADR-021, Paper 30, architecture.md §27.2 |

**DCUtR retry count — confirmed at 1.** Paper 30 shows 97.6% first-attempt success rate from 4.4M traversal attempts. Setting retry count to 1 (not the libp2p default of 3) is correct and is confirmed in `architecture.md §13`. Validation: monitor audit challenge p99 latency under relay at private beta. (ADR-021)

#### Encryption and Key Management

| Question | Answer | ADR |
|---|---|---|
| Does code-then-encrypt with per-chunk keys improve on AONT-RS? | No. AONT-RS achieves 2^256 computational security with zero external key management. Code-then-encrypt with 56 keys per file offers no meaningful security improvement. | ADR-022 |
| Should the Ed25519 signing key be derived from master_secret or stored separately? | Store separately, encrypted under a key derived from master_secret (HKDF `"vyomanaut-keystore-v1"`). A compromised signing key can then be rotated without rotating master_secret or re-encrypting any data. | ADR-020 |
| What fraction of Indian desktop providers lack AES-NI? | Planning estimate: 10–15% lack AES-NI (Celeron N-series, Atom). Both cipher paths must be production-quality. CPUID detection at daemon startup is non-negotiable. | ADR-019 |

#### Erasure Code Selection

| Question | Answer | ADR |
|---|---|---|
| Are Clay / MSR codes feasible at (n=56, k=16)? | No. Sub-packetisation α = 40^16 ≈ 10^25 — computationally intractable (Paper 22). MSR and Clay codes are rejected. Hitchhiker (α=2) is the only viable V3 candidate if BWavg telemetry gate triggers. | ADR-026 |
| Are LRC codes viable? | No. Non-MDS; local group co-locality cannot be guaranteed in a consumer P2P network; repair benefit collapses to RS-level under Indian ISP conditions. | ADR-026 |

#### Storage Engine

| Question | Answer | ADR |
|---|---|---|
| Fixed or variable vLog entry size? | Fixed (262,212 bytes = 256 KB chunk + headers). GC tail advancement requires only arithmetic, no parsing. | ADR-023 |
| Is proactive continuous disk scrubbing justified? | No. The base UE rate is 2–6 per 1,000 drive days (Schroeder et al.). Scrubbing is reactive: triggered by the first audit FAIL. | ADR-023 |
| Does the chunk index fall in the hot, warm, or cold data regime? | Moot at 256 KB values. WiscKey eliminates value movement from compaction — write amplification ≈ 1.0 regardless of access regime. | ADR-023 |

#### Economic Mechanism and Payment

| Question | Answer | ADR |
|---|---|---|
| How should Razorpay `on_hold_until` be set to target first-3-business-days release? | Embed a static `rbi_bank_holidays_YYYY` table (updated each December). Monthly release job runs on the 23rd: set `on_hold_until` to the last working day of the current month. Route releases on the next business day, landing within the first 1–3 days of the following month. | ADR-024 |
| How should the service-denial attack be monitored? | Three layers: (1) structural — RS(16,56) requires > 40 simultaneous refusals; ASN cap limits any group to ~11. (2) scoring signal — 3 independent data owner retrieval failure reports from the same provider within 72h rolling window → 0.3× audit FAIL weight for 24h window. (3) V3 upgrade — repair-event interactions provide microservice-visible retrieval evidence. | ADR-014, ADR-008 |

#### Audit Scalability

| Question | Answer | ADR |
|---|---|---|
| At what provider count does daily full audit become infeasible? | ~100,000 providers × 10,000 chunks (planning estimate). At N=10,000 × 10,000 chunks, challenge rate is 1,157/sec — well within Postgres capacity. At 100,000 × 10,000 = ~11,574/sec, sharding or probabilistic sampling is needed. SHELBY Theorem 1 must be re-verified if sampling is introduced. | ADR-002 |