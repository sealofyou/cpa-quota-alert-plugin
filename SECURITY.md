# Security Policy

## Supported Versions

This repository is a v0.1 release candidate and has no supported production release yet.

## Native Plugin Risk

This project targets a CLIProxyAPI native plugin loaded into the CPA process. Treat the plugin as high-privilege code:

- Install only trusted source builds or release assets whose SHA-256 checksum you verified.
- Review configuration before loading the plugin into a real CPA instance.
- Run new builds in an isolated CPA environment before production dry-run.
- Keep logs redacted and avoid storing raw upstream responses.

## Sensitive Data Rules

Do not commit secrets, real host addresses, real recipient addresses, account identifiers, production configuration, private keys, environment files, or generated state files.

The plugin must not receive or store the CPA Management Key. Codex access material should exist only in memory for a single quota query and must not be written to state, logs, responses, or examples.

## Reporting

Open a GitHub security advisory or private issue path when the public repository is created. Until then, do not publish exploit details in public issues.
