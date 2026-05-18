# Contributing to noxctl

Thanks for your interest in contributing. Bug reports, blueprint extensions, and quality-of-life improvements are all welcome.

By participating, you agree to uphold the [Code of Conduct](.github/CODE_OF_CONDUCT.md).

## Getting started

1. Fork the repository and clone it locally.
2. Make sure you have **Go ≥ 1.26** installed (`go version`).
3. Install the local hooks once:
   ```bash
   pre-commit install
   pre-commit install --hook-type commit-msg
   ```
4. Build everything:
   ```bash
   go build ./...
   ```
5. Run the gates the CI will run:
   ```bash
   go vet ./...
   gofmt -l .                    # must be empty
   golangci-lint run             # must be clean
   go test -race -count=1 ./...  # must pass
   ```

## What stays out of scope

- **Cross-platform support.** Bear is macOS-only; `bearcli` is macOS-only; FSEvents is macOS-only. PRs that introduce cross-platform shims will be closed.
- **New rendering blueprints.** The catalog is deliberately closed at six blueprints. Add a use case to an existing blueprint via configuration before proposing a new one.
- **Networking, telemetry, auto-update.** This is a local-vault tool. It never reaches out.
- **AI assistants in committed code or comments.** No tool-vendor references in code, comments, commit messages, or doc files.

## Code conventions

- Tests live under `tests/<pkg>/`. Zero `*_test.go` files in production package directories — refactor before reaching for an in-package exception.
- Lint thresholds are load-bearing: `gocognit` and `gocyclo` ≤ 15, `lll` ≤ 150, `dupl` ≥ 50 tokens. Extract a helper rather than raising a threshold.
- Doc comments use engineering rationale ("the daemon's self-write gate") instead of internal review-loop vocabulary.
- Commit messages follow Conventional Commits with a 72-character header. Body lines have no hard wrap.

## Filing a bug

Open a [Bug Report](https://github.com/barad1tos/noxctl/issues/new/choose) with the noxctl version, macOS version, Bear version, reproduction steps, and the relevant daemon-log excerpt (`~/.cache/regen-watchd.log`).

## Proposing a feature

Open a [Feature Request](https://github.com/barad1tos/noxctl/issues/new/choose) that motivates the change before sketching implementation. Discussion on shape > a PR that surprises everyone.

## Security

Use [private vulnerability reporting](https://github.com/barad1tos/noxctl/security/advisories/new) for security issues. See [SECURITY.md](.github/SECURITY.md) for the full disclosure policy.

## License

By contributing, you agree that your contributions will be licensed under the [MIT License](LICENSE).
