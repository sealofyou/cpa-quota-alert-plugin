# CPA Quota Alert Plugin

A CLIProxyAPI (CPA) native plugin that monitors Codex quota and sends low-noise alerts.

This is not an OpenAI official project and not a CLIProxyAPI official project.

## Status

- Version: `0.2.1`
- License: MIT
- Language: Go
- Runtime dependencies: Go standard library plus `gopkg.in/yaml.v3 v3.0.1`
- Plugin target: CLIProxyAPI C ABI v1
- Compatibility: `v7.2.83`, `v7.2.120`, `v7.2.157`
- Release asset: Linux amd64 `.so` with SHA-256 checksum on GitHub Releases

The plugin registers with CPA, checks Codex quota, and can send SMTP or webhook notifications. Mail bodies are public-safe templates with aggregate totals only. Recipients and notification secrets stay in a private host file or a root-only env file, never in this repository. Verify the `.so` checksum from the GitHub Release before installing.

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
- Treat a partial total as a lower bound: do not send a low alert from it. After three consecutive incomplete checks, send one data-error alert; a complete low snapshot still alerts immediately.
- Avoid storing credentials, raw upstream responses, account identifiers, Management Keys, or notification secrets in state.

## Important Risks

- Native plugins run inside the CPA process and have high privilege. A plugin panic or memory-safety issue can affect CPA process stability.
- Only install trusted source builds or release assets whose SHA-256 checksum you verified.
- The ChatGPT `wham/usage` backend route is an internal API surface and can change without notice.
- Plan weights are operator configuration, not official product facts. The shipped catalog is a conservative starter for monitoring and remains editable.
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
internal/management/ protected Management routes and CPA settings page
internal/secrets/    private notification value storage
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

### CPA settings page

Open **Quota Alerts** in the CPA Management sidebar, or visit `/v0/resource/plugins/cpa-quota-alert-plugin/ui`. Enter the CPA Management Key for this page session, then edit alert thresholds, plan rules, SMTP, Webhook, mail templates, or advanced query settings. The page keeps the Management Key in memory only. It saves non-secret fields through CPA's protected plugin config API and checks both the saved config and the plugin's active settings before reporting success.

Notification values entered on the page are sent only to a protected plugin Management route. With systemd `StateDirectory`, the plugin stores them at `$STATE_DIRECTORY/notification-secrets.json`; otherwise it uses the CPA service account's user config directory at `cpa-quota-alert-plugin/notification-secrets.json`. The directory must be private and the file is `0600` on Linux. Values are never returned to the page. A nonempty stored value overrides an environment variable with the same name; if no stored value exists, the existing environment variable is used. Clearing a stored value returns to that fallback. The CPA Management Key is never written to this file. Back up this private file alongside the plugin state before a production upgrade; restrict backup permissions as well.

The page validates the complete candidate configuration before saving and reads back the active runtime configuration after CPA's asynchronous reload. If the page says “saved but not confirmed active”, inspect the CPA plugin status and correct the configuration before relying on the new settings. Saving notification values changes the values used by the running plugin immediately; arrange credential changes during a quiet period.

Use `examples/plugin-config.yaml` as the public conservative baseline:

- `dry_run: true` by default. Set `dry_run: false` only after the notification values and a test notification work.
- Plus and Team, both `1x` over `7d`
- Exact Pro tiers: `pro100`, `pro200`, and `pro500`, represented as separate editable `7d` rules
- Upstream `prolite`, represented as a separate editable `7d` rule
- Ambiguous bare `pro` is not a default alias. It remains `plan_changed` unless an operator adds an explicit account-specific mapping.
- Free plans ignored
- Terminal codes: `token_invalidated`, `token_revoked`, `deactivated_workspace`
- Low/recovery thresholds: `1.5` and `1.6`
- Timer cadence: 5 minutes via systemd
- Reminder interval: 24 hours
- Failure alert count: 3
- Stale status window: 900 seconds
- Webhook disabled by default

Use `examples/plan-catalog.yaml` when you only need the editable plan-rule block. Its weights are package defaults for monitoring in Plus-week equivalents, not OpenAI official conversion factors. The exact Pro tier entries are independent, and `prolite` is independent from `pro`. If your upstream returns only bare `pro`, leave it unknown until you have an explicit account-specific mapping; do not silently treat it as Pro 100, Pro 200, Pro 500, or any old Pro20-style pool.

### SMTP and recipients

Notification secrets are referenced only through env names in plugin YAML. You can enter their values in the CPA settings page. Existing env deployments remain supported: copy `deploy/systemd/plugin.env.example` to an ignored root-only path such as `/etc/cpa-quota-alert-plugin/plugin.env`, set owner `root:root`, and set mode `0600`. Put the real SMTP user, password, From, and recipients in that private file when using the env method. Keep the committed example fake.

Install `deploy/systemd/cpa-service-plugin-env.conf.example` as a drop-in under the actual CPA systemd service. After changing notification values, run `systemctl daemon-reload` and restart CPA so the in-process plugin sees the new environment. The oneshot timer does not read `plugin.env`.

The CPA Management Key must not be placed in `ExecStart`, CPA plugin YAML, environment variables, README command lines, logs, or plugin state. The settings page accepts it for the current browser session only. For the timer, put it only in the root-owned curl config copied from `deploy/systemd/curl.conf.example`, with mode `0600`.

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

Unknown event names or placeholders are rejected at config load. Subject must be a single line. Do not put real email addresses in YAML; recipients stay in the private notification store or `plugin.env`.

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

### Plan catalog, unknown plans, and Pro Lite

The public default plan catalog is:

| Canonical rule | Default aliases | Window | Default weight |
| --- | --- | --- | --- |
| `plus` | `plus`, `chatgptplus` | `7d` | `1` |
| `team` | `team`, `chatgptteam` | `7d` | `1` |
| `pro100` | `pro100`, `chatgptpro100` | `7d` | `100` |
| `pro200` | `pro200`, `chatgptpro200` | `7d` | `200` |
| `pro500` | `pro500`, `chatgptpro500` | `7d` | `500` |
| `prolite` | `prolite`, `chatgptprolite` | `7d` | `1` |

OpenAI's current published Pro names are Pro 100, Pro 200, and Pro 500. This plugin represents those exact tiers independently. The numeric weights above are editable monitoring weights in this plugin's Plus-week-equivalent metric; they are not official conversion factors.

An upstream `prolite` value is distinct from `pro`, and the shipped catalog keeps it separate. Do not append `prolite` to a higher-weight Pro rule or ignore it just to dismiss a warning. Plan weights remain operator policy.

An upstream bare `pro` value is intentionally ambiguous. It is not a default alias for any exact Pro tier, and it is not treated as the highest tier. If a site can prove a specific account maps bare `pro` to a specific pool, add that mapping locally for that operator/account; do not commit that private assumption as a package default.

An unknown plan pauses the aggregate quota decision. In v0.2.1, incomplete checks keep the unknown-plan alert active, even when the known subtotal exceeds the recovery threshold. Only a complete successful check clears it. This prevents repeated plan-change emails when the unknown account temporarily fails to respond, while retaining persistent data-error alerts.
