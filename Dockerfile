# syntax=docker/dockerfile:1.18.0@sha256:dabfc0969b935b2080555ace70ee69a5261af8a8f1b4df97b9e7fbcf6722eddf

# Go 1.26.5 (bookworm), pinned to the official multi-platform image index.
FROM docker.io/library/golang:1.26.5-bookworm@sha256:1ecb7edf62a0408027bd5729dfd6b1b8766e578e8df93995b225dfd0944eb651 AS build

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

ENV CGO_ENABLED=0 \
    GOFLAGS=-mod=readonly \
    GOTOOLCHAIN=local

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY main.go example-config.yml ./
COPY seer ./seer

RUN mkdir -p \
      /out/rootfs/bin \
      /out/rootfs/usr/local/bin \
      /out/rootfs/var/lib/nosnode-seer \
      /out/rootfs/var/lib/tenderduty \
    && go build \
      -buildvcs=false \
      -trimpath \
      -ldflags="-s -w -buildid= -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
      -o /out/rootfs/usr/local/bin/nosnode-seer . \
    && ln -s /usr/local/bin/nosnode-seer /out/rootfs/bin/tenderduty \
    && printf 'nosnode-seer:x:26657:26657:NosNode Seer:/var/lib/nosnode-seer:/sbin/nologin\n' > /out/passwd \
    && printf 'nosnode-seer:x:26657:\n' > /out/group

FROM scratch

LABEL org.opencontainers.image.title="NosNode Seer" \
      org.opencontainers.image.description="NosNode🔮 Cosmos validator monitoring foundation" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.source="https://github.com/n0sn0de/tenderduty-nos"

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/passwd /etc/passwd
COPY --from=build /out/group /etc/group
# One-cycle migration bridge: canonical binary/state path plus a deprecated
# legacy command and volume target. The legacy command still runs Seer.
COPY --from=build --chown=26657:26657 /out/rootfs/ /

# Retain the historical Tenderduty UID/GID for one compatibility cycle so
# existing 0755 volumes and 0644/0600 files remain usable without root migration.
USER 26657:26657
WORKDIR /var/lib/nosnode-seer
EXPOSE 8888 28686
ENTRYPOINT ["/usr/local/bin/nosnode-seer"]
