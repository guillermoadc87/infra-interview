# syntax=docker/dockerfile:1.9

# BUILD CONTEXT IS THE REPOSITORY ROOT, not apps/api-service/.
#
# It has to be: apps/api-service/go.mod carries
# `replace infra-interview/pkg/obs => ../../pkg/obs`, and a context rooted at
# the service directory cannot see that path -- the build fails at `go mod
# download` with a missing replacement directory. ci.yml therefore passes
# `context: .` plus `file: apps/api-service/Dockerfile`, and `docker build`
# by hand must do the same:
#
#   docker build -f apps/api-service/Dockerfile .
#
# The builder runs NATIVELY on the runner's architecture and Go cross-compiles to
# $TARGETARCH. That is why this needs no QEMU: emulated aarch64 compilation is
# minutes per build, cross-compilation is seconds. Every dependency here is pure
# Go, so CGO_ENABLED=0 costs nothing.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src

# Module graph before sources, so a code change does not re-download the world.
# BOTH go.mod files are needed up front: resolving the replace directive means
# loading the shared module's own requirements, so `go mod download` fails if
# pkg/obs/go.mod is not already in place.
COPY pkg/obs/go.mod pkg/obs/go.sum ./pkg/obs/
COPY apps/api-service/go.mod apps/api-service/go.sum ./apps/api-service/

WORKDIR /src/apps/api-service
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY pkg/obs/ /src/pkg/obs/
COPY apps/api-service/ /src/apps/api-service/

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/app .

FROM alpine:3.22

# uid 10001 matches securityContext.runAsUser in
# gitops/apps/api-service/base/rollout.yaml. The pod sets runAsNonRoot: true,
# so without this USER the container fails to start with CreateContainerConfigError.
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -H -u 10001 app

USER 10001:10001

COPY --from=builder /out/app /usr/local/bin/app

EXPOSE 8080

# No HEALTHCHECK on purpose: under Kubernetes the kubelet probes defined on the
# Rollout are authoritative, and a Docker HEALTHCHECK would never run.
ENTRYPOINT ["/usr/local/bin/app"]
