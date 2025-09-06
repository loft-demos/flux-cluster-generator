FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS build
# If any dep requires cgo later, uncomment next line:
# RUN apk add --no-cache build-base

WORKDIR /app

# Buildx provides these; set sane defaults so local builds also work
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG GOPRIVATE

ENV GOPRIVATE=${GOPRIVATE}

# 1) Prime module cache
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# 2) Bring in the rest of the source your binary needs
#    If you KNOW it only uses cmd/, keep it; otherwise copy pkg/ + internal/ (or just copy all).
COPY cmd/flux-cluster-generator/ ./cmd/flux-cluster-generator/
# If your code imports from pkg/ or internal/, uncomment:
# COPY pkg/ ./pkg/
# COPY internal/ ./internal/
# Or (simplest & safest): COPY . .   (at the cost of a coarser cache key)
# COPY . .

# 3) Build with BuildKit caches and verbose + strict shell to surface real errors
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build <<'EOF'
set -euxo pipefail
echo "Go env:"
go env
echo "Listing module packages (helps spot missing dirs):"
go list ./... || true
# If you have private deps, you can wire a token via build secret and insteadof:
# if [ -f /run/secrets/GIT_AUTH_TOKEN ]; then
#   git config --global url."https://$(cat /run/secrets/GIT_AUTH_TOKEN)@github.com/".insteadof "https://github.com/"
# fi
CGO_ENABLED=${CGO_ENABLED:-0} GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
  go build -v -trimpath -ldflags="-s -w" \
    -o /out/flux-cluster-generator ./cmd/flux-cluster-generator
EOF

# 4) Minimal runtime
FROM gcr.io/distroless/static:nonroot AS runtime
WORKDIR /
COPY --from=build /out/flux-cluster-generator /flux-cluster-generator
USER nonroot:nonroot
ENTRYPOINT ["/flux-cluster-generator"]
