ARG BUILD_MODE=from-source

# Full compile from source (local dev, default)
FROM golang:1.26 AS build-from-source
ARG TARGETOS TARGETARCH
WORKDIR /workspace
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /manager cmd/main.go

# Pre-built binary (CI — skips Go toolchain entirely)
FROM scratch AS build-prebuilt
ARG TARGETARCH
COPY bin/manager-linux-${TARGETARCH} /manager

# Select build mode
FROM build-${BUILD_MODE} AS binary

FROM gcr.io/distroless/static:nonroot
COPY --from=binary /manager /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
