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

Implemented so far: strict YAML/config validation, typed host callbacks, Codex discovery and quota querying, quota aggregation, alert state transitions, atomic state storage, plugin lifecycle, Linux C ABI exports, protected Management handlers, SMTP/webhook delivery, public-safe example configuration, and systemd timer assets.

Release gate is not complete. Linux systemd integration, VPS dry-run evidence, race testing, and Linux amd64 c-shared release artifacts still need to be verified before any production rollout.

## Intended Behavior

The v0.1 plugin is designed to:

- Run as a CPA native plugin in the CPA process.
- Register protected CPA Management routes:
  - `POST /v0/management/cpa-quota-alert/check`
  - `GET /v0/management/cpa-quota-alert/status`
  - `POST /v0/management/cpa-quota-alert/test-notification`
- Inspect enabled Codex credentials through CPA host callbacks.
- Query Codex quota through CPA networking.
- Convert remaining quota into configurable Plus weekly equivalents.
- Alert below `1.5`, remind every 24 hours while still low, and recover at `1.6`.
- Treat single-account failures as partial account errors instead of whole-pool failures.
- Avoid storing credentials, raw upstream responses, account identifiers, Management Keys, or notification secrets in state.

## Important Risks

- Native plugins run inside the CPA process and have high privilege. A plugin panic or memory-safety issue can affect CPA process stability.
- Only install trusted source builds or release assets whose SHA-256 checksum you verified.
- The ChatGPT `wham/usage` backend route is an internal API surface and can change without notice.
- Plan weights are operator configuration, not official product facts.
- This repository must not contain real credentials, real host addresses, real recipient addresses, account identifiers, Management Keys, or production configuration.

## Repository Layout

```text
cmd/plugin/          Linux c-shared entry point and ABI exports
internal/abi/        host callback envelope and typed client
internal/config/     strict YAML/config parsing and validation
internal/codexquota/ Codex auth discovery and quota querying
internal/quota/      plan rules and quota aggregation
internal/monitor/    alert state machine and pending events
internal/notify/     SMTP and webhook delivery
internal/state/      atomic state persistence
internal/management/ protected Management route handlers
testdata/            sanitized fixtures only
deploy/systemd/      timer, oneshot service, CPA drop-in, and root-only config examples
examples/            conservative fake configuration examples
docs/                project harness and runbooks
```

## Build And Verify

GNU make / POSIX shell entry:

```sh
make verify
```

Windows PowerShell entry:

```powershell
powershell -ExecutionPolicy Bypass -File scripts/verify.ps1
```

Verification runs `gofmt -l`, `go test ./...`, and `go vet ./...` on every supported development host. Race testing and the Linux amd64 c-shared build require a Linux cgo toolchain and remain release gates.

## Configuration

Use `examples/plugin-config.yaml` as the public conservative baseline:

- `dry_run: true` by default
- Plus and Team only, both `1x` over `7d`
- Free plans ignored
- Terminal codes: `token_invalidated`, `token_revoked`, `deactivated_workspace`
- Low/recovery thresholds: `1.5` and `1.6`
- Timer cadence: 5 minutes via systemd
- Reminder interval: 24 hours
- Failure alert count: 3
- Stale status window: 900 seconds
- Webhook disabled by default

Use `examples/operator-confirmed-pro20.yaml` only as a local-operator assumption sample. It is not an OpenAI official fact. It maps ambiguous `pro` to Pro20x and intentionally does not define a default Pro5 mapping.

Notification secrets are referenced only through env names in plugin YAML. The example env file lives at `deploy/systemd/plugin.env.example`; copy it to an ignored root-only path such as `/etc/cpa-quota-alert-plugin/plugin.env`, set owner `root:root`, and set mode `0600`. `CPA_QUOTA_ALERT_SMTP_PASSWORD` is the actual SMTP password value in the local root-only file; keep the committed example fake. Install `deploy/systemd/cpa-service-plugin-env.conf.example` as a drop-in under the actual CPA systemd service, whose service name depends on the CPA installation. The drop-in injects the env file and asks systemd to create `/var/lib/cpa-quota-alert-plugin` with mode `0700` for the actual CPA service identity. After changing notification values, run `systemctl daemon-reload` and perform a controlled restart of CPA so the in-process plugin registration sees the new environment. The oneshot timer still does not read `plugin.env`.

The CPA Management Key must not be placed in `ExecStart`, CPA plugin YAML, environment variables, README command lines, logs, or plugin state. Put it only in the root-owned curl config copied from `deploy/systemd/curl.conf.example`, with mode `0600`.

## Systemd Timer

Install the assets on a Linux host after CPA already loads the plugin:

```sh
sudo install -d -o root -g root -m 0750 /etc/cpa-quota-alert-plugin
sudo install -o root -g root -m 0600 deploy/systemd/curl.conf.example /etc/cpa-quota-alert-plugin/curl.conf
sudo install -o root -g root -m 0600 deploy/systemd/plugin.env.example /etc/cpa-quota-alert-plugin/plugin.env
sudo install -o root -g root -m 0644 deploy/systemd/cpa-quota-alert-plugin.service /etc/systemd/system/cpa-quota-alert-plugin.service
sudo install -o root -g root -m 0644 deploy/systemd/cpa-quota-alert-plugin.timer /etc/systemd/system/cpa-quota-alert-plugin.timer
```

Before enabling the timer, edit `/etc/cpa-quota-alert-plugin/curl.conf` to replace the fake Management Key and confirm the loopback CPA Management port. Do not put the key in shell history or service arguments.

Install the CPA service env drop-in under the actual CPA service name:

```sh
sudo install -d -o root -g root -m 0755 /etc/systemd/system/<actual-cpa-service>.service.d
sudo install -o root -g root -m 0644 deploy/systemd/cpa-service-plugin-env.conf.example /etc/systemd/system/<actual-cpa-service>.service.d/cpa-quota-alert-plugin-env.conf
sudo systemctl daemon-reload
sudo systemctl restart <actual-cpa-service>.service
sudo systemctl enable --now cpa-quota-alert-plugin.timer
```

Replace `<actual-cpa-service>` with the real CPA systemd service name for that installation; this repository does not assume one. The CPA restart is what lets plugin registration see `plugin.env`. The oneshot service runs as `root` only so it can read the root-owned `0600` curl config that contains the Management Key. Its command is still only `curl --config /etc/cpa-quota-alert-plugin/curl.conf`. The curl config performs a POST with `{}` to the loopback protected Management endpoint and carries the fake example `X-Management-Key` placeholder. Replace it only inside the root-only local copy.

After the CPA restart, verify `/var/lib/cpa-quota-alert-plugin` exists, is owned by the actual CPA service identity, has mode `0700`, and is writable by that service before VPS2 or VPS1 dry-run checks. Plugin state must not be world-readable. If the host systemd version or deployment policy does not support `StateDirectory`, create `/var/lib/cpa-quota-alert-plugin` manually, assign it to the actual CPA service user and group, and keep mode `0700`.

The unit keeps `UMask=0077`, `NoNewPrivileges=true`, and hardening that still permits loopback networking and reading the curl config. It is not a long-running service. VPS1 should only receive the plugin and timer assets; do not install an additional long-running service for this plugin.

## Rollout Order

1. Validate the state directory ownership, mode, and write permission before dry-run checks.
2. Validate in an isolated VPS2 CPA instance with dry-run mode and fake notification settings.
3. Move the plugin to VPS1 only after VPS2 behavior matches expectations.
4. Run three VPS1 dry-run checks and compare with the existing monitoring view.
5. Call `test-notification` only after dry-run results are stable.
6. Enable real notification delivery only after the operator confirms the test notification.

## Rollback

Disable the timer first, then disable the plugin configuration in CPA. If hot reload does not remove the plugin cleanly, restore the previous CPA config and perform a controlled CPA restart. Rollback must not delete, disable, refresh, upgrade, or downgrade any CPA auth material.

## Third-Party References

See [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) for the YAML runtime dependency and protocol reference details.
