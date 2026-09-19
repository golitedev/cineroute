# syntax=docker/dockerfile:1

# Build natively on the runner and cross-compile the target binary. This keeps
# the ARM64 build out of QEMU emulation while still producing both images.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /cineroute ./cmd/cineroute

# Build the vendored alass CLI. alass is GPL-3.0 and is only ever executed as a
# separate subprocess (see third_party/alass/README.cineroute.md). Building on
# the target platform under buildx keeps the musl binary native to each arch.
FROM --platform=$TARGETPLATFORM rust:1-alpine AS alass
RUN apk add --no-cache build-base
WORKDIR /alass
COPY third_party/alass/ ./
RUN --mount=type=cache,target=/alass/target \
    cargo build --release --locked --bin alass-cli && \
    cp /alass/target/release/alass-cli /usr/local/bin/alass

# Runtime stage. Alpine (instead of scratch) is required because the subtitle
# workflow needs ffmpeg/ffprobe and the alass binary at runtime.
FROM alpine:3.22
RUN apk add --no-cache ffmpeg tzdata ca-certificates
COPY --from=build /cineroute /cineroute
COPY --from=alass /usr/local/bin/alass /usr/local/bin/alass
USER 1001:10
EXPOSE 8787
ENTRYPOINT ["/cineroute"]
