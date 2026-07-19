# Phased modernization roadmap

This roadmap is sequencing, not a release promise.

## Phase 1 — foundation (this slice)

- supported Go 1.26.5 build/test/race/vet gates;
- pinned static/security tools and an exact reviewed vulnerability baseline;
- deterministic binary and non-publishing container smoke;
- non-root scratch image with no runtime package manager;
- NosNode Seer / NosNode🔮 runtime, CLI, alert, dashboard, docs, and path identity;
- tests for config, state fallback, alerts, embedded assets, and dashboard headers;
- preserve YAML, flag, endpoint, state schema, notification semantics, and metrics compatibility;
- remove unused UIkit/Lodash/logo/screenshots and consolidate operator setup.

## Phase 2 — dependency seam

- isolate Tendermint/Cosmos-specific types behind narrow interfaces;
- add fixture-driven RPC, validator-address, and block-event tests;
- evaluate a compatible CometBFT/Cosmos SDK line rather than blind bulk upgrades;
- eliminate the reviewed govulncheck baseline as fixes become behaviorally verified;
- replace legacy remote-config crypto only with an explicit migration format/version.

The exact blocker is the current Cosmos SDK `v0.45.11` / Tendermint `v0.34.24` compatibility line and its transitive OpenPGP initialization path. A major dependency jump changes RPC and validator behavior and is outside a truthful foundation patch.

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
