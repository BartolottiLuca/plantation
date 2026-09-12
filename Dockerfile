# syntax=docker/dockerfile:1

# Builder runs on the host architecture and cross-compiles for TARGETOS/TARGETARCH
# so linux/amd64 and linux/arm64 images do not need qemu for the Go compile step.
FROM --platform=$BUILDPLATFORM golang:1.23-bookworm AS builder

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
