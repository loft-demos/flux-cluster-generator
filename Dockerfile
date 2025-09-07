# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
WORKDIR /app

# Defaults so local (non-buildx) builds also work
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG GOPRIVATE
ENV GOPRIVATE=${GOPRIVATE}
# Reliable proxy avoids odd 403/429 when hitting VCS directly
ENV GOPROXY=https://proxy.golang.org,direct

# 1) Prime module cache
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

# 2) Copy ALL sources (temporarily) to rule out path issues
COPY . .

# 2.5) Ensure go.sum matches current imports (prevents “missing go.sum entry” in CI)
RUN --mount=type=cache,target=/go/pkg/mod go mod tidy

RUN go version && go env

# 3) Print layout, then build (plain sh; no heredoc swallowing)
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build <<'EOF'
set -eux
echo "TARGETOS=${TARGETOS} TARGETARCH=${TARGETARCH}"
echo "Root files:"; ls -la
echo "cmd tree:"; [ -d cmd ] && find cmd -maxdepth 2 -type f -name "*.go" -print || true
echo "go list:"; go list ./... || true

if [ -f cmd/flux-cluster-generator/main.go ]; then
  BUILD_PATH=./cmd/flux-cluster-generator
else
  BUILD_PATH=./
fi
echo "Building: ${BUILD_PATH}"

CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-amd64} \
  go build -v -trimpath -ldflags="-s -w" \
    -o /out/flux-cluster-generator "${BUILD_PATH}"
EOF

FROM gcr.io/distroless/static:nonroot AS runtime
WORKDIR /
COPY --from=build /out/flux-cluster-generator /flux-cluster-generator
USER nonroot:nonroot
ENTRYPOINT ["/flux-cluster-generator"]
