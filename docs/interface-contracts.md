# Vyomanaut V2 — Interface Contracts

**Status:** Authoritative — backend, protocol, and integration engineers follow this document.
Where this document conflicts with an ADR, the ADR wins. Where it conflicts with
`architecture.md`, the ADR wins. Where it conflicts with `requirements.md`,
`requirements.md` wins.
**Version:** 1.0
**Date:** April 2026
**Author:** Vyomanaut Engineering
**Repository:** https://github.com/masamasaowl/Vyomanaut_Research
**Supersedes:** —
**Companion documents:**
- [`openapi.yaml`](./openapi.yaml) — authoritative REST/HTTP surface
- [`data-model.md`](./data-model.md) — canonical database schema and invariants
- [`architecture.md`](./architecture.md) — system overview and component descriptions
- [`requirements.md`](./requirements.md) — functional and non-functional requirements
- [`ADR-001`](../decisions/ADR-001-coordination-architecture.md) through [`ADR-029`](../decisions/ADR-029-bootstrap-minimum-viable-network.md) — all architectural decisions

---

## Table of Contents

1. [Purpose and Scope](#1-purpose-and-scope)
2. [Component Communication Map](#2-component-communication-map)
3. [REST / HTTP Contracts](#3-rest--http-contracts)
   - [3.1 Heartbeat Multiaddr Update](#31-heartbeat-multiaddr-update)
   - [3.2 — Ed25519 Signing Conventions](#32-ed25519-signing-conventions)
   - [3.3 - Error Envelope Contract](#33-error-envelope-contract)
   - [3.4 - Readiness Gate Contract](#34-readiness-gate-contract)
4. [libp2p Protocol Contracts](#4-libp2p-protocol-contracts)
   - [4.1 Chunk Upload Stream Protocol](#41-chunk-upload-stream-protocol)
   - [4.2 Audit Challenge Protocol](#42-audit-challenge-protocol) 
   - [4.3 Circuit Relay v2 Reservation](#43-circuit-relay-v2-reservation)
   - [4.4 Repair Reconstruction Stream Protocol](#44-repair-reconstruction-stream-protocol)
   - [4.5 Vetting GC Protocol](#45-vetting-gc-protocol)
5. [Internal Go Package Contracts](#5-internal-go-package-contracts)
   - [5.1 `internal/crypto`](#51-internalcrypto)
   - [5.2 `internal/erasure`](#52-internalerasure)
   - [5.3 `internal/storage`](#53-internalstorage)
   - [5.4 `internal/p2p`](#54-internalp2p)
   - [5.5 `internal/audit`](#55-internalaudit)
   - [5.6 `internal/scoring`](#56-internalscoring)
   - [5.7 `internal/repair`](#57-internalrepair)
   - [5.8 `internal/payment`](#58-internalpayment)
   - [5.9 `internal/client`](#59-internalclient)
   - [5.10 `internal/vettingchunk`](#510-internalvettingchunk)
6. [PostgreSQL Row-Level Contracts](#6-postgresql-row-level-contracts)
7. [Razorpay Webhook Contracts](#7-razorpay-webhook-contracts)
   - [7.1 `virtual_account.payment.captured`](#71-virtual_accountpaymentcaptured)
   - [7.2 `payout.reversed`](#72-payoutreversed)
   - [7.3 `account.created`](#73-accountcreated)
8. [Secrets Manager Contract](#8-secrets-manager-contract)
9. [Package Import Constraints](#9-package-import-constraints)
10. [Naming Conventions](#10-naming-conventions)
11. [Forbidden Code Patterns](#11-forbidden-code-patterns)
12. [DHT Key Contract](#12-dht-key-contract)
13. [Versioning and Backwards Compatibility Rules](#13-versioning-and-backwards-compatibility-rules)

---

## 1. Purpose and Scope

This document is the single authoritative reference for the exact data contract between every
pair of components that communicate in the Vyomanaut V2 system. It exists because
[`openapi.yaml`](./openapi.yaml) covers only the HTTP/REST surface; the system's correctness
depends equally on the contracts governing libp2p wire messages, internal Go package
boundaries, PostgreSQL DML restrictions, and external webhook payloads.

This document is the enforcement point for the five invariants in
[`data-model.md §3`](./data-model.md#3-design-invariants). Any code path that would violate
an invariant must be rejected at review time; if a contract in this document permits such a
path, this document is wrong and must be corrected via PR before the code merges.

**In scope:**
- libp2p application protocol messages (framing, size limits, timeouts, 0-RTT policy)
- Internal Go package exported interfaces (signatures, pre/post-conditions, concurrency contracts)
- PostgreSQL per-table DML permissions (what roles may INSERT, UPDATE, DELETE, and under what conditions)
- Razorpay webhook payloads and idempotency contracts
- Secrets manager access patterns for the cluster audit secret
- DHT key derivation and validator contract

**Out of scope:**
- REST/HTTP endpoint schemas — defined exclusively in [`openapi.yaml`](./openapi.yaml); do
  not duplicate here
- Infrastructure provisioning details — covered in [`architecture.md §8`](./architecture.md#8-deployment-topology)
- Capacity calculations — covered in [`capacity.md`](./capacity.md)

**How to add a new interface.** Before implementing any new cross-component call:
1. Identify which section below covers it (or add a new section if it is a new interface class)
2. Write the contract here first — protocol ID, message schema, pre/post-conditions,
   error semantics, concurrency rules
3. Get the contract reviewed before writing the implementation
4. Reference this document from the PR description

---

## 2. Component Communication Map

The diagram below shows every communication link between system components, labelled with
the protocol used on that link. It is the single authoritative picture of which components
are allowed to talk to which. A code path that creates a communication link not shown here is
out of scope for V2 and requires a new ADR before implementation.

```mermaid
flowchart LR
    %% Component Communication Map — Vyomanaut V2
    %% Shows every permitted communication link and the protocol on each.
    %% A link not shown here requires a new ADR before implementation.

    DOC["Data Owner Client\n(desktop / web)"]
    MS["Coordination Microservice\n(3 replicas, gossip cluster)"]
    PD["Provider Daemon\n(×56+ desktops / NAS)"]
    RN["Relay Nodes\n(×3 minimum)"]
    PG["PostgreSQL\n(primary + 2 replicas)"]
    SM["Secrets Manager\n(Vault / AWS SSM / GCP)"]
    RZ["Razorpay\n(Route / SmartCollect / RazorpayX)"]
    DHT["Kademlia DHT\n(embedded in each provider daemon)"]

    DOC -- "HTTPS REST\n(upload assign, file register,\npointer fetch, balance)" --> MS
    DOC -- "libp2p QUIC / TCP+Noise XX\n(chunk upload stream)" --> PD
    DOC -- "libp2p QUIC\n(chunk download stream)" --> PD

    PD -- "HTTPS REST\n(register, heartbeat, depart)" --> MS
    <!-- Audit responses are returned on the same libp2p stream as the challenge (§4.2). No separate HTTP call. -->
    PD -- "libp2p QUIC / TCP+Noise XX\n(chunk transfer during repair)" --> PD
    PD -- "libp2p Kademlia DHT RPC\n(FIND_NODE, FIND_VALUE,\nPUT_VALUE)" --> DHT

    MS -- "libp2p QUIC / TCP+Noise XX\n(audit challenge dispatch)" --> PD
    MS -- "libp2p Circuit Relay v2\n(challenge via relay for sym-NAT)" --> RN

    RN -- "libp2p QUIC\n(relay forwarding)" --> PD

    MS -- "PostgreSQL wire protocol\n(TCP, TLS 1.3)" --> PG
    MS -- "HTTPS REST\n(secrets fetch, 5-min TTL cache)" --> SM
    MS -- "HTTPS REST\n(Razorpay API calls: transfers,\npayouts, accounts)" --> RZ
    RZ -- "HTTPS webhook POST\n(payment.captured,\npayout.reversed,\naccount.created)" --> MS
```

**Demo topology.** In `VYOMANAUT_MODE=demo`, the relay node box and the secrets manager box are absent from the physical topology (MinRelayNodes=0, RequireSecretsManager=false). The logical communication links they represent still exist in the code; they are simply not exercised. The mock PaymentProvider replaces the Razorpay box. The diagram above shows the production topology. (ADR-031)


### Cross-reference: diagram links to ADRs

| Link | Protocol | ADR |
|---|---|---|
| Data Owner Client → Microservice | HTTPS REST | [`ADR-001`](../decisions/ADR-001-coordination-architecture.md) |
| Data Owner Client → Provider Daemon | libp2p QUIC / TCP+Noise XX (chunk upload) | [`ADR-021`](../decisions/ADR-021-p2p-transfer-protocol.md) |
| Provider Daemon → Microservice | HTTPS REST (heartbeat, audit receipt) | [`ADR-028`](../decisions/ADR-028-provider-heartbeat.md), [`ADR-002`](../decisions/ADR-002-proof-of-storage.md) |
| Microservice → Provider Daemon | libp2p QUIC / TCP+Noise XX (challenge dispatch) | [`ADR-002`](../decisions/ADR-002-proof-of-storage.md), [`ADR-021`](../decisions/ADR-021-p2p-transfer-protocol.md) |
| Microservice → Relay Nodes | libp2p Circuit Relay v2 (for symmetric-NAT providers) | [`ADR-021`](../decisions/ADR-021-p2p-transfer-protocol.md) |
| Microservice → PostgreSQL | PostgreSQL wire protocol | [`ADR-013`](../decisions/ADR-013-consistency-model.md), [`data-model.md`](./data-model.md) |
| Microservice → Secrets Manager | HTTPS REST | [`ADR-027`](../decisions/ADR-027-cluster-audit-secret.md) |
| Microservice → Razorpay | HTTPS REST | [`ADR-011`](../decisions/ADR-011-escrow-payments.md) |
| Razorpay → Microservice | HTTPS webhook POST | [`ADR-011`](../decisions/ADR-011-escrow-payments.md) |
| Provider Daemon ↔ DHT | libp2p Kademlia RPC | [`ADR-001`](../decisions/ADR-001-coordination-architecture.md) |
| Provider Daemon ↔ Provider Daemon (repair) | libp2p QUIC / TCP+Noise XX | [`ADR-021`](../decisions/ADR-021-p2p-transfer-protocol.md), [`ADR-004`](../decisions/ADR-004-repair-protocol.md) |

### What this diagram does not show

- Internal PostgreSQL primary-to-replica replication — managed by the cloud provider; opaque
  to the application layer
- Razorpay's internal bank settlement rails — external to Vyomanaut; not a component
  Vyomanaut controls
- DHT gossip between provider daemon instances — this is libp2p-internal; the application
  layer interacts with the DHT only through the `internal/p2p` package interface

---

## 3. REST / HTTP Contracts

All REST/HTTP interface contracts — request schemas, response schemas, status codes, error
bodies, authentication requirements, and idempotency semantics — are defined exclusively in
[`openapi.yaml`](./openapi.yaml).

**Do not duplicate REST contracts here.** If a REST contract needs updating, update
`openapi.yaml`. If a REST contract is ambiguous, raise a PR against `openapi.yaml` with the
clarification before implementing against it.

Cross-references for key REST contract decisions that originated in ADRs:

| Contract concern | Source |
|---|---|
| `challenge_nonce BYTEA(33)` — 33 bytes, not 32 | [`ADR-027`](../decisions/ADR-027-cluster-audit-secret.md), [`requirements.md §9.3`](./requirements.md#93-hard-constraints) |
| All `amount_paise` fields are `int64`, never float | [`ADR-016`](../decisions/ADR-016-payment-db-schema.md), [`NFR-046`](./requirements.md#77-compliance-and-payments) |
| `X-Payout-Idempotency` header mandatory since 15 March 2025 | [`ADR-012`](../decisions/ADR-012-payment-basis.md), Paper 35 |
| HTTP 503 returned from `/api/v1/upload/assign` until readiness gate passes | [`ADR-029`](../decisions/ADR-029-bootstrap-minimum-viable-network.md), [`FR-053`](./requirements.md#611-network-readiness-gate) |
| UPI Intent only — UPI Collect deprecated 28 February 2026 | [`ADR-011`](../decisions/ADR-011-escrow-payments.md), [`NFR-029`](./requirements.md#77-compliance-and-payments) |

---

### 3.1 Heartbeat Multiaddr Update 

**Endpoint:** `POST /api/v1/provider/heartbeat` — schema defined in [`openapi.yaml`](./openapi.yaml).

**Interval:** Every 4 hours ± jitter of ±5 minutes (randomised to prevent thundering herd
after microservice restart). Timer is reset after each successful acknowledgement.

**Required fields in the signed payload:**
- `current_multiaddrs[]` — all current libp2p multiaddrs (QUIC, TCP, relay), ordered by
  preference
- `timestamp` — ISO 8601 UTC; rejected if skew > ±5 minutes from microservice clock
- `provider_sig` — Ed25519 over canonical JSON of all other fields (sorted keys, no trailing
  whitespace, `provider_sig` absent from the signing input)

**Effect on challenge dispatch:** After a successful heartbeat, `providers.last_known_multiaddrs`
is updated and `providers.multiaddr_stale` is set to `false`. The microservice uses this
column as the **primary** address source for audit challenge dispatch. DHT lookup is used only
as a fallback when `multiaddr_stale = true` (after 2+ missed heartbeats).

**Effect on departure detection:** `providers.last_heartbeat_ts` is updated. The departure
detector compares this value to `NOW() - INTERVAL '72 hours'`.

**Token refresh integration with the heartbeat goroutine:**

The provider daemon must check the JWT remaining TTL on every heartbeat cycle.
Pseudocode for the heartbeat goroutine:

```go
func heartbeatLoop(profile NetworkProfile) {
      ticker := time.NewTicker(profile.HeartbeatInterval)
      for range ticker.C {
          // Refresh token if less than 24 hours remaining.
          if tokenExpiresIn() < 24*time.Hour {
              if err := refreshToken(); err != nil {
                  log.Error("token refresh failed; will retry next cycle", err)
              }
          }
          sendHeartbeat()
      }
  }
```

On first startup after a cold-storage absence (e.g. daemon was stopped for >7 days),
the daemon may hold an expired token. It must attempt token refresh before sending the
heartbeat. If the token is beyond the 1-hour grace period and refresh fails, the daemon
must prompt re-registration (full OTP flow) on its local status interface.

**Refer:** ([`ADR-028`](../decisions/ADR-028-provider-heartbeat.md))

### 3.2 Ed25519 Signing Conventions

All Ed25519 operations in Vyomanaut use the same key material, format, and verification
procedure. This section is the canonical reference; all protocol-specific sections cite it.

**Key generation and storage:**

- Provider Ed25519 key pair: generated by the daemon at first launch via crypto/rand.
  Stored in the daemon's local keystore encrypted under a key derived from
  DeriveKeystoreEncKey() (§5.1). The public key is transmitted to the microservice
  at registration and stored in providers.ed25519_public_key (32 bytes).
- Data owner Ed25519 key pair: generated by the client at registration. Public key in
  owners.ed25519_public_key. Used only for pointer file integrity signatures (ADR-020).
- Microservice signing key: stored in the secrets manager at path
  /vyomanaut/signing-key/v1. Loaded at startup. Used for service_sig on audit receipts.

**Canonical signing input serialisation:**
All signing inputs are first hashed: provider_sig = Ed25519(private_key, SHA-256(input_bytes)).
The input_bytes for each payload are defined in the relevant protocol section (§4.1, §4.2)
and must be constructed as a fixed-layout byte sequence, not as JSON serialisation.
JSON serialisation MUST NOT be used for signing inputs — field ordering is not guaranteed
across Go versions.

**Verification procedure:**
1. Retrieve the signer's public key from providers.ed25519_public_key (for provider sigs)
   or from the microservice's known signing public key (for service_sig).
2. Compute SHA-256(input_bytes) using the identical layout as the signer.
3. Call ed25519.Verify(pubKey, sha256Digest, signature). Return ErrInvalidSignature if false.

**What constitutes a valid signature at the microservice:**
- len(sig) == 64 (Ed25519 signatures are always 64 bytes)
- ed25519.Verify returns true
- The public key in providers.ed25519_public_key matches the Peer ID of the connection
  (verified at transport layer — these must not diverge)

**What constitutes an invalid signature:**
- len(sig) != 64
- Verify returns false
- The signing public key does not match the registered provider
In all invalid cases: record audit_result = FAIL with a distinct corruption error code;
do not return ErrInvalidSignature to the caller as a retriable error.

---

### 3.3 Error Envelope Contract

All error responses from the microservice REST API use a standard JSON body.
(Full schema in openapi.yaml; semantic definitions here are authoritative.)

**Standard error body:**
{
  "error_code":  string,   // machine-readable; never localised
  "message":     string,   // human-readable; may change between releases
  "request_id":  string,   // UUIDv7; matches X-Request-ID response header for tracing
  "retry_after": int|null  // seconds; non-null when HTTP 429 or 503 with a known backoff
}

**HTTP status codes and Vyomanaut semantics:**

| HTTP Status | error_code | Trigger |
|---|---|---|
| 400 | INVALID_REQUEST | Missing required field, wrong type, nonce length != 33 |
| 401 | UNAUTHENTICATED | Missing or expired session token |
| 403 | PROVIDER_DEPARTED | Provider's status == 'DEPARTED'; re-registration required |
| 403 | ESCROW_FROZEN | Provider account frozen pending seizure |
| 409 | INSUFFICIENT_ESCROW | Data owner balance < 30-day storage cost (FR-014) |
| 503 | NETWORK_NOT_READY | Readiness gate not satisfied (FR-053); retry_after = 60 |
| 503 | INSUFFICIENT_ASN_DIVERSITY | Cannot place 56 shards across ≥ 5 ASNs; retry_after = 300 |
| 503 | RAZORPAY_UNAVAILABLE | Razorpay API call failed; payment path only |
| 500 | INTERNAL_ERROR | All other server-side failures |
| 500 | VETTING_CAP_EXCEEDED | synthetic chunk cap reached for this provider; assignment service will retry when existing chunks are retired. |
| 500 | REAL_SHARD_ON_VETTING_PROVIDER | attempt to assign a real shard to a VETTING provider; internal error; never surfaced to clients. |
| 500 | DEMO_MODE_REAL_PAYMENT | Startup guard: demo mode + live Razorpay endpoint detected; process refuses to start |
| 500 | PROD_MODE_ENV_SECRET | Startup guard: prod mode + VYOMANAUT_CLUSTER_MASTER_SEED env var detected; process refuses to start |


**Error propagation from Razorpay failures:**
- Razorpay 4xx (bad request from microservice side): log at ERROR, surface as INTERNAL_ERROR
  to the caller — do not expose Razorpay error details in the API response.
- Razorpay 5xx or timeout: surface as RAZORPAY_UNAVAILABLE with retry_after.
- Never forward Razorpay error bodies to API callers; they may contain PII or keys.

The error_code `INSUFFICIENT_ASN_DIVERSITY` must include an additional field:
  "available_asns": int  // current count; helps operators diagnose readiness issues

---

### 3.4 Readiness Gate Contract

The readiness gate is a prerequisite check enforced by the assignment service on every
upload request and re-evaluated every 60 seconds. It is exposed externally at:
  `GET /api/v1/admin/readiness`

All seven conditions must be simultaneously true for uploads to be permitted. If any is
false, `POST /api/v1/upload/assign` returns `HTTP 503` with error_code `NETWORK_NOT_READY`
and `retry_after = 60`.

**Condition evaluation contract:**

| Condition | Data source | Production threshold | Demo threshold | Source |
| --- | --- | --- | --- | --- |
| Active vetted providers | `providers WHERE status IN ('VETTING','ACTIVE')` | ≥ 56 | ≥ 5 | `NetworkProfile.MinActiveProviders` |
| Distinct ASNs | `providers WHERE status IN ('VETTING','ACTIVE')` | ≥ 5 | ≥ 5 | `NetworkProfile.MinDistinctASNs` |
| Distinct metro regions | `providers WHERE status IN ('VETTING','ACTIVE')` | ≥ 3 | ≥ 1 | `NetworkProfile.MinMetroRegions` |
| Full quorum | Gossip membership | 3 healthy replicas | 1 instance (quorum disabled) | `NetworkProfile.RequireQuorum` |
| Razorpay accounts with cooling | `providers WHERE razorpay_cooling_until < NOW()` | ≥ 56 — LIVE QUERY | ≥ 5 (mock, cooling = 0 s) | `NetworkProfile.MinCooledAccounts` |
| Relay nodes | Relay heartbeat table | ≥ 3 | 0 | `NetworkProfile.MinRelayNodes` |
| Cluster audit secret | In-memory on all replicas | Loaded from secrets manager | Loaded from env var | `NetworkProfile.RequireSecretsManager` |

**GET /api/v1/admin/readiness response.** The `"required_value"` field in each condition
object is populated from the active `NetworkProfile.Min*` field, not from a hardcoded constant. Consumers of this endpoint must not assume the value is always 56 for the provider count condition — in demo mode it is 5.

The cooling_complete count is a live query per ADR-029. The assignment service MUST NOT
cache this value between evaluations — it must re-query each 60-second cycle.

**GET /api/v1/admin/readiness response body:**
{
  "ready": bool,
  "conditions": {
    "active_providers": { "value": int, "threshold": 56, "met": bool },
    "distinct_asns":    { "value": int, "threshold": 5,  "met": bool },
    "metro_regions":    { "value": int, "threshold": 3,  "met": bool },
    "quorum_healthy":   { "value": int, "threshold": 3,  "met": bool },
    "razorpay_cooled":  { "value": int, "threshold": 56, "met": bool },
    "relay_nodes":      { "value": int, "threshold": 3,  "met": bool },
    "audit_secret":     { "value": bool,                 "met": bool }
  },
  "evaluated_at": "ISO8601"
}

---

## 4. libp2p Protocol Contracts

This section specifies the application-level wire protocol for every libp2p stream opened
between components. The transport layer (QUIC v1 primary, TCP+Noise XX fallback) and NAT
traversal (AutoNAT → DCUtR → Circuit Relay v2) are governed by
[`ADR-021`](../decisions/ADR-021-p2p-transfer-protocol.md) and are not repeated here.

**Common rules that apply to every libp2p protocol in this section:**

1. **Transport authentication.** The remote Peer ID is cryptographically verified during the
   TLS 1.3 (QUIC) or Noise XX handshake before any application data flows.
   ([`ADR-021`](../decisions/ADR-021-p2p-transfer-protocol.md), [`NFR-016`](./requirements.md#74-security-and-privacy))
   A stream that opens to an unknown or unregistered Peer ID must be closed immediately.

2. **Protocol negotiation.** libp2p multistream-select is used for all protocol negotiation.
   The initiating side proposes the protocol ID string; the receiving side accepts or rejects.

3. **Stream lifecycle.** Each logical operation (one chunk upload, one audit challenge
   round-trip) occupies one independent QUIC stream or yamux substream. Streams are not reused
   across operations.

4. **Error signalling.** Application-level errors are encoded as a one-byte error code at the
   start of the response frame (see per-protocol tables). Transport-level errors (stream reset,
   connection close) propagate as Go `error` values from the libp2p API.

5. **Framing.** All messages use length-prefix framing: a 4-byte big-endian `uint32` length
   field precedes the payload. The maximum frame size for each protocol is specified below.
   A frame exceeding the maximum must cause the receiving side to reset the stream with error
   code `0x01` (FRAME_TOO_LARGE).

6. **Mode-invariant wire formats.** All frame sizes, field layouts, and protocol ID strings defined in §4 are identical in `VYOMANAUT_MODE=demo` and `VYOMANAUT_MODE=prod`. Demo mode only affects time windows, shard counts, and infrastructure dependencies — never the bytes on the wire. (ADR-031)

---

### 4.1 Chunk Upload Stream Protocol

This protocol carries a single 256 KB chunk from the data owner client (or the microservice
during repair) to a provider daemon. It is the only path by which chunk data enters the
provider's vLog.

**Protocol ID:** `/vyomanaut/chunk-upload/1.0.0`

**Participants:** Initiator = data owner client or microservice (repair); Responder = provider
daemon.

**0-RTT policy:** 0-RTT session resumption is **permitted** for this protocol. Replaying a
chunk upload causes the provider to store a duplicate vLog entry for the same `chunk_id`. The
RocksDB UNIQUE constraint on `chunk_id` will reject the second write cleanly; no authentication
or payment consequence results from a replay. ([`ADR-021`](../decisions/ADR-021-p2p-transfer-protocol.md))

**Message flow:**

```
Initiator                          Responder
    │                                   │
    │── UploadRequest (frame 1) ────────►│
    │                                   │── write to vLog (fsync)
    │                                   │── insert RocksDB index
    │◄─ UploadResponse (frame 2) ───────│
```

**Frame 1 — UploadRequest:**

| Field | Type | Size | Description |
| --- | --- | --- | --- |
| `length` | uint32 big-endian | 4 B | Total payload length: `32 + 4 + 72 + 262144 = 262252` bytes. |
| `chunk_id` | bytes | 32 B | SHA-256(chunk_data). Content address. Used as the RocksDB lookup key. |
| `shard_index` | uint32 big-endian | 4 B | Which of the `TotalShards` RS output shards this is (0 … TotalShards-1). |
| `capability_token` | bytes | 72 B | 8-byte `expiry_unix_ms` (int64 big-endian) \|\| 64-byte Ed25519 signature. See token format below. |
| `chunk_data` | bytes | 262144 B | Raw 256 KB AONT-RS encoded shard. |

Maximum frame payload: **262252 bytes** (updated from 262180). A frame with `length > 262252` is a FRAME_TOO_LARGE error.

**Capability token format:**

```go
signing_input = SHA-256(
    "vyomanaut-chunk-upload-cap-v1"   // domain-separation prefix
    || chunk_id          (32 bytes)
    || provider_id       (16 bytes, UUID bytes, big-endian)
    || file_id           (16 bytes, UUID bytes, big-endian)
    || expiry_unix_ms    (8 bytes, int64 big-endian)
)

capability_token = expiry_unix_ms (8 B) || Ed25519_sign(microservice_signing_key, signing_input)
```

The microservice signing key is the same key used to sign JWTs and `service_sig` on audit
receipts. Its public key is available at `GET /.well-known/jwks.json`.

Token lifetime: `NOW() + 3600 seconds` (1 hour). Chosen to exceed the maximum expected
upload duration for a 100 MB file on a 5 Mbps connection (~3 minutes per segment × many
segments), with generous headroom for retries.

**Provider verification steps (before writing anything to disk):**

1. `len(capability_token) == 72` — reject with `0x03` immediately if wrong.
2. Parse `expiry_unix_ms` from token bytes 0–7.
3. Check `expiry_unix_ms > NOW_unix_ms - 30_000` (30-second clock-skew grace).Use `0x07 CAPABILITY_EXPIRED` as the status byte (new code, see below).
4. Verify Ed25519 signature (token bytes 8–71) over `signing_input` using the cached
microservice public key. Reject with `0x03 NOT_ASSIGNED` if signature is invalid.
5. The `chunk_id` in the signing input is implicitly verified because the provider
re-derives `signing_input` using the `chunk_id` received in the frame header. A mismatch
causes signature verification to fail (step 4), which returns `0x03`.

**Frame 2 — UploadResponse:**

| Field | Type | Size | Description |
|---|---|---|---|
| `length` | uint32 big-endian | 4 B | Payload length. Success: 1 + 64 = 65 bytes. Error: 1 byte. |
| `status` | uint8 | 1 B | `0x00` = OK, chunk stored durably. `0x01` = FRAME_TOO_LARGE. `0x02` = CHUNK_ID_MISMATCH (SHA-256 of received data does not match `chunk_id`). `0x03` = NOT_ASSIGNED (provider is not the assigned holder for this chunk_id). `0x04` = STORAGE_FULL (provider has reached `declared_storage_gb` cap). `0x05` = INTERNAL_ERROR (vLog write or RocksDB insert failed). `0x06` = ALREADY_STORED — idempotent; treat as 0x00. `0x07` CAPABILITY_EXPIRED — token `expiry_unix_ms` is in the past. Data owner must request a fresh assignment from the microservice. |
| `provider_sig` | bytes | 64 B | Ed25519 signature by the provider over `SHA-256(chunk_id ‖ shard_index ‖ provider_id_bytes ‖ timestamp_unix_ms)`. Present only when `status = 0x00`. This is the upload receipt that the initiator must retain as proof of acknowledged storage. |

**Timeout:** The initiator must receive `UploadResponse` within 5,000 ms of sending
`UploadRequest`. If no response is received within this window, the initiator resets the stream
and may retry on a different connection.

**Pre-conditions on the responder (provider daemon):**
- The provider's `providers.status` must be `ACTIVE` or `VETTING` at the time the stream is
  accepted. If `status = DEPARTED`, the stream must be reset immediately with a TCP RST
  equivalent (stream reset, no application response).
- The `chunk_id` must match `SHA-256(chunk_data)`. If it does not, respond with
  `status = 0x02` before writing anything to disk.

**Post-conditions on a successful response (`status = 0x00`):**
- The chunk_data is durably written to the vLog (fsync completed).
- The RocksDB index entry `(chunk_id → vlog_offset, chunk_size)` is inserted.
- The `content_hash = SHA-256(chunk_data)` is embedded in the vLog entry.
- The provider_sig covers the stored content and timestamp, forming the upload receipt.

---

### 4.2 Audit Challenge Protocol

This protocol carries a single audit challenge from the microservice to the provider daemon
and returns the provider's signed audit response. It is the most latency-sensitive protocol
in the system — the response must arrive before the per-provider deadline.

**Protocol ID:** `/vyomanaut/audit-challenge/1.0.0`

**Participants:** Initiator = coordination microservice; Responder = provider daemon.

**0-RTT policy:** 0-RTT session resumption is **prohibited** for this protocol.
([`ADR-021`](../decisions/ADR-021-p2p-transfer-protocol.md))
An audit response carries a cryptographic proof tied to a specific nonce and timestamp. A
replayed response could falsely credit a provider with a PASS for data they no longer hold.
The QUIC connection for audit challenges must set `DisableEarlyData: true` on the TLS
configuration. The provider daemon must reject any `ClientHello` that contains early data
on this protocol ID.

**Message flow:**

```
Microservice                        Provider Daemon
    │                                   │
    │── ChallengeRequest (frame 1) ─────►│
    │                                   │── Bloom filter check
    │                                   │── RocksDB lookup
    │                                   │── vLog read (1 random disk I/O)
    │                                   │── content_hash verification
    │                                   │── compute response_hash
    │                                   │── sign receipt
    │◄─ ChallengeResponse (frame 2) ────│
```

**Frame 1 — ChallengeRequest:**

| Field | Type | Size | Description |
|---|---|---|---|
| `length` | uint32 big-endian | 4 B | Payload length. Must equal 32 + 33 + 8 = 73 bytes. |
| `chunk_id` | bytes | 32 B | SHA-256 content address of the chunk to prove. The provider uses this as the RocksDB lookup key. The microservice already holds this in chunk_assignments.chunk_id. |
| `challenge_nonce` | bytes | 33 B | 1-byte version prefix \|\| 32-byte HMAC-SHA256. **Must be exactly 33 bytes.** ([`ADR-027`](../decisions/ADR-027-cluster-audit-secret.md), [`requirements.md §9.3`](./requirements.md#93-hard-constraints)) A frame with a nonce of any other length must be rejected with `status = 0x03`. |
| `server_challenge_ts_ms` | int64 big-endian | 8 B | Unix timestamp in milliseconds, set by the microservice. The provider must embed this value in the signed receipt without modification — it is the anti-backdating mechanism. ([`ADR-017`](../decisions/ADR-017-audit-receipt-schema.md)) |

Maximum frame payload: 73 bytes.

**Frame 2 — ChallengeResponse:**

| Field | Type | Size | Description |
|---|---|---|---|
| `length` | uint32 big-endian | 4 B | Payload length. Success: 1 + 32 + 64 = 97 bytes. Error frames `0x01/0x02` are 1 + 64 = 65 bytes, not 1 byte |
| `status` | uint8 | 1B  | `0x00` = OK (PASS). `0x01` = FAIL_NOT_FOUND (Bloom filter absent — chunk not on this provider). `0x02` = FAIL_CORRUPTION (`SHA-256(chunk_data) ≠ content_hash` — disk corruption). `0x03` = INVALID_NONCE (nonce is not 33 bytes). `0x04` = INTERNAL_ERROR (vLog read failed for a reason other than corruption). |
| `response_hash` | bytes | 32 B | `SHA-256(chunk_data \|\| challenge_nonce)`. Present only when `status = 0x00`. |
| `provider_sig` | bytes | 64 B | Ed25519 signature by the provider over `SHA-256(response_hash \|\| challenge_nonce \|\| server_challenge_ts_ms \|\| provider_id)`. For `0x01/0x02`, signing input is `SHA-256(status_byte ‖ challenge_nonce ‖ server_challenge_ts_ms ‖ provider_id)`. This proves the provider deliberately reported FAIL rather than a transport drop. Present for `0x00`, `0x01`, `0x02`; absent for `0x03`, `0x04`. |

**Timeout:** The microservice must receive `ChallengeResponse` within the per-provider RTO:

```go
RTO = avg_rtt_ms + 4 × var_rtt_ms
```

>**NOTE:** This RTO governs the stream-level response timeout only. It is distinct from the JIT detection deadline (256 / p95_throughput_kbps) × 1.5 (§4.2 pre-conditions on the responder), which is the floor below which a response is flagged as anomalously fast. Both values are stored per-provider in the providers table.

Values are stored as `(avg_rtt_ms FLOAT, var_rtt_ms FLOAT)` per provider in the `providers`
table. New providers use the pool-median RTO until `rto_sample_count ≥ 5`.
([`ADR-006`](../decisions/ADR-006-polling-interval.md), [`FR-040`](./requirements.md#68-audit-system))

If the timeout elapses, the microservice records `audit_result = TIMEOUT` and resets the
stream. The provider daemon must not retry — the microservice will issue a new challenge at
the next scheduled audit cycle.

**Concurrency.** The microservice may open multiple concurrent audit challenge streams to a
single provider (one per assigned chunk per audit cycle). Each stream is independent. Stream
multiplexing is handled by QUIC. The provider daemon must handle at least 32 concurrent
challenge streams without queuing delay.

**Pre-conditions on the responder (provider daemon):**
- The provider must verify that `challenge_nonce[0]` (the version byte) corresponds to a
  currently-valid `server_secret_vN`. If the version byte refers to a retired secret (past
  the 24-hour rotation overlap window), the provider should respond with `status = 0x03`.
- The provider must verify `SHA-256(chunk_data) == content_hash` before computing
  `response_hash`. If this check fails, respond with `status = 0x02`.

**Post-conditions on `status = 0x00`:**
- `response_hash` = `SHA-256(chunk_data || challenge_nonce)` — unforgeable without the chunk
- `provider_sig` covers both the response and the server-set timestamp — prevents replay
- The provider has NOT recorded the receipt in any local database — the microservice owns
  the authoritative receipt record

>**NOTE:** The correctness of response_hash — that it was computed over the actual 256 KB chunk and not fabricated — is guaranteed by the computational hardness of SHA-256 preimage inversion, not by independent microservice verification. Please note the microservice never verifies the response_hash, it knows the chunk_id but not the 256 KB chunk data making the SHA-256(chunk_data || challenge_nonce) unverifiable. It only verifies the Ed25519 signature.

---

### 4.3 Circuit Relay v2 Reservation

Providers behind symmetric NAT cannot accept inbound connections directly. They must establish
a relay reservation with a Vyomanaut relay node before the microservice can dispatch audit
challenges to them. This section specifies the Vyomanaut-specific configuration layered on top
of the libp2p Circuit Relay v2 standard.

**Protocol ID:** `/libp2p/circuit/relay/0.2.0/hop` (standard libp2p; not a Vyomanaut-specific
protocol)

**Relay node multiaddrs:** Injected into the provider daemon at install time via the
`--relay-addrs` flag. Relay nodes are Vyomanaut-operated and co-located in Indian cloud
regions. The provider daemon attempts each relay in order until a reservation is confirmed.

**Reservation TTL:** 30 minutes (libp2p default). The daemon must refresh the reservation
before expiry. A refresh failure triggers an immediate retry to the next relay in the list.

**Concurrent reservations:** Each relay node supports 128 simultaneous active reservations.
([`ADR-021`](../decisions/ADR-021-p2p-transfer-protocol.md), [`capacity.md §5.2`](./capacity.md#52-relay-infrastructure-scaling))

**Effect on heartbeat:** When a relay reservation is active, the daemon includes the relay
multiaddr (`/p2p-circuit/p2p/<PeerID>`) in the `current_multiaddrs[]` array sent with each
heartbeat. The microservice stores this relay address alongside the direct addresses and
dials it when all direct addresses are unreachable.

**0-RTT policy:** Relay-forwarded streams inherit the 0-RTT policy of the application
protocol they carry. Audit challenge streams forwarded through a relay are still subject to
`DisableEarlyData: true`.

**Relay overhead constraint.** Relay-mediated connections must add < 50 ms RTT from
Indian cloud-hosted relay nodes. ([`NFR-006`](./requirements.md#72-availability)) This is
validated during the M8 launch milestone per [`mvp.md §Relay latency measurement`](./mvp.md#milestone-8--network-readiness-gate-and-private-beta).

---

### 4.4 Repair Reconstruction Stream Protocol

During repair, the repair scheduler (running within the microservice) contacts k=16
surviving shard holders, downloads their shards, reconstructs the AONT package via
RS decode, generates the missing parity shards, and uploads them to replacement providers.
This involves two sub-protocols on separate streams.

**4.4.1 — Repair Download Stream (microservice → existing holder):**

Protocol ID: /vyomanaut/repair-download/1.0.0
Initiator: Microservice repair scheduler
Responder: Surviving provider daemon

0-RTT policy: PROHIBITED. The repair download authenticates the microservice's right
to access the shard. A replayed stream could exfiltrate chunk data to an unauthenticated
party.

Authentication: The microservice presents its Peer ID (derived from its Ed25519 keypair,
distinct from any provider's keypair). The provider daemon must verify that the requesting
Peer ID is registered as a microservice replica in its locally-cached microservice peer list
(refreshed via DHT and heartbeat acknowledgements). Requests from unregistered Peer IDs
are rejected immediately with status 0x02 (NOT_AUTHORISED).

Frame 1 — RepairDownloadRequest:
| Field | Type | Size | Description |
|---|---|---|---|
| length | uint32 be | 4 B | Must equal 32 + 64 = 96 bytes |
| chunk_id | bytes | 32 B | Content address of the requested shard |
| repair_auth_sig | bytes | 64 B | Ed25519 signature by the microservice signing key over SHA-256(chunk_id ‖ request_ts_ms ‖ microservice_peer_id). Proves the request originates from a legitimate microservice replica. |

Frame 2 — RepairDownloadResponse:
| Field | Type | Size | Description |
|---|---|---|---|
| length | uint32 be | 4 B | Success: 1 + 262144 = 262145 B. Error: 1 B |
| status | uint8 | 1 B | 0x00=OK, 0x01=NOT_FOUND, 0x02=NOT_AUTHORISED, 0x03=CORRUPTION (content_hash mismatch), 0x04=INTERNAL_ERROR |
| chunk_data | bytes | 262144 B | Raw shard data. Present only when status=0x00 |

Timeout: 10,000 ms (longer than the upload timeout to account for cold disk reads).

**4.4.2 — Repair Upload Stream (microservice → replacement provider):**

Protocol ID: /vyomanaut/chunk-upload/1.0.0 — IDENTICAL to the standard upload protocol (§4.1).
The repair scheduler uses the same upload stream as the data owner client.
The replacement provider cannot distinguish a repair upload from a normal upload; it should not.
The microservice must pre-register the chunk assignment (INSERT into chunk_assignments) BEFORE
initiating the upload stream so the provider's NOT_ASSIGNED check (status 0x03) passes.

Post-repair confirmation: After receiving the upload receipt (Frame 2, status 0x00), the
repair scheduler marks the repair_job as COMPLETED and updates chunk_assignments.status from
REPAIRING back to ACTIVE for the new provider's row.

---

### 4.5 Vetting GC Protocol

On the ACTIVE transition for a provider previously in VETTING status, the microservice delivers a GC instruction listing all synthetic chunk IDs that the provider must delete from its vLog. This instruction is delivered via a dedicated libp2p stream rather than the REST heartbeat path because the chunk list can be large (up to `declared_storage_gb × 400`entries) and because the provider's HTTP session may not be active at transition time.

**Protocol ID:** `/vyomanaut/vetting-gc/1.0.0`

**Participants:** Initiator = coordination microservice; Responder = provider daemon.

**0-RTT policy:** **Prohibited.** A GC instruction causes the daemon to permanently delete data from its vLog via `DeleteChunk`. Replaying a GC instruction is idempotent at the `DeleteChunk` layer (deleting an already-deleted entry is a no-op), but the prohibition is maintained as a defensive policy because the instruction stream carries operational consequence. Set `DisableEarlyData: true` on the TLS configuration for this protocol.

**When the stream is initiated:**

1. Immediately after `providers.status` is set to `'ACTIVE'` in the database.
2. If the provider is offline, the microservice retries on the provider's next successful heartbeat connection (`POST /api/v1/provider/heartbeat` returns HTTP 200).
3. Retries use exponential backoff: 5 min → 15 min → 60 min → next heartbeat.

**Frame 1 — VettingGCRequest:**

| Field | Type | Size | Description |
| --- | --- | --- | --- |
| `length` | uint32 big-endian | 4 B | Payload length. `4 + (chunk_count × 32)` bytes. |
| `chunk_count` | uint32 big-endian | 4 B | Number of chunk IDs in this batch. Maximum 10,000 per frame. |
| `chunk_ids` | bytes[] | `chunk_count × 32 B` | Array of 32-byte chunk IDs (SHA-256 content addresses) to delete. These are all synthetic vetting chunk IDs where `is_vetting_chunk = TRUE AND provider_id = $1 AND status = 'ACTIVE'` at the time of the ACTIVE transition. |

If the provider holds more than 10,000 synthetic chunks, the microservice sends multiple sequential frames on the same stream (each a complete `VettingGCRequest`), waiting for `VettingGCResponse` after each before sending the next.

Maximum single frame payload: `4 + (10000 × 32) = 320,004 bytes`.

**Frame 2 — VettingGCResponse:**

| Field | Type | Size | Description |
| --- | --- | --- | --- |
| `length` | uint32 big-endian | 4 B | Payload length: `1 + ceil(chunk_count / 8)` bytes. |
| `status` | uint8 | 1 B | `0x00` = all deletions succeeded. `0x01` = partial failure (see `failure_bitmap`). `0x02` = INTERNAL_ERROR (vLog or RocksDB failure; microservice must retry the full batch). |
| `failure_bitmap` | bytes | `ceil(chunk_count / 8) B` | Present only when `status = 0x01`. Bit N is set if deletion of `chunk_ids[N]` failed. The microservice must retry failed entries on the next connection. Absent when `status = 0x00`. |

**Timeout:** The microservice must receive `VettingGCResponse` within 30,000 ms per frame. A longer timeout than the audit challenge (which processes one 256 KB read) is warranted because the daemon may be deleting thousands of RocksDB entries.

**Post-conditions on `status = 0x00` for a batch:**

- `DeleteChunk` has been called for each chunk ID in the batch on the daemon side.
- Each successfully deleted chunk ID should have its `chunk_assignments.status` set to `'DELETED'` by the microservice after receiving `status = 0x00`.

**Failure handling:**

- `status = 0x01`: Microservice retries only the failed chunk IDs on next connection. Successfully deleted chunks in the same batch are marked `'DELETED'` immediately.
- `status = 0x02`: Full batch retry on next connection. No `chunk_assignments` rows are marked `'DELETED'` — they remain `'PENDING_DELETION'` until the batch succeeds.
- Provider offline at transition time: all synthetic chunk rows set to `'PENDING_DELETION'`; stream initiated on next heartbeat. The audit scheduler must not issue challenges for `'PENDING_DELETION'` rows.

**Concurrency:** Only one vetting GC stream may be active per provider at a time. If a second ACTIVE transition is somehow triggered (e.g. after a re-registration), the microservice must complete the first GC stream before initiating another.

---

## 5. Internal Go Package Contracts

This section specifies the exported interface of every internal package, including:
- Exported function/method signatures (as compilable Go code)
- Pre-conditions: what must be true before the call (caller's responsibility)
- Post-conditions: what is guaranteed if the call returns `nil` error
- Error semantics: which errors are recoverable (`error` return), which are fatal
  (`panic` in debug builds)
- Concurrency contract: whether the exported surface is goroutine-safe and what
  synchronisation obligation falls on the caller

**Convention.** A function whose pre-condition is violated causes a `panic` in `debug`
build tags and returns a sentinel error in `release` builds. Pre-condition violations are
always bugs in the caller, not in the callee.

---

### 5.1 `internal/crypto`

Provides all key derivation and cipher primitives. All functions are **pure** (no shared
mutable state) and **goroutine-safe** by design — they take all inputs as arguments and
return all outputs as values.

```go
// Package crypto implements all cryptographic primitives for Vyomanaut V2.
// Every function is goroutine-safe. No function writes to shared mutable state.
//
// INVARIANT: No function in this package accepts a float64 or float32 parameter
// or returns one. All monetary calculations are delegated to internal/payment.
//
// See ADR-019 (ChaCha20-256 / AES-256-CTR), ADR-020 (HKDF key hierarchy).
package crypto

// DeriveAESNIAvailable detects hardware AES-NI support via CPUID (x86) or
// equivalent. Called once at daemon startup and stored as a package-level
// constant. Never re-checked at runtime.
//
// Pre-conditions: none.
// Post-conditions: returns true iff AES-NI instructions are available on this CPU.
// Goroutine-safe: yes (read-only hardware probe).
func DetectAESNI() bool

// DeriveFileKey derives a 32-byte file key using HKDF-SHA256.
//   file_key = HKDF-SHA256(ikm=masterSecret, salt=ownerID, info="vyomanaut-file-v1"||fileID)
//
// Pre-conditions:
//   - len(masterSecret) == 32  (panic in debug if violated)
//   - len(ownerID) == 16       (UUID bytes)
//   - len(fileID) == 16        (UUID bytes)
// Post-conditions:
//   - returns a 32-byte key that is deterministic for the given inputs.
// Error semantics: no errors returned; pre-condition violations panic.
// Goroutine-safe: yes.
func DeriveFileKey(masterSecret, ownerID, fileID []byte) [32]byte

// DerivePointerEncKey derives the 32-byte key used to encrypt a pointer file.
//   key = HKDF-SHA256(ikm=masterSecret, salt=ownerID, info="vyomanaut-pointer-v1"||fileID)
//
// Pre/post/error semantics identical to DeriveFileKey.
func DerivePointerEncKey(masterSecret, ownerID, fileID []byte) [32]byte

// DeriveKeystoreEncKey derives the 32-byte key used to encrypt the daemon's
// local keystore.
//   key = HKDF-SHA256(ikm=masterSecret, salt=ownerID, info="vyomanaut-keystore-v1")
//
// Pre/post/error semantics identical to DeriveFileKey.
func DeriveKeystoreEncKey(masterSecret, ownerID []byte) [32]byte

// DeriveMasterSecret derives the 32-byte master secret from the owner's passphrase
// using Argon2id. The cost parameters (time, memory, parallelism) are supplied by the
// caller from the active NetworkProfile rather than being hardcoded, so that demo mode
// can use reduced parameters without changing the code path.
//
// Pre-conditions:
//   - len(passphrase) >= 8     (panic in debug if violated)
//   - len(ownerID) == 16       (UUID bytes, used as Argon2id salt)
//   - argon2Time >= 1          (minimum 1 iteration)
//   - argon2Memory >= 4096     (minimum 4 MB; production uses 65536 KiB)
//   - argon2Threads >= 1
// Post-conditions:
//   - returns a 32-byte master secret, deterministic for the given inputs.
//   - execution time is governed by the supplied Argon2id parameters.
//     Production: >= 200ms. Demo: ~20-50ms.
// Error semantics: no errors returned; pre-condition violations panic.
// Goroutine-safe: yes (pure function).
func DeriveMasterSecret(passphrase, ownerID []byte, argon2Time uint32, argon2Memory uint32, argon2Threads uint8) [32]byte
// Caller responsibility. The microservice and provider daemon must pass profile.Argon2Time, profile.Argon2Memory, and profile.Argon2Threads from the active NetworkProfile. They must never hardcode Argon2id parameters inline. (ADR-031)


// EncryptPointerFile encrypts the serialised pointer file plaintext using
// AEAD_CHACHA20_POLY1305 (RFC 8439, ADR-019).
//
// Pre-conditions:
//   - len(key) == 32
//   - len(nonce) == 12         (96-bit counter; caller increments BEFORE this call)
//   - len(aad) > 0             (must include ownerID || fileID || schemaVersion)
// Post-conditions:
//   - returned ciphertext is len(plaintext)+16 bytes (plaintext + 16-byte Poly1305 tag)
//   - tag is over ciphertext with the supplied AAD
// Error semantics: returns error if the underlying cipher construction fails (should
//   never happen with correct inputs; treat as fatal if it does).
// Goroutine-safe: yes.
func EncryptPointerFile(key [32]byte, nonce [12]byte, aad, plaintext []byte) ([]byte, error)

// DecryptPointerFile decrypts and verifies a pointer file ciphertext.
// CRITICAL: The Poly1305 tag is verified with constant-time comparison before
// any plaintext is returned. (NFR-019, ADR-019)
//
// Pre-conditions:
//   - len(key) == 32
//   - len(nonce) == 12
//   - len(ciphertext) >= 16    (must include the 16-byte tag)
// Post-conditions (on nil error):
//   - returned plaintext is authenticated under the given key, nonce, and aad
//   - tag was verified with crypto/subtle.ConstantTimeCompare
// Error semantics:
//   - ErrTagMismatch: tag verification failed; caller MUST NOT use any returned bytes.
//   - Other errors: internal cipher failure; treat as fatal.
// Goroutine-safe: yes.
func DecryptPointerFile(key [32]byte, nonce [12]byte, aad, ciphertext []byte) ([]byte, error)

// AONTEncodeSegment applies the All-or-Nothing Transform to a plaintext segment.
// Cipher selection: ChaCha20-256 if aesNIAvailable==false, AES-256-CTR if true.
// The AONT key K is generated fresh via crypto/rand and embedded in the output package.
// (ADR-022, ADR-019)
//
// Pre-conditions:
//   - len(segment) is a multiple of 16 (segment is pre-padded by the caller to 4 MB minimum)
//   - aesNIAvailable must be the value returned by DetectAESNI() at startup
// Post-conditions:
//   - returns the AONT package: (s+1) 16-byte words where s = len(segment)/16
//   - the last word is K XOR SHA-256(all preceding codewords)
//   - the second-to-last word is the canary (fixed 16-byte value defined in aont_canary.go)
//   - K is not returned; it is embedded and inaccessible without assembling all s+1 words
// Error semantics: returns error if crypto/rand fails (treat as fatal).
// Goroutine-safe: yes.
func AONTEncodeSegment(segment []byte, aesNIAvailable bool) ([]byte, error)

// AONTDecodePackage recovers the plaintext segment from an AONT package.
// Also verifies the canary word after decryption. (FR-018, ADR-022)
//
// Pre-conditions:
//   - len(aontPackage) >= 32   (must have at least one data word, canary word, key-block)
//   - len(aontPackage) is a multiple of 16
//   - aesNIAvailable must be the value returned by DetectAESNI() at startup
// Post-conditions (on nil error):
//   - returned plaintext has the canary stripped
//   - canary has been verified; a wrong canary causes ErrCanaryMismatch
// Error semantics:
//   - ErrCanaryMismatch: the decoded segment is corrupt; caller MUST NOT return any
//     plaintext to the data owner. Zero the buffer before returning.
//   - Other errors: internal cipher failure; treat as fatal.
// Goroutine-safe: yes.
func AONTDecodePackage(aontPackage []byte, aesNIAvailable bool) ([]byte, error)

// DeriveDHTOwnerKey derives the 32-byte per-file DHT lookup key component.
// The DHT key used to store/find providers is HMAC-SHA256(chunk_hash, file_owner_key).
//   file_owner_key = HKDF-SHA256(
//       ikm  = masterSecret,
//       salt = ownerID,
//       info = "vyomanaut-dht-v1" || fileID,
//       len  = 32
//   )
//
// Pre-conditions:
//   - len(masterSecret) == 32
//   - len(ownerID) == 16  (UUID bytes)
//   - len(fileID) == 16   (UUID bytes)
// Post-conditions:
//   - returned key is deterministic for the given inputs
//   - key must be passed to DeriveDHTKey to produce the actual DHT lookup key
// Goroutine-safe: yes.
func DeriveDHTOwnerKey(masterSecret, ownerID, fileID []byte) [32]byte

// DeriveDHTKey produces the 32-byte DHT lookup key for a specific chunk.
//   dht_key = HMAC-SHA256(chunkHash, fileOwnerKey)
//
// Pre-conditions:
//   - len(chunkHash) == 32
//   - len(fileOwnerKey) == 32  (output of DeriveDHTOwnerKey)
// Goroutine-safe: yes.
func DeriveDHTKey(chunkHash, fileOwnerKey [32]byte) [32]byte

// Addtions made for the BIP-39 functions

// MasterSecretToMnemonic encodes the 32-byte master secret as a BIP-39 24-word
// mnemonic phrase. The mnemonic IS the master secret expressed as English words
// — it is not derived from the master secret; it encodes it directly.
//
// The encoding uses 32 bytes of entropy (256 bits) → 24 words with an 8-bit
// checksum appended, per BIP-39 §Generating the mnemonic.
//
// The BIP-39 English wordlist (2048 words) is the only permitted wordlist.
// Vyomanaut never uses passphrases on top of the mnemonic (BIP-39 §From mnemonic
// to seed is not used; the mnemonic itself IS the master secret, not a seed input).
//
// Pre-conditions:
//   - len(masterSecret) == 32  (panic in debug if violated)
// Post-conditions:
//   - returns exactly 24 lowercase English words from the BIP-39 wordlist
//   - MnemonicToMasterSecret(result) == masterSecret (round-trip identity)
// Error semantics: returns error only if the BIP-39 wordlist is missing or corrupt
//   (treat as fatal startup condition).
// Goroutine-safe: yes (pure function).
func MasterSecretToMnemonic(masterSecret [32]byte) ([]string, error)

// MnemonicToMasterSecret recovers the 32-byte master secret from a 24-word
// BIP-39 mnemonic. This is the recovery path for owners who have lost their
// passphrase but retained their mnemonic backup. (FR-004)
//
// Pre-conditions:
//   - len(words) == 24
//   - all words are lowercase entries in the BIP-39 English wordlist
// Post-conditions (on nil error):
//   - returned masterSecret is the 32-byte value encoded by this mnemonic
//   - MasterSecretToMnemonic(result) == words (round-trip identity)
// Error semantics:
//   - ErrInvalidMnemonic: wrong word count, unknown word, or BIP-39 checksum
//     failure. Caller must surface "Invalid recovery phrase — please check your
//     words and try again." Do not expose which word failed (timing oracle).
// Goroutine-safe: yes (pure function; no shared state).
func MnemonicToMasterSecret(words []string) ([32]byte, error)

// SelectConfirmationWords returns two distinct random word indices (0–23) for
// the mnemonic confirmation gate. The UI prompts the data owner to type the
// words at these positions before allowing the registration flow to proceed.
// (FR-003)
//
// In demo mode (profile.SkipMnemonicConfirm == true), the mnemonic is displayed
// but the caller skips prompting — this function may still be called; the caller
// simply does not block on user input.
//
// Pre-conditions:
//   - len(mnemonic) == 24  (panic in debug if violated)
// Post-conditions:
//   - 0 <= indexA, indexB <= 23
//   - indexA != indexB
//   - indices are drawn with crypto/rand (not math/rand)
// Goroutine-safe: yes.
func SelectConfirmationWords(mnemonic []string) (indexA, indexB int)
```

**Sentinel errors exported by this package:**

```go
var (
    ErrTagMismatch    = errors.New("crypto: Poly1305 tag verification failed")
    ErrCanaryMismatch = errors.New("crypto: AONT canary word mismatch after decode")
    ErrInvalidMnemonic = errors.New("crypto: invalid BIP-39 mnemonic") 
)
```

---

### 5.2 `internal/erasure`

Provides Reed-Solomon RS(s=16, r=40) encode and decode over GF(2^8). Implemented using
`github.com/klauspost/reedsolomon`. All functions are **goroutine-safe**.

```go
// Package erasure implements Reed-Solomon erasure coding.
// The data, parity, and total shard counts are NOT package-level constants —
// they are injected from the active NetworkProfile via NewEngine(profile).
// This allows demo mode (DataShards=3, TotalShards=5) and production mode
// (DataShards=16, TotalShards=56) to use identical code paths. (ADR-031)
//
// ShardSize (262,144 bytes) IS a package-level constant and must not change between
// modes. A compiler-enforced test verifies this.
package erasure

const ShardSize = 262144 // lf — 256 KB; fixed in both modes; never profile-variable

// Engine is the erasure coding instance parameterised by NetworkProfile.
// Create with NewEngine; do not use DataShards/TotalShards constants directly.
type Engine struct {
    DataShards   int // s — from NetworkProfile
    ParityShards int // r — from NetworkProfile
    TotalShards  int // n — from NetworkProfile
}

// NewEngine constructs an erasure Engine from the active NetworkProfile.
//
// Pre-conditions:
//   - profile.DataShards >= 1
//   - profile.TotalShards == profile.DataShards + profile.ParityShards
//   - profile.ShardSize == ShardSize (compile-time constant; verified by test)
func NewEngine(profile config.NetworkProfile) (*Engine, error)

// EncodeSegment splits an AONT package into e.TotalShards shards of ShardSize bytes.
// Pre-conditions:
//   - len(aontPackage) == e.DataShards * ShardSize exactly
// Post-conditions:
//   - returns exactly e.TotalShards byte slices, each of length ShardSize
//   - any e.DataShards of the returned shards can reconstruct the original package
// Error semantics: returns error only if the underlying GF arithmetic fails (fatal).
// Goroutine-safe: yes (no shared mutable state).
func (e *Engine) EncodeSegment(aontPackage []byte) ([][]byte, error)

// DecodeSegment reconstructs an AONT package from any e.DataShards of the e.TotalShards shards.
// Pre-conditions:
//   - len(shards) == e.TotalShards
//   - at least e.DataShards entries are non-nil
// Post-conditions:
//   - returns the reconstructed AONT package of length DataShards * ShardSize
//   - all nil entries in shards are filled in-place (the caller's slice is modified)
// Error semantics:
//   - ErrTooFewShards: fewer than DataShards non-nil shards provided
//   - ErrShardSize: a non-nil shard has the wrong length
//   - Other errors from klauspost/reedsolomon: treat as fatal.
// Goroutine-safe: yes.
func (e *Engine) DecodeSegment(shards [][]byte) ([]byte, error)

var (
    ErrTooFewShards = errors.New("erasure: fewer than 16 non-nil shards provided")
    ErrShardSize    = errors.New("erasure: shard has incorrect length")
)
```

---

### 5.3 `internal/storage`

Provides the WiscKey-style chunk storage engine: RocksDB index + append-only vLog.
([`ADR-023`](../decisions/ADR-023-provider-storage-engine.md), [`NFR-023`](./requirements.md#75-reliability-and-correctness))

**Critical concurrency rule.** `AppendChunk` is **NOT goroutine-safe**. It is designed to be
called exclusively from a single writer goroutine. All other goroutines must submit write
requests through a channel to that goroutine. Calling `AppendChunk` from multiple goroutines
concurrently produces undefined vLog corruption. This is Invariant 5 in the storage engine
design and is enforced in production by the daemon's upload manager.

```go
// Package storage implements the WiscKey key-value separated chunk storage engine.
// The vLog (value log) is an append-only file; the index is RocksDB.
//
// CONCURRENCY CONTRACT: AppendChunk is NOT goroutine-safe. It must only be called
// from the single designated writer goroutine. See ADR-023 §Single writer goroutine.
// All other exported functions are goroutine-safe (read-only paths).
package storage

// ChunkStore is the main storage interface. Create with NewChunkStore.
type ChunkStore interface {
    // AppendChunk writes a 256 KB chunk to the vLog and inserts the index entry.
    //
    // *** SINGLE WRITER ONLY — NOT goroutine-safe ***
    // Callers must ensure this is called from exactly one goroutine at a time.
    //
    // Pre-conditions:
    //   - len(chunkID) == 32         (SHA-256 content address)
    //   - len(chunkData) == 262144   (exactly 256 KB)
    //   - SHA-256(chunkData) == chunkID  (caller must verify before calling)
    //   - The vLog file handle is open and positioned at the end
    // Post-conditions (on nil error):
    //   - chunkData and its content_hash are durably fsync'd to the vLog
    //   - The RocksDB index entry (chunkID → vlog_offset, chunk_size) is inserted
    //   - The returned vlogOffset is the byte offset where the entry begins in the vLog
    // Error semantics:
    //   - ErrVLogFsync: the fsync failed; the vLog may be in an inconsistent state;
    //     the daemon must halt and restart (crash recovery will fix the tail on next start)
    //   - ErrRocksDBInsert: the index insert failed after a successful vLog write;
    //     the next startup's crash recovery scan will repair the missing index entry
    // Goroutine-safe: NO — single writer goroutine only.
    AppendChunk(chunkID [32]byte, chunkData []byte) (vlogOffset uint64, err error)

    // LookupChunk retrieves a chunk from the vLog by content address.
    // Verifies content_hash = SHA-256(chunk_data) before returning.
    //
    // Pre-conditions:
    //   - len(chunkID) == 32
    // Post-conditions (on nil error):
    //   - returned data is exactly 262144 bytes
    //   - SHA-256(returned data) == chunkID  (verified internally; no need to re-check)
    // Error semantics:
    //   - ErrChunkNotFound: Bloom filter or RocksDB has no entry for chunkID;
    //     this is the expected response for an audit challenge on an unassigned chunk
    //   - ErrContentHashMismatch: the data is present but its hash does not match;
    //     indicates silent disk corruption; caller must return audit FAIL with
    //     status=0x02 (FAIL_CORRUPTION) — see §4.2
    //   - ErrVLogRead: an I/O error reading the vLog; treat as fatal
    // Goroutine-safe: yes (read-only path via RocksDB + vLog pread).
    LookupChunk(chunkID [32]byte) ([]byte, error)

    // DeleteChunk removes a chunk from the index. The vLog entry is not immediately
    // freed; it is reclaimed during the next GC cycle.
    //
    // Pre-conditions:
    //   - len(chunkID) == 32
    // Post-conditions (on nil error):
    //   - The RocksDB index entry for chunkID is deleted
    //   - Subsequent LookupChunk calls for this chunkID will return ErrChunkNotFound
    //   - The vLog entry remains on disk until GC reclaims it
    // VETTING GC PATH: This function is also called by the vetting GC handler
    // (§4.5) to delete synthetic vetting chunks on the ACTIVE provider transition.
    // For synthetic chunks, the call semantics are identical: the chunk ID is removed
    // from RocksDB, subsequent LookupChunk calls return ErrChunkNotFound, and the
    // vLog space is reclaimed during the next GC cycle. The daemon has no visibility
    // into whether the deleted chunk was synthetic or real. (ADR-030)
    // Goroutine-safe: yes.
    DeleteChunk(chunkID [32]byte) error

    // RecoverFromCrash scans the vLog from the last known head offset and re-inserts
    // any RocksDB index entries that are missing. Must be called once at daemon startup
    // before the writer goroutine is started. (ADR-023 §Crash recovery, NFR-024)
    //
    // Pre-conditions:
    //   - AppendChunk has not been called since the store was opened
    //   - No writer goroutine is running
    // Post-conditions (on nil error):
    //   - All vLog entries up to the current EOF are reflected in RocksDB
    // Goroutine-safe: NO — must be called before the writer goroutine starts.
    RecoverFromCrash() error

    // RunGC performs garbage collection on the vLog, reclaiming space from deleted
    // chunks. Runs in a background goroutine; the caller provides a context for
    // cancellation. (ADR-023 §Garbage collection)
    //
    // Goroutine-safe: yes (GC uses a separate read handle from the writer goroutine).
    RunGC(ctx context.Context) error

    // Close flushes and closes the RocksDB instance and the vLog file handle.
    // After Close returns, the ChunkStore must not be used.
    Close() error
}

var (
    ErrChunkNotFound      = errors.New("storage: chunk not found")
    ErrContentHashMismatch = errors.New("storage: content hash mismatch (disk corruption)")
    ErrVLogFsync          = errors.New("storage: vLog fsync failed")
    ErrVLogRead           = errors.New("storage: vLog read failed")
    ErrRocksDBInsert      = errors.New("storage: RocksDB index insert failed")
)
```

---

### 5.4 `internal/p2p`

Provides peer identity management, transport stack initialisation, and the Kademlia DHT
interface. Goroutine-safe after construction.

```go
// Package p2p manages the libp2p host, transport stack (QUIC primary / TCP+Noise fallback),
// NAT traversal, and the Kademlia DHT with the custom HMAC key validator.
// (ADR-021, ADR-001)
package p2p

// Host is the primary libp2p interface. Constructed once at daemon startup.
// All methods are goroutine-safe.
type Host interface {
    // PeerID returns the local Ed25519-based libp2p Peer ID.
    // This is multihash(ed25519_public_key). (ADR-021)
    PeerID() peer.ID

    // Connect dials a remote peer at the given multiaddrs and performs the
    // transport-layer authentication handshake. Returns when the connection
    // is established and the Peer ID is verified.
    //
    // Pre-conditions:
    //   - len(addrs) >= 1
    // Post-conditions (on nil error):
    //   - A connection to peerID is established
    //   - The Peer ID of the remote is verified against peerID (not self-reported)
    // Error semantics:
    //   - ErrPeerIDMismatch: the remote peer's actual ID does not match peerID
    //   - ErrAllAddrsFailed: all provided addresses failed to connect
    Connect(ctx context.Context, peerID peer.ID, addrs []multiaddr.Multiaddr) error

    // NewStream opens a new application-level stream to peerID for the given
    // protocol. Requires an existing connection (call Connect first).
    //
    // 0-RTT policy: 0-RTT is disabled for protocols with IDs ending in
    // "-audit" or "-challenge". The caller does not need to enforce this; the
    // Host enforces it automatically based on the protocol ID. (ADR-021)
    NewStream(ctx context.Context, peerID peer.ID, protocolID protocol.ID) (network.Stream, error)

    // SetStreamHandler registers a handler for incoming streams of the given
    // protocol. The handler is called in a new goroutine per stream.
    SetStreamHandler(protocolID protocol.ID, handler network.StreamHandler)

    // NATType returns the current AutoNAT classification of the local node.
    // Updated periodically by the AutoNAT service.
    NATType() (natType autonat.NATStatus)

    // Close shuts down the libp2p host and all open connections.
    Close() error
}

// DHT is the Kademlia DHT interface with the custom HMAC key validator.
// (ADR-001 §DHT Key Contract)
type DHT interface {
    // PutProviderRecord announces that the local peer holds the value for the given DHT key. The key must be an HMAC-derived key (HMAC-SHA256(chunk_hash, file_owner_key)); the validator will reject any key that does not pass the HMAC format check.
    // Pre-conditions:
    //   - len(key) == 32  (HMAC output is always 32 bytes)
    //   - key was computed by crypto.DeriveDHTKey at upload time and is stored locally by the daemon (the daemon MUST NOT recompute it at republication time because file_owner_key is not available after upload completes)
    PutProviderRecord(ctx context.Context, key []byte) error

    // FindProviders returns up to maxCount peers that have announced holding
    // the value for key. The key must be an HMAC-derived key.
    FindProviders(ctx context.Context, key []byte, maxCount int) ([]peer.AddrInfo, error)

    // Bootstrap connects to the well-known seed nodes and fills the k-buckets.
    // Must be called once at daemon startup after the Host is created.
    Bootstrap(ctx context.Context) error
}

var (
    ErrPeerIDMismatch  = errors.New("p2p: remote Peer ID does not match expected")
    ErrAllAddrsFailed  = errors.New("p2p: all provided multiaddrs failed to connect")
    ErrDHTKeyInvalid   = errors.New("p2p: DHT key does not pass HMAC validator")
)
```

---

### 5.5 `internal/audit`

Provides the microservice-side audit challenge generation, dispatch, receipt validation, and
the two-phase crash-safe database write. Goroutine-safe.

```go
// Package audit manages the audit challenge lifecycle on the microservice side.
// (ADR-002, ADR-015, ADR-017, ADR-027)
package audit

// ChallengeNonce generates a 33-byte versioned challenge nonce.
//   nonce = version_byte || HMAC-SHA256(serverSecretVN, chunkID || serverTsMs)
//
// Pre-conditions:
//   - len(serverSecretVN) == 32
//   - versionByte == N mod 256 (must match the version of serverSecretVN)
//   - len(chunkID) == 32
//   - serverTsMs is current Unix time in ms (set by caller from time.Now())
// Post-conditions:
//   - returns a 33-byte nonce; nonce[0] == versionByte
// Goroutine-safe: yes.
func ChallengeNonce(serverSecretVN []byte, versionByte uint8,
    chunkID [32]byte, serverTsMs int64) [33]byte


// ValidateResponse verifies the structural and cryptographic properties of a provider's audit response that the microservice CAN verify without holding chunk_data:
//   1. len(challengeNonce) == 33
//   2. challengeNonce[0] identifies a currently-valid secret version
//   3. providerSig is a valid Ed25519 signature by providerPubKey over the canonical signing input (see §4.2 for the exact signing input bytes)
//
// LIMITATION: The microservice CANNOT verify that responseHash == SHA-256(chunkData ‖ challengeNonce) because it never holds chunkData. The correctness of responseHash depends on economic deterrence (incorrect hash → audit FAIL → score penalty → escrow risk) and the JIT detection mechanism (ADR-014 Defence 3). This is a stated design property, not a gap to be closed by adding verification code.
// What this function verifies: signature validity and nonce format only.
// What this function does NOT verify: that responseHash encodes the correct chunk content.
// Goroutine-safe: yes.
func ValidateResponse(challengeNonce [33]byte, responseHash [32]byte,
    providerSig [64]byte, providerPubKey [32]byte) error

// WriteReceiptPhase1 performs the crash-safe Phase 1 INSERT to audit_receipts.
// Inserts a PENDING row (audit_result = NULL) with the provider signature.
// Returns the receipt_id (UUIDv7) assigned to the row.
// (ADR-015 §Crash-safe receipt writing)
//
// Pre-conditions:
//   - All required receipt fields are non-zero
//   - The database connection is open
// Post-conditions (on nil error):
//   - A row with audit_result = NULL exists in audit_receipts
//   - The row is durable (WAL-flushed) before this function returns
// Error semantics: database errors are returned; caller must not proceed with
//   Phase 2 if Phase 1 fails.
// Goroutine-safe: yes (uses connection pool).
func WriteReceiptPhase1(ctx context.Context, db *sql.DB, fields ReceiptFields) (receiptID uuid.UUID, err error)

// WriteReceiptPhase2 performs the crash-safe Phase 2 UPDATE on audit_receipts.
// Sets audit_result, service_sig, and service_countersign_ts atomically.
// (ADR-015 §Crash-safe receipt writing, Invariant 1 in data-model.md)
//
// Pre-conditions:
//   - receiptID must identify an existing PENDING row (audit_result IS NULL)
//   - result must be PASS, FAIL, or TIMEOUT (not NULL)
//   - len(serviceSig) == 64
// Post-conditions (on nil error):
//   - The row is updated; audit_result is no longer NULL
//   - The row security policy permits this specific NULL → terminal transition
// Error semantics:
//   - ErrReceiptAlreadyFinal: the row already has a non-NULL audit_result (idempotent; caller
//     should treat this as success and return the existing service_sig)
//   - Other database errors: return to caller.
// Goroutine-safe: yes.
func WriteReceiptPhase2(ctx context.Context, db *sql.DB,
    receiptID uuid.UUID, result AuditResult,
    serviceSig [64]byte, serviceTS time.Time) error

// AuditResult is the terminal state of an audit receipt.
type AuditResult int

const (
    AuditPass    AuditResult = iota
    AuditFail
    AuditTimeout
)

var (
    ErrInvalidSignature    = errors.New("audit: invalid Ed25519 signature")
    ErrNonceLength         = errors.New("audit: challenge nonce must be exactly 33 bytes")
    ErrReceiptAlreadyFinal = errors.New("audit: receipt already has a terminal result")
)
```

---

### 5.6 `internal/scoring`

Computes the three-window reliability score from the audit receipt history. Read-only
against the database. Goroutine-safe.

```go
// Package scoring computes per-provider reliability scores from the audit_receipts table.
// Scores are non-I-confluent (floor ≥ 0 constraint) and must be computed by a single
// authoritative scorer instance. (ADR-008, ADR-013)
package scoring

// ProviderScore holds the three-window scores and the weighted composite.
type ProviderScore struct {
    Score24h      float64 // window weight 0.50 in composite
    Score7d       float64 // window weight 0.30
    Score30d      float64 // window weight 0.20
    Composite     float64 // 0.50*24h + 0.30*7d + 0.20*30d
    DualWindowFlag bool   // true when score30d - score7d > 0.20 (ADR-024 §3)
}

// GetScore queries the mv_provider_scores materialised view for the given provider.
// The view may be up to 60 seconds stale; this is acceptable for scoring queries.
//
// Pre-conditions:
//   - providerID is a valid non-zero UUID
// Post-conditions (on nil error):
//   - all score fields are in [0.0, 1.0]
//   - DualWindowFlag is set iff score30d - score7d > 0.20
// Error semantics:
//   - ErrProviderNotFound: the provider does not exist in the view yet (no audits)
// Goroutine-safe: yes.
func GetScore(ctx context.Context, db *sql.DB, providerID uuid.UUID) (ProviderScore, error)

// IncrementConsecutivePasses atomically increments consecutive_audit_passes for a
// provider. If the new value equals 80, also sets status = 'ACTIVE'. This is the
// VETTING → ACTIVE transition. (ADR-005, FR-026)
//
// Pre-conditions:
//   - providerID identifies a provider with status = 'VETTING'
// Post-conditions (on nil error):
//   - consecutive_audit_passes is incremented by 1
//   - if new value == 80, status is set to 'ACTIVE' in the same transaction
// Error semantics:
//   - ErrProviderNotVetting: provider is not in VETTING status; no-op
// Goroutine-safe: yes (uses SELECT FOR UPDATE within a transaction).
func IncrementConsecutivePasses(ctx context.Context, db *sql.DB, providerID uuid.UUID) error

// ResetConsecutivePasses resets consecutive_audit_passes to 0 on any non-PASS audit.
// (ADR-005 — any FAIL or TIMEOUT resets the counter)
//
// Goroutine-safe: yes.
func ResetConsecutivePasses(ctx context.Context, db *sql.DB, providerID uuid.UUID) error

var (
    ErrProviderNotFound   = errors.New("scoring: provider not found in score view")
    ErrProviderNotVetting = errors.New("scoring: provider is not in VETTING status")
)
```

---

### 5.7 `internal/repair`

Manages the repair job queue and orchestrates fragment reconstruction. Goroutine-safe.

```go
// Package repair manages the repair job queue and orchestrates lazy repair.
// (ADR-004, ADR-014)
package repair

// Priority mirrors the repair_priority DB enum.
type Priority int

const (
    PriorityPermanentDeparture Priority = iota // drains before PriorityPreWarning
    PriorityPreWarning
)

// TriggerType mirrors the repair_trigger_type DB enum.
type TriggerType int

const (
    TriggerSilentDeparture    TriggerType = iota
    TriggerAnnouncedDeparture
    TriggerThresholdWarning
    TriggerEmergencyFloor
)

// EnqueueJob inserts a repair job into the repair_jobs table.
// The priority is derived from triggerType automatically (enforcing the repair_jobs_priority_matches_trigger constraint in data-model.md).
//
// Pre-conditions:
//   - len(chunkID) == 32
//   - segmentID is a valid UUID
//   - availableShardCount is in [16, 56] 
//   - The chunk_assignments row for (chunkID, any active provider) must have is_vetting_chunk = FALSE. Calling EnqueueJob for a synthetic vetting chunk (is_vetting_chunk = TRUE) is a calling contract violation that panics in debug builds. The departure handler must check is_vetting_chunk before calling EnqueueJob. (ADR-030, Invariant 6)
// Post-conditions (on nil error):
//   - A row with status='QUEUED' is inserted into repair_jobs
//   - priority is set to PERMANENT_DEPARTURE iff triggerType is SilentDeparture or AnnouncedDeparture; PRE_WARNING otherwise
// Error semantics: database errors returned; nil means success.
// Goroutine-safe: yes.
func EnqueueJob(ctx context.Context, db *sql.DB,
    chunkID [32]byte, segmentID uuid.UUID, providerID *uuid.UUID,
    triggerType TriggerType, availableShardCount int) error

// DequeueNextJob retrieves and atomically marks as IN_PROGRESS the highest-priority
// QUEUED repair job. PERMANENT_DEPARTURE jobs drain before PRE_WARNING jobs (Paper 39).
// Returns nil, nil if the queue is empty.
//
// Goroutine-safe: yes (uses SELECT ... FOR UPDATE SKIP LOCKED).
func DequeueNextJob(ctx context.Context, db *sql.DB) (*RepairJob, error)

// IsVettingChunk returns true if the given chunkID is a synthetic vetting chunk.
// Must be called by the departure handler and threshold monitor before invoking
// EnqueueJob. If true, no repair job should be enqueued. (ADR-030)
//
// Pre-conditions:
//   - len(chunkID) == 32
//   - providerID is the provider suspected of departure (used to disambiguate
//     if the same chunk_id somehow exists on multiple providers, synthetic and real)
// Post-conditions (on nil error):
//   - returns true iff a chunk_assignments row exists with the given (chunkID, providerID)
//     and is_vetting_chunk = TRUE
// Goroutine-safe: yes.
func IsVettingChunk(ctx context.Context, db *sql.DB,
    chunkID [32]byte, providerID uuid.UUID) (bool, error)

// DeleteVettingChunksOnDeparture soft-deletes all synthetic chunk assignments for a
// departing vetting provider. Must be called by the departure handler when
// providers.status is being set to 'DEPARTED' for a provider that was in VETTING status.
// Does not enqueue any repair jobs. (ADR-030, FR-065)
//
// Pre-conditions:
//   - providerID identifies a provider with status transitioning to 'DEPARTED'
//   - All chunk_assignments for this provider must have is_vetting_chunk = TRUE
//     (departure handler verifies no real shard assignments exist)
// Post-conditions (on nil error):
//   - All chunk_assignments rows for providerID with is_vetting_chunk = TRUE
//     are set to status = 'DELETED', deleted_at = NOW()
//   - Zero repair_jobs rows are created
// Error semantics: database errors returned; nil means success.
// Goroutine-safe: yes.
func DeleteVettingChunksOnDeparture(ctx context.Context, db *sql.DB,
    providerID uuid.UUID) error

// MarkJobComplete sets a repair job's status to COMPLETED or FAILED.
//
// Goroutine-safe: yes.
func MarkJobComplete(ctx context.Context, db *sql.DB, jobID uuid.UUID, success bool) error

// RepairPromotionTimeout returns the duration after which a PRE_WARNING job
// is promoted to PERMANENT_DEPARTURE priority if not yet serviced.
// The value comes from NetworkProfile.RepairPromotionTimeout (6 h in production,
// 3 min in demo). The scheduler must call this rather than reading a constant.
// (ADR-031, FR-043)
func RepairPromotionTimeout(profile config.NetworkProfile) time.Duration {
    return profile.RepairPromotionTimeout
}

// RepairJob is the in-memory representation of a dequeued job.
type RepairJob struct {
    JobID               uuid.UUID
    ChunkID             [32]byte
    SegmentID           uuid.UUID
    ProviderID          *uuid.UUID // nil for THRESHOLD_WARNING and EMERGENCY_FLOOR
    TriggerType         TriggerType
    Priority            Priority
    AvailableShardCount int
}
```

---

### 5.8 `internal/payment`

Implements the `PaymentProvider` interface and the append-only escrow ledger.
([`ADR-011`](../decisions/ADR-011-escrow-payments.md), [`ADR-016`](../decisions/ADR-016-payment-db-schema.md))

**INVARIANT: All `amountPaise` parameters and return values are integer paise (₹1 = 100 paise).
Passing a float64 or float32 anywhere in this package is a calling contract violation and will
cause a `panic` in debug builds. Floating-point arithmetic is prohibited throughout the payment
path.** ([`ADR-016`](../decisions/ADR-016-payment-db-schema.md), [`NFR-046`](./requirements.md#77-compliance-and-payments), Invariant 4 in [`data-model.md §3`](./data-model.md#3-design-invariants))

```go
// Package payment implements the PaymentProvider interface and the escrow ledger.
// ALL monetary amounts are int64 paise (₹1 = 100 paise). Passing float64 is a
// calling contract violation — it panics in debug builds. (ADR-016, NFR-046)
package payment

// PaymentProvider is the abstraction over Razorpay Route + RazorpayX.
// Implement this interface to add a new payment gateway (e.g. Stripe Connect).
// (ADR-011 §Abstraction layer)
type PaymentProvider interface {
    // InitiateEscrow creates a virtual UPI address for the data owner and
    // records the expected deposit amount. Returns the VPA and QR code URL.
    //
    // Pre-conditions:
    //   - amountPaise > 0 and is an integer (not a fractional paise)
    // Goroutine-safe: yes.
    InitiateEscrow(ctx context.Context, ownerID uuid.UUID,
        amountPaise int64, contractID uuid.UUID) (vpa string, qrURL string, err error)

    // ReleaseEscrow initiates a monthly payout to the provider's linked account.
    // The idempotency key prevents double-payment on retry.
    //
    // Pre-conditions:
    //   - amountPaise > 0 and is an integer
    //   - idempotencyKey == SHA-256(providerID || auditPeriodID), hex-encoded, 64 chars
    //     (mandatory for Razorpay since 15 March 2025 — ADR-012, Paper 35)
    // Post-conditions (on nil error):
    //   - A payout transfer is initiated to the provider's Razorpay Linked Account
    //   - The transfer's on_hold_until is set to the last working day of the current month
    // Goroutine-safe: yes.
    ReleaseEscrow(ctx context.Context, providerID uuid.UUID,
        amountPaise int64, auditPeriodID uuid.UUID,
        idempotencyKey string) error

    // Penalise seizes all escrow from a departed provider's rolling 30-day window.
    // Used on silent departure. (ADR-024 §5)
    //
    // Pre-conditions:
    //   - amountPaise > 0 and is an integer
    //   - idempotencyKey == SHA-256(providerID || "seizure" || departedAt), hex-encoded
    // Goroutine-safe: yes.
    Penalise(ctx context.Context, providerID uuid.UUID,
        amountPaise int64, idempotencyKey string) error

    // GetBalance returns the current escrow balance for an entity.
    // Always computed as SUM(DEPOSIT) - SUM(RELEASE + SEIZURE) from escrow_events.
    // (ADR-016, Invariant 2)
    //
    // Return value: integer paise; never negative.
    // Goroutine-safe: yes.
    GetBalance(ctx context.Context, providerID uuid.UUID) (int64, error)
}

// InsertEscrowEvent appends one row to the escrow_events table.
// This is the only permitted write to escrow_events. (Invariant 2)
//
// Pre-conditions:
//   - amountPaise > 0 (must be positive; sign is encoded in eventType)
//   - idempotencyKey is unique across all existing rows (UNIQUE constraint at DB level)
// Post-conditions (on nil error):
//   - One INSERT-only row added to escrow_events
// Error semantics:
//   - ErrDuplicateIdempotencyKey: a row with this idempotency key already exists;
//     treat as idempotent success (the event was already recorded)
// Goroutine-safe: yes.
func InsertEscrowEvent(ctx context.Context, db *sql.DB,
    providerID uuid.UUID, eventType EscrowEventType,
    amountPaise int64, idempotencyKey string,
    auditPeriodID *uuid.UUID) error

type EscrowEventType string

const (
    EscrowDeposit  EscrowEventType = "DEPOSIT"
    EscrowRelease  EscrowEventType = "RELEASE"
    EscrowSeizure  EscrowEventType = "SEIZURE"
    EscrowReversal  EscrowEventType = "REVERSAL"
)

var (
    ErrDuplicateIdempotencyKey = errors.New("payment: idempotency key already exists")
)
```

---

### 5.9 `internal/client`

Provides the upload orchestrator used by the data owner client. Goroutine-safe;
coordinates AONT-RS encoding, provider assignment, parallel upload, and pointer file
creation.

```go
// Package client provides the data owner upload and retrieval orchestrators.
// (ADR-022, ADR-020, ADR-021, FR-060)
package client

// UploadOrchestrator manages the full upload lifecycle for one file.
// ERRATA: Each ShardAssignment in the UploadAssignResponse includes a capability_token field. The upload orchestrator must include this token verbatim in the capability_token field of the UploadRequest frame sent to that provider. Tokens are single-use per assignment and expire 1 hour after issuance.
// If a provider returns 0x07 (CAPABILITY_EXPIRED), the orchestrator must call POST /api/v1/upload/assign again with the same file_id to obtain fresh tokens.
// The assignment service returns the same provider set (idempotent on file_id) but generates new tokens with a fresh expiry.
type UploadOrchestrator interface {
    // UploadFile encodes, distributes, and registers a file.
    //
    // Pre-conditions:
    //   - masterSecret is the 32-byte Argon2id-derived master secret (in memory, not on disk)
    //   - ownerID is the data owner's UUID
    //   - plaintext is the raw file bytes (may be any size; orchestrator handles segmentation
    //     and padding internally)
    //   - The data owner's escrow balance must cover 30 days of storage for len(plaintext)
    //     (the orchestrator calls the assignment service which enforces this via HTTP 409)
    // Post-conditions (on nil error):
    //   - All 56 × num_segments shards are durably stored on provider daemons
    //   - The encrypted pointer file ciphertext is stored with the microservice
    //   - The local session state file is cleaned up
    //   - The fileID returned can be used with RetrieveFile
    // Error semantics:
    //   - ErrInsufficientEscrow: balance check failed; data owner must deposit first
    //   - ErrNetworkNotReady: the microservice returned HTTP 503 (readiness gate)
    //   - ErrUploadIncomplete: some shards failed after retries; session state is
    //     persisted to disk for a future resume attempt (FR-060)
    // Goroutine-safe: yes (constructs fresh goroutines per upload).
    UploadFile(ctx context.Context,
        masterSecret [32]byte, ownerID uuid.UUID,
        plaintext []byte) (fileID uuid.UUID, err error)

    // ResumeUpload resumes an interrupted upload using the persisted session state.
    // (FR-060 — crash recovery without retransmitting acknowledged shards)
    //
    // Pre-conditions:
    //   - A session state file for fileID exists on disk
    //   - masterSecret is the same master secret used for the original upload
    // Post-conditions: same as UploadFile on success.
    ResumeUpload(ctx context.Context,
        masterSecret [32]byte, ownerID, fileID uuid.UUID) error

    // RetrieveFile downloads and decodes a file.
    //
    // Pre-conditions:
    //   - masterSecret is the 32-byte master secret
    //   - fileID identifies an ACTIVE file owned by ownerID
    // Post-conditions (on nil error):
    //   - returned plaintext is verified: Poly1305 tag passed, all shard content addresses
    //     verified, AONT canary verified
    //   - plaintext is exactly original_size_bytes (padding stripped)
    // Error semantics:
    //   - ErrPointerTagMismatch: Poly1305 verification failed; no plaintext returned
    //   - ErrTooFewShards: fewer than 16 providers reachable (FR-016 path exhausted)
    //   - ErrCanaryMismatch: AONT canary failed after decode; no plaintext returned (FR-018)
    // Goroutine-safe: yes.
    RetrieveFile(ctx context.Context,
        masterSecret [32]byte, ownerID, fileID uuid.UUID) (plaintext []byte, err error)
}

var (
    ErrInsufficientEscrow  = errors.New("client: escrow balance insufficient for 30-day storage")
    ErrNetworkNotReady     = errors.New("client: network readiness gate not satisfied (HTTP 503)")
    ErrUploadIncomplete    = errors.New("client: upload incomplete; session state saved for resume")
    ErrPointerTagMismatch  = errors.New("client: pointer file Poly1305 tag verification failed")
    ErrTooFewShards        = errors.New("client: fewer than 16 shards reachable for this segment")
    ErrCanaryMismatch      = errors.New("client: AONT canary mismatch after decode (segment corrupt)")
)
```

---

### 5.10 `internal/vettingchunk`

```go
// Package vettingchunk manages the synthetic chunk lifecycle: generation, assignment,
// GC instruction delivery, and departure cleanup. (ADR-030)
package vettingchunk

// Generator produces synthetic vetting chunks for assignment to VETTING providers.
type Generator interface {
    // GenerateChunk creates a single 256 KB random block and returns its chunk ID.
    // The generated data is immediately uploaded to the provider via the standard
    // chunk upload stream (/vyomanaut/chunk-upload/1.0.0).
    //
    // Pre-conditions:
    //   - providerID identifies a provider with status = 'VETTING'
    //   - The provider's current synthetic chunk count < floor(declared_storage_gb × 400)
    //     (cap enforcement is the caller's responsibility before invoking GenerateChunk)
    // Post-conditions (on nil error):
    //   - 256 KB of crypto/rand data is generated, uploaded, and acknowledged by the provider
    //   - A chunk_assignments row is inserted with is_vetting_chunk = TRUE,
    //     segment_id = NULL, shard_index = NULL
    //   - The chunkID (SHA-256 of the generated data) is returned for record-keeping
    //
    // Note: The raw 256 KB data is NOT retained by the microservice after upload confirmation.
    // The chunkID alone is sufficient to issue future audit challenges.
    //
    // Goroutine-safe: yes.
    GenerateChunk(ctx context.Context,
        providerID uuid.UUID) (chunkID [32]byte, err error)

    // CurrentCount returns the number of ACTIVE synthetic chunks for a provider.
    // Used by the assignment service for cap enforcement.
    //
    // Goroutine-safe: yes.
    CurrentCount(ctx context.Context, db *sql.DB, providerID uuid.UUID) (int, error)

    // Cap returns the maximum allowed synthetic chunks for a provider.
    // cap = floor(declared_storage_gb × 400)
    // This is a pure function of declared_storage_gb; no database read required.
    Cap(declaredStorageGB int) int
}

// GCDelivery manages the delivery of vetting GC instructions on ACTIVE transition.
type GCDelivery interface {
    // DeliverGCInstruction sends the vetting GC instruction list to the provider daemon
    // via the /vyomanaut/vetting-gc/1.0.0 libp2p protocol. If the provider is offline,
    // marks all synthetic chunk_assignments as 'PENDING_DELETION' and queues retry.
    //
    // Pre-conditions:
    //   - providerID has just transitioned to status = 'ACTIVE'
    //   - providerID had status = 'VETTING' immediately before the transition
    // Post-conditions (on nil error):
    //   - All synthetic chunk_assignments for providerID are marked 'DELETED'
    //   - The provider's vLog no longer contains any synthetic chunk data
    // Error semantics:
    //   - ErrProviderOffline: provider not reachable; rows set to 'PENDING_DELETION';
    //     caller must retry on next heartbeat connection
    // Goroutine-safe: yes.
    DeliverGCInstruction(ctx context.Context, providerID uuid.UUID) error
}

var (
    ErrProviderOffline   = errors.New("vettingchunk: provider not reachable for GC delivery")
    ErrCapExceeded       = errors.New("vettingchunk: synthetic chunk cap exceeded for provider")
    ErrNotVettingProvider = errors.New("vettingchunk: provider is not in VETTING status")
)
```

---

## 6. PostgreSQL Row-Level Contracts

This section specifies exactly what DML each application role may perform on each table.
These contracts are the enforcement point for Invariants 1–3 from
[`data-model.md §3`](./data-model.md#3-design-invariants). The row security policies in
[`data-model.md §6`](./data-model.md#6-row-security-policies) implement these contracts at
the database layer; the contracts here document the intent for application-layer code review.

**Database roles used:**
- `vyomanaut_app` — the microservice application role; all normal application writes
- `vyomanaut_gc` — the background garbage-collection process; minimal privileges
- `vyomanaut_ro` — read-only analytics and admin tooling; no writes

---

### `owners`

| Operation | Permitted by | Condition |
|---|---|---|
| INSERT | `vyomanaut_app` | At OTP-verified registration only |
| UPDATE `smart_collect_vpa` | `vyomanaut_app` | On Razorpay `virtual_account.created` webhook only |
| UPDATE any other column | **Prohibited** | No application path modifies owner identity |
| DELETE | **Prohibited** | No physical deletion; V2 has no account deletion feature |

### `providers`

| Operation | Permitted by | Condition |
|---|---|---|
| INSERT | `vyomanaut_app` | At registration only |
| UPDATE `status` | `vyomanaut_app` | Transitions must follow the state machine in [`ADR-007`](../decisions/ADR-007-provider-exit-states.md): `PENDING_ONBOARDING → VETTING`, `VETTING → ACTIVE`, any `→ DEPARTED`. No transition moves backward. |
| UPDATE `last_known_multiaddrs`, `last_heartbeat_ts`, `multiaddr_stale` | `vyomanaut_app` | On heartbeat receipt only |
| UPDATE `p95_throughput_kbps`, `avg_rtt_ms`, `var_rtt_ms`, `rto_sample_count` | `vyomanaut_app` | After each audit response, via EWMA update |
| UPDATE `consecutive_audit_passes` | `vyomanaut_app` | Incremented on PASS; reset to 0 on non-PASS |
| UPDATE `accelerated_reaudit` | `vyomanaut_app` | Set when >1 FAIL in rolling 7-day window; cleared when window clears |
| UPDATE `frozen`, `departed_at` | `vyomanaut_app` | On departure declaration only; `departed_at` is never cleared once set |
| UPDATE `razorpay_linked_account_id`, `razorpay_cooling_until` | `vyomanaut_app` | On Razorpay `account.created` webhook only |
| UPDATE `promised_return_at` | `vyomanaut_app` | On `POST /api/v1/provider/downtime`; cleared on return heartbeat or overrun |
| UPDATE `first_chunk_assignment_at` | `vyomanaut_app` | Set once, by the assignment service, on first chunk assignment. Must not be overwritten. |
| DELETE (physical) | **Prohibited for all roles** | Soft-delete via `status = 'DEPARTED'` only. Invariant 3. |


### `files`

| Operation | Permitted by | Condition |
|---|---|---|
| INSERT | `vyomanaut_app` | On `POST /api/v1/file/register` only; only after all shards are acknowledged |
| UPDATE `status` to `'DELETED'` | `vyomanaut_app` | On `DELETE /api/v1/file/{file_id}` only |
| UPDATE any other column | **Prohibited** | Pointer files are immutable once registered |
| DELETE (physical) | **Prohibited** | Soft-delete via `status = 'DELETED'` only |

### `segments` and `chunk_assignments`

| Operation | Permitted by | Condition |
|---|---|---|
| INSERT `segments` | `vyomanaut_app` | During upload assignment only |
| INSERT `chunk_assignments` | `vyomanaut_app` | During upload assignment and repair replacement only; ASN cap enforced at INSERT time |
| INSERT `is_vetting_chunk = TRUE` | `vyomanaut_app` | Only when the target provider's `status = 'VETTING'`. The assignment service must verify provider status before INSERT. |
| INSERT `is_vetting_chunk = FALSE` | `vyomanaut_app` | Only when the target provider's `status = 'ACTIVE'`. Inserting real shards to a VETTING provider is a calling contract violation (Invariant 6). |
| UPDATE `chunk_assignments.status` | `vyomanaut_app` | Permitted transitions only: `ACTIVE → REPAIRING`, `ACTIVE → PENDING_DELETION`, `REPAIRING → ACTIVE`, `PENDING_DELETION → DELETED` |
| UPDATE `chunk_assignments` status to 'DELETED', set deleted_at = NOW() | `vyomanaut_app` | Departure handler only, when `providers.status = 'DEPARTED'`. This is a soft-delete. The row is never physically removed. The `active_chunk_assignments` view excludes these rows from challenge scheduling automatically. Physical `DELETE` is prohibited for all roles — it breaks the audit receipt trail. |
| UPDATE `status` to `'PENDING_DELETION'` for synthetic chunks | `vyomanaut_app` | On ACTIVE transition (before GC delivery) or when provider goes offline after `ACTIVE` transition. The audit scheduler must stop issuing challenges for `PENDING_DELETION` rows. |
| UPDATE `status` to `'DELETED'` for synthetic chunks | `vyomanaut_app` | After successful GC confirmation from the daemon via the vetting GC protocol (§4.5). |
| DELETE `segments` | **Prohibited** | Segments are immutable references |
| Repair job creation for `is_vetting_chunk = TRUE` rows | Prohibited for all roles | Any code path that calls `repair.EnqueueJob` for a synthetic chunk is a bug. Invariant 6. `IsVettingChunk()` must be called before any EnqueueJob invocation in the departure handler and threshold monitor. |

**Demo-mode shard_index range.** The `shard_index BETWEEN 0 AND 55` CHECK shown in the
schema DDL reflects the production value (`TotalShards-1 = 55`). In demo mode
(`TotalShards=5`), the generated constraint is `shard_index BETWEEN 0 AND 4`. The row
security policy and all DML permissions are identical in both modes; only the CHECK bound
differs. (ADR-031, `migrations/generator.go`)

### `audit_receipts`

This `table enforces Invariant 1. The row security policy in
[`data-model.md §6`](./data-model.md#6-row-security-policies) implements these restrictions at
the database level, independent of application code.

| Operation | Permitted by | Condition |
|---|---|---|
| INSERT | `vyomanaut_app` | Phase 1 of the two-phase write: `audit_result = NULL`, `provider_sig` populated |
| UPDATE `audit_result`, `service_sig`, `service_countersign_ts`, `jit_flag` | `vyomanaut_app` | Phase 2 of the two-phase write only; `WHERE audit_result IS NULL AND abandoned_at IS NULL`; `audit_result` must be `PASS`, `FAIL`, or `TIMEOUT` — never re-set to NULL |
| UPDATE `abandoned_at` | `vyomanaut_gc` | GC process only; `WHERE audit_result IS NULL AND abandoned_at IS NULL AND server_challenge_ts < NOW() - INTERVAL '48 hours'`; sets to `NOW()` and never to NULL |
| UPDATE any other column | **Prohibited** | All other columns are immutable after INSERT |
| DELETE | **Prohibited for all roles** | No deletion ever. Invariant 1. |

### `escrow_events`

This table enforces Invariant 2.

| Operation | Permitted by | Condition |
|---|---|---|
| INSERT | `vyomanaut_app` | One row per DEPOSIT, RELEASE, or SEIZURE event; `amount_paise` must be `> 0`; `idempotency_key` must be unique |
| UPDATE | **Prohibited for all roles** | Balance is always recomputed from the immutable event log. Invariant 2. |
| DELETE | **Prohibited for all roles** | Invariant 2. |

### `audit_periods` and `repair_jobs`

| Operation | Permitted by | Condition |
|---|---|---|
| INSERT both tables | `vyomanaut_app` | Normal application writes |
| UPDATE `audit_periods.audit_passes/fails/timeouts`, `release_computed` | `vyomanaut_app` | Materialised tally updates and monthly release flag |
| UPDATE `repair_jobs.status`, `started_at`, `completed_at` | `vyomanaut_app` | Job lifecycle transitions only: `QUEUED → IN_PROGRESS → COMPLETED/FAILED` |
| DELETE | **Prohibited** | Historical records; soft-status only |

---

## 7. Razorpay Webhook Contracts

Razorpay delivers webhooks as HTTP POST requests to the microservice endpoint
`POST /webhooks/razorpay`. All incoming webhooks must be verified using the Razorpay webhook
signature (`X-Razorpay-Signature` header) against the shared webhook secret before any
database write is triggered. Unverified webhooks must be rejected with HTTP 400.

All database writes triggered by webhooks use an idempotency key to handle Razorpay's
at-least-once delivery guarantee. A webhook delivered a second time with the same payload
must produce a 200 OK response without creating a duplicate database row.

---

### 7.1 `virtual_account.payment.captured`

Triggered when a data owner's UPI payment to their virtual account is captured by Razorpay
Smart Collect 2.0. This is the **only** event that credits a data owner's escrow balance.

**Razorpay event name:** `virtual_account.payment.captured`

**Fields read from payload:**

| Field path | Type | Mapped to |
|---|---|---|
| `payload.payment.entity.id` | string | `idempotency_key` = `SHA-256("deposit" || payment_id)` |
| `payload.virtual_account.entity.id` | string | Used to look up `owners.owner_id` via the Smart Collect VPA mapping table |
| `payload.payment.entity.amount` | integer (paise from Razorpay) | `escrow_events.amount_paise` — **already in paise; do not multiply** |

**Database write triggered:**

```sql
INSERT INTO escrow_events (
    event_id, provider_id, event_type, amount_paise,
    audit_period_id, idempotency_key, created_at
) VALUES (
    gen_random_uuid(), <owner_escrow_account_id>, 'DEPOSIT',
    <amount_from_webhook>, NULL,
    SHA256('deposit' || <payment_id>), NOW()
)
ON CONFLICT (idempotency_key) DO NOTHING;
```

**Idempotency:** The `ON CONFLICT DO NOTHING` on the `UNIQUE(idempotency_key)` index handles
duplicate delivery. A second delivery of the same webhook returns HTTP 200 without creating a
duplicate deposit.

**Failure mode:** If the database INSERT fails (connection error, constraint violation other
than idempotency), the microservice returns HTTP 500. Razorpay will retry delivery with
exponential backoff. The handler must be idempotent — retries must be safe.

---

### 7.2 `payout.reversed`

Triggered when a provider payout initiated by the microservice is reversed by Razorpay (bank
rejection, invalid UPI handle, etc.). This event restores the payout amount to the provider's
effective escrow balance.

**Razorpay event name:** `payout.reversed`

**Fields read from payload:**

| Field path | Type | Mapped to |
|---|---|---|
| `payload.payout.entity.id` | string | Used to look up the original RELEASE event's `idempotency_key` |
| `payload.payout.entity.amount` | integer (paise) | The reversal amount; must match the original RELEASE row |
| `payload.payout.entity.reference_id` | string | The `X-Payout-Idempotency` value from the original payout call |

**Database write triggered:**

A negative-RELEASE event is inserted to restore the balance:

```sql
INSERT INTO escrow_events (
    event_id, provider_id, event_type, amount_paise,
    audit_period_id, idempotency_key, created_at
) VALUES (
    gen_random_uuid(),
    <provider_id>,
    'REVERSAL',                          -- dedicated type; amount_paise stays positive
    <amount_paise>,                      -- always positive; sign encoded in event_type
    <original_audit_period_id>,
    SHA256('reversal' || <original_idempotency_key>),
    NOW()
)
ON CONFLICT (idempotency_key) DO NOTHING;
```

**Failure mode:** If the microservice cannot find the original payout in its records (the
`reference_id` is unknown), log the event at WARNING level and return HTTP 200. Do not return
an error that would cause Razorpay to retry indefinitely.

---

### 7.3 `account.created`

Triggered when Razorpay completes creation of a Route Linked Account for a newly registered
provider. This event starts the 24-hour cooling period timer.

**Razorpay event name:** `account.created` (or `account.activated` depending on Razorpay API
version — check Paper 35 and the Razorpay changelog at deployment time)

**Fields read from payload:**

| Field path | Type | Mapped to |
|---|---|---|
| `payload.account.entity.id` | string | `providers.razorpay_linked_account_id` |
| `payload.account.entity.notes.provider_id` | UUID string | Used to look up the `providers` row |

**Database write triggered:**

```sql
UPDATE providers
SET razorpay_linked_account_id = <account_id>,
    razorpay_cooling_until = NOW() + INTERVAL '24 hours'
WHERE provider_id = <provider_id_from_notes>
  AND razorpay_linked_account_id IS NULL; -- idempotent: no-op if already set
```

**Idempotency:** The `AND razorpay_linked_account_id IS NULL` guard prevents double-update on
redelivery.

**Failure mode:** If `provider_id` from the webhook notes does not match any row in
`providers`, log at ERROR level and return HTTP 200. This indicates a registration-side bug;
do not block Razorpay webhook delivery.

---

## 8. Secrets Manager Contract

The coordination microservice fetches the cluster audit secret from a secrets manager
(HashiCorp Vault, AWS SSM Parameter Store, or GCP Secret Manager) at startup and caches it
for 5 minutes. The following contract governs this interaction.
([`ADR-027`](../decisions/ADR-027-cluster-audit-secret.md))

**Path naming convention:**

```
/vyomanaut/audit-secret/v{N}
```

Where `N` is the version integer (starts at 1 at cluster bootstrap; incremented on rotation).
Each path stores a 32-byte (256-bit) secret, base64-encoded.

**Read contract:**
- The microservice reads `server_secret_vN` at startup and on every 5-minute cache refresh.
- If the secrets manager is unreachable at **startup**, the replica **must not start** (fail-closed).
  Better to produce zero audit challenges than to issue challenges that cannot be validated.
- If the secrets manager becomes unreachable **during operation** after the initial load, the
  microservice continues serving using the cached value for up to 5 minutes, then returns
  `ErrSecretExpired` to the challenge generation function. The caller must back off and retry;
  it must not issue challenges with an expired secret.

**Rotation contract (24-hour overlap window):**
- During rotation, both `v{N}` and `v{N+1}` must exist in the secrets manager simultaneously.
- The microservice reads both versions and caches them both.
- New challenges are issued under `v{N+1}` (higher version byte in the nonce prefix).
- Validation accepts nonces with version byte `N` or `N+1` during the overlap window.
- After 24 hours, `v{N}` is removed from the secrets manager.
- Any nonce with version byte `N` received after 24 hours is rejected as expired.
  ([`ADR-027`](../decisions/ADR-027-cluster-audit-secret.md) §4)

**Write contract:** The microservice **never writes to the secrets manager**. Only the
operator (human or operator tooling via `vyomanaut-admin rotate-secret`) writes new versions.
The microservice is a read-only consumer.

**Local development / simulation mode:** The `VYOMANAUT_CLUSTER_MASTER_SEED` environment
variable may substitute for the secrets manager in `dev` and `--sim-count` modes. This
variable must be **absent** in all production deployments. Presence of this variable in a
non-dev environment is a critical misconfiguration that the startup check must detect and halt.

**Go interface for the secrets manager client:**

```go
// SecretsManagerClient abstracts over Vault, AWS SSM, and GCP Secret Manager.
type SecretsManagerClient interface {
    // GetSecret retrieves the secret at the given path.
    // Returns the raw bytes of the secret value.
    //
    // Pre-conditions:
    //   - path is a valid secrets path (e.g. "/vyomanaut/audit-secret/v3")
    // Post-conditions (on nil error):
    //   - returned bytes are the decoded secret value (not base64)
    // Error semantics:
    //   - ErrSecretNotFound: the path does not exist
    //   - ErrSecretManagerUnavailable: the secrets manager is unreachable
    // Goroutine-safe: yes.
    GetSecret(ctx context.Context, path string) ([]byte, error)
}

var (
    ErrSecretNotFound           = errors.New("secrets: path not found")
    ErrSecretManagerUnavailable = errors.New("secrets: manager unreachable")
    ErrSecretExpired            = errors.New("secrets: cached secret TTL expired and manager unavailable")
)
```

---

## 9. Package Import Constraints

The following import directions are **prohibited**. A PR that introduces any prohibited import must be rejected at review regardless of the stated justification. These constraints exist because the packages involved are either security-critical (no business-logic dependency allowed) or architecturally separated (payment must not depend on repair to avoid cycles through the departure handler).

| Package | Must NOT import |
| --- | --- |
| `internal/crypto` | Any other `internal/` package. This package is purely functional — no shared state, no I/O, no dependency on the data layer. Any utility needed here (e.g. byte comparison) uses the standard library only. |
| `internal/erasure` | Any other `internal/` package. RS encoding takes bytes in and produces bytes out. It has no knowledge of the storage engine, the network, or the payment system. |
| `internal/storage` | `internal/payment`, `internal/scoring`, `internal/repair`. The storage engine is unaware of economics or network topology. |
| `internal/payment` | `internal/repair`, `internal/p2p`. The payment system does not initiate repair or open network connections. It receives instructions from the microservice entrypoint. |
| `internal/scoring` | `internal/repair`, `internal/payment`. Score computation is read-only against the audit receipt history. It does not trigger repairs or move money. |
| `internal/audit` | `internal/scoring`, `internal/repair`, `internal/payment`. The audit package handles challenge generation and receipt writing only. Score updates and repair triggers are the caller's responsibility (the microservice entrypoint orchestrates these after the audit result is written). |
| Any `internal/client/*` | `cmd/`. Client packages are imported by the CLI entrypoint; they do not import the CLI. |

The permitted dependency graph flows in one direction: `cmd/*` → `internal/client/*` → (`internal/crypto`, `internal/erasure`, `internal/p2p`) → no further `internal/` imports. The microservice entrypoint wires `internal/audit`, `internal/scoring`, `internal/repair`, and `internal/payment` together; none of these four packages imports any of the others directly.

**Enforcement.** `go build ./...` catches circular imports. `go vet ./...` with the import-graph analyser catches prohibited non-circular imports. Both are CI required checks. A PR that disables or modifies the import-graph check must be rejected.

---

## 10. Naming Conventions

### Exported Go symbol names

Exported function and type names must match exactly the names specified in §5 of this document. Renaming an exported symbol requires updating both the implementation and §5 in the same PR. No silent renames.

The following names are frozen — changing them breaks cross-package references, static analysis checks, or Grafana dashboard configurations that reference them by string:

`AONTEncodeSegment`, `AONTDecodePackage` — crypto package; referenced in security boundary documentation. `ChunkStore`, `AppendChunk`, `LookupChunk`, `DeleteChunk` — storage engine interface; used in vetting GC path. `InsertEscrowEvent`, `EscrowEventType` — ledger functions; referenced in `TestNoFloatArithmetic` static check. `ChallengeNonce` — must always produce a 33-byte slice; referenced in nonce-length CI grep. `PaiseAmount` — monetary amount type; renaming breaks the float-prevention static analysis.

### Error sentinel names

Exported sentinel errors (`var Err... = errors.New(...)`) must never be renamed after first commit. Callers use `errors.Is()` for matching — renaming breaks all callers silently. Adding a new sentinel is additive and safe. Removing or renaming one is a breaking change requiring a search across all call sites before merging.

### Prometheus metric names

All metrics must follow the pattern `vyomanaut_{subsystem}_{name}_{unit}`. The subsystem matches the `internal/` package name (e.g. `audit`, `repair`, `storage`, `payment`). Unit suffixes use the OpenMetrics standard: `_total` for counters, `_seconds` for timing histograms, `_bytes` for size gauges. Metric names defined in NFR-025 and NFR-026 are frozen — changing them requires updating Grafana dashboard JSON and alert rules in the same PR.

### Migration files

`NNN_short_description.sql` where NNN is zero-padded to three digits, sequential with no gaps. See `data-model.md §9` for the full convention.

### Simulation mode paths

The simulation instance data path is `/tmp/vyomanaut-sim/{instance_id}/` where `instance_id` is the zero-padded instance index (e.g. `0000`, `0001`). Sub-directories: `keys/` (Ed25519 key pair), `db/` (RocksDB instance), `vlog/chunks.vlog` (value log file). This path is used verbatim in CI test scripts and the `--sim-data-dir` default; changing it requires updating both in the same PR.

### Runbook filenames

Must use the exact topic names from `architecture.md §23`: `microservice-failover`, `postgres-failover`, `relay-node-replacement`, `secrets-manager-outage`, `razorpay-api-outage`, `provider-mass-departure`, `rbi-holiday-table-update`, `audit-secret-rotation`. Format: `{topic-name}.md`. Grafana alert runbook links reference these names.

---

## 11. Forbidden Code Patterns

The following categories are prohibited from the repository. A PR introducing any of the items below must be rejected at review and must not be merged.

**Secrets and credentials.** No private keys of any kind (Ed25519, TLS, SSH) even in encrypted or base64-encoded form. The `VYOMANAUT_CLUSTER_MASTER_SEED` value must live in the secrets manager only — the environment variable name may appear in code and documentation; the value may never appear. Razorpay live API keys (`rzp_live_*`) must live in the secrets manager. Test keys (`rzp_test_*`) may appear only in CI environment variables via GitHub Actions secrets, never as literals in source files. BIP-39 mnemonic words for any real account must never appear — test vectors from RFC documents with fixed entropy are acceptable.

**Float arithmetic in `internal/payment/`.** `float64`, `float32`, `FLOAT`, `DECIMAL`, `NUMERIC` in any form within `internal/payment/`. The `TestNoFloatArithmetic` CI check enforces this. Disabling or weakening this test is itself a prohibited change.

**Deprecated UPI Collect endpoint calls.** UPI Collect was deprecated by NPCI on 28 February 2026. Any call to the Razorpay Collect API path must be rejected. All deposit flows use UPI Intent per NFR-029.

**`challenge_nonce` as a 32-byte field.** The `challenge_nonce` column is always `BYTEA(33)`. A migration or schema change introducing `BYTEA(32)` for this field is rejected. The CI grep check enforces this.

**Business logic in `cmd/`.** `cmd/` is wiring only — flag parsing, dependency construction, signal handling. Any function with testable behaviour belongs in `internal/`. An engineer who needs to test a `cmd/` function should move it to an `internal/` package first.

**Prohibited cross-package imports.** See §9. Any import violating the table in §11 must be rejected regardless of stated justification.

**Convergent encryption or K reuse.** Each AONT key K is fresh random per segment by design. Any code path that reuses K across files or segments violates the zero-knowledge property and is a correctness violation.

**References to non-existent ADRs.** References to ADR numbers above the current highest assigned number (currently ADR-031) must fail the CI reference check. Stale ADR references create false confidence in decisions that have not been made.

**Hardcoded RBI bank holiday data outside `internal/payment/rbi_holidays.go`.** Holiday dates hardcoded in test files, migration scripts, or deployment configuration bypass the annual update procedure documented in `runbooks/rbi-holiday-table-update.md`.

**Vendoring `go-libp2p` without documenting the decision.** If `TestDHTKeyValidatorPersists` fails after an upgrade and the dependency must be vendored, the vendoring decision must be recorded in `architecture.md §4.1` with the version and reason. Silent vendoring is prohibited.

---

## 12. DHT Key Contract

Every key stored in the Kademlia DHT by a Vyomanaut provider daemon or retrieved by the data
owner client must satisfy this contract. The custom key validator registered with libp2p
enforces it at the DHT layer.
([`ADR-001`](../decisions/ADR-001-coordination-architecture.md))

**Key derivation:**

```
dht_key = HMAC-SHA256(chunk_hash, file_owner_key)

where:
    chunk_hash      = SHA-256(chunk_data)  — the content address
    file_owner_key  = HKDF-SHA256(
                          ikm  = master_secret,
                          salt = owner_id,
                          info = "vyomanaut-dht-v1" || file_id,
                          len  = 32
                      )
```

Only the file owner — who holds `master_secret` — can derive `file_owner_key` and reverse-map
a DHT key back to its `chunk_hash`. A monitoring node observing DHT traffic cannot correlate
lookup requests with file identity. This closes DHT Challenge 3 from the SoK DSN survey.
([`ADR-001`](../decisions/ADR-001-coordination-architecture.md), [`NFR-017`](./requirements.md#78-privacy),
[`NFR-032`](./requirements.md#78-privacy))

**Key validator ID string registered with libp2p:**

```
/vyomanaut/dht-key/1.0.0
```

**What the validator accepts:**
- Any 32-byte key (the HMAC output is always exactly 32 bytes)

**What the validator rejects:**
- Keys shorter or longer than 32 bytes
- Keys that are plaintext SHA-256 hashes (detectable by a prefix check: Vyomanaut plain
  chunk hashes are stored with a `vyom-chunk:` prefix in other contexts, never in the DHT)
- The libp2p default CID namespace keys (which use a multihash encoding not produced by
  HMAC-SHA256)

**Anti-upgrade-drift test (mandatory in CI):**

The following test must pass on every commit that touches `internal/p2p` or upgrades the
`go-libp2p` dependency:

```go
func TestDHTKeyValidatorPersists(t *testing.T) {
    host := buildTestHost(t)
    dht := host.DHT()
    
    // A valid HMAC-derived key must be accepted
    validKey := deriveTestDHTKey(t)
    require.NoError(t, dht.PutProviderRecord(ctx, validKey))
    
    // A plain CID must be rejected
    plainCID := cid.NewCidV1(cid.Raw, []byte("test")).Bytes()
    require.ErrorIs(t, dht.PutProviderRecord(ctx, plainCID), ErrDHTKeyInvalid)
    
    // A 31-byte key must be rejected
    shortKey := validKey[:31]
    require.ErrorIs(t, dht.PutProviderRecord(ctx, shortKey), ErrDHTKeyInvalid)
}
```

This test catches the scenario where a `go-libp2p` version upgrade silently resets the
namespace configuration to defaults and begins accepting plain CIDs — which would leak file
identity in DHT traffic. ([`mvp.md §Security Verification Checklist`](./mvp.md#security-verification-checklist))

**Key namespace pinning.** The custom HMAC validator must be registered in the daemon
initialisation path (`cmd/provider/main.go`) using a configuration constant, not an inline
string. This ensures a global search for the string `/vyomanaut/dht-key/1.0.0` finds all
registration points. Whenever `go-libp2p` is upgraded, the developer must verify this
constant survives the upgrade by running `TestDHTKeyValidatorPersists`.

---

### 12.1 DHT Record Value Format

DHT records use libp2p's standard content routing mechanism (provider records).
When a provider daemon calls DHT.PutProviderRecord(key), the libp2p DHT implementation
stores the calling node's peer.AddrInfo as the provider record. This implicitly contains:

1. `peer.ID` — the provider's cryptographic identifier, derived from its Ed25519 public key as `multihash(ed25519_public_key)`. The data owner verifies this matches the registered `ed25519_public_key` from the microservice before using the returned addresses.
2. `multiaddr` — the provider's current listen addresses at the time of the call.

FindProviders(key) returns []peer.AddrInfo for all nodes that have announced themselves
as providers for that key. The data owner dials these addresses as a fallback when the
microservice's heartbeat record for the target provider is stale.

---

### 12.2 DHT Republication Contract (Corrected)

Provider daemons are responsible for republishing their own DHT records. The daemon's
heartbeat goroutine triggers PutProviderRecord for all currently ACTIVE chunk assignments
at every `NetworkProfile.DHTRepublishInterval`. This is coordinated with the heartbeat cycle:

```go
func heartbeatAndRepublish(profile NetworkProfile, store ChunkStore, dht DHT) {
      heartbeatTicker := time.NewTicker(profile.HeartbeatInterval)
      republishTicker := time.NewTicker(profile.DHTRepublishInterval)
      for {
          select {
          case <-heartbeatTicker.C:
              sendHeartbeat()
          case <-republishTicker.C:
              for _, chunkID := range store.AllChunkIDs() {
                  dhtKey := crypto.DeriveDHTKey(chunkID, fileOwnerKey)
                  dht.PutProviderRecord(ctx, dhtKey)
              }
          }
      }
  }
```

Note: the daemon does not know `file_owner_key` for real production shards — that key belongs
to the data owner. For DHT republication, the daemon only needs to call PutProviderRecord
with the pre-computed dht_key that was stored alongside the chunk at upload time. The
`dht_key` is stored in the `chunk_assignments` row and included in the upload receipt, so the
daemon caches it locally without needing the `file_owner_key`.

The `dht_key` must be persisted locally by the daemon (e.g., in RocksDB alongside the
`vlog_offset`) so that republication does not require the data owner to be online.

---

## Stale Address Fallback Path

The DHT is the FALLBACK path, not the primary path. The normal retrieval sequence is:

1. Fetch pointer file from microservice (`pointer_ciphertext` → decrypt → provider_ids[])
2. Fetch each provider's current multiaddrs from `providers.last_known_multiaddrs` via
    microservice heartbeat record (fresh if `providers.multiaddr_stale = false`).
3. Dial providers directly using multiaddrs from step 2
4. If `multiaddr_stale = true` for a provider: fall back to `DHT.FindProviders(dht_key)`
    to locate the provider's current address.

The DHT is not used in the normal retrieval path at all. Its purpose is address discovery
for providers whose IP has rotated since their last heartbeat.

---

## 13. Versioning and Backwards Compatibility Rules

This section governs when and how breaking changes are permitted across each interface class.
The general principle: **additive changes are always safe; breaking changes require explicit
coordination across the affected components before deployment**.

### REST / HTTP API (`openapi.yaml`)

- **Additive changes** (new optional fields in responses, new optional query parameters):
  allowed without a version bump; existing clients ignore unknown fields.
- **Breaking changes** (removing a field, changing a field's type, changing HTTP method or
  path): require incrementing the URL path version prefix (`/api/v2/...`). The old version
  (`/api/v1/...`) must remain available for at least one full release cycle (approximately
  one sprint) to give provider daemons time to update.
- **Schema corrections** that fix a bug without changing semantics (e.g. `challenge_nonce`
  from `BYTEA(32)` to `BYTEA(33)` per [`ADR-027`](../decisions/ADR-027-cluster-audit-secret.md)):
  treated as a bug fix; rolled out in a single migration; all components must be updated in
  the same sprint deployment.

### libp2p Protocol Strings

- **Protocol IDs are semver strings** embedded in the protocol ID (e.g.
  `/vyomanaut/chunk-upload/1.0.0`). The version suffix must be incremented whenever the wire format changes in any backwards-incompatible way.
- **Old and new protocol versions must coexist** for at least one full release cycle. Both responders (providers) and initiators (clients, microservice) must negotiate via multistream-select and handle both the old and new protocol for the overlap period.
- **Never change the framing without a version bump.** Changing the `length` field encoding
  or a message field's byte offset is always a breaking change.
- `/vyomanaut/vetting-gc/1.0.0` — initial version. The GC instruction frame format (VettingGCRequest / VettingGCResponse) must increment the version string if any of the following change: the `chunk_count` field encoding, the batch size limit, the failure bitmap format, or the response status byte semantics. The protocol must remain in the daemon binary indefinitely: an `ACTIVE provider` that received GC instructions years earlier may need to re-run GC after a crash (the `PENDING_DELETION` rows will retrigger delivery on reconnect). Removing the protocol handler from the daemon requires a coordinated network-wide migration.
- **Protocol strings are mode-invariant.** All libp2p protocol IDs (`/vyomanaut/chunk-upload/1.0.0`, `/vyomanaut/audit-challenge/1.0.0`, etc.) are identical in demo and production. The wire format, frame sizes, and 0-RTT policies documented in §4 apply in both modes. A demo provider daemon can interoperate with a production microservice at the protocol layer (though the readiness gate will prevent uploads until production conditions are met). (ADR-031)


### Internal Go Package Interfaces

- **Additive changes** (new exported functions, new optional parameters via a new overloaded
  function name): allowed within a milestone without coordination.
- **Breaking changes** (changing an exported function's signature, changing pre/post-conditions, changing error sentinel values): require a new milestone entry in [`mvp.md`](./mvp.md) identifying all affected call sites. Changes must be made atomically — the PR that changes the interface must also update all call sites.
- **Sentinel error identity.** Exported `var Err... = errors.New(...)` values must never be renamed; callers use `errors.Is()` for matching. Adding a new sentinel is additive and safe.
- **NetworkProfile fields.** A new `NetworkProfile` field is not a versioned interface change
— it is a configuration change. However, adding a field requires simultaneous values in both `ProductionProfile` and `DemoProfile` in the same PR. The Go struct-literal syntax enforces this at compile time: an omitted field is a compile error, not a silent zero-value default. If a new field's zero value is a valid production setting (e.g. `false`), add a comment explaining why the zero value is intentional; otherwise the intent is ambiguous to future engineers. (ADR-031)


### PostgreSQL Schema

- **Additive changes** (new nullable columns, new indexes, new materialised views): allowed
  via a new versioned migration file (`migrations/NNN_description.sql`). Existing queries are
  unaffected.
- **Breaking changes** (removing a column, changing a column type, changing a constraint):
  require explicit coordination. The migration must include both the schema change and any
  application-layer changes that depend on it. Both must be reviewed and deployed together.
- **Row security policy changes**: treated as breaking; require a PR against this document and
  [`data-model.md §6`](./data-model.md#6-row-security-policies) to update the contracts before
  the migration is written.
- **Invariants 1–5 in `data-model.md §3` may never be relaxed** by any migration. A migration
  that would weaken these invariants is rejected regardless of business justification — open a
  new ADR to change the design instead.

### Razorpay Webhook Payloads

- The Razorpay webhook payload schema is controlled by Razorpay, not by Vyomanaut. Monitor
  the [Razorpay changelog](https://razorpay.com/docs/api/changelog) as part of the December
  deployment. Any field renamed or removed by Razorpay requires an update to
  [Section 7](#7-razorpay-webhook-contracts) of this document and a corresponding code update
  before the change takes effect in production.
- **Field reads must use safe accessors** (typed unmarshalling with explicit `omitempty` on
  optional fields). Code that crashes on an unexpected webhook shape causes payment processing
  outages.

### DHT Key Contract

- The key validator ID string `/vyomanaut/dht-key/1.0.0` must remain unchanged until a
  network-wide migration plan is in place. Changing the validator while providers are running
  old daemon versions causes those providers' DHT records to be rejected by peers running new
  daemons — effectively partitioning the DHT for those providers.
- Any change to the key derivation formula (`HMAC-SHA256(chunk_hash, file_owner_key)`) is a
  **network-breaking change** requiring a full re-indexing of all DHT records. This is a V3+
  concern; do not change the formula in V2.





  