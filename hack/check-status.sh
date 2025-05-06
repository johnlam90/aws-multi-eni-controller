#!/bin/bash

# Check the status of the NodeENI resource
kubectl get nodeeni multus-eni-config -o yaml

# Check the controller logs
echo -e "\nController logs:"
kubectl -n eni-controller-system logs -l app=eni-controller --tail=50
