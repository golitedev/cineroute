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
# separate subprocess (see third_party/alass/README.cineroute.md).
#
# This stage runs for the target platform, so the linux/arm64 build compiles Rust
# under QEMU. Two things keep that from dominating the build:
#   * the cargo registry and target directories are cache mounts, so repeat builds
#     reuse downloaded crates and already-compiled dependencies;
#   * the vendored workspace enables LTO with a single codegen unit for release
#     builds, which is the most expensive part under emulation. Both are turned
#     off here: for a subtitle aligner the runtime difference is negligible.
FROM --platform=$TARGETPLATFORM rust:1-alpine AS alass
ENV CARGO_PROFILE_RELEASE_LTO=false \
    CARGO_PROFILE_RELEASE_CODEGEN_UNITS=16
RUN apk add --no-cache build-base
WORKDIR /alass
COPY third_party/alass/ ./
RUN --mount=type=cache,target=/alass/target \
    --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/usr/local/cargo/git \
    cargo build --release --locked --bin alass-cli && \
    cp /alass/target/release/alass-cli /usr/local/bin/alass

# Runtime stage. ffmpeg provides ffprobe for probing and for subtitle extraction;
# alass performs the synchronization. Both are needed for the Subtitles page.
FROM alpine:3.22
RUN apk add --no-cache ffmpeg tzdata ca-certificates
COPY --from=build /cineroute /cineroute
COPY --from=alass /usr/local/bin/alass /usr/local/bin/alass
USER 1001:10
EXPOSE 8787
ENTRYPOINT ["/cineroute"]
