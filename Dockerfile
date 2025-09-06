
FROM --platform=$BUILDPLATFORM golang:1.22 AS build
ARG TARGETOS
ARG TARGETARCH

WORKDIR /app

# 1) Prime the module cache (cheap layer, great cache reuse)
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

# 2) Copy only sources needed for this binary (keeps cache granular)
#    Add pkg/ or internal/ if you have them.
COPY cmd/flux-cluster-generator/ ./cmd/flux-cluster-generator/
# COPY pkg/ ./pkg/
# COPY internal/ ./internal/

# 3) Build with BuildKit caches for speed
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" \
      -o /out/flux-cluster-generator ./cmd/flux-cluster-generator

# 4) Minimal runtime
FROM gcr.io/distroless/static:nonroot AS runtime
WORKDIR /
COPY --from=build /out/flux-cluster-generator /flux-cluster-generator
USER nonroot:nonroot
ENTRYPOINT ["/flux-cluster-generator"]
