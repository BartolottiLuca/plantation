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
FROM --platform=$BUILDPLATFORM golang:1.25-bookworm AS builder

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

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /out/plantation /plantation

USER nonroot:nonroot

ENTRYPOINT ["/plantation"]
CMD ["serve"]
