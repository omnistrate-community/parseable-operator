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

**Suspend / resume**: annotate with `parseable.com/workspace=suspend` (or `suspend-ingestor` / `suspend-querier`) to scale matching workloads to 0, and `resume*` (or remove the annotation) to restore them. See [docs/suspend-resume-usage.md](docs/suspend-resume-usage.md) for the full reference.

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
  crd/bases/, samples/                   # generated CRDs + example CRs
helm/
  parseable-operator/                    # chart to install the Parseable operator
  parseable-cr/                          # chart to install a ParseableCluster via templated CR
docs/                                    # usage guides
```

## Prerequisites

- A Kubernetes cluster (kubectl configured) — go.mod targets client libraries for k8s 1.30.
- [Helm](https://helm.sh/) 3.x
- An S3-compatible object store (MinIO, AWS S3, DO Spaces, GCS, etc.) for Parseable's storage backend.
- [metrics-server](https://github.com/kubernetes-sigs/metrics-server) if using `ParseableClusterAutoscaler` or `ParseableClusterChaos` (both read `metrics.k8s.io`).
- Go 1.25+ and Docker, only if building the operator image yourself.

## Installing the operator

```bash
kubectl create ns parseable-operator
cd helm/parseable-operator
helm upgrade --install parseable-operator . -n parseable-operator
```

Environment-specific value overrides are in `values-staging.yaml` / `values-production.yaml` (set `image.repository`/`tag` to your own operator image).

## Deploying a Parseable cluster

1. **Provision object storage** — for local/dev, MinIO works well; for production point at S3/GCS/etc.
2. **Create the Parseable env secret** (object-store credentials + config), e.g. from [config/samples/parseable-env-secret](config/samples/parseable-env-secret):
   ```bash
   kubectl create ns pbc-1
   kubectl create secret generic parseable-env-secret --from-env-file=config/samples/parseable-env-secret -n pbc-1
   ```
3. **Apply a `ParseableCluster` CR**, e.g. [config/samples/pb-cr-sample.yaml](config/samples/pb-cr-sample.yaml), or use the [helm/parseable-cr](helm/parseable-cr) chart (also supports `database.connectionString` and per-cloud overlays under `cloud/` / `components/`).
4. **(Optional, alpha) Enable autoscaling** — install `metrics-server`, then apply an autoscaler CR such as [config/samples/pb-cr-scaler.yaml](config/samples/pb-cr-scaler.yaml).
5. **(Optional, alpha) Enable chaos testing** of node churn with [config/samples/pb-chaos.yaml](config/samples/pb-chaos.yaml).

### Suspending/resuming a workspace

```bash
kubectl annotate parseablecluster <name> -n <namespace> parseable.com/workspace=suspend
kubectl annotate parseablecluster <name> -n <namespace> parseable.com/workspace=resume --overwrite
```

See [docs/suspend-resume-usage.md](docs/suspend-resume-usage.md) for ingestor-only/querier-only suspension and the full annotation reference.

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
| `WATCH_NAMESPACE` | *(all namespaces)* | Restrict the operator to one or more comma-separated namespaces. |
| `DENY_LIST` | *(chart-defined)* | Namespaces the operator should never reconcile in. |

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
