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
| `listen_port` | integer/string | Dashboard port; default example `8888`. |
| `hide_logs` | bool | Hide dashboard logs and some node detail. This is not authentication. |
| `node_down_alert_minutes` | integer | Delay before node-down alerting. |
| `node_down_alert_severity` | string | PagerDuty severity for node-down alerts. |
| `prometheus_enabled` | bool | Open the Prometheus listener. |
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
