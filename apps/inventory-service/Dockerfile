# syntax=docker/dockerfile:1.9

# The builder runs NATIVELY on the runner's architecture and Go cross-compiles to
# $TARGETARCH. That is why this needs no QEMU: emulated aarch64 compilation is
# minutes per build, cross-compilation is seconds. Both dependencies here are
# pure Go, so CGO_ENABLED=0 costs nothing.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/app .

FROM alpine:3.22

# uid 10001 matches securityContext.runAsUser in
# gitops/apps/inventory-service/base/deployment.yaml. The pod sets runAsNonRoot: true,
# so without this USER the container fails to start with CreateContainerConfigError.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 app

USER 10001:10001

COPY --from=builder /out/app /usr/local/bin/app

EXPOSE 8081

# No HEALTHCHECK on purpose: under Kubernetes the kubelet probes defined on the
# Deployment are authoritative, and a Docker HEALTHCHECK would never run.
ENTRYPOINT ["/usr/local/bin/app"]
