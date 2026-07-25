# Architecture and trust boundaries

NosNode Seer is one Go process with embedded static dashboard assets. It does not hold validator signing keys and must never be given a mnemonic, private key, or consensus key.

## Runtime flow

```text
operator YAML + optional chains.d
             |
             v
      configuration validation
             |
     +-------+--------+
     |                |
     v                v
CometBFT/Tendermint   public fallback (optional)
compatible RPC        HTTP discovery
HTTP/WebSocket
     |
     v
chain/validator state --> alert fan-out --> configured third-party integrations
     |                        |
     +--> dashboard cache     +--> PagerDuty / Discord / Telegram / Slack
     +--> Prometheus gauges   +--> healthcheck ping URL
```

The `seer` package owns config loading, RPC health, WebSocket monitoring, state, alert suppression, and metrics. `seer/dashboard` serves the embedded HTML, CSS, and JavaScript and broadcasts cached status/log messages. `main` owns CLI parsing, version output, state-path compatibility, and config encryption/decryption commands.

## Consensus dependency seam

Core monitoring uses the internal `rpcClient`, `rpcClientFactory`, and
`validatorAddressCodec` interfaces plus first-party status, validator, slashing,
block-event, and vote-event DTOs. `ChainConfig` reads the client and validator
snapshots under the existing monitoring lock where a coherent read is required.
Client replacement and validator publication remain separate locked operations,
matching the existing endpoint-selection and refresh sequence rather than adding
an atomic cross-operation guarantee.

`seer/cometbft_adapter.go` is the only Go source file that directly imports the
Cosmos SDK or CometBFT. It owns the current `rpchttp.HTTP` client, Cosmos
staking/slashing protobuf requests and responses, Ed25519/secp256k1 consensus-key
handling, Bech32 conversion, and CometBFT/Tendermint-compatible block/vote wire decoding. Core RPC
selection sees only network/catch-up status; validator refresh sees only the
fields used by alarms, metrics, and the dashboard; block/vote handlers see only
normalized event DTOs.

The seam is internal and does not add a supported public Go API. The current
adapter targets Cosmos SDK `v0.53.7` and CometBFT `v0.38.23`, an active `2025.1`
Cosmos Stack release-family pair. Static loopback RPC fixtures exercise both
Ed25519 and compressed secp256k1 consensus keys through the real protobuf and
adapter path. Public endpoint discovery and the Gorilla WebSocket transport
remain separate existing boundaries; they were not redesigned in this slice.

## Inbound listeners

| Listener | Host when omitted | Port | Authentication | Guidance |
|---|---|---:|---|---|
| Dashboard + WebSocket | wildcard (compatibility default) | `8888` | none | On bare metal, set `listen_host` to loopback/private management IP; use an authenticated TLS proxy for wider access. |
| Prometheus | wildcard (compatibility default) | `28686` | none | On bare metal, set `prometheus_listen_host` to loopback/trusted monitoring IP; labels can reveal validator metadata. |

The original enable and port keys remain unchanged. Optional `listen_host` and
`prometheus_listen_host` keys add explicit hostname/IPv4/IPv6 binding without
changing the omitted wildcard behavior. Addresses are validated together and
constructed with `net.JoinHostPort` before either service starts. Disabled
listeners open nothing. The example compose file deliberately keeps process
hosts omitted and publishes the wildcard container listeners on **host**
loopback; binding container loopback would make ordinary bridged publication
unreachable.

### Listener ownership and shutdown

```text
load + validate both enabled addresses
             |
             v
 synchronously pre-bind owned listeners
 (rollback all if either bind fails)
             |
             v
 dashboard service             Prometheus service
 explicit http.Server          explicit http.Server
 explicit ServeMux             explicit ServeMux
 cache broadcaster worker      metrics update worker
 tracked /ws connections       /metrics handler
             \                 /
              runtime lifecycle
```

The process starts monitoring only after every enabled listener has been
validated and pre-bound. Unexpected serve errors cancel the shared runtime and
are returned to `main`; no listener goroutine calls `log.Fatal`. SIGINT, SIGTERM,
SIGHUP, or parent-context cancellation stops new alert ingress, cancels monitors,
closes outbound RPC WebSockets, closes inbound dashboard WebSockets, performs
bounded HTTP `Shutdown`, closes listeners, and joins service workers. Accepted
notifications still drain before the single durable checkpoint. An incomplete
drain still fails without claiming a checkpoint, preserving the existing state
contract.

Dashboard `Shutdown` tracks upgraded/hijacked `/ws` connections explicitly,
because `net/http` does not own them after upgrade. Cancellation closes those
connections and unblocks otherwise idle handlers before the service join. After
successful shutdown neither listener accepts and both ports can be rebound.

## Outbound trust boundaries

- RPC endpoints supply untrusted network data. Configure multiple endpoints and validate ownership/TLS.
- `public_fallback` delegates endpoint discovery to a public service and is off in the example.
- Notification webhooks and healthcheck URLs receive operator-selected alert information.
- Remote encrypted config downloads cross an additional HTTP trust boundary and require a password; HTTPS is strongly recommended.

## State

The state JSON stores recent block results and alert-suppression timestamps. It contains no signing key, but it may reveal operational history. The new default is `.nosnode-seer-state.json`; the legacy `.tenderduty-state.json` fields are consumed in place, and new checkpoints add only the top-level `version` field described in the [migration contract](migration.md#durable-state-and-rollback-contract).

## Dashboard assets

The dashboard is embedded at build time and uses first-party HTML/CSS/vanilla JavaScript only. UIkit, Lodash, upstream logos, and legacy screenshots were removed after confirming that the small interface needed only a narrow subset of their behavior. Future committee-owned visual assets can be added under `seer/static/` and referenced from `index.html`; this foundation intentionally ships only text/CSS fallbacks. The existing `/`, `/state`, `/logs`, `/logsenabled`, and `/ws` routes remain unchanged. Their read-only application shape is not treated as a process-level read-only guarantee; no-signing design and container/filesystem permissions remain separate trust boundaries.
