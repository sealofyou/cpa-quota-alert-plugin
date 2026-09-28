# Iteration 003: CPA settings page

## Goal

Let an operator configure quota alerts from the CPA Management sidebar without editing plugin YAML or a notification environment file.

## Implementation

- Register a static `Quota Alerts` resource at `/v0/resource/plugins/cpa-quota-alert-plugin/ui`.
- Read and patch non-secret settings through CPA's protected plugin config API, then compare the saved object with the active plugin configuration after reload.
- Validate candidate settings through a protected plugin route before writing. Notification values use a separate protected route and are never returned by it.
- Keep notification values in a private host file. Linux systemd `STATE_DIRECTORY` takes precedence; other hosts use the service account's user config directory. Existing environment variables remain a fallback.
- Keep the CPA Management Key in the page session only. The static resource contains no configuration or credentials.

## Verification

- Windows `scripts/verify.ps1`: passed (structure, public-safety scan, Go tests, vet).
- Browser mock: desktop and narrow-screen rendering, rule row insertion, config load, save/readback, and notification value non-echo passed. The narrow-screen overflow found in the first visual check was corrected.
- Linux `make linux-release-gate`: pending.
- VPS1 production resource and service readback: pending.

## Operational boundary

The page does not edit the systemd timer, CPA Management Key, Codex auth files, or unrelated plugins. A saved notification value can affect the running notification sender immediately. Production rollout requires a backup of the plugin binary, CPA config, state, service unit, and private notification values when present.
