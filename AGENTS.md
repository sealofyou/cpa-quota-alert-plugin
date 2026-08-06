# CPA Quota Alert Plugin Agent Guide

This repository is the public project harness for `cpa-quota-alert-plugin`, an MIT-licensed Go native plugin for CLIProxyAPI (CPA).

## Project Facts

- Public repository target: `github.com/sealofyou/cpa-quota-alert-plugin`
- Go module: `github.com/sealofyou/cpa-quota-alert-plugin`
- License: MIT
- Runtime dependency policy: Go standard library plus `gopkg.in/yaml.v3 v3.0.1`
- Plugin target: CLIProxyAPI C ABI v1 native plugin
- Minimum CPA compatibility target: `v7.2.83`
- Additional compatibility target: `v7.2.120`
- First release target: Linux amd64 `.so` plus SHA-256 checksum
- Current phase: v0.1 release candidate; implementation and isolated CPA validation are complete, with production rollout gates still open

## Scope Rules

- Keep this repository public-safe. Do not commit credentials, real host addresses, real recipient addresses, account identifiers, Management Keys, OAuth material, SMTP credentials, or production configuration.
- Use only documented fake examples such as `alerts@example.com`.
- The `wham/usage` endpoint is an internal ChatGPT backend surface, not a stable public API. Treat response shape changes as a product risk.
- Plan rules are operator configuration. Do not encode private pool assumptions as official package defaults.
- Native plugin work runs in the CPA process. Treat memory safety, panic recovery, timeouts, and redaction as release gates.

## Implementation Boundaries

Keep changes narrow and public-safe. The current implementation uses these directories:

- `cmd/plugin/` for the c-shared plugin package
- `internal/abi/` for C ABI and host callback envelope handling
- `internal/config/` for configuration parsing and validation
- `internal/codexquota/` for Codex quota request and response parsing
- `internal/quota/` for plan rules and aggregation
- `internal/monitor/` for alert state transitions
- `internal/notify/` for SMTP and webhook delivery
- `internal/state/` for atomic state persistence
- `internal/management/` for CPA Management routes

## Verification

Before claiming completion, run the narrow verification that matches the current phase:

- `git diff --check`
- placeholder and public-safety scans over tracked text files
- `go test ./...`
- `go vet ./...`
- `scripts/verify.ps1` on Windows

Release-candidate evidence is recorded in `docs/iterations/ITERATION-002.md`. Public GitHub CI, `v0.1.0` tag/release publication, VPS1 three-round production dry-run, and rollback rehearsal remain release gates.

## Git Discipline

- Work on feature branches, not directly on `main`.
- Commit messages must follow the Lore Commit Protocol with an intent line and useful trailers such as `Confidence:`, `Scope-risk:`, `Tested:`, and `Not-tested:`.
- Keep diffs small and public-safe.
