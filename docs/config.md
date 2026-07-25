# Configuration reference

NosNode Seer reads a primary YAML file (`config.yml` by default) and then every `*.yml` file in `chains.d`. A chain file is decoded as one chain object; its filename without `.yml` becomes the chain display name and overrides a same-name entry from the primary file.

Print the embedded example without monitoring:

```sh
nosnode-seer -example-config
```

## Global keys

| Key | Type | Meaning |
|---|---|---|
| `enable_dashboard` | bool | Open the dashboard/WebSocket listener. |
| `listen_host` | string | Optional dashboard bind host or unbracketed IP. Omitted/blank preserves the historical wildcard bind. |
| `listen_port` | integer/string | Dashboard port; default example `8888`. |
| `hide_logs` | bool | Hide dashboard logs and some node detail. This is not authentication. |
| `node_down_alert_minutes` | integer | Delay before node-down alerting. |
| `node_down_alert_severity` | string | PagerDuty severity for node-down alerts. |
| `prometheus_enabled` | bool | Open the Prometheus listener. |
| `prometheus_listen_host` | string | Optional Prometheus bind host or unbracketed IP. Omitted/blank preserves the historical wildcard bind. |
| `prometheus_listen_port` | integer | Prometheus port; default example `28686`. |
| `pagerduty`, `discord`, `telegram`, `slack` | object | Global integration gates/default credentials. |
| `healthcheck` | object | Optional dead-man's-switch ping. |
| `chains` | map | Display name to chain configuration. |

A notification must be enabled globally **and** inside the chain's `alerts` object. Blank chain-specific credentials inherit global values.

### Notification objects

- `pagerduty`: `enabled`, `api_key`, `default_severity`
- `discord`: `enabled`, `webhook`, `mentions`
- `telegram`: `enabled`, `api_key`, `channel`, `mentions`
- `slack`: `enabled`, `webhook`, `mentions`
- `healthcheck`: `enabled`, `ping_url`, `ping_rate` (seconds)

## Chain keys

| Key | Type | Meaning |
|---|---|---|
| `chain_id` | string | Expected network ID; endpoints on another network are rejected. |
| `valoper_address` | string | Validator operator address used to discover consensus identity. |
| `valcons_override` | string | Optional consensus address override. |
| `public_fallback` | bool | Permit public endpoint discovery when configured nodes fail. |
| `alerts` | object | Alert thresholds and per-chain integration gates. |
| `nodes` | list | RPC URL plus `alert_if_down`. |

RPC URLs must include a scheme. Tendermint TCP URLs and HTTP(S) URLs are accepted by the existing client behavior. Prefer authenticated/private or verified TLS endpoints.

## Alert keys

| Key | Meaning |
|---|---|
| `stalled_enabled`, `stalled_minutes` | Alert after no new block for the configured duration. |
| `consecutive_enabled`, `consecutive_missed`, `consecutive_priority` | Consecutive miss alert and severity. |
| `percentage_enabled`, `percentage_missed`, `percentage_priority` | Sliding-window missed percentage alert and severity. |
| `alert_if_inactive` | Alert when validator leaves the active set, is jailed, or tombstoned. |
| `alert_if_no_servers` | Alert when no RPC endpoint is usable. |
| `pagerduty`, `discord`, `telegram`, `slack` | Per-chain enablement and optional overrides. |

Legacy booleans `discord_alerts`, `telegram_alerts`, and `pagerduty_alerts` remain decoded where present because their struct tags are retained. New configurations should use the nested objects in `example-config.yml`.

## Config precedence

1. `-f PATH` selects the primary file.
2. If `-f` is not changed and `CONFIG` is set, `CONFIG` selects it.
3. `-cc DIR` selects the chain directory.
4. Chain files override same-name primary `chains` entries.

Unknown YAML fields are currently tolerated for legacy compatibility. Review spelling carefully; strict decoding is a later migration phase.

## Listener bind compatibility and guidance

The host fields are optional and the port fields are unchanged. Seer validates
all enabled listener addresses before opening either one, constructs explicit
IPv4/IPv6 endpoints with Go's `net.JoinHostPort` rules, and returns bind failures
to the process caller. A disabled listener ignores its host/port and opens
nothing.

| Deployment | Dashboard host | Prometheus host | Result |
|---|---|---|---|
| Existing config (host keys omitted/blank) | wildcard | wildcard | Exact historical process bind behavior; secure it with firewall/publishing rules. |
| Bare metal, local only | `127.0.0.1` or `::1` | `127.0.0.1` or `::1` | Loopback-only process listeners. |
| Private management interface | explicit interface IP | explicit interface IP | Accept only on that interface; authentication is still external. |
| Bridged container using `example-docker-compose.yml` | omitted/blank | omitted/blank | Listener remains reachable inside the container while host publishing stays `127.0.0.1`. |
| Disabled | ignored | ignored | No socket is opened. |

Provide a host only: `127.0.0.1`, `::1`, `localhost`, or a valid hostname. Do
not include a scheme, brackets, path, zone, or port. In particular, use `::1`,
not `[::1]`; Seer adds IPv6 brackets safely. Binding the process to loopback
inside an ordinary bridged container prevents the host's published port from
reaching it, so keep the process wildcard bind there and constrain the **host**
side as the checked-in compose example does.
