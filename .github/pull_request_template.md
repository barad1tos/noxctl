## Summary

<!-- Brief description of what this PR does and why. -->

## Changes

-

## Test Plan

- [ ] `go build ./...` succeeds
- [ ] `go vet ./...` clean
- [ ] `golangci-lint run` clean (no new findings)
- [ ] `go test ./... -count=1 -race` passes
- [ ] `noxctl verify --config examples/personal.toml` passes, or the PR explains why the vault-bound gate was not run
- [ ] Destructive idempotency gate documented when applicable: `noxctl verify --config examples/personal.toml --with-apply` passes, or vault writes were explicitly skipped

## Related Issues

<!-- Link to related issues: Fixes #123, Closes #456 -->

## Checklist

- [ ] Commit messages follow Conventional Commits format
- [ ] No unused imports, dead code, or threshold raises
- [ ] New code paths covered by tests under `tests/<pkg>/` (no `*_test.go` in production package directories)
- [ ] Engineering rationale captured in code comments where helpful
- [ ] If schema or config shape changed: example in `examples/` updated AND validator coverage extended
