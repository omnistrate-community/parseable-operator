# Parseable on Omnistrate (BYOC, AWS)

Distributes Parseable Enterprise into customers' own AWS accounts. Omnistrate
provisions an EKS deployment cell per customer account; each instance is a
`ParseableCluster` CR in its own namespace, backed by a dedicated S3 bucket in
the customer's account.

| File | Purpose |
|---|---|
| `spec-byoc.yaml` | ServicePlanSpec: Terraform storage resource + operator CR resource with create/modify/stop/start/delete workflows |
| `terraform/aws/` | Per-instance S3 bucket, bucket-scoped IAM user/access key, cluster secret |
| `cell-amenities-aws.yaml` | Installs the operator chart once per deployment cell |

## 1. Publish the operator image and chart

Tagging a release builds `quay.io/parseablehq/parseable-operator:<tag>`
(amd64 + arm64) and pushes the chart to `oci://quay.io/parseablehq/charts`:

```bash
git tag v0.1.0 && git push origin v0.1.0
```

The chart version is the tag without `v`; keep `ChartVersion` in
`cell-amenities-aws.yaml` in sync. Make sure the `charts` repository on Quay is
public, or that cells can pull it.

## 2. Install the operator on deployment cells

```bash
omnistrate-ctl login
omnistrate-ctl deployment-cell generate-config-template --cloud aws > cell-aws.yaml
# merge the customAmenities entry from cell-amenities-aws.yaml into cell-aws.yaml
omnistrate-ctl deployment-cell update-config-template --environment GLOBAL --cloud aws -f cell-aws.yaml
```

New cells pick this up automatically. Cells that already exist need
`update-config-template --id <cell-id> --sync-with-template` followed by
`apply-pending-changes -i <cell-id> --force`. A CR applied before the amenity
fails with `no matches for kind ParseableCluster`.

## 3. Build the plan

`byoaDeployment` points at the Control Plane account config
(AWS Demo Account, 541226919566). From this directory:

```bash
omctl docs validate --file spec-byoc.yaml
omnistrate-ctl build -f spec-byoc.yaml --spec-type ServicePlanSpec \
  --product-name "Parseable" --environment Dev --environment-type Dev \
  --release-as-preferred
```

`artifactRelativePath: terraform/aws` is resolved relative to the working
directory, so run the build from `omnistrate/`.

## 4. Onboard a customer account and deploy

```bash
omnistrate-ctl account customer create ...   # prints the CloudFormation onboarding link
omnistrate-ctl instance create --service Parseable --plan "Parseable BYOC" \
  --environment Dev --cloud-provider aws --region us-east-1 \
  --resource parseableCluster --customer-account-id <id> \
  --param '{"adminPassword":"...","licenseData":"<base64 json>","licenseSignature":"<base64 sig>"}'
```

The `CUSTOM_TERRAFORM_POLICY` feature adds S3/IAM permissions (scoped to
`parseable-*` names) to the customer onboarding stack. Changing it means
existing customers must update their CloudFormation stack.

## Endpoints

| Endpoint | Host | Backend |
|---|---|---|
| `ui` (primary) | `$sys.network.externalClusterEndpoint` | `parseable-prism-service` |
| `ingest` | `ingest-<externalClusterEndpoint>` | `parseable-ingestor-service` |

Both are nginx Ingresses created by the workflows, terminated with the
per-instance `google-public-ca-tls` certificate.

## Lifecycle

| Verb | What happens | Gate |
|---|---|---|
| create / modify | env + license secrets, `ParseableCluster`, two Ingresses | `status.phase == Ready` |
| stop | annotate `parseable.com/workspace=suspend` (all StatefulSets → 0) | `status.phase == Suspended` |
| start | annotate `parseable.com/workspace=resume` (restores stored replicas) | `status.phase == Ready` |
| delete | Ingresses, CR (finalizer removes StatefulSets then PVCs), secrets; Terraform then destroys the bucket **and its data** | — |

## Not yet verified on Omnistrate

Validated locally against a real API server (kind) with a stub Parseable
image: spec renders, the CR applies with no field drift, and
create → modify → stop → start → delete behave as described. Still to confirm
on a live BYOC cell:

- `{{ $parseableStorage.out.* }}` resolving inside operator workflow arguments
- the cell's nginx ingress class / `google-public-ca-tls` secret on BYOC cells
- the operator amenity installing from the OCI chart repo
- a real Parseable Enterprise image + license reaching `Ready`
