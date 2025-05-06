#!/bin/bash

# Set the image name and tag with a timestamp to avoid caching
TIMESTAMP=$(date +%Y%m%d%H%M%S)
IMAGE_NAME=${1:-"johnlam90/eni-controller:v1-$TIMESTAMP"}

# Build the Docker image
docker build -t "$IMAGE_NAME" .

# Push the Docker image (if it's a remote registry)
if [[ $IMAGE_NAME == *"/"* ]]; then
  docker push "$IMAGE_NAME"
fi

# Update the deployment manifest
sed -i '' "s|\${CONTROLLER_IMAGE}|$IMAGE_NAME|g" deploy/deployment.yaml

# Apply the CRD
kubectl apply -f deploy/crds/networking.example.com_nodeenis_crd.yaml

# Apply the deployment
kubectl apply -f deploy/deployment.yaml

# Wait for the deployment to be ready
kubectl -n eni-controller-system rollout status deployment/eni-controller

# Apply the sample NodeENI resource
kubectl apply -f deploy/samples/networking_v1alpha1_nodeeni.yaml

echo "ENI Controller deployed successfully!"
echo "To label a node for testing, run:"
echo "kubectl label node <node-name> ng=multi-eni"
