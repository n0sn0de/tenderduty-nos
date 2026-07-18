# syntax=docker/dockerfile:1

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

RUN go build \
      -buildvcs=false \
      -trimpath \
      -ldflags="-s -w -buildid= -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${BUILD_DATE}" \
      -o /out/nosnode-seer . \
    && mkdir -p /out/state \
    && printf 'nosnode-seer:x:65532:65532:NosNode Seer:/var/lib/nosnode-seer:/sbin/nologin\n' > /out/passwd \
    && printf 'nosnode-seer:x:65532:\n' > /out/group

FROM scratch

LABEL org.opencontainers.image.title="NosNode Seer" \
      org.opencontainers.image.description="NosNode🔮 Cosmos validator monitoring foundation" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.source="https://github.com/n0sn0de/tenderduty-nos"

COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/passwd /etc/passwd
COPY --from=build /out/group /etc/group
COPY --from=build --chown=65532:65532 /out/state /var/lib/nosnode-seer
COPY --from=build --chown=65532:65532 /out/nosnode-seer /usr/local/bin/nosnode-seer

USER 65532:65532
WORKDIR /var/lib/nosnode-seer
EXPOSE 8888 28686
ENTRYPOINT ["/usr/local/bin/nosnode-seer"]
