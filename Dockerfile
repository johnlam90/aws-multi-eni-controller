FROM golang:1.19-alpine AS builder

WORKDIR /workspace

# Copy the Go modules manifests
COPY go.mod go.mod
COPY go.sum go.sum

# Cache dependencies
RUN go mod download

# Copy the source code
COPY cmd/ cmd/
COPY pkg/ pkg/

# Build the ENI Controller
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o manager cmd/main.go

# Build the ENI Manager
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o eni-manager cmd/eni-manager/main.go

# Use a minimal base image for the final image
FROM alpine:3.16

# Install iproute2 for the ip command (used as fallback by ENI Manager)
RUN apk --no-cache add iproute2

# Copy the binaries from the builder stage
WORKDIR /
COPY --from=builder /workspace/manager .
COPY --from=builder /workspace/eni-manager .
COPY build/entrypoint.sh /entrypoint.sh

# Make the entrypoint script executable
RUN chmod +x /entrypoint.sh

# Set the entrypoint script
ENTRYPOINT ["/entrypoint.sh"]
