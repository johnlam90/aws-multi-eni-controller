// Package v1alpha1 contains API Schema definitions for the networking v1alpha1 API group.
//
// This package defines the NodeENI custom resource, which is used to specify which nodes
// should have AWS Elastic Network Interfaces (ENIs) attached, and with what configuration.
// The NodeENI controller watches these resources and automatically manages the lifecycle
// of ENIs for matching nodes.
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NodeENI is a specification for a NodeENI resource
type NodeENI struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeENISpec   `json:"spec"`
	Status NodeENIStatus `json:"status,omitempty"`
}

// NodeENISpec is the spec for a NodeENI resource
type NodeENISpec struct {
	// NodeSelector selects nodes that should have ENIs attached
	NodeSelector map[string]string `json:"nodeSelector"`

	// SubnetID is the AWS Subnet ID where the ENI should be created
	// +optional
	SubnetID string `json:"subnetID,omitempty"`

	// SubnetName is the AWS Subnet Name where the ENI should be created
	// If SubnetID is not provided, SubnetName will be used to look up the subnet
	// +optional
	SubnetName string `json:"subnetName,omitempty"`

	// SubnetIDs is a list of AWS Subnet IDs where the ENI can be created
	// If specified, one subnet will be selected from this list
	// +optional
	SubnetIDs []string `json:"subnetIDs,omitempty"`

	// SubnetNames is a list of AWS Subnet Names where the ENI can be created
	// If SubnetIDs is not provided, SubnetNames will be used to look up the subnets
	// +optional
	SubnetNames []string `json:"subnetNames,omitempty"`

	// SecurityGroupIDs are the AWS Security Group IDs to attach to the ENI
	// +optional
	SecurityGroupIDs []string `json:"securityGroupIDs,omitempty"`

	// SecurityGroupNames are the AWS Security Group Names to attach to the ENI
	// If SecurityGroupIDs is not provided, SecurityGroupNames will be used to look up the security groups
	// +optional
	SecurityGroupNames []string `json:"securityGroupNames,omitempty"`

	// DeviceIndex is the device index for the ENI (default: 1)
	// +optional
	DeviceIndex int `json:"deviceIndex,omitempty"`

	// DeleteOnTermination indicates whether to delete the ENI when the node is terminated
	// +optional
	DeleteOnTermination bool `json:"deleteOnTermination,omitempty"`

	// Description is a description for the ENI
	// +optional
	Description string `json:"description,omitempty"`

	// MTU is the Maximum Transmission Unit for the ENI (default: system default)
	// +optional
	MTU int `json:"mtu,omitempty"`

	// EnableDPDK indicates whether to bind the ENI to DPDK driver after attachment
	// +optional
	EnableDPDK bool `json:"enableDPDK,omitempty"`

	// DPDKDriver is the driver to use for DPDK binding (default: vfio-pci)
	// +optional
	DPDKDriver string `json:"dpdkDriver,omitempty"`

	// DPDKResourceName is the resource name to use for the DPDK interface in SRIOV device plugin
	// If not specified, a default name will be generated based on the interface index
	// +optional
	DPDKResourceName string `json:"dpdkResourceName,omitempty"`
}

// NodeENIStatus is the status for a NodeENI resource
type NodeENIStatus struct {
	// Attachments is a list of ENI attachments
	// +optional
	Attachments []ENIAttachment `json:"attachments,omitempty"`
}

// ENIAttachment represents an ENI attachment to a node
type ENIAttachment struct {
	// NodeID is the ID of the node
	NodeID string `json:"nodeID"`

	// InstanceID is the EC2 instance ID
	InstanceID string `json:"instanceID"`

	// ENIID is the ID of the attached ENI
	ENIID string `json:"eniID"`

	// AttachmentID is the ID of the ENI attachment
	AttachmentID string `json:"attachmentID"`

	// SubnetID is the ID of the subnet where the ENI was created
	// +optional
	SubnetID string `json:"subnetID,omitempty"`

	// SubnetCIDR is the CIDR block of the subnet where the ENI was created
	// +optional
	SubnetCIDR string `json:"subnetCIDR,omitempty"`

	// MTU is the Maximum Transmission Unit configured for the ENI
	// +optional
	MTU int `json:"mtu,omitempty"`

	// DeviceIndex is the device index used for this ENI attachment
	// +optional
	DeviceIndex int `json:"deviceIndex,omitempty"`

	// Status is the status of the attachment
	Status string `json:"status"`

	// LastUpdated is the timestamp of the last update
	LastUpdated metav1.Time `json:"lastUpdated"`

	// DPDKBound indicates whether the ENI is bound to a DPDK driver
	// +optional
	DPDKBound bool `json:"dpdkBound,omitempty"`

	// DPDKDriver is the driver used for DPDK binding
	// +optional
	DPDKDriver string `json:"dpdkDriver,omitempty"`

	// DPDKResourceName is the resource name used for the DPDK interface in SRIOV device plugin
	// +optional
	DPDKResourceName string `json:"dpdkResourceName,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NodeENIList is a list of NodeENI resources
type NodeENIList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []NodeENI `json:"items"`
}
