# CPA Quota Alert Plugin

A CLIProxyAPI (CPA) native plugin that monitors Codex quota and sends low-noise alerts.

This is not an OpenAI official project and not a CLIProxyAPI official project.

## Status

- Version: `0.1.2`
- License: MIT
- Language: Go
- Runtime dependencies: Go standard library plus `gopkg.in/yaml.v3 v3.0.1`
- Plugin target: CLIProxyAPI C ABI v1
- Compatibility: `v7.2.83`, `v7.2.120`, `v7.2.157`
- Release asset: Linux amd64 `.so` with SHA-256 checksum on GitHub Releases

The plugin registers with CPA, checks Codex quota, and can send SMTP or webhook notifications. Mail bodies are public-safe templates with aggregate totals only. Recipients and SMTP secrets stay in a root-only env file, never in this repository. Verify the `.so` checksum from the GitHub Release before installing.

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

Verification runs `gofmt -l`, `go test ./...`, and `go vet ./...` on every supported development host. Windows `go test`, `go vet`, and `scripts/verify.ps1` pass when using a dedicated Go cache. GitHub Actions CI and Release workflows build and publish the Linux amd64 `.so`.

## Configuration

Use `examples/plugin-config.yaml` as the public conservative baseline:

- `dry_run: true` by default. Set `dry_run: false` only after SMTP env and a test notification work.
- Plus and Team only, both `1x` over `7d`
- Free plans ignored
- Terminal codes: `token_invalidated`, `token_revoked`, `deactivated_workspace`
- Low/recovery thresholds: `1.5` and `1.6`
- Timer cadence: 5 minutes via systemd
- Reminder interval: 24 hours
- Failure alert count: 3
- Stale status window: 900 seconds
- Webhook disabled by default

Use `examples/operator-confirmed-pro20.yaml` only as a local-operator assumption sample. It is not an OpenAI official fact. In that file, K12 is configured as `0.2x` over `5h`, ambiguous `pro` is configured as `20x` over `7d`, and no default Pro5 mapping is defined.

### SMTP and recipients

Notification secrets are referenced only through env names in plugin YAML. Copy `deploy/systemd/plugin.env.example` to an ignored root-only path such as `/etc/cpa-quota-alert-plugin/plugin.env`, set owner `root:root`, and set mode `0600`. Put the real SMTP user, password, From, and recipients only in that file. Keep the committed example fake.

Install `deploy/systemd/cpa-service-plugin-env.conf.example` as a drop-in under the actual CPA systemd service. After changing notification values, run `systemctl daemon-reload` and restart CPA so the in-process plugin sees the new environment. The oneshot timer does not read `plugin.env`.

The CPA Management Key must not be placed in `ExecStart`, CPA plugin YAML, environment variables, README command lines, logs, or plugin state. Put it only in the root-owned curl config copied from `deploy/systemd/curl.conf.example`, with mode `0600`.

### Mail templates

Default mail is English and contains only aggregate fields: remaining Plus-week equivalents, thresholds, partial/unresolved counts, stable error codes, and unrecognized plan names. It does not include account emails, tokens, Management Keys, or host names.

Omit `mail.templates` to keep the built-in text. To change subject or body, copy the block you need from `examples/mail-templates.example.yaml` into the CPA plugin YAML:

```yaml
mail:
  templates:
    low:
      subject: "[CPA quota] remaining {{total}} Plus-week equivalents"
      body: |
        Remaining: {{total}}
        Low threshold: {{low_threshold}}
```

Supported events: `low`, `low_reminder`, `recovery`, `data_error`, `plan_changed`, `test_notification`.

Allowed placeholders: `{{kind}}`, `{{total}}`, `{{low_threshold}}`, `{{recovery_threshold}}`, `{{consecutive_failures}}`, `{{error_code}}`, `{{unknown_plans}}`, `{{partial}}`, `{{unresolved_count}}`, `{{occurred_at}}`.

Unknown event names or placeholders are rejected at config load. Subject must be a single line. Do not put real email addresses in YAML; recipients stay in `plugin.env`.

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

After the CPA restart, verify `/var/lib/cpa-quota-alert-plugin` exists, is owned by the actual CPA service identity, has mode `0700`, and is writable by that service before enabling the timer. Plugin state must not be world-readable. If the host systemd version or deployment policy does not support `StateDirectory`, create `/var/lib/cpa-quota-alert-plugin` manually, assign it to the actual CPA service user and group, and keep mode `0700`.

The unit keeps `UMask=0077`, `NoNewPrivileges=true`, and hardening that still permits loopback networking and reading the curl config. It is not a long-running service. Install only the plugin and timer assets; do not add another long-running quota service on the CPA host.

## Rollout Order

1. Validate the state directory ownership, mode, and write permission.
2. Load the plugin with `dry_run: true` and confirm CPA logs `plugin registered`.
3. Run a few timer or Management `check` calls and compare totals with your existing quota view.
4. Fill root-only `plugin.env` and restart CPA.
5. Call `test-notification` with `dry_run: false` and confirm the generic test mail arrives.
6. Set `dry_run: false` in plugin YAML and enable the timer.
7. If another quota mailer is still running, disable it first so you do not get duplicate mail.

## Rollback

Disable the timer first, then disable the plugin configuration in CPA. If hot reload does not remove the plugin cleanly, restore the previous CPA config and perform a controlled CPA restart. Rollback must not delete, disable, refresh, upgrade, or downgrade any CPA auth material.

## Third-Party References

See [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md) for the YAML runtime dependency and protocol reference details.
