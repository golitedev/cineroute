# syntax=docker/dockerfile:1

# Export the public interface as static assets; Node is build-only.
FROM --platform=$BUILDPLATFORM node:24-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# Build natively on the runner and cross-compile the target binary. This keeps
# the ARM64 build out of QEMU emulation while still producing both images.
FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download
COPY . .
COPY --from=web /web/out/ internal/publicui/dist/
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

# The music downloader is an in-process worker calling this bundled CLI.
# Pin the same tested streamrip revision used by the original music engine.
FROM --platform=$TARGETPLATFORM alpine:3.22 AS streamrip
ARG STREAMRIP_REF=e3291615ba6be34aa76df19da8aeb6f41673c6a0
RUN apk add --no-cache python3 py3-pip git build-base python3-dev libffi-dev openssl-dev
RUN python3 -m venv /opt/streamrip && /opt/streamrip/bin/pip install --no-cache-dir \
    "streamrip @ git+https://github.com/nathom/streamrip.git@${STREAMRIP_REF}"

# One runtime container includes the movie/TV and music workers and their tools.
FROM alpine:3.22
RUN apk add --no-cache ffmpeg tzdata ca-certificates python3 flac tini
COPY --from=streamrip /opt/streamrip /opt/streamrip
RUN ln -s /opt/streamrip/bin/rip /usr/local/bin/rip
COPY --from=build /cineroute /cineroute
COPY --from=alass /usr/local/bin/alass /usr/local/bin/alass
ENV HOME=/tmp PYTHONDONTWRITEBYTECODE=1
USER 1000:1000
EXPOSE 8787 8788
ENTRYPOINT ["/sbin/tini", "--", "/cineroute"]
