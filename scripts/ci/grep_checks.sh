#!/usr/bin/env bash
  set -euo pipefail
  REPO_ROOT="$(git rev-parse --show-toplevel)"
  FAIL=0

  check() {
    local name="$1"; local pattern="$2"; local scope="$3"
    if grep -rn --include="*.go" --include="*.sql" --exclude="doc.go" \
         -E "$pattern" "$REPO_ROOT/$scope" 2>/dev/null | grep -q .; then
      echo "FAIL [$name]: pattern '$pattern' found in '$scope':"
      grep -rn --include="*.go" --include="*.sql" --exclude="doc.go" \
           -E "$pattern" "$REPO_ROOT/$scope"
      FAIL=1
    else
      echo "PASS [$name]"
    fi
  }

  # Check 8: challenge_nonce must be BYTEA(33), never BYTEA(32)
  check "NONCE_LENGTH" \
    " octet_length\(challenge_nonce\)\s*=\s*32\b" \
    "."

  # Check 9: no float types in payment package
  check "NO_FLOAT_PAYMENT" \
    "(float64|float32|FLOAT|DECIMAL|NUMERIC)" \
    "internal/payment"

  # Check 10: no references to non-existent ADRs (above ADR-031)
  # Pattern: ADR-0[3-9][2-9]|ADR-[1-9][0-9]{2,}
  check "ADR_REFERENCE" \
    "ADR-0[3-9][2-9]|ADR-[1-9][0-9]{2,}" \
    "."

  # Check 11: no UPI Collect API endpoint calls
  check "NO_UPI_COLLECT" \
    "virtual_accounts|upi/collect|collect/request" \
    "internal"

  # Supplementary check (not one of the 16 numbered CI gates — add alongside them)
  # Session 7.1.1: ChallengeNonce returns [33]byte, never [32]byte. Catches a
  # call site that narrows the result to [32]byte at the Go level — the
  # source-level complement to check 8's BYTEA(33) schema check.
  check "NONCE_TRUNCATION_GO" \
    "\[32\]byte.*ChallengeNonce\(|ChallengeNonce\([^)]*\).*\[32\]byte" \
    "internal"

  exit $FAIL