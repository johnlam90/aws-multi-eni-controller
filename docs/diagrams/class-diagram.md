# AWS Multi-ENI Controller Class Diagram

This diagram illustrates the key components and their relationships in the AWS Multi-ENI Controller.

```mermaid
%%{
  init: {
    'theme': 'base',
    'themeVariables': {
      'primaryColor': '#f5f5f7',
      'primaryTextColor': '#1d1d1f',
      'primaryBorderColor': '#d2d2d7',
      'lineColor': '#86868b',
      'secondaryColor': '#f5f5f7',
      'tertiaryColor': '#e8e8ed'
    }
  }
}%%

classDiagram
    class NodeENIReconciler {
        +Client client
        +Log log
        +Scheme scheme
        +Recorder recorder
        +AWS EC2Interface
        +Config ControllerConfig
        +SetupWithManager()
        +Reconcile()
        +handleDeletion()
        +cleanupENIAttachment()
        +processNodeENI()
        +processNode()
        +createAndAttachENI()
        +verifyENIAttachments()
    }
    
    class EC2Interface {
        +CreateENI()
        +AttachENI()
        +DetachENI()
        +DeleteENI()
        +DescribeENI()
        +WaitForENIDetachment()
        +GetSubnetIDByName()
        +GetSecurityGroupIDByName()
    }
    
    class NodeENI {
        +Spec NodeENISpec
        +Status NodeENIStatus
    }
    
    class NodeENISpec {
        +nodeSelector map[string]string
        +subnetID string
        +subnetIDs []string
        +subnetName string
        +subnetNames []string
        +securityGroupIDs []string
        +securityGroupNames []string
        +deviceIndex int
        +mtu int
        +deleteOnTermination bool
        +description string
    }
    
    class NodeENIStatus {
        +attachments []ENIAttachment
    }
    
    class ENIAttachment {
        +nodeID string
        +instanceID string
        +eniID string
        +attachmentID string
        +subnetID string
        +subnetCIDR string
        +mtu int
        +status string
        +lastUpdated metav1.Time
    }
    
    class ENIManager {
        +Config ENIManagerConfig
        +checkAndBringUpInterfaces()
        +bringUpInterface()
        +setInterfaceMTU()
        +monitorInterfaces()
        +updateMTUFromNodeENI()
    }
    
    class ENIManagerConfig {
        +CheckInterval time.Duration
        +PrimaryInterface string
        +DebugMode bool
        +InterfaceUpTimeout time.Duration
        +ENIPattern string
        +IgnoreInterfaces []string
        +DefaultMTU int
        +InterfaceMTUs map[string]int
    }
    
    NodeENIReconciler --> EC2Interface : uses
    NodeENIReconciler --> NodeENI : watches
    NodeENI --> NodeENISpec : has
    NodeENI --> NodeENIStatus : has
    NodeENIStatus --> ENIAttachment : contains
    ENIManager --> ENIManagerConfig : uses
```

## How to Use This Diagram

This Mermaid.js class diagram is directly rendered by GitHub when viewing the markdown file. You can also:

1. Copy the Mermaid code to any Mermaid live editor to modify it
2. Include it in other markdown files by copying the code block
3. Export it as an image using a Mermaid live editor if needed

## Updating the Diagram

To update this diagram:

1. Edit the Mermaid code in this file
2. Commit the changes to the repository
3. GitHub will automatically render the updated diagram

## Diagram Explanation

This class diagram shows the main components of the AWS Multi-ENI Controller:

1. **NodeENIReconciler**: The main controller that reconciles NodeENI resources
2. **EC2Interface**: Interface for AWS EC2 operations
3. **NodeENI**: Custom resource with Spec and Status
4. **NodeENISpec**: Specification for the NodeENI resource
5. **NodeENIStatus**: Status of the NodeENI resource
6. **ENIAttachment**: Represents an ENI attachment to a node
7. **ENIManager**: DaemonSet component that configures network interfaces
8. **ENIManagerConfig**: Configuration for the ENI Manager

The arrows show the relationships between these components:
- NodeENIReconciler watches NodeENI resources
- NodeENIReconciler uses EC2Interface for AWS operations
- NodeENI has Spec and Status
- NodeENIStatus contains ENIAttachments
- ENIManager uses ENIManagerConfig

## Mermaid Resources

- [Mermaid.js Documentation](https://mermaid-js.github.io/mermaid/#/)
- [Mermaid Live Editor](https://mermaid.live/)
