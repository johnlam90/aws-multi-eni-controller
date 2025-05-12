# ENI Lifecycle Diagram

This diagram illustrates the complete lifecycle of an ENI managed by the AWS Multi-ENI Controller.

```mermaid
flowchart TD
    Start([Start]) --> NodeENICreated[NodeENI created with\nnode selector]
    NodeENICreated --> Requested[Requested]
    Requested --> ControllerIdentifies[Controller identifies\nmatching node]
    ControllerIdentifies --> Creating[Creating]
    Creating --> ENICreated[ENI created in AWS]
    ENICreated --> Created[Created]
    Created --> AttachingToNode[Attaching to Node]
    AttachingToNode --> Attaching[Attaching]
    Attaching --> SuccessfullyAttached[Successfully attached]
    SuccessfullyAttached --> Attached[Attached]
    Attached --> ENIManagerConfigures[ENI Manager configures\ninterface]
    ENIManagerConfigures --> Configured[Configured]
    Configured --> ENIReady[ENI ready for use]
    ENIReady --> Running[Running]
    
    Running --> PeriodicVerification{{Periodic verification}}
    PeriodicVerification --> ENIStillValid{ENI still valid}
    ENIStillValid -->|Yes| Verifying[Verifying]
    Verifying --> Running
    
    NodeENIDeleted[NodeENI deleted/updated] --> ENIInvalid{ENI invalid/stale}
    ENIStillValid -->|No| ENIInvalid
    ENIInvalid --> Detaching[Detaching]
    Detaching --> SuccessfullyDetached[Successfully detached]
    SuccessfullyDetached --> Deleting[Deleting]
    Deleting --> ENIDeleted[ENI deleted]
    ENIDeleted --> End([End])
    
    classDef process fill:#f9f,stroke:#333,stroke-width:2px;
    classDef decision fill:#bbf,stroke:#333,stroke-width:2px;
    classDef state fill:#dfd,stroke:#333,stroke-width:2px;
    
    class ControllerIdentifies,ENICreated,AttachingToNode,SuccessfullyAttached,ENIManagerConfigures,ENIReady,SuccessfullyDetached,ENIDeleted process;
    class ENIStillValid,ENIInvalid decision;
    class Requested,Creating,Created,Attaching,Attached,Configured,Running,Verifying,Detaching,Deleting state;
```

## How to Use This Diagram

This Mermaid.js diagram is directly rendered by GitHub when viewing the markdown file. You can also:

1. Copy the Mermaid code to any Mermaid live editor to modify it
2. Include it in other markdown files by copying the code block
3. Export it as an image using a Mermaid live editor if needed

## Updating the Diagram

To update this diagram:

1. Edit the Mermaid code in this file
2. Commit the changes to the repository
3. GitHub will automatically render the updated diagram

## Mermaid Resources

- [Mermaid.js Documentation](https://mermaid-js.github.io/mermaid/#/)
- [Mermaid Live Editor](https://mermaid.live/)
