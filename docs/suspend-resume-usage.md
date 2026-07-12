# Suspend/Resume Usage Guide

## Workspace Suspension and Resume

The Parseable operator supports suspending and resuming workspaces using the `parseable.com/workspace` annotation.

### Full Workspace Suspension

To suspend all components (query, ingestor, prism):
```bash
kubectl annotate parseablecluster <name> -n <namespace> parseable.com/workspace=suspend
```

To resume all components:
```bash
kubectl annotate parseablecluster <name> -n <namespace> parseable.com/workspace=resume --overwrite
```

### Ingestor-Only Suspension

To suspend only ingestor nodes while keeping query and prism running:
```bash
kubectl annotate parseablecluster <name> -n <namespace> parseable.com/workspace=suspend-ingestor
```

To resume only ingestor nodes:
```bash
kubectl annotate parseablecluster <name> -n <namespace> parseable.com/workspace=resume-ingestor --overwrite
```

### Querier-Only Suspension

To suspend only querier nodes while keeping ingestor and prism running:
```bash
kubectl annotate parseablecluster <name> -n <namespace> parseable.com/workspace=suspend-querier
```

To resume only querier nodes:
```bash
kubectl annotate parseablecluster <name> -n <namespace> parseable.com/workspace=resume-querier --overwrite
```

### Automatic Resume on Annotation Removal

Removing the `parseable.com/workspace` annotation will automatically resume the suspended components:
```bash
kubectl annotate parseablecluster <name> -n <namespace> parseable.com/workspace-
```

This works for full suspension, ingestor-only suspension, and querier-only suspension - the operator tracks the previous state and resumes accordingly.

## Implementation Details

- **Full suspension (`suspend`)**: Scales all StatefulSets/Deployments to 0 and stores original replica counts
- **Ingestor suspension (`suspend-ingestor`)**: Only scales down components with "ingestor" in their name
- **Querier suspension (`suspend-querier`)**: Only scales down components with "querier" in their name (StatefulSets only)
- **Resume**: Restores components to their original replica counts (for full suspend) or spec-defined counts (for ingestor/querier)
- **State tracking**: Uses `parseable.com/workspace-previous-state` annotation to handle annotation removal