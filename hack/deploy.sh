#!/bin/bash

# Check if we should use the GitHub Container Registry image
USE_GHCR=${USE_GHCR:-false}

if [ "$USE_GHCR" = "true" ]; then
  # Use the GitHub Container Registry image
  UNIFIED_IMAGE=${1:-"ghcr.io/johnlam90/aws-multi-eni-controller:latest"}
  echo "Using GitHub Container Registry image: $UNIFIED_IMAGE"
else
  # Set the image name and tag with a timestamp to avoid caching
  TIMESTAMP=$(date +%Y%m%d%H%M%S)
  UNIFIED_IMAGE=${1:-"johnlam90/aws-multi-eni:v1-$TIMESTAMP"}

  # Build the unified Docker image
  echo "Building unified image: $UNIFIED_IMAGE"
  docker build -t "$UNIFIED_IMAGE" .

  # Push the Docker image (if it's in a remote registry)
  if [[ $UNIFIED_IMAGE == *"/"* ]]; then
    echo "Pushing unified image: $UNIFIED_IMAGE"
    docker push "$UNIFIED_IMAGE"
  fi
fi

# Update the deployment manifests
sed -i '' "s|\${UNIFIED_IMAGE}|$UNIFIED_IMAGE|g" deploy/deployment.yaml
sed -i '' "s|\${UNIFIED_IMAGE}|$UNIFIED_IMAGE|g" deploy/eni-manager-daemonset.yaml

# Apply the CRD
kubectl apply -f deploy/crds/networking.k8s.aws_nodeenis_crd.yaml

# Apply the deployment
kubectl apply -f deploy/deployment.yaml

# Wait for the deployment to be ready
kubectl -n eni-controller-system rollout status deployment/eni-controller

# Apply the ENI Manager DaemonSet
kubectl apply -f deploy/eni-manager-daemonset.yaml

# Apply the sample NodeENI resource
kubectl apply -f deploy/samples/networking_v1alpha1_nodeeni.yaml

echo "ENI Controller deployed successfully!"
echo "To label a node for testing, run:"
echo "kubectl label node <node-name> ng=multi-eni"
