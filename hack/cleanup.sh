#!/bin/bash

# Delete the sample NodeENI resource
kubectl delete -f deploy/samples/networking_v1alpha1_nodeeni.yaml

# Delete the deployment
kubectl delete -f deploy/deployment.yaml

# Delete the CRD
kubectl delete -f deploy/crds/networking.example.com_nodeenis_crd.yaml

# Remove the label from nodes
for NODE in $(kubectl get nodes -o jsonpath='{.items[?(@.metadata.labels.ng=="multi-eni")].metadata.name}'); do
  kubectl label node $NODE ng-
done

echo "ENI Controller cleanup completed!"
