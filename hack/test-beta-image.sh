#!/bin/bash
# Script to deploy the controller using a beta image from a sandbox branch

set -e

# Default values
BETA_TAG="beta-latest"
NAMESPACE="eni-controller-system"
DEPLOY_ENI_MANAGER=true

# Parse command line arguments
while [[ $# -gt 0 ]]; do
  key="$1"
  case $key in
    --tag)
      BETA_TAG="$2"
      shift
      shift
      ;;
    --branch)
      BRANCH="$2"
      BETA_TAG="beta-$BRANCH"
      shift
      shift
      ;;
    --namespace)
      NAMESPACE="$2"
      shift
      shift
      ;;
    --skip-eni-manager)
      DEPLOY_ENI_MANAGER=false
      shift
      ;;
    --help)
      echo "Usage: $0 [options]"
      echo ""
      echo "Options:"
      echo "  --tag TAG             Use a specific beta tag (e.g., beta-sandbox-testv0.1)"
      echo "  --branch BRANCH       Use the beta image from a specific branch (e.g., sandbox-testv0.1)"
      echo "  --namespace NS        Deploy to a specific namespace (default: eni-controller-system)"
      echo "  --skip-eni-manager    Don't deploy the ENI manager daemonset"
      echo "  --help                Display this help message"
      exit 0
      ;;
    *)
      echo "Unknown option: $1"
      exit 1
      ;;
  esac
done

# Set the image
IMAGE="ghcr.io/johnlam90/aws-multi-eni-controller:${BETA_TAG}"
echo "Using beta image: $IMAGE"

# Create namespace if it doesn't exist
if ! kubectl get namespace "$NAMESPACE" &>/dev/null; then
  echo "Creating namespace: $NAMESPACE"
  kubectl create namespace "$NAMESPACE"
fi

# Apply CRDs
echo "Applying CRDs..."
kubectl apply -f deploy/crds/networking.k8s.aws_nodeenis_crd.yaml

# Create temporary deployment files with the beta image
echo "Creating temporary deployment files with beta image..."
TMP_DIR=$(mktemp -d)
cp deploy/deployment.yaml "$TMP_DIR/deployment.yaml"
cp deploy/eni-manager-daemonset.yaml "$TMP_DIR/eni-manager-daemonset.yaml"

# Replace the image placeholder with the beta image
sed -i.bak "s|\${UNIFIED_IMAGE}|$IMAGE|g" "$TMP_DIR/deployment.yaml"
sed -i.bak "s|\${UNIFIED_IMAGE}|$IMAGE|g" "$TMP_DIR/eni-manager-daemonset.yaml"

# Apply the deployment
echo "Deploying controller to namespace: $NAMESPACE"
kubectl apply -f "$TMP_DIR/deployment.yaml" -n "$NAMESPACE"

# Apply the ENI manager daemonset if requested
if [ "$DEPLOY_ENI_MANAGER" = true ]; then
  echo "Deploying ENI manager daemonset to namespace: $NAMESPACE"
  kubectl apply -f "$TMP_DIR/eni-manager-daemonset.yaml" -n "$NAMESPACE"
fi

# Clean up temporary files
rm -rf "$TMP_DIR"

echo ""
echo "Deployment complete!"
echo "To check the status of the controller:"
echo "  kubectl get pods -n $NAMESPACE"
echo ""
echo "To view the controller logs:"
echo "  kubectl logs -n $NAMESPACE -l app=eni-controller -f"
echo ""
if [ "$DEPLOY_ENI_MANAGER" = true ]; then
  echo "To view the ENI manager logs:"
  echo "  kubectl logs -n $NAMESPACE -l app=eni-manager -f"
  echo ""
fi
echo "To create a test NodeENI resource:"
echo "  kubectl apply -f deploy/samples/nodeeni-example.yaml"
echo ""
echo "To clean up:"
echo "  kubectl delete -f deploy/samples/nodeeni-example.yaml"
echo "  kubectl delete -f $TMP_DIR/deployment.yaml -n $NAMESPACE"
if [ "$DEPLOY_ENI_MANAGER" = true ]; then
  echo "  kubectl delete -f $TMP_DIR/eni-manager-daemonset.yaml -n $NAMESPACE"
fi
echo ""
