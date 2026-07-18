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
```

The runtime stage is `scratch`, uses UID/GID `65532`, and contains only CA roots, minimal account metadata, the state directory, and the static binary.

## Frontend policy

The dashboard is embedded HTML, original CSS, and vanilla JavaScript. Check script syntax with:

```sh
node --check seer/static/status.js
node --check seer/static/grid.js
```

Do not introduce a frontend package manager for simple styling. Future Blender assets belong in `seer/static/assets/` after an independently reviewed asset change; the current foundation intentionally has text/emoji fallbacks only.
