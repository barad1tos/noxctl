#!/usr/bin/env bash
#
# scripts/ship-gate.sh — hard gate before any release / migration /
# catalog cut. Composes hermetic + vault-bound checks into a single
# PASS/FAIL signal.
#
# Tier 1 (hermetic) runs anywhere: CI, dev box, fresh clone. Tier 2
# (vault-bound) requires Bear + bearcli on the host; CI skips it.
#
# Pass arguments through to `noxctl verify` for opt-in extras:
#
#   scripts/ship-gate.sh                  # default — read-only
#   scripts/ship-gate.sh --with-apply     # destructive idempotency
#   scripts/ship-gate.sh --strict         # fail on untracked tag-families
#
# Honours these env vars:
#   NOXCTL_CONFIG  — catalog path (default examples/roman.toml)
#   SHIP_GATE_HERMETIC_ONLY  — set to skip Tier 2 (used by CI)

set -euo pipefail

cd "$(git rev-parse --show-toplevel)"

CONFIG="${NOXCTL_CONFIG:-examples/roman.toml}"

echo "═══ ship-gate ═══"
echo "  repo:    $(pwd)"
echo "  config:  $CONFIG"
echo "  args:    $*"
echo

# ---------------- Tier 1: hermetic ----------------

run_step() {
  printf "→ %-32s " "$1"
  shift
  if "$@" >/tmp/ship-gate-step.log 2>&1; then
    echo "PASS"
  else
    rc=$?
    echo "FAIL"
    echo "--- last 30 lines ---"
    tail -30 /tmp/ship-gate-step.log
    return "$rc"
  fi
}

echo "Tier 1: hermetic"
run_step "go build"      go build ./...
run_step "go vet"        go vet ./...
run_step "gofmt -l"      bash -c 'unfmt=$(gofmt -l .); [ -z "$unfmt" ] || { echo "$unfmt"; exit 1; }'
run_step "golangci-lint" golangci-lint run
run_step "go test"       go test -race -count=1 ./...
run_step "noxctl validate" go run ./cmd/noxctl validate "$CONFIG"

# ---------------- Tier 2: vault-bound ----------------

if [ -n "${SHIP_GATE_HERMETIC_ONLY:-}" ]; then
  echo
  echo "Tier 2 skipped (SHIP_GATE_HERMETIC_ONLY set)."
  echo
  echo "✓ ship-gate: PASS (hermetic only)"
  exit 0
fi

echo
echo "Tier 2: vault-bound"
echo "→ noxctl verify --config $CONFIG $*"
echo
go run ./cmd/noxctl verify --config "$CONFIG" "$@"

echo
echo "✓ ship-gate: PASS"
