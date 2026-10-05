# ITERATION-002 - v0.1 Release Candidate Evidence

## Goal

Move the public documentation from scaffold/pre-alpha wording to a v0.1 release candidate record without claiming production readiness.

## Completed Evidence

- Windows verification passed: `go test ./...`, `go vet ./...`, and `scripts/verify.ps1`.
- Windows default Go cache produced ACL warnings, but a dedicated `GOCACHE` run was clean.
- Isolated Linux verification using the official `golang:1.24-bookworm` image passed:
  - `go test ./...`
  - `go test -race ./...`
  - `go vet ./...`
  - Linux amd64 c-shared build
- Linux amd64 `.so` candidate SHA-256:

```text
c66bb40b9fb80b44a7494105b1a8b93f5b7631d6fde257d23a97cb658123e63a
```

- Official CPA `v7.2.83` and `v7.2.120` loopback instances both loaded the plugin and passed Management-key authentication.
- CPA behavior checks passed for normal `2.1` mixed-window aggregation, partial `2.0`, terminal zeroing, unknown plans, third consecutive failure `data_error`, low/recovery transitions, stale status, and concurrent `409`.
- Root-only SMTP configuration isolation was validated with `test-notification` delivered.

## Public-Safety Boundary

- Do not publish real paths, IP addresses, recipient addresses, Management Keys, OAuth material, SMTP credentials, account identifiers, or production configuration.
- The operator-confirmed Pro20 sample is not an OpenAI official fact. In that sample, K12 is `0.2x` over `5h`, ambiguous `pro` is `20x` over `7d`, and Pro5 has no default mapping.
- Do not describe the project as production-ready before the remaining gates close.

## Remaining Release Gates

- Public GitHub CI must pass on the public repository.
- Create and publish the `v0.1.0` tag/release.
- Confirm the GitHub Release contains only `dist/cpa-quota-alert-plugin.so` and `dist/cpa-quota-alert-plugin.so.sha256` as release assets.
- Run three VPS1 production dry-run checks and compare with the existing monitoring view.
- Rehearse rollback before enabling real notification delivery.

## Notes

`ITERATION-001` remains the historical public harness scaffold record. This iteration records the implementation and validation evidence that supersedes scaffold-only status text.

## v0.2.1 regression validation

- Unknown-plan alerts now survive incomplete checks until a complete successful snapshot.
- Regression tests cover low and high partial subtotals, repeated partial failures, resolution, retriggering after resolution, and independently configured Pro Lite weights.
- The old monitor failed the new tests before the change; the fixed monitor passes.
- Windows verify.ps1 passed, including all Go tests, vet, formatting, placeholder and public-safety scans.
- Linux make linux-release-gate passed, including all Go tests, vet, race and amd64 c-shared build.
- A loopback-only CPA v8.0.10 instance loaded v0.2.1 with a separate auth directory and state file, no notification channels, and filesystem denial of production auth/state paths. Real quota checks with an explicit test-only rule returned complete, computable snapshots and stale=false. Production weights and rollout remain a separate operator action.
