#!/bin/bash

# Get the first node name
NODE_NAME=${1:-$(kubectl get nodes -o jsonpath='{.items[0].metadata.name}')}

# Label the node
kubectl label node $NODE_NAME ng=multi-eni

echo "Node $NODE_NAME labeled with ng=multi-eni"
echo "To check the status of the NodeENI resource, run:"
echo "kubectl get nodeeni multus-eni-config -o yaml"
