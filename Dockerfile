FROM golang:1.23-alpine AS builder

# Set build arguments
ARG GOTOOLCHAIN=auto

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

# Build the ENI Controller with optimizations for size
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=${GOTOOLCHAIN} \
    go build -a -ldflags="-s -w" -trimpath -o manager cmd/main.go

# Build the ENI Manager with optimizations for size
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=${GOTOOLCHAIN} \
    go build -a -ldflags="-s -w" -trimpath -o eni-manager cmd/eni-manager/main.go

# Use UPX to compress the binaries
FROM alpine:3.19 AS compressor
RUN apk --no-cache add upx
COPY --from=builder /workspace/manager /manager
COPY --from=builder /workspace/eni-manager /eni-manager
RUN upx --best --lzma /manager /eni-manager

# Use a minimal base image for the final image
FROM alpine:3.19

# Install only the necessary packages
RUN apk --no-cache add iproute2 --no-scripts

# Copy the compressed binaries from the compressor stage
WORKDIR /
COPY --from=compressor /manager .
COPY --from=compressor /eni-manager .
COPY build/entrypoint.sh /entrypoint.sh

# Make the entrypoint script executable
RUN chmod +x /entrypoint.sh

# Set the entrypoint script
ENTRYPOINT ["/entrypoint.sh"]
