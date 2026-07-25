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

| Listener | Default | Authentication | Guidance |
|---|---:|---|---|
| Dashboard + WebSocket | `8888` | none | Bind to loopback/private management networks; use an authenticated TLS proxy for wider access. |
| Prometheus | `28686` | none | Scrape from a trusted monitoring network; metric labels can reveal validator metadata. |

Both are controlled by existing YAML keys. A disabled listener is not opened. The current config accepts ports, not arbitrary bind addresses; the example compose file therefore publishes them on host loopback.

## Outbound trust boundaries

- RPC endpoints supply untrusted network data. Configure multiple endpoints and validate ownership/TLS.
- `public_fallback` delegates endpoint discovery to a public service and is off in the example.
- Notification webhooks and healthcheck URLs receive operator-selected alert information.
- Remote encrypted config downloads cross an additional HTTP trust boundary and require a password; HTTPS is strongly recommended.

## State

The state JSON stores recent block results and alert-suppression timestamps. It contains no signing key, but it may reveal operational history. The new default is `.nosnode-seer-state.json`; the legacy `.tenderduty-state.json` fields are consumed in place, and new checkpoints add only the top-level `version` field described in the [migration contract](migration.md#durable-state-and-rollback-contract).

## Dashboard assets

The dashboard is embedded at build time and uses first-party HTML/CSS/vanilla JavaScript only. UIkit, Lodash, upstream logos, and legacy screenshots were removed after confirming that the small interface needed only a narrow subset of their behavior. Future committee-owned visual assets can be added under `seer/static/` and referenced from `index.html`; this foundation intentionally ships only text/CSS fallbacks.
