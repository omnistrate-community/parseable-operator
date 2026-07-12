# Parseable Operator

Parseable Operator is a Kubernetes operator for deploying, scaling, and operating [Parseable](https://parseable.com) Enterprise clusters. It is built with [Kubebuilder](https://book.kubebuilder.io/) / [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime) and ships three CRDs, all reconciled by a single controller-manager binary (`manager`, see [cmd/main.go](cmd/main.go)):

| CRD | Short names | Status | Purpose |
|---|---|---|---|
| `ParseableCluster` | `pbc`, `pbcs` | Stable | Declaratively defines and reconciles a multi-component Parseable cluster (ingestor, query, prism nodes) as StatefulSets/Deployments and Services. |
| `ParseableClusterAutoscaler` | `pbca`, `pbcas` | **Alpha** | Watches node CPU usage and scales ingestor node groups (including spot-labeled pools) up/down by adding or removing whole StatefulSets. |
| `ParseableClusterChaos` | `pbcc`, `pbccs` | **Alpha** | Cordons, drains, and deletes aged, low-utilization nodes matching a label selector, to validate cluster resilience under node churn (e.g. spot reclamation). |

## How it works

### ParseableCluster

Each `ParseableCluster` is composed of `nodes` (logical groups by `type: ingestor | query | prism`), `k8sConfig` (image, resources, volumes, services, affinity, etc.), `parseableConfig` (env secret + CLI args), and a `deploymentOrder`. On reconcile ([internal/parseablecluster_controller/reconciler.go](internal/parseablecluster_controller/reconciler.go)) the controller builds the desired StatefulSet/Service objects ([pkg/objects/objects.go](pkg/objects/objects.go)), diffs them via a hash annotation, and rolls node groups out sequentially in `deploymentOrder` on upgrades. A `deletepvc.finalizers.parseable.com` finalizer ensures StatefulSets are removed before their PVCs on delete. If autoscaling is active for a node group, the base controller defers to the autoscaler instead of overwriting replica counts.

**Suspend / resume**: annotate with `parseable.com/workspace=suspend` (or `suspend-ingestor` / `suspend-querier`) to scale matching workloads to 0, and `resume` / `resume-ingestor` / `resume-querier` to restore them. See [Suspending/resuming a workspace](#suspendingresuming-a-workspace) below for the full reference.

**Volume expansion**: growing a `volumeClaimTemplate` size is detected on reconcile ([internal/parseablecluster_controller/volume_expansion.go](internal/parseablecluster_controller/volume_expansion.go)) and applied via PVC patch + orphan-delete/recreate of the StatefulSet, if the `StorageClass` allows expansion. Shrinking is rejected.

### ParseableClusterAutoscaler *(Alpha)*

Targets a `ParseableCluster` (`scaleTargetRef`) with per-node-type `scalingConfig` (`minReplicas`/`maxReplicas`, CPU `threshold`, scale-up/down step policies). Rather than resizing a StatefulSet in place, it adds/removes whole parallel StatefulSets (see [pkg/scaler](pkg/scaler)): it reads node CPU usage from `metrics.k8s.io`, decides scale-up/down/none, and on scale-up clones the latest matching StatefulSet under a new name, waiting for it to become available before recording it in `status.ingestorIndex`. Setting `spec.stop: true` halts the autoscaler and hands control back to `ParseableCluster`. The state machine lives in [internal/parseableclusterautoscaler_controller/state_machine.go](internal/parseableclusterautoscaler_controller/state_machine.go).

### ParseableClusterChaos *(Alpha)*

Selects nodes by `nodeSelectorLabels` and, for every matching node older than `age` hours with CPU usage below `cpuUsage`%, cordons it, evicts `component: ingestor` pods on it, and deletes it — see [internal/parseablecluster­chaos_controller](internal/parseableclusterchaos_controller).

> **Note:** this deletes real Kubernetes nodes. Only point it at node pools you intend to churn (e.g. a dedicated spot/test node group), never at a production control surface without a matching safety net.

## Repository layout

```
api/v1/                                  # CRD Go types
cmd/main.go                              # controller-manager entrypoint
internal/
  parseablecluster_controller/           # ParseableCluster reconciler
  parseableclusterautoscaler_controller/ # ParseableClusterAutoscaler reconciler (alpha)
  parseableclusterchaos_controller/      # ParseableClusterChaos reconciler (alpha)
pkg/
  objects/                               # builds StatefulSet/Service objects from CR specs
  scaler/                                # scaling decision + StatefulSet add/remove
  utils/                                 # generic patch/status helpers
config/
  crd/bases/                             # generated CRDs
```

## Prerequisites

- A Kubernetes cluster (kubectl configured) — go.mod targets client libraries for k8s 1.30.
- An S3-compatible object store (MinIO, AWS S3, DO Spaces, GCS, etc.) for Parseable's storage backend.
- [metrics-server](https://github.com/kubernetes-sigs/metrics-server) if using `ParseableClusterAutoscaler` or `ParseableClusterChaos` (both read `metrics.k8s.io`).
- Go 1.25+ (and Docker, only if building the operator image yourself).

## Installing the operator

The repo currently ships only the generated CRDs ([config/crd/bases](config/crd/bases)) and the controller source — there is no packaged Helm chart or kustomize deployment overlay checked in yet. To run the operator:

```bash
# apply the CRDs
kubectl apply -f config/crd/bases

# build and run the controller-manager
make build
./bin/manager   # or `make run` to run against your current kubeconfig without building a binary
```

To run it in-cluster, build and push an image (see [Building the operator image](#building-the-operator-image)) and deploy it with a Deployment/RBAC of your own, granting it access to the three CRDs plus `metrics.k8s.io` (if autoscaling/chaos are used) and `apps`/`core` resources it manages (StatefulSets, Deployments, Services, PVCs).

## Deploying a Parseable cluster

1. **Provision object storage** — for local/dev, MinIO works well; for production point at S3/GCS/etc.
2. **Create the Parseable env secret** (object-store credentials + config) referenced by `spec.parseableConfig[].secretName`:
   ```bash
   kubectl create ns pbc-1
   kubectl create secret generic parseable-env-secret --from-env-file=<your-env-file> -n pbc-1
   ```
3. **Apply a `ParseableCluster` CR** — hand-write one against the schema in [config/crd/bases/parseable.com_parseableclusters.yaml](config/crd/bases/parseable.com_parseableclusters.yaml) and the field reference below.
4. **(Optional, alpha) Enable autoscaling** — install `metrics-server`, then apply a `ParseableClusterAutoscaler` CR targeting it via `spec.scaleTargetRef`.
5. **(Optional, alpha) Enable chaos testing** of node churn with a `ParseableClusterChaos` CR.

### Suspending/resuming a workspace

The Parseable operator supports suspending and resuming workspaces using the `parseable.com/workspace` annotation on a `ParseableCluster`.

**Full workspace suspension** — suspend all components (query, ingestor, prism):

```bash
kubectl annotate parseablecluster <name> -n <namespace> parseable.com/workspace=suspend
```

Resume all components:

```bash
kubectl annotate parseablecluster <name> -n <namespace> parseable.com/workspace=resume --overwrite
```

**Ingestor-only suspension** — suspend only ingestor nodes while keeping query and prism running:

```bash
kubectl annotate parseablecluster <name> -n <namespace> parseable.com/workspace=suspend-ingestor
```

Resume only ingestor nodes:

```bash
kubectl annotate parseablecluster <name> -n <namespace> parseable.com/workspace=resume-ingestor --overwrite
```

**Querier-only suspension** — suspend only querier nodes while keeping ingestor and prism running:

```bash
kubectl annotate parseablecluster <name> -n <namespace> parseable.com/workspace=suspend-querier
```

Resume only querier nodes:

```bash
kubectl annotate parseablecluster <name> -n <namespace> parseable.com/workspace=resume-querier --overwrite
```

Always resume by setting the annotation to the matching `resume*` value shown above, not by deleting the `parseable.com/workspace` annotation outright. The controller only restores replicas when it sees an explicit `resume` / `resume-ingestor` / `resume-querier` value; simply removing the annotation does not reliably trigger a resume, since the normal reconcile loop skips re-applying a StatefulSet's replica count when the CR spec itself hasn't changed.

**Implementation details:**

- **Full suspension (`suspend`)**: Scales all StatefulSets/Deployments to 0 and stores each object's original replica count in a `parseable.com/original-replicas` annotation on that object.
- **Ingestor suspension (`suspend-ingestor`)**: Only scales down components with "ingestor" in their name.
- **Querier suspension (`suspend-querier`)**: Only scales down StatefulSets whose name contains `parseable-query`.
- **Resume (`resume`)**: Restores components to the replica count stored in their `parseable.com/original-replicas` annotation, then removes it.
- **Resume (`resume-ingestor` / `resume-querier`)**: Restores components to the replica count from `spec.nodes[].replicas` in the `ParseableCluster` CR (no per-object stored-state annotation is used for these).

## Configuration reference

### ParseableCluster fields

| Field | Type | Description |
|---|---|---|
| `spec.deploymentOrder` | `[]string` | Node `type`s in rollout order. |
| `spec.nodes[].type` | `ingestor \| query \| prism` | Logical role of the node group. |
| `spec.nodes[].kind` | `string` | Workload kind (`statefulset` supported). |
| `spec.nodes[].replicas` | `*int32` | Desired replica count (ignored while autoscaling owns this node group). |
| `spec.nodes[].k8sConfig` / `parseableConfig` | `string` | Name references into `spec.k8sConfig[]` / `spec.parseableConfig[]`. |
| `spec.k8sConfig[]` | — | Image, resources, probes, volumes, services, affinity, tolerations, node selector, image pull secrets, service account, init containers — see [api/v1/common_types.go](api/v1/common_types.go). |
| `spec.parseableConfig[]` | — | `secretName` (env secret to mount) and `cliArgs` (Parseable CLI flags, e.g. `s3-store`). |
| `status.ingestorIndex` | `map[string]string` | Maps a configured StatefulSet name to its current (possibly autoscaler-renamed) StatefulSet name. |
| `status.enableAutoscaling` / `isScaling` | `bool` | Set by the autoscaler controller; gates whether the base controller reconciles a node group's replica count. |

### ParseableClusterAutoscaler fields *(Alpha)*

| Field | Type | Description |
|---|---|---|
| `spec.stop` | `bool` | Disables the autoscaler and returns control to `ParseableCluster`. |
| `spec.scaleTargetRef` | `{apiVersion, kind, name}` | The `ParseableCluster` to scale. |
| `spec.scalingConfig[].nodeType` | `NodeType` | Node type this policy applies to. |
| `spec.scalingConfig[].selectorLabels` | `map[string]string` | Labels used to find the StatefulSets to scale. |
| `spec.scalingConfig[].minReplicas` / `maxReplicas` | `int32` | Scaling bounds. |
| `spec.scalingConfig[].threshold` | `float64` | CPU usage % that triggers scale up/down. |
| `spec.scalingConfig[].behavior.scaleUp/scaleDown.policies[]` | `{type: pods, value}` | Replicas added/removed per scaling step. |
| `spec.scalingConfig[].stabilizationWindowSeconds` | `int64` | Cooldown between scaling actions. |

### ParseableClusterChaos fields *(Alpha)*

| Field | Type | Description |
|---|---|---|
| `spec.nodeSelectorLabels` | `map[string]string` | Labels selecting candidate nodes. |
| `spec.age` | `int64` | Minimum node age in hours before it's eligible for chaos. |
| `spec.cpuUsage` | `float64` | Maximum CPU usage % for a node to still be considered idle/eligible. |

## Environment variables (operator)

| Variable | Default | Purpose |
|---|---|---|
| `PB_RECONCILE_WAIT` | `10s` | Requeue interval for the `ParseableCluster` controller. |
| `PB_AUTOSCALER_RECONCILE_WAIT` | `20s` | Requeue interval for the `ParseableClusterAutoscaler` controller. |
| `PB_CHAOS_RECONCILE_WAIT` | `20s` | Requeue interval for the `ParseableClusterChaos` controller. |
| `DENY_LIST` | *(none)* | Comma-separated namespaces the `ParseableCluster` controller should never reconcile. |

## Development

```bash
make manifests generate   # regenerate CRDs / DeepCopy code from Go types after editing api/v1
make fmt vet               # go fmt / go vet
make lint                  # golangci-lint
make test                  # envtest-based controller tests
make build                 # build the manager binary
make run                   # run the controller locally against your current kubeconfig
make docker-build docker-push IMG=<registry>/parseable-operator:<tag>
```

CRD/RBAC manifests are generated via [controller-gen](https://book.kubebuilder.io/reference/controller-gen) from `+kubebuilder` markers in `api/v1/*_types.go`. Controller tests live alongside each controller package and use [envtest](https://book.kubebuilder.io/reference/envtest) + Ginkgo/Gomega — run with `make test`.

## Building the operator image

```bash
docker build -t <registry>/parseable-operator:<tag> .
```

Multi-stage build (`golang:1.25.10` → `gcr.io/distroless/static:nonroot`), runs as non-root, only copies in the Go sources needed for the manager binary.

## License

Parseable Operator is licensed under the [GNU Affero General Public License v3.0](LICENSE).
