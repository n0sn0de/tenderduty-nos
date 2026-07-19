# NosNode Seer · NosNode🔮

[![CI](https://github.com/n0sn0de/tenderduty-nos/actions/workflows/ci.yml/badge.svg)](https://github.com/n0sn0de/tenderduty-nos/actions/workflows/ci.yml)
[![Container](https://github.com/n0sn0de/tenderduty-nos/actions/workflows/container.yml/badge.svg)](https://github.com/n0sn0de/tenderduty-nos/actions/workflows/container.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

**NosNode Seer** is a self-hosted Cosmos validator monitor with a static live dashboard, notification integrations, Prometheus metrics, and YAML configuration. The visible operator identity is **NosNode🔮**: read the chain, guard the validator.

This repository is a modernized fork of the archived [`blockpane/tenderduty`](https://github.com/blockpane/tenderduty). The original MIT copyright and license remain intact; see [provenance](docs/provenance.md).

> **Foundation status:** this first bounded modernization establishes a supported, pinned Go toolchain, deterministic build gates, non-root scratch container, runtime rebrand, compatibility tests, and operator documentation. Core Tendermint/Cosmos monitoring semantics and notification integrations are intentionally retained. The legacy Cosmos SDK line remains a reviewed modernization blocker; see [security](docs/security.md) and [roadmap](docs/roadmap.md).

## What it watches

- validator signing, proposals, missed prevotes/precommits, jail and tombstone state;
- stalled chains and unhealthy or wrong-network RPC endpoints;
- configurable Discord, Slack, Telegram, PagerDuty, and healthcheck notifications;
- a same-origin WebSocket dashboard and Prometheus exporter.

## Quickstart

Requirements: Podman 4+ or Docker with Compose support. No published NosNode Seer image is assumed by this foundation.

```sh
git clone https://github.com/n0sn0de/tenderduty-nos.git
cd tenderduty-nos
cp example-config.yml config.yml
mkdir -p chains.d
# Edit config.yml: set validator and RPC values; keep integrations disabled until secrets are supplied.
podman build --tag nosnode-seer:local .
podman run --rm --network none --read-only nosnode-seer:local -version
podman compose -f example-docker-compose.yml up --build
```

The example compose file binds the public application defaults to loopback only:

- dashboard: `http://127.0.0.1:8888`
- Prometheus: `http://127.0.0.1:28686/metrics`

Do not expose either listener to an untrusted network without an authenticated TLS reverse proxy. The dashboard has no built-in authentication.

Generate the canonical example without starting monitoring:

```sh
podman run --rm --network none nosnode-seer:local -example-config
```

## Compatibility promises in this slice

Existing Tenderduty YAML keys, flags, environment variables, dashboard endpoints, and `tenderduty_*` Prometheus metric names are preserved. Legacy state JSON remains directly readable; new atomic checkpoints preserve the legacy fields, add only a rollback-safe top-level version field, and maintain a `.bak` copy. An old Tenderduty writer may remove the unknown version field on rollback; Seer reads that result as legacy version 0. The default state path is now `.nosnode-seer-state.json`; when no `-state` flag is given and only `.tenderduty-state.json` exists, Seer deterministically uses the legacy file and emits a migration notice. The container retains historical UID/GID `26657:26657` and a deprecated `/bin/tenderduty` plus `/var/lib/tenderduty` bridge for one migration cycle.

See the complete [migration and compatibility table](docs/migration.md) before replacing an existing process.

## Documentation

- [Architecture and trust boundaries](docs/architecture.md)
- [Configuration reference](docs/config.md)
- [Migration from Tenderduty](docs/migration.md)
- [Notifications](docs/notifications.md)
- [Prometheus](docs/prometheus.md)
- [Security and secret handling](docs/security.md)
- [Development and verification](docs/development.md)
- [Upstream provenance](docs/provenance.md)
- [Phased modernization roadmap](docs/roadmap.md)

## CLI

```text
nosnode-seer -f config.yml -cc chains.d -state .nosnode-seer-state.json
nosnode-seer -version
nosnode-seer -h
```

Legacy flags `-f`, `-cc`, `-state`, `-example-config`, `-encrypt`, `-decrypt`, `-encrypted-config`, and `-password` remain accepted. `CONFIG` and `PASSWORD` remain supported for compatibility; file-based secret injection is preferred where your runtime supports it.

## License

MIT. Copyright (c) 2021 Block Pane, LLC, as preserved in [`LICENSE`](LICENSE). NosNode Seer changes are distributed under the same license. Rebranding does not erase upstream attribution.
