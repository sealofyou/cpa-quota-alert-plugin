# CPA Quota Alert Plugin

Pre-alpha implementation of a CLIProxyAPI (CPA) native plugin for monitoring Codex quota and sending low-noise quota alerts.

This is not an OpenAI official project and not a CLIProxyAPI official project.

## Status

- Phase: pre-alpha implementation
- License: MIT
- Language: Go
- Runtime dependencies: Go standard library plus `gopkg.in/yaml.v3 v3.0.1`
- Plugin target: CLIProxyAPI C ABI v1
- Minimum CPA compatibility target: `v7.2.83`
- Additional compatibility target: `v7.2.120`
- First release target: Linux amd64 `.so` with SHA-256 checksum

Implemented so far: strict YAML/config validation, typed host callbacks, Codex discovery and quota querying, quota aggregation, alert state transitions, atomic state storage, plugin lifecycle, and Linux C ABI exports.

Management handlers, SMTP/webhook delivery, deployment assets, Linux integration evidence, and release binaries are not implemented yet.

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
cmd/plugin/          Linux c-shared entry point and ABI exports
internal/abi/        host callback envelope and typed client
internal/config/     strict YAML/config parsing and validation
internal/codexquota/ Codex auth discovery and quota querying
internal/quota/      plan rules and quota aggregation
internal/monitor/    alert state machine and pending events
internal/notify/     future SMTP and webhook delivery
internal/state/      atomic state persistence
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

Verification runs `gofmt -l`, `go test ./...`, and `go vet ./...` on every supported development host. Race testing and the Linux amd64 c-shared build require a Linux cgo toolchain and remain release gates.

## Third-Party References

See [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) for the YAML runtime dependency and protocol reference details.
