# Image URL to use all building/pushing image targets
IMG ?= eni-controller:latest

# Get the currently used golang install path (in GOPATH/bin, unless GOBIN is set)
ifeq (,$(shell go env GOBIN))
GOBIN=$(shell go env GOPATH)/bin
else
GOBIN=$(shell go env GOBIN)
endif

all: build

# Build manager binary
build: fmt vet
	go build -o bin/manager cmd/main.go

# Run against the configured Kubernetes cluster in ~/.kube/config
run: fmt vet
	go run ./cmd/main.go

# Run go fmt against code
fmt:
	go fmt ./...

# Run go vet against code
vet:
	go vet ./...

# Build the docker image
docker-build:
	docker build . -t ${IMG}

# Push the docker image
docker-push:
	docker push ${IMG}

# Install CRDs into a cluster
install:
	kubectl apply -f deploy/crds/

# Deploy controller in the configured Kubernetes cluster in ~/.kube/config
deploy: install
	cd deploy && kustomize edit set image controller=${IMG}
	kubectl apply -f deploy/

# Generate manifests e.g. CRD, RBAC etc.
manifests:
	controller-gen crd rbac:roleName=eni-controller-role webhook paths="./..." output:crd:artifacts:config=deploy/crds

# Run unit tests
test: fmt vet
	go test ./... -short -coverprofile cover.out

# Run unit tests only
test-unit:
	go test -v ./pkg/... -short

# Run integration tests (requires AWS credentials)
test-integration:
	go test -v ./pkg/aws -tags=integration

# Run end-to-end tests (requires AWS credentials and Kubernetes cluster)
test-e2e:
	go test -v ./test/e2e -tags=e2e

# Run all tests
test-all: test-unit test-integration test-e2e

# Run unit tests with coverage
test-coverage:
	go test -v ./pkg/... -short -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html

# Clean up
clean:
	rm -rf bin/
	rm -f coverage.out coverage.html cover.out

.PHONY: all build run fmt vet docker-build docker-push install deploy manifests test test-unit test-integration test-e2e test-all test-coverage clean
