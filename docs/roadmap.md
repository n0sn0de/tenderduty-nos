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

## Phase 2 — dependency seam (in progress)

Completed in the first bounded slice:

- core endpoint selection, health checking, validator refresh, and WebSocket monitoring now consume first-party RPC interfaces and DTOs rather than Tendermint/Cosmos response types;
- the legacy Tendermint/Cosmos implementation is concentrated in `seer/tendermint_adapter.go`, including status translation, staking/slashing protobuf queries, Bech32 conversion, validator-key handling, and block/vote wire-event conversion;
- deterministic local fixtures cover complete, malformed, missing-field, non-validator, wrong-network, and RPC-error paths while retaining the existing lifecycle and alerting tests.

Still deferred:

- evaluate a compatible CometBFT/Cosmos SDK line through the seam rather than by bulk upgrade;
- eliminate the reviewed govulncheck baseline as fixes become behaviorally verified;
- replace legacy remote-config crypto only with an explicit migration format/version;
- decide whether public endpoint discovery and WebSocket transport construction need separate injectable adapters after a candidate dependency line is tested.

This slice deliberately does **not** change Cosmos SDK, Tendermint, Go, container, workflow, or vulnerability baselines. The exact blocker remains the current Cosmos SDK `v0.45.11` / Tendermint `v0.34.24` compatibility line and its transitive OpenPGP initialization path. A major dependency jump changes RPC and validator behavior and requires a later, separately reviewed compatibility evaluation.

## Phase 3 — runtime hardening

- configurable bind addresses and graceful HTTP shutdown;
- bounded HTTP clients and unified cancellation;
- strict config mode with actionable unknown-key migration;
- dashboard authentication guidance or an opt-in auth boundary;
- integration contract tests with local fake servers only.

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
