#!/usr/bin/env bash
# verify-precommit-hook.sh — proves.git/hooks/pre-commit blocks a
# `go vet` failure end-to-end. Plants a deliberate vet error in a
# throwaway file on a temporary branch, attempts `git commit`,
# asserts non-zero exit, then rolls back. Idempotent — safe to re-run.
#
# Usage: bash tests/scripts/verify-precommit-hook.sh
# Exit: 0 = hook blocked the planted failure; non-zero = HOOK BROKEN.

set -euo pipefail

WORKTREE_CLEAN_GUARD=$(git status --porcelain)
if [[ -n "$WORKTREE_CLEAN_GUARD" ]]; then
    echo "ERROR: worktree dirty; refusing to plant fixture." >&2
    exit 64
fi

ORIG_BRANCH=$(git rev-parse --abbrev-ref HEAD)
SMOKE_BRANCH="precommit-smoke-$(date +%s)"
FIXTURE='bear/_precommit_smoke_probe.go'

cleanup() {
    set +e
    git restore --staged --worktree -- "$FIXTURE" 2>/dev/null
    rm -f "$FIXTURE"
 # Reset any half-finished commit on the smoke branch and switch back.
    if [[ "$(git rev-parse --abbrev-ref HEAD)" == "$SMOKE_BRANCH" ]]; then
        git reset --hard HEAD >/dev/null 2>&1
        git checkout "$ORIG_BRANCH" >/dev/null 2>&1
    fi
    git branch -D "$SMOKE_BRANCH" >/dev/null 2>&1 || true
    set -e
}
trap cleanup EXIT

git checkout -b "$SMOKE_BRANCH" >/dev/null 2>&1

cat > "$FIXTURE" <<'EOF'
package bear

// Deliberate go vet failure: type mismatch in return.
func _precommitSmokeProbe() string { return 1 }
EOF

git add "$FIXTURE"

if git commit -m "smoke: planted vet failure" >/dev/null 2>&1; then
    echo "FAIL: pre-commit hook accepted a planted vet failure." >&2
    exit 1
fi

echo "PASS: pre-commit hook rejected planted vet failure."
