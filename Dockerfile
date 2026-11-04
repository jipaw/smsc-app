# syntax=docker/dockerfile:1
# Image smscd: dashboard React ter-embed + biner Go statis, berjalan sebagai nonroot.
#   docker build -t smsc/server:latest --build-arg VERSION=$(git describe --always) .
FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=secret,id=ca_bundle,required=false \
    if [ -s /run/secrets/ca_bundle ]; then export NODE_EXTRA_CA_CERTS=/run/secrets/ca_bundle; fi; npm ci --no-audit --no-fund
COPY contracts/ /src/contracts/
COPY web/ ./
RUN npm run build

FROM golang:1.24-alpine AS server
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
# di balik proxy dengan inspeksi TLS: docker build --secret id=ca_bundle,src=/path/ca.crt ...
RUN --mount=type=secret,id=ca_bundle,required=false \
    if [ -s /run/secrets/ca_bundle ]; then export SSL_CERT_FILE=/run/secrets/ca_bundle; fi; go mod download
COPY server/ ./
COPY --from=web /src/web/dist/ ./internal/webui/dist/
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X github.com/jipaw/smsc-app/server/internal/buildinfo.Version=${VERSION}" \
    -o /out/smscd ./cmd/smscd && mkdir -p /out/state/tailnet

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=server /out/smscd /usr/local/bin/smscd
# state tailnet ter-embed (TAILNET_STATE_DIR=/var/lib/smscd/tailnet) milik nonroot; volume bernama mewarisi pemilik ini
COPY --from=server --chown=65532:65532 --chmod=0700 /out/state /var/lib/smscd
USER nonroot:nonroot
# Hanya loopback: ekspos lewat tailscale serve (sidecar berbagi network namespace) — lihat deploy/compose.
ENV HTTP_ADDR=127.0.0.1:8080 METRICS_ADDR=127.0.0.1:9090
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s CMD ["/usr/local/bin/smscd", "health"]
ENTRYPOINT ["/usr/local/bin/smscd"]
CMD ["serve"]
