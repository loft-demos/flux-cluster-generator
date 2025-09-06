# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build
WORKDIR /app

# Safer defaults so local (non-buildx) works too
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# Reliable public proxy; avoids weird corporate MITM or GH rate limits
ENV GOPROXY=https://proxy.golang.org,direct

# 1) Bring in modules (best cache)
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# 2) Bring in *all* sources for now (to rule out missing dirs)
#    Once it builds, you can revert to selective COPYs.
COPY . .

# 3) Prove layout, then build. Plain sh here so output is not swallowed.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    sh -ec '
      set -eux
      echo "TARGETOS=${TARGETOS} TARGETARCH=${TARGETARCH}"
      echo "Top level:"
      ls -la
      echo "cmd tree:"
      [ -d cmd ] && find cmd -maxdepth 2 -type f -name "*.go" -print || true
      echo "go list (all):"
      go list ./... || true

      # If your main IS at cmd/flux-cluster-generator, build that path.
      if [ -f cmd/flux-cluster-generator/main.go ]; then
        BUILD_PATH=./cmd/flux-cluster-generator
      else
        # Fallback: build the module root (works if main.go is elsewhere)
        BUILD_PATH=./
      fi
      echo "Building: ${BUILD_PATH}"

      CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
        go build -v -trimpath -ldflags="-s -w" \
          -o /out/flux-cluster-generator "${BUILD_PATH}"
    '

# 4) Minimal runtime
FROM gcr.io/distroless/static:nonroot AS runtime
WORKDIR /
COPY --from=build /out/flux-cluster-generator /flux-cluster-generator
USER nonroot:nonroot
ENTRYPOINT ["/flux-cluster-generator"]
