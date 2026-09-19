# syntax=docker/dockerfile:1

# Builder runs on the host architecture and cross-compiles for TARGETOS/TARGETARCH
# so linux/amd64 and linux/arm64 images do not need qemu for the Go compile step.
#
# go.mod's `go 1.23` directive is a minimum, not a pin: the builder toolchain can
# be newer. It must be, here — gopkg.in/yaml.v3's own go.mod is unpruned, so any
# real import of it (internal/catalog) drags its test-only dependency chain
# (rogpeppe/go-internal, which requires go >= 1.25) into `go mod download`'s build
# list even though nothing we build ever runs yaml.v3's tests. A builder pinned to
# 1.23 fails on that with no recourse short of vendoring; 1.25+ resolves it for free.
FROM --platform=$BUILDPLATFORM golang:1.27-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
	go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
	--mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -trimpath \
	-ldflags="-s -w -X github.com/BartolottiLuca/plantation/internal/web.Version=${VERSION} -X github.com/BartolottiLuca/plantation/internal/web.Commit=${COMMIT}" \
	-o /out/plantation ./cmd/plantation

FROM scratch

# scratch ships no trust store. Without this the binary still starts, /healthz and
# /readyz still answer 200, and the dashboard still renders — while every outbound
# HTTPS call (Open-Meteo, Tado, Discord) fails with x509: certificate signed by
# unknown authority, watering quietly degrades to base_interval, and nothing says
# so for 24 h. CI asserts this file is present; do not drop it.
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/plantation /plantation

# Numeric on purpose: scratch has no /etc/passwd, and a named user would leave
# kubelet unable to verify runAsNonRoot. Matches securityContext.runAsUser.
USER 65532:65532

ENTRYPOINT ["/plantation"]
CMD ["serve"]
