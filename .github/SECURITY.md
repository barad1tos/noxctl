# Security Policy

## Scope

`noxctl` is a macOS-only CLI that mutates Bear notes in place. It has no networking code and stores no credentials. The relevant security surface is limited to:

- Local file-system access (`bearcli` shell-out, FSEvents watcher, per-project state JSON).
- Supply chain attacks through Go module dependencies.
- Distribution integrity (the published binary on the release page).

## Supported Versions

| Version | Supported |
|---------|-----------|
| 1.x.x   | Yes       |
| < 1.0   | No        |

Only the latest minor release line receives security fixes.

## Reporting a Vulnerability

**Preferred: GitHub Private Vulnerability Reporting.**

Use the [Report a vulnerability](https://github.com/barad1tos/noxctl/security/advisories/new) button in this repository's Security tab. This keeps your report confidential until a fix is available.

Please include:

- Description of the vulnerability
- Steps to reproduce
- Affected versions
- Potential impact

## What to Expect

- Acknowledgment within **7 days**
- Assessment and response within **30 days**
- For confirmed issues: a fix in the next release
- Credit in the changelog (unless you prefer anonymity)

## What Qualifies

- Code-injection vectors via the configuration file (TOML loader, blueprint dispatch).
- Path-traversal or symlink-following bugs in the state-file writer or `bearcli` shell-out.
- Dependency vulnerabilities in the runtime Go module chain.
- Distribution integrity issues (binary tampering, supply chain).

## What Does NOT Qualify

- Cosmetic bugs (wrong note content, idempotency drift) — use [regular issue templates](https://github.com/barad1tos/noxctl/issues/new/choose).
- Bear app vulnerabilities — report those to [Shiny Frog](https://bear.app).
- Theoretical attacks requiring physical access to the developer's machine.

## Bug Bounty

This is a solo-developer open source project. There is no monetary bug bounty, but confirmed reporters will be credited in the release notes.
