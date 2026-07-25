# Development and verification

## Pinned toolchain

The module requires Go `1.26.0` semantics and recommends `go1.26.5`. Local and CI receipts use the official multi-platform image index:

```text
docker.io/library/golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651
```

The resolved Linux/amd64 image manifest used for local receipts was:

```text
sha256:3f6236bd765f898a2a3c2946112b04097814c4529d44534674700cd07b9c6b4c
```

No host Go installation is required. Equivalent Docker commands may replace Podman commands.

## Consensus dependency line

This repository selects Cosmos SDK `v0.53.7` with CometBFT `v0.38.23`. This is
the established `2025.1` release family, which remains active alongside
`2026.1`; it is intentionally narrower than moving immediately to the newer,
breaking SDK `0.54.x` / CometBFT `0.39.x` family.

Authoritative selection evidence:

- the [Cosmos Stack release-family policy](https://github.com/cosmos/docs/blob/main/sdk/latest/release-family.mdx)
  pairs SDK `0.53.x` with CometBFT `0.38.x`, supports up to two families, and
  marks SDK `0.50.x` / CometBFT `0.37.x` and lower end-of-life;
- [Cosmos SDK v0.53.7](https://github.com/cosmos/cosmos-sdk/releases/tag/v0.53.7)
  is the latest stable patch on the selected SDK line;
- [CometBFT v0.38.23](https://github.com/cometbft/cometbft/releases/tag/v0.38.23)
  is the latest stable patch on the selected CometBFT line, while
  [CSA-2026-001](https://github.com/cometbft/cometbft/security/advisories/GHSA-c32p-wcqj-j677)
  requires at least `v0.38.21`;
- CometBFT documents patch releases as history-compatible within a minor line;
- the SDK module requires CometBFT `v0.38.21`, and the release-family and patch
  guarantees support selecting the later `v0.38.23` patch.

SDK `v0.53.7` resolves `github.com/bytedance/sonic` `v1.14.2`, which does not
compile with the pinned Go `1.26.5` runtime API. The module therefore selects
[Sonic v1.15.1](https://github.com/bytedance/sonic/releases/tag/v1.15.1), the
latest stable patch after v1.15.0 added Go 1.26 support. This is a build
compatibility override, not a runtime configuration change.

## Required Go gates

```sh
go version
go mod verify
go mod tidy -diff
test -z "$(gofmt -l $(git ls-files '*.go'))"
go test -count=1 ./...
go test -race -count=1 ./...
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@v0.7.0 -- ./...
go run github.com/securego/gosec/v2/cmd/gosec@v2.28.0 -quiet ./...
report="$(mktemp)"
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 -format json ./... > "$report"
go run ./scripts/check-govulncheck.go -allow security/govulncheck-allowlist.txt "$report"
rm -f "$report"
```

`govulncheck` is intentionally baseline-aware. The helper compares only symbol-reachable findings and fails on either a new or removed finding. The reviewed legacy set is documented in `security/govulncheck-allowlist.txt` and the roadmap.

## Dependency-seam fixture and lifecycle gates

The RPC seam has no live-network tests. Repeat its local status/ABCI fixtures,
validator address lookup, block/vote conversion, wrong-network/error paths, and
the cancellation/WebSocket/runtime synchronization probes with:

```sh
go test -count=20 ./seer -run 'Test(FirstPartyDependencySeamInjection|NewRPCPreservesOrderedFallbackAndSharedDeadline|NewRPCPropagatesParentCancellation|CometBFTRPCStatusFixtures|NewRPCPreservesWrongNetworkAndErrorPaths|ValidatorLookupAndAddressNormalizationFixtures|ValidatorKeyAlgorithmFixtures|ValidatorLookupMalformedAndMissingValues|BlockEventFixtureConversion|VoteEventFixtureConversion|NormalizeConsensusAddressCopiesBytesToUpperHex|ToBytesRetainsLegacyExportedCompatibility)$'
go test -race -count=10 ./seer -run 'Test(WsRunCancellationSerializesOnePublishedWebSocketClose|ConcurrentRPCValidatorRefreshAndWebSocketWorkloadIsRaceFree|ShutdownDrainsAcceptedDeliveryBeforeSingleCheckpoint|CloseWebSocketsUnblocksRead|ShutdownDrainTimeoutSkipsCheckpointAndFails)$'
go test -race -count=10 ./seer -run 'Test(ConcurrentPersistedMutationsAndSnapshotAreRaceFree|SnapshotSavedStateIsOneCoherentInstant)$'
```

The first-party seam is internal: `seer/rpc_contract.go` defines the interfaces
and DTOs consumed by core monitoring. The exact direct source imports are
intentionally confined to `seer/cometbft_adapter.go`:

- CometBFT `rpc/client/http` for the HTTP RPC implementation;
- Cosmos SDK Ed25519 and secp256k1 key types;
- Cosmos SDK Bech32 plus staking and slashing protobuf types.

`go.mod` and `go.sum` select Cosmos SDK `v0.53.7`, CometBFT `v0.38.23`, and the
Go-1.26-compatible Sonic `v1.15.1` transitive override. The Go, container, and
workflow pins are unchanged. Public endpoint discovery and the Gorilla
WebSocket transport are existing non-Cosmos boundaries and remain direct until
a later slice proves a second adapter is useful.

Run the same core checks without host Go:

```sh
podman run --rm \
  --userns=keep-id \
  -e GOTOOLCHAIN=local \
  -e GOFLAGS=-mod=readonly \
  -v "$PWD":/src:Z \
  -w /src \
  docker.io/library/golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 \
  sh -euc '
    go version
    go mod verify
    go mod tidy -diff
    go test -count=1 ./...
    go test -race -count=1 ./...
    go vet ./...
  '
```

## Reproducible binary check

CI builds twice with `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, a blank build ID, fixed version metadata, and `SOURCE_DATE_EPOCH=0`, then compares the bytes. The version surface is:

```sh
./nosnode-seer -version
```

## Container check

```sh
podman build \
  --build-arg VERSION=local \
  --build-arg COMMIT=local \
  --build-arg BUILD_DATE=reproducible-local \
  -t nosnode-seer:local .
podman run --rm --network none --read-only --cap-drop all \
  --security-opt no-new-privileges nosnode-seer:local -version
CONTAINER_RUNTIME=podman bash scripts/container-state-smoke.sh nosnode-seer:local
```

The runtime stage is `scratch`, retains the historical UID/GID `26657:26657`, and contains only CA roots,
minimal account metadata, the canonical and deprecated migration state
directories, the canonical static binary, and its deprecated executable alias.
The state smoke uses no network and exercises checkpoint, restart, rollback,
explicit path, and legacy container migration contracts.

## Frontend policy

The dashboard is embedded HTML, original CSS, and vanilla JavaScript. Check script syntax with:

```sh
node --check seer/static/status.js
node --check seer/static/grid.js
```

Do not introduce a frontend package manager for simple styling. Future Blender assets belong in `seer/static/assets/` after an independently reviewed asset change; the current foundation intentionally has text/emoji fallbacks only.
