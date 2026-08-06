# Third Party Notices

This project is MIT licensed.

## Runtime Dependencies

### `gopkg.in/yaml.v3`

Used to decode CPA plugin configuration supplied through `ConfigYAML`.

- Version: `v3.0.1`
- Source: <https://gopkg.in/yaml.v3>
- License: dual MIT and Apache License 2.0
- Full upstream license text: [`YAML_V3_LICENSE`](YAML_V3_LICENSE)

## Protocol and Behavior References

- CLIProxyAPI official plugin examples: referenced to understand the native plugin ABI, Management routes, and host callback behavior.
- `AllenReder/CLIProxyAPI-Quota-Inspector`: referenced to understand quota inspection behavior and ecosystem context.

The native C ABI declarations in `cmd/plugin/main_linux.go` follow the public CLIProxyAPI v1 interface and official Go examples. No quota, notification, or Management business logic was copied from either reference.

Future copied or adapted third-party implementation code must be recorded here with its exact source, license, and affected files.
