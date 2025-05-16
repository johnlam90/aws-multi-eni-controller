// Package main implements the AWS Multi-ENI Manager, which is responsible for
// bringing up secondary network interfaces on AWS EC2 instances.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	networkingv1alpha1 "github.com/johnlam90/aws-multi-eni-controller/pkg/apis/networking/v1alpha1"
	"github.com/johnlam90/aws-multi-eni-controller/pkg/config"
	vnetlink "github.com/vishvananda/netlink"
)

// PCIDeviceInfo represents a PCI device information
type PCIDeviceInfo struct {
	PCIAddress string `json:"pciAddress"`
	Driver     string `json:"driver,omitempty"`
	Vendor     string `json:"vendor,omitempty"`
	DeviceID   string `json:"deviceID,omitempty"`
}

// SRIOVDeviceConfig represents the configuration for a SRIOV device
type SRIOVDeviceConfig struct {
	ResourceName string          `json:"resourceName"`
	RootDevices  []string        `json:"rootDevices,omitempty"`
	DeviceType   string          `json:"deviceType,omitempty"`
	Devices      []PCIDeviceInfo `json:"devices,omitempty"`
}

// SRIOVDPConfig represents the configuration for the SRIOV device plugin
type SRIOVDPConfig struct {
	ResourceList []SRIOVDeviceConfig `json:"resourceList"`
}

// bindInterfaceToDPDK binds a network interface to a DPDK driver
func bindInterfaceToDPDK(link vnetlink.Link, driver string, cfg *config.ENIManagerConfig) error {
	ifaceName := link.Attrs().Name
	log.Printf("Binding interface %s to DPDK driver %s", ifaceName, driver)

	// Get the PCI address for the interface
	pciAddress, err := getPCIAddressForInterface(ifaceName)
	if err != nil {
		return fmt.Errorf("failed to get PCI address for interface %s: %v", ifaceName, err)
	}

	// Check if the DPDK binding script exists
	if _, err := os.Stat(cfg.DPDKBindingScript); os.IsNotExist(err) {
		return fmt.Errorf("DPDK binding script %s does not exist", cfg.DPDKBindingScript)
	}

	// Execute the DPDK binding script to bind the interface to the DPDK driver
	cmd := exec.Command(cfg.DPDKBindingScript, "-b", driver, pciAddress)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to bind interface %s to DPDK driver %s: %v, output: %s",
			ifaceName, driver, err, string(output))
	}

	log.Printf("Successfully bound interface %s (PCI: %s) to DPDK driver %s", ifaceName, pciAddress, driver)

	// Update the SRIOV device plugin configuration
	if err := updateSRIOVDevicePluginConfig(ifaceName, pciAddress, driver, cfg); err != nil {
		log.Printf("Warning: Failed to update SRIOV device plugin config: %v", err)
		// Continue anyway, the interface is bound to DPDK
	}

	return nil
}

// getPCIAddressForInterface gets the PCI address for a network interface
func getPCIAddressForInterface(ifaceName string) (string, error) {
	// Path to the sysfs PCI device link for this interface
	sysfsPCIPath := fmt.Sprintf("/sys/class/net/%s/device", ifaceName)

	// Read the symlink to get the PCI path
	pciPath, err := os.Readlink(sysfsPCIPath)
	if err != nil {
		return "", fmt.Errorf("failed to read PCI path for interface %s: %v", ifaceName, err)
	}

	// Extract the PCI address from the path
	// The path is typically something like "../../../0000:00:06.0"
	pciAddress := filepath.Base(pciPath)

	// Validate the PCI address format
	pciRegex := regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-9a-f]$`)
	if !pciRegex.MatchString(pciAddress) {
		return "", fmt.Errorf("invalid PCI address format: %s", pciAddress)
	}

	return pciAddress, nil
}

// updateSRIOVDevicePluginConfig updates the SRIOV device plugin configuration
func updateSRIOVDevicePluginConfig(ifaceName, pciAddress, driver string, cfg *config.ENIManagerConfig) error {
	// Get the resource name for this interface
	resourceName, ok := cfg.DPDKResourceNames[ifaceName]
	if !ok {
		// Generate a default resource name based on the interface index
		// Extract the index from the interface name (e.g., eth2 -> 2)
		indexRegex := regexp.MustCompile(`[0-9]+$`)
		indexMatch := indexRegex.FindString(ifaceName)
		if indexMatch == "" {
			// If we can't extract an index, use a default
			resourceName = "intel.com/intel_sriov_netdevice_default"
		} else {
			resourceName = fmt.Sprintf("intel.com/intel_sriov_netdevice_%s", indexMatch)
		}
		// Store the generated resource name for future use
		cfg.DPDKResourceNames[ifaceName] = resourceName
	}

	// Check if the SRIOV device plugin config file exists
	var config SRIOVDPConfig
	if _, err := os.Stat(cfg.SRIOVDPConfigPath); os.IsNotExist(err) {
		// Create a new config file with this device
		config = SRIOVDPConfig{
			ResourceList: []SRIOVDeviceConfig{
				{
					ResourceName: resourceName,
					DeviceType:   "netdevice",
					Devices: []PCIDeviceInfo{
						{
							PCIAddress: pciAddress,
							Driver:     driver,
						},
					},
				},
			},
		}
	} else {
		// Read the existing config file
		configData, err := os.ReadFile(cfg.SRIOVDPConfigPath)
		if err != nil {
			return fmt.Errorf("failed to read SRIOV device plugin config: %v", err)
		}

		// Parse the config
		if err := json.Unmarshal(configData, &config); err != nil {
			return fmt.Errorf("failed to parse SRIOV device plugin config: %v", err)
		}

		// Check if the resource already exists
		resourceExists := false
		deviceExists := false
		for i, resource := range config.ResourceList {
			if resource.ResourceName == resourceName {
				resourceExists = true
				// Check if the device already exists
				for _, device := range resource.Devices {
					if device.PCIAddress == pciAddress {
						deviceExists = true
						break
					}
				}
				// If the device doesn't exist, add it
				if !deviceExists {
					config.ResourceList[i].Devices = append(config.ResourceList[i].Devices, PCIDeviceInfo{
						PCIAddress: pciAddress,
						Driver:     driver,
					})
				}
				break
			}
		}

		// If the resource doesn't exist, add it
		if !resourceExists {
			config.ResourceList = append(config.ResourceList, SRIOVDeviceConfig{
				ResourceName: resourceName,
				DeviceType:   "netdevice",
				Devices: []PCIDeviceInfo{
					{
						PCIAddress: pciAddress,
						Driver:     driver,
					},
				},
			})
		}
	}

	// Write the updated config back to the file
	configData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal SRIOV device plugin config: %v", err)
	}

	if err := os.WriteFile(cfg.SRIOVDPConfigPath, configData, 0644); err != nil {
		return fmt.Errorf("failed to write SRIOV device plugin config: %v", err)
	}

	log.Printf("Successfully updated SRIOV device plugin config for interface %s with resource name %s",
		ifaceName, resourceName)
	return nil
}

// processDPDKBindingForNodeENI processes DPDK binding for a NodeENI resource
func processDPDKBindingForNodeENI(nodeENI networkingv1alpha1.NodeENI, nodeName string, cfg *config.ENIManagerConfig) {
	// Skip if DPDK is not enabled globally
	if !cfg.EnableDPDK && !nodeENI.Spec.EnableDPDK {
		return
	}

	log.Printf("Processing DPDK binding for NodeENI %s", nodeENI.Name)

	// Check if this NodeENI applies to our node
	if nodeENI.Status.Attachments == nil {
		log.Printf("NodeENI %s has no attachments, skipping DPDK binding", nodeENI.Name)
		return
	}

	// Process each attachment
	for _, attachment := range nodeENI.Status.Attachments {
		processDPDKBindingForAttachment(attachment, nodeENI, nodeName, cfg)
	}
}

// processDPDKBindingForAttachment processes DPDK binding for a single NodeENI attachment
func processDPDKBindingForAttachment(attachment networkingv1alpha1.ENIAttachment, nodeENI networkingv1alpha1.NodeENI,
	nodeName string, cfg *config.ENIManagerConfig) {

	// Skip if this attachment is not for our node
	if attachment.NodeID != nodeName {
		return
	}

	// Skip if this attachment is already bound to DPDK
	if attachment.DPDKBound {
		log.Printf("ENI %s is already bound to DPDK driver %s", attachment.ENIID, attachment.DPDKDriver)
		return
	}

	// This attachment is for our node
	log.Printf("Processing DPDK binding for NodeENI attachment: %s, ENI: %s", nodeName, attachment.ENIID)

	// Determine the DPDK driver to use
	dpdkDriver := cfg.DefaultDPDKDriver
	if nodeENI.Spec.DPDKDriver != "" {
		dpdkDriver = nodeENI.Spec.DPDKDriver
	}

	// Get the interface name for this ENI
	ifaceName, err := getInterfaceNameForENI(attachment.ENIID)
	if err != nil {
		log.Printf("Error getting interface name for ENI %s: %v", attachment.ENIID, err)
		return
	}

	if ifaceName == "" {
		log.Printf("Could not find interface name for ENI %s, skipping DPDK binding", attachment.ENIID)
		return
	}

	// Get the link for this interface
	link, err := vnetlink.LinkByName(ifaceName)
	if err != nil {
		log.Printf("Error getting link for interface %s: %v", ifaceName, err)
		return
	}

	// Bind the interface to DPDK
	if err := bindInterfaceToDPDK(link, dpdkDriver, cfg); err != nil {
		log.Printf("Error binding interface %s to DPDK: %v", ifaceName, err)
		return
	}

	log.Printf("Successfully bound interface %s to DPDK driver %s", ifaceName, dpdkDriver)

	// Store the resource name for this interface if specified in the NodeENI
	if nodeENI.Spec.DPDKResourceName != "" {
		cfg.DPDKResourceNames[ifaceName] = nodeENI.Spec.DPDKResourceName
		// Update the SRIOV device plugin config with the specified resource name
		pciAddress, err := getPCIAddressForInterface(ifaceName)
		if err == nil {
			if err := updateSRIOVDevicePluginConfig(ifaceName, pciAddress, dpdkDriver, cfg); err != nil {
				log.Printf("Warning: Failed to update SRIOV device plugin config with resource name %s: %v",
					nodeENI.Spec.DPDKResourceName, err)
			}
		}
	}
}

// Note: We're using the getInterfaceNameForENI function from main.go

// updateDPDKBindingFromNodeENI updates DPDK binding from NodeENI resources
func updateDPDKBindingFromNodeENI(nodeName string, cfg *config.ENIManagerConfig, nodeENIs []networkingv1alpha1.NodeENI) {
	log.Printf("Updating DPDK binding from NodeENI resources for node %s", nodeName)

	// Process each NodeENI resource
	for _, nodeENI := range nodeENIs {
		processDPDKBindingForNodeENI(nodeENI, nodeName, cfg)
	}
}
