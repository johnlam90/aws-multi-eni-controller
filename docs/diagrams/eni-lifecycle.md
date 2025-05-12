# ENI Lifecycle Diagram

This diagram illustrates the complete lifecycle of an ENI managed by the AWS Multi-ENI Controller.

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
    },
    'flowchart': {
      'curve': 'basis'
    }
  }
}%%

flowchart TD
    Start([Start]) --> NodeENICreated["NodeENI created with<br/>node selector"]
    NodeENICreated --> Requested[Requested]
    Requested --> ControllerIdentifies["Controller identifies<br/>matching node"]
    ControllerIdentifies --> Creating[Creating]
    Creating --> ENICreated[ENI created in AWS]
    ENICreated --> Created[Created]
    Created --> AttachingToNode[Attaching to Node]
    AttachingToNode --> Attaching[Attaching]
    Attaching --> SuccessfullyAttached[Successfully attached]
    SuccessfullyAttached --> Attached[Attached]
    Attached --> ENIManagerConfigures["ENI Manager configures<br/>interface"]
    ENIManagerConfigures --> Configured[Configured]
    Configured --> ENIReady[ENI ready for use]
    ENIReady --> Running[Running]

    Running --> PeriodicVerification{{Periodic verification}}
    PeriodicVerification --> ENIStillValid{ENI still valid}
    ENIStillValid -->|Yes| Verifying[Verifying]
    Verifying --> Running

    NodeENIDeleted["NodeENI deleted/updated"] --> ENIInvalid{"ENI invalid/stale"}
    ENIStillValid -->|No| ENIInvalid
    ENIInvalid --> Detaching[Detaching]
    Detaching --> SuccessfullyDetached[Successfully detached]
    SuccessfullyDetached --> Deleting[Deleting]
    Deleting --> ENIDeleted[ENI deleted]
    ENIDeleted --> End([End])

    %% Apple-like styling
    classDef default fill:#f5f5f7,stroke:#d2d2d7,stroke-width:1px,color:#1d1d1f,font-family:SF Pro Display,Helvetica,Arial,sans-serif;
    classDef terminal fill:#f5f5f7,stroke:#d2d2d7,stroke-width:1px,color:#1d1d1f,font-family:SF Pro Display,Helvetica,Arial,sans-serif,stroke-dasharray: 5 5;
    classDef process fill:#ffffff,stroke:#d2d2d7,stroke-width:1px,color:#1d1d1f;
    classDef decision fill:#f5f5f7,stroke:#d2d2d7,stroke-width:1px,color:#1d1d1f;
    classDef state fill:#f5f5f7,stroke:#d2d2d7,stroke-width:1px,color:#1d1d1f;
    classDef action fill:#0071e3,stroke:none,color:#ffffff,font-weight:500;

    class Start,End terminal;
    class ControllerIdentifies,ENICreated,AttachingToNode,SuccessfullyAttached,ENIManagerConfigures,ENIReady,SuccessfullyDetached,ENIDeleted process;
    class ENIStillValid,ENIInvalid,PeriodicVerification decision;
    class Running,Verifying action;
    class Requested,Creating,Created,Attaching,Attached,Configured,Detaching,Deleting state;
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
