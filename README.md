# CPA Quota Alert Plugin

Pre-alpha public scaffold for a CLIProxyAPI (CPA) native plugin that will monitor Codex quota and send low-noise quota alerts.

This is not an OpenAI official project and not a CLIProxyAPI official project.

## Status

- Phase: pre-alpha scaffold
- License: MIT
- Language: Go
- Runtime dependency target: Go standard library only
- Plugin target: CLIProxyAPI C ABI v1
- Minimum CPA compatibility target: `v7.2.83`
- Additional compatibility target: `v7.2.120`
- First release target: Linux amd64 `.so` with SHA-256 checksum

No ABI adapter, quota query, alert state machine, notification sender, or deployable plugin binary exists in this scaffold.

## Intended Behavior

The planned v0.1 plugin will:

- Run as a CPA native plugin in the CPA process.
- Register protected CPA Management routes for `check`, `status`, and `test-notification`.
- Inspect enabled Codex credentials through CPA host callbacks.
- Query Codex quota through CPA networking.
- Convert remaining quota into configurable Plus weekly equivalents.
- Alert below `1.5`, remind every 24 hours while still low, and recover at `1.6`.
- Treat single-account failures as partial account errors instead of whole-pool failures.
- Avoid storing credentials, raw upstream responses, account identifiers, or notification secrets in state.

## Important Risks

- Native plugins run inside the CPA process and have high privilege.
- Only install trusted source builds or release assets whose SHA-256 checksum you verified.
- The ChatGPT `wham/usage` backend route is an internal API surface and can change without notice.
- Plan weights are operator configuration, not official product facts.
- This repository must not contain real credentials, real host addresses, real recipient addresses, account identifiers, or production configuration.

## Repository Layout

```text
cmd/plugin/          future c-shared main package and ABI exports
internal/abi/        future C ABI and host callback envelope handling
internal/config/     future configuration parsing and validation
internal/codexquota/ future Codex quota request and parsing
internal/quota/      future plan rules and quota aggregation
internal/monitor/    future alert state machine and pending events
internal/notify/     future SMTP and webhook delivery
internal/state/      future atomic state persistence
internal/management/ future Management route handlers
testdata/            sanitized fixtures only
deploy/systemd/      future service, timer, and curl config examples
examples/            conservative fake configuration examples
docs/                project harness and runbooks
```

## Development

GNU make / POSIX shell entry:

```sh
make verify
```

Windows PowerShell entry:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/verify.ps1
```

The scaffold has no Go packages yet, so Go test and vet checks are skipped until implementation files exist. When implementation packages exist, verification expands to `go test ./...`, `go vet ./...`, and read-only `gofmt -l` checks.

## Third-Party References

This project studies CLIProxyAPI official plugin examples and `AllenReder/CLIProxyAPI-Quota-Inspector` for protocol and behavior reference only. No source code is copied in this scaffold.
