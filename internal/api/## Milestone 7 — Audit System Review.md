## Milestone 7 — Audit System Review

I cloned the repo, fetched `build.md`/`build_part2.md` from the research repo, cross-referenced every M7 file against `docs/data-model.md`, `docs/interface-contracts.md`, `docs/architecture.md`, `docs/requirements.md`, `migrations/generator.go`, `.golangci.yml`, and `scripts/ci/grep_checks.sh`, and then actually compiled and ran the DB-independent test suite (I stood up Go 1.22 + an isolated module in the sandbox — `go build`, `go vet`, and 19 non-DB tests all pass cleanly). Verdict up front: **the code is unusually well self-documented about its own gaps** — this build session has a habit of flagging spec/schema inconsistencies in-line rather than silently working around them, which made this review much easier and more productive than usual. But there are real issues, including one that I don't think has been caught anywhere yet: **the audit system's replay-protection guarantee is currently silently broken**, and a second one where **the JIT detection feature computes a value that is never written to the database**.

---

### 1. Headline bugs

#### 1a. Replay protection (DM §3 Invariant 5) is not actually enforced — new finding

This is the most important thing I found. Sometime after M7 was originally built, a later fix (ADR-033, visible in `migrations/generator.go`, part of the "Milestone 4 fix" commits) converted `audit_receipts` to a **partitioned table** (`PARTITION BY RANGE (server_challenge_ts)`). Because Postgres can't enforce a global `UNIQUE` constraint on a column that isn't the partition key, `challenge_nonce`'s uniqueness constraint became `UNIQUE (challenge_nonce, server_challenge_ts)` — **local to each partition only**. To compensate, the generator now creates a second table:

```
audit_receipt_nonces (challenge_nonce BYTEA PRIMARY KEY, server_challenge_ts TIMESTAMPTZ)
```

with an explicit comment: *"the microservice INSERTs the nonce here in the SAME TRANSACTION as the receipt (IC §6, ADR-033). A duplicate nonce raises a PK violation and aborts the audit write."*

`internal/audit/receipt.go`'s `WriteReceiptPhase1` does **not** write to this table — there's zero mention of `audit_receipt_nonces` anywhere in `internal/audit`. It also doesn't use a transaction at all (a bare `db.ExecContext`, not `db.BeginTx`), so even a naive fix can't just add a second `Exec` call — it needs `sql.Tx`. Practically: today, replaying an old challenge nonce with a slightly different `server_challenge_ts` sails straight through the "unique" constraint, because the only enforcement left is per-partition and keyed on the pair, not the nonce alone. No test in `receipt_test.go`/`audit_test.go` catches this because none of them insert a duplicate nonce with a different timestamp — they only exercise same-nonce-same-everything idempotency.

This is a genuine "milestone assumed a schema that has since drifted out from under it" problem — M7 predates ADR-033 partitioning by several commits, and nothing re-synced it.

#### 1b. `jit_flag` is computed but never persisted — new finding

`EvaluateJIT()` (Phase 7.5) is a pure function returning a `bool`. Nothing in the package writes that bool anywhere. `WriteReceiptPhase2`'s `UPDATE` statement only sets `audit_result`, `service_sig`, `service_countersign_ts`. IC §6 (`docs/interface-contracts.md` §6, the DML contract) explicitly lists `jit_flag` as one of the four columns Phase 2 is supposed to write (`audit_result, service_sig, service_countersign_ts, jit_flag`). Since the column defaults to `FALSE` and nothing ever flips it, ARCH §20's "three JIT flags in 7 days → 0.5× score penalty" mechanism has no data to act on — it's currently unreachable dead code by construction, not because anyone decided to defer it. This should be flagged alongside the already-documented `response_hash`/`provider_sig` gap in `WriteReceiptPhase2` — they're the same root cause (see §5 below).

#### 1c. `ClusterSecretCache.Load` can't bootstrap a replica added after multiple rotations

`Load()` always starts probing from `currentVersion` (or `1` on a cold cache) and only checks `base` and `base+1`. If a brand-new replica joins after several rotations and the secrets manager has already pruned `v1`/`v2`/etc. (which IC §8's write contract explicitly permits: *"after 24 hours, v{N} is removed"*), that replica's very first `Load()` fails on `secretPath(1)` and it can never discover the actual current version — there's no upward scan or "give me whatever's current" query. `TestClusterSecretCacheRotationOverlap` doesn't catch this because it always sets up `v1` and `v2` together from a cold cache, never simulates a cache that's cold *and* several versions behind. Worth flagging for whoever wires this into Milestone 12/13 provider bootstrap — this will surface the first time the cluster does more than one rotation before a new replica joins.

#### 1d. Rotation-overlap clock resets on restart

`overlapExpiresAt` is set to `time.Now().Add(24h)` at the moment *this specific replica* first observes a rotation (in `Load`), not at any global "rotation started" timestamp. A replica that's offline through a real rotation and restarts after the cluster-wide 24h window has already closed will re-arm a fresh 24h acceptance window for the retired secret on itself. This is a legitimate corner case for the exact failover scenario ADR-027 exists to protect. Not urgent, but worth a note for Milestone 17 (production hardening) since it weakens the rotation story under replica churn.

#### 1e. Minor: phantom test in the test-inventory comments

`challenge_test.go`'s header comment lists `TestChallengeNonceLength` as a test in the file — it doesn't exist (I confirmed by grepping `^func Test` and by running `go test -v`). The property is compile-time guaranteed by the `[33]byte` return type, so this is genuinely untestable-as-written and was presumably dropped deliberately — but the comment block wasn't updated, and the `build.md` VERIFY block still lists it too. Harmless (the loose `-run TestChallengeNonce` prefix match in VERIFY still exits 0), just a stale comment.

---

### 2. Cross-session dependencies within M7 (as built, this sequencing is correct)

```
7.1.1 (errors.go, challenge.go)  ← foundation, everything else depends on this
  ├─ 7.2.1 (validate.go)         — also needs M2 crypto.VerifyBytes
  ├─ 7.3.1 (receipt.go: AuditResult, ReceiptFields, Phase1) — needs google/uuid added
  │    └─ 7.3.2 (receipt.go: Phase2) — needs 7.3.1's types
  ├─ 7.4.1 (secret.go, secrets_iface.go) — also needs M1 config (nominally)
  └─ 7.5.1 (jit.go) — independent, no dependency beyond 7.1.1
       └─ 7.6.1 (audit_test.go) — needs ALL of the above; specifically needs 7.4.1
          for TestCrossReplicaNonceValidation and 7.3.2 for the two-phase tests
```

All files for all six sessions are present on disk, `go build`/`go vet` are clean, and `go test` on the non-DB-dependent subset (19 tests across `challenge_test.go`, `validate_test.go`, `jit_test.go`, `secret_test.go`) passes. The one *soft* dependency `build.md` itself calls out — 7.2.1 (`ValidateResponse`) on 7.4.1 (`IsVersionValid`) — was correctly **not** hard-wired: `ValidateResponse` has no reference to `ClusterSecretCache`, exactly as the flagged resolution specifies, with the version check pushed to the (not-yet-built) Milestone 12 caller. That's the right call structurally, but it does mean **no code anywhere in the repo yet actually calls `IsVersionValid`** — it's exercised only by its own unit tests. Not a defect, just worth knowing M7 is "complete" only in the sense that Milestone 12 hasn't arrived to consume it yet.

---

### 3. Interface mismatches — what's self-documented vs. what I independently verified

The code carries an unusual number of "flagged — spec says X, we did Y, here's why" comments. I checked the ones I could verify against primary sources rather than taking them at face value:

- **`ValidateResponse` signature gap (IC §5.5 vs IC §4.2).** Confirmed — IC §5.5 really does declare a 4-parameter signature with no room for `server_challenge_ts_ms`/`provider_id`, and IC §4.2's wire format really does require both in the signing input. The 6-parameter resolution in `validate.go` matches IC §4.2's frame-2 field order exactly.
- **JIT multiplier confusion (ARCH §14/§20 say ×0.3, IC §4.2's aside says ×1.5).** Confirmed both documents say what the code claims. `jit.go`'s resolution (use ARCH's ×0.3, treat IC §4.2's aside as describing the unrelated RTO deadline) is correct and the code's separate `msPerSecond` unit-correction is sound arithmetic (mixing KB/s-derived seconds against a milliseconds latency value would otherwise make the flag fire on nearly every response).
- **PostgreSQL RLS "UPDATE needs a SELECT policy" finding.** I doubted this one initially — the conceptual RLS docs read as if an UPDATE-only policy should be self-sufficient — but the **CREATE POLICY reference page** settles it explicitly: *"an UPDATE command also needs to read data from columns... in a WHERE clause... SELECT rights are also required... the appropriate SELECT or ALL policies will be applied in addition to the UPDATE policies."* Since `WriteReceiptPhase2`'s `WHERE` clause reads `audit_result`/`abandoned_at`, and DM §6 (as originally written) had no SELECT policy on `audit_receipts`, the "UPDATE silently affects 0 rows" finding is **correct**, not a misdiagnosis. Good catch by the build session.
- **However — this fix has already landed, and the code doesn't know it.** `migrations/generator.go` now has `ALTER TABLE audit_receipts FORCE ROW LEVEL SECURITY` plus explicit `audit_receipts_app_select`/`audit_receipts_gc_select` policies, with a comment citing ADR-032 that's word-for-word the same diagnosis as `receipt.go`'s. But **`docs/data-model.md` §6 was never updated to match** — the doc still shows only the original three policies (insert, phase2_update, gc_abandon), no `FORCE ROW LEVEL SECURITY`, no SELECT policies, no ADR-032 reference. So: code (generator.go) ✅, design doc (data-model.md) ❌ stale, and `internal/audit`'s own comments (`receipt.go`, `receipt_test.go`) ❌ still describe this as an open TODO needing a 3-CODEOWNERS-reviewer migration change. All three should be reconciled — right now a maintainer reading `receipt.go` would think this blocks production, when the actual blocker (per generator.go, which is CI's ground truth) is already resolved.
- **The `response_hash`/`provider_sig` CHECK-constraint gap** (only `AuditTimeout` is reachable through `WriteReceiptPhase2`) — confirmed still live in the current `generator.go`, this one has *not* been fixed downstream. See §5 for a suggested resolution.
- **IC §6 adds a wrinkle the code's own comments don't mention:** IC §6's DML contract table says INSERT (Phase 1) should carry `provider_sig` populated. That's the *original* "Phase 1 happens after the provider responds" model — the same one `receipt.go`'s "PHASE TIMING NOTE" already flags as reinterpreted (Phase 1 now happens at dispatch, before any response exists). The code only cites ADR-015's prose as the source of that tension; IC §6 independently corroborates it. This matters because of the causal link in §5 below.

---

### 4. Import constraints

Clean. `NEGATIVE_CHECKS` greps for `internal/scoring|internal/repair|internal/payment` return nothing across all of `internal/audit/*.go`, and the `.golangci.yml` depguard `audit:` block matches (`$gostd`, `internal/config`, `internal/crypto`, `google/uuid`, `lib/pq`). One minor looseness: **`internal/config` is in the allow-list but never actually imported.** `secret.go`'s constructor deliberately never reads `NetworkProfile` — it pushes that decision to the caller, per its own doc comment ("this constructor does not re-check that, and never reads NetworkProfile itself"). That's arguably the *better* design (keeps `audit` decoupled from config), but it means the depguard entry is currently permitting an import path nothing uses — worth tightening later so a future session can't casually reach for `internal/config` without a deliberate depguard change forcing a second look.

---

### 5. Suggested better implementation — the two-phase write's real problem

The `response_hash`/`provider_sig`/`jit_flag`/`response_latency_ms` gaps in §1b and the already-flagged CHECK-constraint gap aren't three separate problems — they're one problem with one cause: **Phase 1 was moved to dispatch-time** (before any response exists), so none of the response-derived fields have anywhere to land except Phase 2, and Phase 2's signature was never widened to carry them. Two ways to fix it, worth weighing against each other rather than defaulting to the first one the code's comments propose:

- **Option A (minimal, what the comments already suggest):** widen `WriteReceiptPhase2` to accept `responseHash *[32]byte, providerSig *[64]byte, responseLatencyMs *int, jitFlag bool` (pointers nil for TIMEOUT, matching the CHECK constraint's branching). Smallest diff, keeps the current two-phase shape.
- **Option B (closer to the original spec, cleaner separation of concerns):** a genuine three-step write — Phase 1 (dispatch, unchanged), a new "record response" step that UPDATEs the response-derived columns the instant a signed response is validated (still `audit_result IS NULL`), then Phase 2 purely adjudicates PASS/FAIL/TIMEOUT + writes `service_sig`. This also gives `EvaluateJIT` a natural home to be *called and persisted* in the same step response data is recorded, rather than floating as an orphaned pure function. I'd lean this direction since it matches what IC §6 already documents and resolves the CHECK-constraint gap as a side effect rather than a workaround.

Either way, this is the actual blocker for Milestone 12: **as written today, the audit pipeline can durably record a TIMEOUT and nothing else** — a provider that legitimately proves possession of a chunk can never have that PASS committed to the ledger that everything else (scoring, escrow, repair triggers) depends on. That's worth being explicit about with Uni before M12 starts, since it's the one gap in this milestone that isn't cosmetic.

A smaller, related optimization once `audit_receipts` is partitioned: `WriteReceiptPhase2`'s `WHERE receipt_id = $1 AND ...` has no partition key, so Postgres has to scan every partition to find the row. Threading `serverChallengeTs` through (the caller already has it from Phase 1's return, or could look it up) would let Postgres prune to one partition — worth doing before the table accumulates many months of partitions.

One more small, concrete suggestion: `internal/payment/ledger.go` already has an established pattern for turning a `*pq.Error` unique-violation into a clean sentinel (I found it via a grep for `lib/pq` non-test usage). Whichever session ends up adding the `audit_receipt_nonces` INSERT should reuse that pattern for a `ErrReplayDetected`-style sentinel rather than surfacing the raw driver error — keeps the two packages' error-handling style consistent.

---

### 6. What's genuinely good here

Worth naming explicitly since it's easy for a review to read as all-critical: the fixed-array typing trick (`[33]byte`, `[32]byte`, `[64]byte` return/param types instead of slices) that makes several classes of length bugs compile-time-impossible is used consistently and well; the `IsLoaded()` retroactive addition to `ClusterSecretCache` for Milestone 11's readiness gate is a clean example of extending an earlier milestone's type without breaking its original contract; and the amount of "here's what I found live-testing against real Postgres, here's the exact repro, here's what I patched sandbox-only vs. left for CODEOWNERS" documentation embedded in `receipt.go`/`receipt_test.go` is genuinely better practice than silently working around a spec gap — it's exactly why I could independently verify (and in the RLS case, ultimately confirm) rather than guess.

---

### 7. Final Check

I traced whether anything already built in Milestones 8–11 depends on the gaps I flagged — this is worse than "M7 has an internal gap," it's actively blocking downstream milestones today:

- **`internal/api/provider.go` (M11, Session 11.6.4 — `GET /api/v1/provider/receipts`)** already selects and returns `response_hash`, `response_latency_ms`, `provider_sig` from `audit_receipts`, and lets callers filter by `result=PASS|FAIL|TIMEOUT`. Its own docstring calls this *"the provider's primary dispute evidence path (FR-058)."* Since `WriteReceiptPhase2` can currently only ever write `TIMEOUT`, this endpoint's evidence fields are `null` for every receipt in existence, and a `result=PASS` or `result=FAIL` filter returns zero rows by construction — not because of a query bug, but because no such row can ever be written. FR-058 is effectively non-functional in the current build.
- **`internal/scoring` (M8)** — `passes.go`/`rto.go` are built entirely around "concurrent audit-PASS events" and "must be called once per PASS or FAIL response," aggregating into `mv_provider_scores`. With `audit_result` never landing on `PASS`/`FAIL` in production, that materialized view has nothing but `TIMEOUT`/`NULL` rows to aggregate from — M8's scoring output is currently computed from an empty signal.
- That cascades further: M9 (repair) prioritizes off scores, M10 (payment/escrow release) almost certainly gates off audit proof. I didn't re-audit M8–M10 line-by-line (out of scope for this pass), but structurally, **the single root cause in `WriteReceiptPhase2` is very likely the reason several downstream milestones "pass their own tests" while being non-functional end-to-end.**
- I also tried to pull recent GitHub Actions run history to see whether CI is currently green or catching any of this — the API call hit an unauthenticated rate limit (60/hr, already exhausted from earlier lookups this session), so I can't confirm CI status directly. Given none of the existing test suites (M7's own, or presumably M8's) construct a genuine end-to-end PASS receipt against a live DB, I'd expect CI to stay green regardless — which is itself worth flagging: **green CI here does not mean the pipeline works.**

Nothing else changed my earlier findings; this pass only sharpened their priority.

---

### 8. Instructions for the engineering team

**Before writing any new code for M12+:** stop and fix the `WriteReceiptPhase2` gap first. This is one root cause behind what will otherwise look like four separate mysteries in M8/M9/M10/M11 later. Fixing it here is cheaper than debugging it downstream.

1. **Widen `WriteReceiptPhase2` (or split it) so PASS/FAIL is actually reachable.** Decide between the minimal signature-extension approach and the three-phase approach discussed above — write this as an explicit ADR before touching code, since it changes the two-phase write's contract and IC §5.5/§6 both need updating to match whatever is chosen, not just the Go code. Include `jit_flag` and `response_latency_ms` in this same change — they're the same shape of fix, don't split them into a separate session later.
2. **Add the `audit_receipt_nonces` write to `WriteReceiptPhase1`, wrapped in an explicit `sql.Tx`.** This is the actual replay-protection guarantee (DM §3 Invariant 5) and it is currently not enforced at all. Treat this as equal priority to #1, not a follow-up — do them in the same session since both touch `WriteReceiptPhase1`/`Phase2` and both need a live-DB test pass before merge. Follow `internal/payment/ledger.go`'s existing `pq.Error` unique-violation pattern for the replay-detected sentinel, for consistency.
3. **Reconcile the three places that currently disagree about the RLS fix.** `migrations/generator.go` already has `FORCE ROW LEVEL SECURITY` + the SELECT policies (ADR-032) — but `docs/data-model.md` §6 doesn't show them, and `receipt.go`/`receipt_test.go`'s comments still describe this as an open TODO needing a 3-CODEOWNERS-reviewer migration. Update the doc and strip the stale TODO language before anyone reads those comments and either redoes finished work or distrusts a fix that's actually already live.
4. **Add a genuine end-to-end regression test once #1 and #2 land:** dispatch → record a real signed PASS response → promote to terminal → read it back through `internal/api/provider.go`'s receipts endpoint → confirm `response_hash`/`provider_sig`/`jit_flag` all round-trip non-null. This is the smoke test that actually closes the loop across M7/M8/M11 — none of the current per-package test suites would have caught any of this, by design (each one tests its own package in isolation against fixtures that don't reflect the current schema's real constraints).
5. **Re-run M8's scoring tests against a live DB seeded with real PASS/FAIL rows** (not synthetic `mv_provider_scores` fixtures) once #1 lands, to confirm the view actually populates the way `passes.go`/`rto.go` assume. If M8 was verified only against fixtures, this hasn't been checked for real yet.
6. **Lower priority, track but don't block on:**
    - `ClusterSecretCache.Load` can't bootstrap a replica added after multiple rotations (no upward version-discovery beyond `base+1`) — needs attention before M12/M13 provider bootstrap, not before that.
    - The rotation-overlap window resets to a fresh 24h on any replica restart rather than anchoring to a global rotation timestamp — flag for M17 hardening.
    - Tighten the `audit:` depguard block in `.golangci.yml` to drop the unused `internal/config` entry, so a future session can't accidentally couple `audit` to `config` without a deliberate review.
    - Remove the phantom `TestChallengeNonceLength` reference from `challenge_test.go`'s header comment and `build.md`'s VERIFY block (test doesn't exist; property is compile-time guaranteed and doesn't need one).
    - `docs/requirements.md` FR-039 only mentions PASS/FAIL as the described transition — add TIMEOUT so the requirement text matches the schema's actual CHECK constraint.
7. **Process fix, not a code fix:** the ADR-033 partitioning change is what silently orphaned `WriteReceiptPhase1` from `audit_receipt_nonces` — a later milestone's schema change broke an earlier, already-shipped milestone's assumptions with nothing catching it. Worth adding a standing step to `build.md`'s own process: **any session that alters a table's schema (new column, new constraint, new companion table, partitioning) must grep all `internal/*` packages for existing writers/readers of that table and either update them in the same session or open an explicit tracked TODO naming the affected package.** Right now that check doesn't exist anywhere in the build process, and this is exactly the failure mode it would have caught.