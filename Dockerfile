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

# Runtime stage
FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=build /cineroute /cineroute
USER 1001:10
EXPOSE 8787
ENTRYPOINT ["/cineroute"]
