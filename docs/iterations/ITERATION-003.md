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
- Linux Go tests, vet, race check, and c-shared `.so` build: passed in a VPS1 Go container.
- VPS1 production: v0.2.0 registered; deployed `.so` SHA-256 `676754fae5d14cfd4a24f499ef0d355165a72f5cecb46f7391163918d85aadde`; CPA and timer active with `NRestarts=0`. The UI resource returned 200, unauthenticated protected routes and `/v1/models` returned 401, and authenticated config/status/effective-config/secret-name routes returned 200. Scheduled checks at 11:50 and 11:55 UTC were complete; the status route reported `stale=false` and `failure_count=0`.
- Existing saved rules and active rules matched after sorting. The active view adds an empty mail-template object absent from the saved YAML, so raw JSON equality is not a valid no-change check for those two fields.
- The rollout shell stream had an extra CR after its success marker and exited 1; independent post-rollout readback confirmed the new binary, healthy services, protected routes, and real scheduled checks.

## Operational boundary

The page does not edit the systemd timer, CPA Management Key, Codex auth files, or unrelated plugins. A saved notification value can affect the running notification sender immediately. Production rollout requires a backup of the plugin binary, CPA config, state, service unit, and private notification values when present.

## v0.3.0 per-tier plan weights

- Problem: plan weights were correct per account in code, but the shipped sample and the
  deployed rule set folded the ambiguous upstream value `pro` into a single `pro20` rule.
  Nothing stopped an operator from adding a second upstream tier, such as `prolite`, to that
  same rule as an alias, which would have weighed a Pro 100 account at the Pro 200 multiplier.
- Change: `internal/config` now knows which plan types the upstream account surface reports as
  distinct values and rejects any plan rule that claims more than one of them, naming the
  conflicting tiers. Operator-local spellings such as `chatgptplus` or `pro20` are not upstream
  values and stay usable as aliases. No default multiplier is added for any tier.
- Sample: `examples/operator-confirmed-pro20.yaml` is renamed to
  `examples/operator-confirmed-plan-tiers.yaml` and gives `prolite` and `pro` separate rules and
  separate weights. `promax` and every other unconfirmed tier stay unmapped so that an account on
  one of them stops the weighted total and raises a plan-change alert.
- Regression tests: a rule claiming two upstream tiers is rejected (and is accepted again when the
  guard is removed, which is how the guard was shown to be load-bearing); a mixed
  plus/prolite/pro pool weighs each account by its own tier; swapping the two Pro tiers changes
  the total; an unconfigured tier still stops the total; a failed account keeps the snapshot
  partial without hiding the unknown tier; a terminal credential still counts as zero capacity.
- Plan multipliers remain operator policy. Nothing in this repository asserts an OpenAI
  entitlement ratio for any tier.
