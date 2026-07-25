# Phased modernization roadmap

This roadmap is sequencing, not a release promise.

## Phase 1 — foundation (complete)

- supported Go 1.26.5 build/test/race/vet gates;
- pinned static/security tools and an exact reviewed vulnerability baseline;
- deterministic binary and non-publishing container smoke;
- non-root scratch image with no runtime package manager;
- NosNode Seer / NosNode🔮 runtime, CLI, alert, dashboard, docs, and path identity;
- tests for config, state fallback, alerts, embedded assets, and dashboard headers;
- preserve YAML, flag, endpoint, state schema, notification semantics, and metrics compatibility;
- remove unused UIkit/Lodash/logo/screenshots and consolidate operator setup.

## Phase 2 — dependency seam and active compatibility line (in progress)

Completed in the first bounded slice:

- core endpoint selection, health checking, validator refresh, and WebSocket monitoring now consume first-party RPC interfaces and DTOs rather than Tendermint/Cosmos response types;
- the dependency implementation was concentrated in one adapter (now `seer/cometbft_adapter.go`), including status translation, staking/slashing protobuf queries, Bech32 conversion, validator-key handling, and block/vote wire-event conversion;
- deterministic local fixtures cover complete, malformed, missing-field, non-validator, wrong-network, and RPC-error paths while retaining the existing lifecycle and alerting tests.

Completed in the second bounded slice:

- migrated only the adapter/import/module/test/doc surfaces to Cosmos SDK
  `v0.53.7` and CometBFT `v0.38.23`, the active `2025.1` release-family line;
- retained the first-party DTO seam, ordered endpoint selection, shared
  deadlines, validator address behavior, block/vote classification, and
  WebSocket cancellation/close contracts;
- added real protobuf-over-ABCI fixtures for deterministic Ed25519 and
  compressed secp256k1 validator consensus keys;
- selected Sonic `v1.15.1` because the SDK's transitive `v1.14.2` does not
  compile under the existing pinned Go `1.26.5` toolchain;
- removed the symbol-reachable transaction-decoding finding `GO-2024-3339`
  from the exact govulncheck baseline.

Still deferred:

- eliminate the remaining reviewed govulncheck baseline only with an explicit
  remote-config/OpenPGP migration;
- replace legacy remote-config crypto only with an explicit migration format/version;
- decide whether public endpoint discovery and WebSocket transport construction need separate injectable adapters after a candidate dependency line is tested.

This slice deliberately does **not** change Go, container, workflow, release,
remote-config format, runtime-hardening, or visual scope. `GO-2026-5932`
remains symbol-reachable through the legacy OpenPGP path and SDK package
initialization. That residual has no fixed version and is not hidden by the
consensus migration; removing it requires a separately versioned remote-config
format rather than an opportunistic crypto swap.

## Phase 3 — runtime hardening

Completed in the first bounded slice:

- optional dashboard `listen_host` and Prometheus `prometheus_listen_host` with
  omitted wildcard compatibility and safe hostname/IPv4/IPv6 construction;
- preflight validation and synchronous pre-bind rollback before monitoring
  starts, with disabled listeners opening nothing;
- explicit dashboard/Prometheus mux, `http.Server`, listener, worker, and error
  ownership;
- shared signal/context cancellation, bounded HTTP shutdown, explicit inbound
  dashboard WebSocket close, service joins, port-rebind/no-post-shutdown tests,
  and unchanged notification-drain-before-checkpoint ordering.

Still deferred:

- bounded HTTP clients and unified cancellation;
- strict config mode with actionable unknown-key migration;
- dashboard authentication guidance or an opt-in auth boundary;
- wider integration contracts beyond the local real-listener/fake-service tests.

## Phase 4 — release engineering

- define release/version policy and migration support window;
- reproducible multi-architecture images with SBOM, provenance, signatures, and digest-pinned bases;
- restore publication workflows only after non-publishing builds and permissions are reviewed;
- release notes generated from tested compatibility changes;
- reconcile the documented historical comparison SHA with the repository's imported Git ancestry before claiming cryptographic lineage provenance.

The root MIT license and source attribution remain unchanged. The current provenance
page records the foundation's reviewed content comparison, but the imported Git
history does not itself prove ancestry to that upstream SHA; that legacy P3 audit
is intentionally separate from this runtime/state repair.

## Phase 5 — committee visual integration

A separate committee workstream owns Blender/visual assets. When reviewed, add assets under `seer/static/`, include text alternatives and no-JavaScript fallbacks, update the embedded-asset test, and verify licensing/size. The foundation does not depend on an unmerged asset branch.
