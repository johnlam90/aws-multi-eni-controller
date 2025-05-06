#!/bin/bash

# Apply the CRD
kubectl apply -f deploy/crds/networking.example.com_nodeenis_crd.yaml

# Create a sample NodeENI resource
kubectl apply -f deploy/samples/networking_v1alpha1_nodeeni.yaml

# Label a node for testing
NODE_NAME=$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')
kubectl label node $NODE_NAME ng=multi-eni

# Run the controller locally
go run cmd/main.go

# Clean up
kubectl label node $NODE_NAME ng-
kubectl delete -f deploy/samples/networking_v1alpha1_nodeeni.yaml
kubectl delete -f deploy/crds/networking.example.com_nodeenis_crd.yaml
