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

	// DPDKPCIAddress is the PCI address of the device to bind to DPDK
	// If specified, this will be used instead of relying on device index mapping
	// Example: "0000:00:06.0"
	// +optional
	DPDKPCIAddress string `json:"dpdkPCIAddress,omitempty"`
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

	// DPDKPCIAddress is the PCI address of the device bound to DPDK
	// +optional
	DPDKPCIAddress string `json:"dpdkPCIAddress,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NodeENIList is a list of NodeENI resources
type NodeENIList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []NodeENI `json:"items"`
}

// +genclient
// +genclient:nonNamespaced
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NodeSystemConfig is a specification for system-level node configurations
// including huge pages, sysctl parameters, and other performance optimizations
type NodeSystemConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NodeSystemConfigSpec   `json:"spec"`
	Status NodeSystemConfigStatus `json:"status,omitempty"`
}

// NodeSystemConfigSpec defines the desired system-level configuration for nodes
type NodeSystemConfigSpec struct {
	// NodeSelector selects nodes that should have system configurations applied
	NodeSelector map[string]string `json:"nodeSelector"`

	// HugePagesConfig configures huge pages on the selected nodes
	// +optional
	HugePagesConfig *HugePagesConfig `json:"hugePagesConfig,omitempty"`

	// SysctlConfig configures kernel parameters via sysctl
	// +optional
	SysctlConfig *SysctlConfig `json:"sysctlConfig,omitempty"`

	// KernelModulesConfig configures kernel modules to be loaded
	// +optional
	KernelModulesConfig *KernelModulesConfig `json:"kernelModulesConfig,omitempty"`

	// CPUConfig configures CPU-related optimizations
	// +optional
	CPUConfig *CPUConfig `json:"cpuConfig,omitempty"`

	// MemoryConfig configures memory-related optimizations
	// +optional
	MemoryConfig *MemoryConfig `json:"memoryConfig,omitempty"`
}

// HugePagesConfig defines huge pages configuration
type HugePagesConfig struct {
	// Enabled indicates whether huge pages should be configured
	Enabled bool `json:"enabled"`

	// Pages defines the huge pages configuration for different page sizes
	// +optional
	Pages []HugePageSpec `json:"pages,omitempty"`

	// MountPath is the path where huge pages should be mounted
	// Default: /dev/hugepages
	// +optional
	MountPath string `json:"mountPath,omitempty"`

	// NUMAAware indicates whether to configure huge pages in a NUMA-aware manner
	// +optional
	NUMAAware bool `json:"numaAware,omitempty"`

	// AllocationStrategy defines how huge pages should be allocated
	// Valid values: "runtime", "persistent", "best-effort"
	// Default: "runtime"
	// +optional
	AllocationStrategy string `json:"allocationStrategy,omitempty"`

	// PreAllocationCheck indicates whether to check memory availability before allocation
	// Default: true
	// +optional
	PreAllocationCheck *bool `json:"preAllocationCheck,omitempty"`

	// RetryPolicy defines retry behavior for failed allocations
	// +optional
	RetryPolicy *HugePageRetryPolicy `json:"retryPolicy,omitempty"`
}

// HugePageRetryPolicy defines retry behavior for huge page allocation failures
type HugePageRetryPolicy struct {
	// MaxRetries is the maximum number of retry attempts
	// Default: 3
	// +optional
	MaxRetries *int32 `json:"maxRetries,omitempty"`

	// RetryInterval is the interval between retry attempts
	// Default: "30s"
	// +optional
	RetryInterval string `json:"retryInterval,omitempty"`

	// BackoffMultiplier is the multiplier for exponential backoff
	// Default: 2.0
	// +optional
	BackoffMultiplier *float64 `json:"backoffMultiplier,omitempty"`
}

// HugePageSpec defines configuration for a specific huge page size
type HugePageSpec struct {
	// Size is the huge page size (e.g., "2Mi", "1Gi")
	Size string `json:"size"`

	// Count is the number of huge pages to allocate
	Count int32 `json:"count"`

	// NUMANode specifies which NUMA node to allocate pages on (optional)
	// +optional
	NUMANode *int32 `json:"numaNode,omitempty"`

	// MinCount is the minimum acceptable number of pages to allocate
	// If specified and allocation fails to meet this minimum, the allocation is considered failed
	// +optional
	MinCount *int32 `json:"minCount,omitempty"`

	// AllowPartialAllocation indicates whether partial allocation is acceptable
	// Default: true
	// +optional
	AllowPartialAllocation *bool `json:"allowPartialAllocation,omitempty"`

	// Priority defines the allocation priority for this page size
	// Higher values indicate higher priority (0-100)
	// Default: 50
	// +optional
	Priority *int32 `json:"priority,omitempty"`
}

// SysctlConfig defines sysctl parameter configurations
type SysctlConfig struct {
	// Enabled indicates whether sysctl parameters should be configured
	Enabled bool `json:"enabled"`

	// Parameters is a map of sysctl parameter names to their values
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`

	// Presets defines predefined sysctl configurations for common use cases
	// +optional
	Presets []string `json:"presets,omitempty"`
}

// KernelModulesConfig defines kernel modules configuration
type KernelModulesConfig struct {
	// Enabled indicates whether kernel modules should be managed
	Enabled bool `json:"enabled"`

	// Load is a list of kernel modules to load
	// +optional
	Load []string `json:"load,omitempty"`

	// Unload is a list of kernel modules to unload
	// +optional
	Unload []string `json:"unload,omitempty"`

	// Parameters is a map of module names to their parameters
	// +optional
	Parameters map[string]map[string]string `json:"parameters,omitempty"`
}

// CPUConfig defines CPU-related optimizations
type CPUConfig struct {
	// Enabled indicates whether CPU optimizations should be applied
	Enabled bool `json:"enabled"`

	// IsolatedCPUs defines CPU cores to isolate for dedicated workloads
	// +optional
	IsolatedCPUs string `json:"isolatedCPUs,omitempty"`

	// Governor defines the CPU frequency governor to use
	// +optional
	Governor string `json:"governor,omitempty"`

	// TurboBoost indicates whether to enable/disable turbo boost
	// +optional
	TurboBoost *bool `json:"turboBoost,omitempty"`
}

// MemoryConfig defines memory-related optimizations
type MemoryConfig struct {
	// Enabled indicates whether memory optimizations should be applied
	Enabled bool `json:"enabled"`

	// Swappiness defines the kernel swappiness value (0-100)
	// +optional
	Swappiness *int32 `json:"swappiness,omitempty"`

	// TransparentHugePages defines THP configuration
	// +optional
	TransparentHugePages string `json:"transparentHugePages,omitempty"`

	// KSM defines Kernel Samepage Merging configuration
	// +optional
	KSM *KSMConfig `json:"ksm,omitempty"`
}

// KSMConfig defines Kernel Samepage Merging configuration
type KSMConfig struct {
	// Enabled indicates whether KSM should be enabled
	Enabled bool `json:"enabled"`

	// ScanInterval defines the scan interval in milliseconds
	// +optional
	ScanInterval *int32 `json:"scanInterval,omitempty"`

	// PagesToScan defines the number of pages to scan per interval
	// +optional
	PagesToScan *int32 `json:"pagesToScan,omitempty"`
}

// NodeSystemConfigStatus defines the observed state of NodeSystemConfig
type NodeSystemConfigStatus struct {
	// Configurations is a list of system configurations applied to nodes
	// +optional
	Configurations []SystemConfigStatus `json:"configurations,omitempty"`

	// LastUpdated is the timestamp of the last status update
	// +optional
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`
}

// SystemConfigStatus represents the status of system configuration on a node
type SystemConfigStatus struct {
	// NodeID is the ID of the node
	NodeID string `json:"nodeID"`

	// NodeName is the name of the node
	NodeName string `json:"nodeName"`

	// HugePagesStatus contains the status of huge pages configuration
	// +optional
	HugePagesStatus *HugePagesStatus `json:"hugePagesStatus,omitempty"`

	// SysctlStatus contains the status of sysctl configuration
	// +optional
	SysctlStatus *SysctlStatus `json:"sysctlStatus,omitempty"`

	// KernelModulesStatus contains the status of kernel modules configuration
	// +optional
	KernelModulesStatus *KernelModulesStatus `json:"kernelModulesStatus,omitempty"`

	// CPUStatus contains the status of CPU configuration
	// +optional
	CPUStatus *CPUStatus `json:"cpuStatus,omitempty"`

	// MemoryStatus contains the status of memory configuration
	// +optional
	MemoryStatus *MemoryStatus `json:"memoryStatus,omitempty"`

	// OverallStatus is the overall status of the configuration
	OverallStatus string `json:"overallStatus"`

	// LastUpdated is the timestamp of the last update
	LastUpdated metav1.Time `json:"lastUpdated"`

	// Message contains additional information about the configuration status
	// +optional
	Message string `json:"message,omitempty"`
}

// HugePagesStatus represents the status of huge pages configuration
type HugePagesStatus struct {
	// Configured indicates whether huge pages are configured
	Configured bool `json:"configured"`

	// Pages contains the status of each configured page size
	// +optional
	Pages []HugePageStatus `json:"pages,omitempty"`

	// MountPath is the actual mount path used
	// +optional
	MountPath string `json:"mountPath,omitempty"`

	// TotalAllocated is the total number of huge pages allocated
	// +optional
	TotalAllocated int32 `json:"totalAllocated,omitempty"`

	// TotalAvailable is the total number of huge pages available
	// +optional
	TotalAvailable int32 `json:"totalAvailable,omitempty"`

	// AllocationStrategy is the strategy used for allocation
	// +optional
	AllocationStrategy string `json:"allocationStrategy,omitempty"`

	// AllocationStatus indicates the overall allocation status
	// Valid values: "Success", "PartialSuccess", "Failed", "Pending", "InsufficientMemory"
	// +optional
	AllocationStatus string `json:"allocationStatus,omitempty"`

	// LastAllocationAttempt is the timestamp of the last allocation attempt
	// +optional
	LastAllocationAttempt metav1.Time `json:"lastAllocationAttempt,omitempty"`

	// RetryCount is the number of retry attempts made
	// +optional
	RetryCount int32 `json:"retryCount,omitempty"`

	// PersistenceStatus indicates whether the configuration will survive reboot
	// Valid values: "Persistent", "Runtime", "Unknown"
	// +optional
	PersistenceStatus string `json:"persistenceStatus,omitempty"`

	// MemoryInfo contains memory information relevant to huge pages
	// +optional
	MemoryInfo *HugePageMemoryInfo `json:"memoryInfo,omitempty"`

	// Errors contains any errors encountered during allocation
	// +optional
	Errors []string `json:"errors,omitempty"`
}

// HugePageMemoryInfo contains memory information relevant to huge pages allocation
type HugePageMemoryInfo struct {
	// TotalMemory is the total system memory in bytes
	// +optional
	TotalMemory int64 `json:"totalMemory,omitempty"`

	// AvailableMemory is the available memory for huge pages allocation in bytes
	// +optional
	AvailableMemory int64 `json:"availableMemory,omitempty"`

	// FragmentationLevel indicates memory fragmentation (0-100)
	// Higher values indicate more fragmentation
	// +optional
	FragmentationLevel *int32 `json:"fragmentationLevel,omitempty"`

	// LargestContiguousBlock is the size of the largest contiguous memory block in bytes
	// +optional
	LargestContiguousBlock int64 `json:"largestContiguousBlock,omitempty"`

	// NUMANodes contains per-NUMA node memory information
	// +optional
	NUMANodes []NUMANodeMemoryInfo `json:"numaNodes,omitempty"`
}

// NUMANodeMemoryInfo contains memory information for a specific NUMA node
type NUMANodeMemoryInfo struct {
	// NodeID is the NUMA node ID
	NodeID int32 `json:"nodeID"`

	// TotalMemory is the total memory on this NUMA node in bytes
	// +optional
	TotalMemory int64 `json:"totalMemory,omitempty"`

	// AvailableMemory is the available memory on this NUMA node in bytes
	// +optional
	AvailableMemory int64 `json:"availableMemory,omitempty"`

	// HugePagesAllocated is the number of huge pages already allocated on this node
	// +optional
	HugePagesAllocated map[string]int32 `json:"hugePagesAllocated,omitempty"`
}

// HugePageStatus represents the status of a specific huge page size
type HugePageStatus struct {
	// Size is the huge page size
	Size string `json:"size"`

	// Requested is the number of pages requested
	Requested int32 `json:"requested"`

	// Allocated is the number of pages actually allocated
	Allocated int32 `json:"allocated"`

	// Available is the number of pages available for use
	Available int32 `json:"available"`

	// NUMANode is the NUMA node where pages are allocated (if applicable)
	// +optional
	NUMANode *int32 `json:"numaNode,omitempty"`

	// AllocationStatus indicates the allocation status for this page size
	// Valid values: "Success", "PartialSuccess", "Failed", "Pending", "InsufficientMemory"
	// +optional
	AllocationStatus string `json:"allocationStatus,omitempty"`

	// AllocationMethod indicates how the pages were allocated
	// Valid values: "Runtime", "Persistent", "PreAllocated"
	// +optional
	AllocationMethod string `json:"allocationMethod,omitempty"`

	// LastAllocationAttempt is the timestamp of the last allocation attempt for this page size
	// +optional
	LastAllocationAttempt metav1.Time `json:"lastAllocationAttempt,omitempty"`

	// AllocationErrors contains any errors specific to this page size allocation
	// +optional
	AllocationErrors []string `json:"allocationErrors,omitempty"`

	// MemoryRequirement is the total memory requirement for this page size in bytes
	// +optional
	MemoryRequirement int64 `json:"memoryRequirement,omitempty"`

	// Priority is the allocation priority that was used
	// +optional
	Priority *int32 `json:"priority,omitempty"`

	// PartialAllocationAccepted indicates whether partial allocation was accepted
	// +optional
	PartialAllocationAccepted *bool `json:"partialAllocationAccepted,omitempty"`
}

// SysctlStatus represents the status of sysctl configuration
type SysctlStatus struct {
	// Configured indicates whether sysctl parameters are configured
	Configured bool `json:"configured"`

	// AppliedParameters contains the actually applied sysctl parameters
	// +optional
	AppliedParameters map[string]string `json:"appliedParameters,omitempty"`

	// FailedParameters contains parameters that failed to apply
	// +optional
	FailedParameters map[string]string `json:"failedParameters,omitempty"`
}

// KernelModulesStatus represents the status of kernel modules configuration
type KernelModulesStatus struct {
	// Configured indicates whether kernel modules are configured
	Configured bool `json:"configured"`

	// LoadedModules contains the list of successfully loaded modules
	// +optional
	LoadedModules []string `json:"loadedModules,omitempty"`

	// UnloadedModules contains the list of successfully unloaded modules
	// +optional
	UnloadedModules []string `json:"unloadedModules,omitempty"`

	// FailedModules contains modules that failed to load/unload
	// +optional
	FailedModules []string `json:"failedModules,omitempty"`
}

// CPUStatus represents the status of CPU configuration
type CPUStatus struct {
	// Configured indicates whether CPU optimizations are configured
	Configured bool `json:"configured"`

	// IsolatedCPUs contains the actually isolated CPU cores
	// +optional
	IsolatedCPUs string `json:"isolatedCPUs,omitempty"`

	// Governor contains the current CPU frequency governor
	// +optional
	Governor string `json:"governor,omitempty"`

	// TurboBoost indicates the current turbo boost status
	// +optional
	TurboBoost *bool `json:"turboBoost,omitempty"`
}

// MemoryStatus represents the status of memory configuration
type MemoryStatus struct {
	// Configured indicates whether memory optimizations are configured
	Configured bool `json:"configured"`

	// Swappiness contains the current swappiness value
	// +optional
	Swappiness *int32 `json:"swappiness,omitempty"`

	// TransparentHugePages contains the current THP setting
	// +optional
	TransparentHugePages string `json:"transparentHugePages,omitempty"`

	// KSMStatus contains the current KSM configuration
	// +optional
	KSMStatus *KSMStatus `json:"ksmStatus,omitempty"`
}

// KSMStatus represents the status of Kernel Samepage Merging
type KSMStatus struct {
	// Enabled indicates whether KSM is enabled
	Enabled bool `json:"enabled"`

	// ScanInterval contains the current scan interval
	// +optional
	ScanInterval *int32 `json:"scanInterval,omitempty"`

	// PagesToScan contains the current pages to scan setting
	// +optional
	PagesToScan *int32 `json:"pagesToScan,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// NodeSystemConfigList is a list of NodeSystemConfig resources
type NodeSystemConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata"`

	Items []NodeSystemConfig `json:"items"`
}
