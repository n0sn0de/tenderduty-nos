# Security and secret handling

## Never provide signing material

NosNode Seer needs public validator identifiers and RPC access only. Never mount or paste validator private keys, consensus keys, mnemonics, keyring directories, or signing-service credentials into its config or container.

## Integration secrets

Webhook URLs, bot tokens, routing keys, and healthcheck URLs are credentials.

- Keep real `config.yml` files out of version control; the repository ignores them.
- Restrict local config permissions (for example, owner read/write only).
- Mount config read-only into the non-root container.
- Prefer your platform's secret-file mechanism. `PASSWORD` remains for compatibility but environment variables can be exposed through process/runtime inspection.
- Rotate any secret accidentally written to logs, shell history, an image layer, or Git.

The example uses disabled integrations and `example.invalid`/`replace-me` placeholders.

## Dashboard and Prometheus

Neither listener provides authentication. `hide_logs: true` reduces detail but is not an authorization boundary. Bind host publishing to loopback or a trusted management network. For remote access, place an authenticated TLS reverse proxy in front and restrict `/ws`, `/state`, `/logs`, and `/metrics` consistently.

The dashboard now sends a restrictive Content Security Policy, referrer policy, permissions policy, and MIME-sniffing protection. These headers do not replace network access control.

## Remote and encrypted config

`-encrypt` and `-decrypt` preserve the existing authenticated encrypted-config format for compatibility. Remote config requires a password and should use HTTPS. Treat this mechanism as legacy compatibility rather than a secrets manager: protect the password independently, validate the remote host, and avoid plaintext output on shared storage.

Examples:

```sh
nosnode-seer -encrypt -f config.yml -encrypted-config config.yml.asc
nosnode-seer -decrypt -encrypted-config config.yml.asc -f config.yml
```

## Dependency security baseline

CI runs pinned `gosec`, `staticcheck`, and `govulncheck`. Safe targeted updates removed reachable YAML and `x/net` findings found during foundation work. Two symbol-reachable findings remain inherited through the Cosmos SDK 0.45 dependency line and are listed by ID in `security/govulncheck-allowlist.txt`. The gate requires an exact match: any new finding or resolved finding fails until reviewed.

A safe fix requires upgrading the Tendermint/Cosmos compatibility line and revalidating RPC/validator behavior; it is intentionally not disguised as complete in this bounded PR. The allowlist is a review mechanism, not a declaration that the findings are harmless.

Report sensitive findings privately to repository maintainers. Do not include live endpoints, credentials, validator topology, or step-by-step exploitation details in public issues.
