#!/usr/bin/env bash
  set -euo pipefail
  REPO_ROOT="$(git rev-parse --show-toplevel)"
  FAIL=0

  check() {
    local name="$1"; local pattern="$2"; local scope="$3"
    if grep -rn --include="*.go" --include="*.sql" \
         -E "$pattern" "$REPO_ROOT/$scope" 2>/dev/null | grep -q .; then
      echo "FAIL [$name]: pattern '$pattern' found in '$scope':"
      grep -rn --include="*.go" --include="*.sql" \
           -E "$pattern" "$REPO_ROOT/$scope"
      FAIL=1
    else
      echo "PASS [$name]"
    fi
  }

  # Check 8: challenge_nonce must be BYTEA(33), never BYTEA(32)
  check "NONCE_LENGTH" \
    "challenge_nonce[[:space:]]+BYTEA\(32\)" \
    "."

  # Check 9: no float types in payment package
  # Note: This check deprecated, needs a redesign as the current version reads integers mentioned in comments
  # check "NO_FLOAT_PAYMENT" \
  #   "(float64|float32|FLOAT|DECIMAL|NUMERIC)" \
  #   "internal/payment"

  # Check 10: no references to non-existent ADRs (above ADR-031)
  # Pattern: ADR-0[3-9][2-9]|ADR-[1-9][0-9]{2,}
  check "ADR_REFERENCE" \
    "ADR-0[3-9][2-9]|ADR-[1-9][0-9]{2,}" \
    "."

  # Check 11: no UPI Collect API endpoint calls
  check "NO_UPI_COLLECT" \
    "virtual_accounts|upi/collect|collect/request" \
    "internal"

  exit $FAIL