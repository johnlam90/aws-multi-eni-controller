#!/bin/bash
set -e

echo "===== Installing Cloud-Native DPDK Integration ====="

# Step 1: Install the DPDK CRD
echo "Step 1: Installing DPDK CRD..."
kubectl apply -f dpdk-crd.yaml

# Wait for the CRD to be established
echo "Waiting for DPDK CRD to be established..."
until kubectl get crd dpdkconfigs.networking.k8s.aws -o jsonpath='{.status.conditions[?(@.type=="Established")].status}' 2>/dev/null | grep -q "True"; do
  echo "Waiting for CRD to be established..."
  sleep 5
done
echo "DPDK CRD is established"

# Step 2: Deploy the DPDK Feature Discovery
echo "Step 2: Deploying DPDK Feature Discovery..."
kubectl apply -f dpdk-feature-discovery.yaml

# Wait for the DPDK Feature Discovery to start
echo "Waiting for DPDK Feature Discovery to start..."
kubectl rollout status daemonset/dpdk-feature-discovery -n eni-controller-system --timeout=120s || echo "Warning: Timeout waiting for DPDK Feature Discovery"

# Step 3: Deploy the DPDK Module Installer
echo "Step 3: Deploying DPDK Module Installer..."
kubectl apply -f dpdk-module-installer.yaml

# Step 4: Deploy the DPDK Operator
echo "Step 4: Deploying DPDK Operator..."
kubectl apply -f dpdk-operator-deployment.yaml

# Wait for the DPDK Operator to start
echo "Waiting for DPDK Operator to start..."
kubectl rollout status deployment/dpdk-operator -n eni-controller-system --timeout=120s || echo "Warning: Timeout waiting for DPDK Operator"

# Step 5: Create the SRIOV Device Plugin Configuration
echo "Step 5: Creating SRIOV Device Plugin Configuration..."
kubectl create configmap sriov-device-plugin-config -n kube-system --from-file=config.json=sriov-config.json --dry-run=client -o yaml | kubectl apply -f -

# Step 6: Deploy the SRIOV Device Plugin
echo "Step 6: Deploying SRIOV Device Plugin..."
kubectl apply -f sriov-device-plugin.yaml

# Step 7: Patch the ENI Manager
echo "Step 7: Patching ENI Manager..."
kubectl patch daemonset eni-manager -n eni-controller-system --patch "$(cat eni-manager-patch.yaml)"

# Step 8: Create a DPDKConfig Resource
echo "Step 8: Creating DPDKConfig Resource..."
kubectl apply -f dpdk-config.yaml

echo "===== Cloud-Native DPDK Integration Installed ====="
echo "Checking node labels..."
kubectl get nodes --show-labels | grep dpdk

echo "Checking DPDK resources..."
kubectl get dpdkconfigs

echo "Checking NodeENI resources..."
kubectl get nodeeni

echo "===== Installation Complete ====="
echo "The DPDK integration will continue to set up the environment in the background."
echo "Check the logs of the DPDK components for progress:"
echo "  kubectl logs -n eni-controller-system -l app=dpdk-feature-discovery"
echo "  kubectl logs -n eni-controller-system -l app=dpdk-module-installer"
echo "  kubectl logs -n eni-controller-system -l app=dpdk-operator"
echo "  kubectl logs -n kube-system -l app=sriov-device-plugin"
echo "  kubectl logs -n eni-controller-system -l app=eni-manager"
