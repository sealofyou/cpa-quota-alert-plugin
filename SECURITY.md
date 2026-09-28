# Security Policy

## Supported Versions

This repository publishes Linux amd64 plugin builds. Treat each release as operator-installed software: verify the checksum, review local config, and keep secrets out of Git.

## Native Plugin Risk

This project targets a CLIProxyAPI native plugin loaded into the CPA process. Treat the plugin as high-privilege code:

- Install only trusted source builds or release assets whose SHA-256 checksum you verified.
- Review configuration before loading the plugin into a real CPA instance.
- Run new builds in an isolated CPA environment before production dry-run.
- Keep logs redacted and avoid storing raw upstream responses.

## Sensitive Data Rules

Do not commit secrets, real host addresses, real recipient addresses, account identifiers, production configuration, private keys, environment files, or generated state files.

The plugin must not receive or store the CPA Management Key. Codex access material should exist only in memory for a single quota query and must not be written to state, logs, responses, or examples.

The CPA settings page is an unauthenticated static resource; it must contain no operator configuration or credentials. Its data requests use CPA's authenticated Management API. Notification values saved through the page are kept in a private service-account directory and are never returned by the API. Protect backups of this file as credentials.

## Reporting

Use GitHub Security Advisories or private vulnerability reporting when available. Do not publish exploit details, credentials, or production configuration in public issues.
