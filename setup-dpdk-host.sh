#!/bin/bash
set -e

echo "Setting up DPDK environment on the host..."

# Detect package manager and install required packages
if command -v yum &> /dev/null; then
    echo "Using yum package manager..."
    yum install -y kernel-modules-extra
elif command -v apt-get &> /dev/null; then
    echo "Using apt package manager..."
    apt-get update
    apt-get install -y linux-modules-extra-$(uname -r) || apt-get install -y linux-modules-$(uname -r) || echo "Could not install extra kernel modules, continuing anyway..."
else
    echo "No supported package manager found, skipping package installation"
fi

# Load kernel modules
echo "Loading kernel modules..."
modprobe vfio || echo "Failed to load vfio module"
modprobe vfio-pci || echo "Failed to load vfio-pci module"

# Enable unsafe NOIOMMU mode
echo "Enabling unsafe NOIOMMU mode..."
if [ -f /sys/module/vfio/parameters/enable_unsafe_noiommu_mode ]; then
    echo 1 > /sys/module/vfio/parameters/enable_unsafe_noiommu_mode || echo "Failed to enable unsafe NOIOMMU mode"
else
    echo "NOIOMMU mode parameter not found, this might be normal depending on your kernel version"
fi

# Create module load configuration for persistence across reboots
echo "Setting up persistence across reboots..."
mkdir -p /etc/modules-load.d
echo "vfio" > /etc/modules-load.d/dpdk.conf
echo "vfio-pci" >> /etc/modules-load.d/dpdk.conf
chmod 644 /etc/modules-load.d/dpdk.conf

# Create modprobe configuration for persistence across reboots
mkdir -p /etc/modprobe.d
echo "options vfio enable_unsafe_noiommu_mode=1" > /etc/modprobe.d/dpdk.conf
chmod 644 /etc/modprobe.d/dpdk.conf

echo "DPDK setup completed successfully."
