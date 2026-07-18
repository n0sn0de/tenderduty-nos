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
Tendermint RPC   public fallback (optional)
HTTP/WebSocket       HTTP discovery
     |
     v
chain/validator state --> alert fan-out --> configured third-party integrations
     |                        |
     +--> dashboard cache     +--> PagerDuty / Discord / Telegram / Slack
     +--> Prometheus gauges   +--> healthcheck ping URL
```

The `seer` package owns config loading, RPC health, WebSocket monitoring, state, alert suppression, and metrics. `seer/dashboard` serves the embedded HTML, CSS, and JavaScript and broadcasts cached status/log messages. `main` owns CLI parsing, version output, state-path compatibility, and config encryption/decryption commands.

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

The state JSON stores recent block results and alert-suppression timestamps. It contains no signing key, but it may reveal operational history. The new default is `.nosnode-seer-state.json`; the legacy `.tenderduty-state.json` schema is consumed unchanged through deterministic fallback.

## Dashboard assets

The dashboard is embedded at build time and uses first-party HTML/CSS/vanilla JavaScript only. UIkit, Lodash, upstream logos, and legacy screenshots were removed after confirming that the small interface needed only a narrow subset of their behavior. Future committee-owned visual assets can be added under `seer/static/` and referenced from `index.html`; this foundation intentionally ships only text/CSS fallbacks.
