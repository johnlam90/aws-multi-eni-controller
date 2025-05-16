FROM golang:1.23-alpine AS builder

# Set build arguments
ARG GOTOOLCHAIN=auto
ARG SKIP_TESTS=false
ARG TARGETARCH=amd64

WORKDIR /workspace

# Copy the Go modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# Cache dependencies
# Remove any toolchain directive for compatibility with older Go versions
# Use the GOTOOLCHAIN build argument
RUN sed -i '/^toolchain/d' go.mod && GOTOOLCHAIN=${GOTOOLCHAIN} go mod download

# Copy the source code
COPY cmd/ cmd/
COPY pkg/ pkg/

# Run tests if not skipped (can be skipped in CI since tests are run separately)
RUN if [ "$SKIP_TESTS" = "false" ]; then \
      echo "Running tests..." && \
      go test -v ./pkg/...; \
    else \
      echo "Skipping tests..."; \
    fi

# Build the ENI Controller with optimizations for size
# Use the TARGETARCH build arg to support multi-architecture builds
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} GOTOOLCHAIN=${GOTOOLCHAIN} \
    go build -a -ldflags="-s -w" -trimpath -o manager cmd/main.go

# Build the ENI Manager with optimizations for size
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} GOTOOLCHAIN=${GOTOOLCHAIN} \
    go build -a -ldflags="-s -w" -trimpath -o eni-manager cmd/eni-manager/main.go

# Use UPX to compress the binaries (only if not in CI to save time)
FROM alpine:3.19 AS compressor
RUN apk --no-cache add upx
COPY --from=builder /workspace/manager /manager
COPY --from=builder /workspace/eni-manager /eni-manager
ARG SKIP_UPX=false
RUN if [ "$SKIP_UPX" = "true" ]; then \
      echo "Skipping UPX compression..."; \
    else \
      echo "Compressing binaries with UPX..." && \
      upx --best --lzma /manager /eni-manager; \
    fi

# Use a minimal base image for the final image
FROM alpine:3.19

# Install only the necessary packages
RUN apk --no-cache add iproute2 pciutils python3 --no-scripts

# Create DPDK directories and add DPDK binding script
RUN mkdir -p /opt/dpdk /etc/pcidp /opt/dpdk/scripts /opt/dpdk/scripts/patches
COPY build/dpdk-devbind.py /opt/dpdk/
COPY build/dpdk-scripts/dpdk-setup.sh /opt/dpdk/scripts/
COPY build/dpdk-scripts/get-vfio-with-wc.sh /opt/dpdk/scripts/
COPY build/dpdk-scripts/patches/ /opt/dpdk/scripts/patches/
RUN chmod +x /opt/dpdk/dpdk-devbind.py /opt/dpdk/scripts/*.sh

# Copy the compressed binaries from the compressor stage
WORKDIR /
COPY --from=compressor /manager .
COPY --from=compressor /eni-manager .
COPY build/entrypoint.sh /entrypoint.sh

# Make the entrypoint script executable
RUN chmod +x /entrypoint.sh

# Set the entrypoint script
ENTRYPOINT ["/entrypoint.sh"]
