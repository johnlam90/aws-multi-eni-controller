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
	controller-gen crd:trivialVersions=true rbac:roleName=eni-controller-role webhook paths="./..." output:crd:artifacts:config=deploy/crds

# Run tests
test: fmt vet
	go test ./... -coverprofile cover.out

# Clean up
clean:
	rm -rf bin/

.PHONY: all build run fmt vet docker-build docker-push install deploy manifests test clean
