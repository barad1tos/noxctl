## Summary

<!-- Brief description of what this PR does and why. -->

## Changes

-

## Test Plan

- [ ] `go build ./...` succeeds
- [ ] `go vet ./...` clean
- [ ] `golangci-lint run` clean (no new findings)
- [ ] `go test ./... -count=1 -race` passes
- [ ] `noxctl apply --once` reaches `unchanged` within ≤3 passes
  (idempotency contract; describe manual verification if applicable)

## Related Issues

<!-- Link to related issues: Fixes #123, Closes #456 -->

## Checklist

- [ ] Commit messages follow conventional commits format
- [ ] No unused imports, dead code, or threshold raises
- [ ] New code paths covered by tests under `tests/<pkg>/` (no
  `*_test.go` in production package directories)
- [ ] Engineering rationale captured in code comments where helpful
- [ ] If schema or config shape changed: example in `examples/`
  updated AND validator coverage extended
