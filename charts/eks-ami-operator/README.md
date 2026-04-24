# eks-ami-operator

A Kubernetes operator for automating EKS managed node group AMI upgrades

[![Artifact Hub](https://img.shields.io/endpoint?url=https://artifacthub.io/badge/repository/eks-ami-operator)](https://artifacthub.io/packages/helm/eks-ami-operator/eks-ami-operator)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://github.com/harshvijaythakkar/eks-ami-operator/blob/main/LICENSE)

## Chart info

| Field | Value |
|---|---|
| Chart version | `0.1.0` |
| App version | `v0.1.0` |
| Kubernetes | `>=1.16.0-0` |
| Helm | `v3.0+` |

## Prerequisites

- Kubernetes `>=1.16.0-0`
- Helm `v3.0+`
- AWS credentials with the following permissions:
  - `eks:DescribeCluster`
  - `eks:DescribeNodegroup`
  - `eks:UpdateNodegroupVersion`
  - `eks:DescribeUpdate`
  - `ssm:GetParameter` on `arn:aws:ssm:<region>:*:parameter/aws/service/eks/*`
  - `ec2:DescribeRegions`

IRSA (IAM Roles for Service Accounts) is strongly recommended over static credentials.

## Installation

Both the container image and Helm chart are published to GHCR. No separate registry setup is needed.

| Artifact | Location |
|---|---|
| Container image | `ghcr.io/harshvijaythakkar/eks-ami-operator:v0.1.0` |
| Helm chart (OCI) | `oci://ghcr.io/harshvijaythakkar/charts/eks-ami-operator` |

### Basic install

```bash
helm install eks-ami-operator \
  oci://ghcr.io/harshvijaythakkar/charts/eks-ami-operator \
  --version v0.1.0 \
  --namespace eks-ami-operator-system \
  --create-namespace
```

### With IRSA (recommended for production)

```bash
helm install eks-ami-operator \
  oci://ghcr.io/harshvijaythakkar/charts/eks-ami-operator \
  --version v0.1.0 \
  --namespace eks-ami-operator-system \
  --create-namespace \
  --set serviceAccount.annotations."eks\.amazonaws\.com/role-arn"=arn:aws:iam::<account-id>:role/<irsa-role-name>
```

### With Prometheus ServiceMonitor

```bash
helm install eks-ami-operator \
  oci://ghcr.io/harshvijaythakkar/charts/eks-ami-operator \
  --version v0.1.0 \
  --namespace eks-ami-operator-system \
  --create-namespace \
  --set serviceMonitor.enabled=true \
  --set serviceMonitor.labels.release=prometheus-stack
```

### Upgrade

```bash
helm upgrade eks-ami-operator \
  oci://ghcr.io/harshvijaythakkar/charts/eks-ami-operator \
  --version <new-version> \
  --namespace eks-ami-operator-system \
  --reuse-values
```

### Uninstall

```bash
helm uninstall eks-ami-operator --namespace eks-ami-operator-system
```

> Note: The CRD is installed separately via the `crds/` directory and is not removed on uninstall.
> To remove the CRD: `kubectl delete crd nodegroupupgradepolicies.eks.aws.harsh.dev`

## Quick start

Once the operator is running, create a `NodeGroupUpgradePolicy` to manage a node group:

```yaml
apiVersion: eks.aws.harsh.dev/v1alpha1
kind: NodeGroupUpgradePolicy
metadata:
  name: my-nodegroup
  namespace: eks-ami-operator-system
spec:
  region: us-east-1
  clusterName: my-eks-cluster
  nodeGroupName: my-managed-nodegroup
  checkInterval: 24h
  autoUpgrade: false   # set to true to enable automatic upgrades
```

```bash
kubectl apply -f policy.yaml
kubectl get nodegroupupgradepolicies -n eks-ami-operator-system
kubectl describe nodegroupupgradepolicy my-nodegroup -n eks-ami-operator-system
```

## Webhook

The admission webhook registers two handlers:

- **Mutating** (`/mutate-eks-aws-harsh-dev-v1alpha1-nodegroupupgradepolicy`) — applies defaults (`checkInterval: 24h`, `scheduleTimezone: UTC`)
- **Validating** (`/validate-eks-aws-harsh-dev-v1alpha1-nodegroupupgradepolicy`) — validates required fields, cron expressions, IANA timezones, and blocks changes to immutable fields

The API server requires HTTPS for all webhook calls. The chart supports two TLS modes:

### Self-signed (default, no cert-manager required)

The chart generates a self-signed CA and certificate via Helm and stores them in a Secret. On upgrades, the existing Secret is reused so the certificate is not rotated unnecessarily.

```bash
helm install eks-ami-operator ./charts/eks-ami-operator \
  --set webhook.enabled=true   # default
```

### cert-manager

If cert-manager is installed, set `webhook.certManager.enabled=true`. The chart creates a self-signed `Issuer` and a `Certificate`. cert-manager provisions the Secret and injects the CA bundle into both webhook configurations automatically.

```bash
helm install eks-ami-operator ./charts/eks-ami-operator \
  --set webhook.certManager.enabled=true
```

### Disable webhook (local development)

```bash
helm install eks-ami-operator ./charts/eks-ami-operator \
  --set webhook.enabled=false
```

When disabled, `ENABLE_WEBHOOKS=false` is set on the container and no webhook resources are created. Field defaulting and validation will not be enforced on admission.

## Metrics

When `metrics.enabled=true` (default), the operator exposes Prometheus metrics on port `8080` at `/metrics`.

To scrape metrics with the Prometheus operator, enable the ServiceMonitor:

```bash
--set serviceMonitor.enabled=true
--set serviceMonitor.labels.release=<your-prometheus-release-label>
```

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity rules for the operator Pod. |
| commonAnnotations | object | `{}` | Annotations added to every resource created by this chart. Users can add any annotations here — cost tracking, team ownership, compliance tags, etc. Example:   cost-center: platform-team   owner: ops@example.com |
| extraEnv | list | `[]` | Extra environment variables injected into the manager container. |
| extraVolumeMounts | list | `[]` | Extra volume mounts added to the manager container. |
| extraVolumes | list | `[]` | Extra volumes added to the Pod. |
| fullnameOverride | string | `""` | Override the full release name. |
| image.pullPolicy | string | `"IfNotPresent"` | Image pull policy. |
| image.repository | string | `"ghcr.io/harshvijaythakkar/eks-ami-operator"` | Container image repository. |
| image.tag | string | `""` | Container image tag. Defaults to the chart appVersion when empty. |
| imagePullSecrets | list | [] | Image pull secrets for private registries. |
| livenessProbe | object | `{"failureThreshold":3,"httpGet":{"path":"/healthz","port":8081},"initialDelaySeconds":15,"periodSeconds":20,"timeoutSeconds":1}` | Liveness probe configuration. |
| manager.enableHTTP2 | bool | `false` | Enable HTTP/2 for the metrics and webhook servers. Disabled by default to avoid HTTP/2 stream cancellation vulnerabilities. |
| manager.healthProbeBindAddress | string | `":8081"` | Address the health probe endpoint binds to. |
| manager.leaderElection | bool | `true` | Enable leader election. Required when running more than one replica. |
| manager.metricsBindAddress | string | `":8080"` | Address the metrics endpoint binds to. |
| manager.metricsSecure | bool | `false` | Serve metrics over TLS. The binary default is true (HTTPS). Set to false to use plain HTTP on port 8080. When set to true, also set manager.metricsBindAddress to ":8443" and metrics.port to 8443, and ensure rbac.create=true so the metrics-auth ClusterRole is created. |
| metrics.enabled | bool | `true` | Expose the /metrics endpoint. |
| metrics.port | int | `8080` | Metrics server port. |
| nameOverride | string | `""` | Override the chart name. |
| nodeSelector | object | `{}` | Node selector for the operator Pod. |
| podAnnotations | object | `{}` | Annotations added to the operator Pod. |
| podLabels | object | `{}` | Labels added to the operator Pod. |
| podSecurityContext | object | `{"runAsNonRoot":true,"seccompProfile":{"type":"RuntimeDefault"}}` | Pod-level security context. |
| priorityClassName | string | `""` | Priority class name for the operator Pod. |
| rbac.create | bool | `true` | Create RBAC resources (ClusterRole, ClusterRoleBinding, Role, RoleBinding). |
| readinessProbe | object | `{"failureThreshold":3,"httpGet":{"path":"/readyz","port":8081},"initialDelaySeconds":5,"periodSeconds":10,"timeoutSeconds":1}` | Readiness probe configuration. |
| replicaCount | int | `1` | Number of replicas. Keep at 1; leader election handles HA. |
| resources | object | `{"limits":{"cpu":"500m","memory":"128Mi"},"requests":{"cpu":"10m","memory":"64Mi"}}` | Resource requests and limits for the manager container. |
| securityContext | object | `{"allowPrivilegeEscalation":false,"capabilities":{"drop":["ALL"]},"readOnlyRootFilesystem":true,"runAsNonRoot":true}` | Container-level security context. |
| serviceAccount.annotations | object | `{}` | Annotations on the ServiceAccount. Commonly used to attach an IRSA role ARN:   eks.amazonaws.com/role-arn: arn:aws:iam::<account-id>:role/<role-name> |
| serviceAccount.automountServiceAccountToken | bool | `false` | Disable automatic token mounting (recommended). |
| serviceAccount.create | bool | `true` | Create a ServiceAccount for the operator. |
| serviceAccount.name | string | `""` | ServiceAccount name. Defaults to the release fullname when empty. |
| serviceMonitor.enabled | bool | `false` | Create a Prometheus ServiceMonitor. |
| serviceMonitor.interval | string | `"30s"` | Scrape interval. |
| serviceMonitor.labels | object | `{}` | Additional labels on the ServiceMonitor. Must match the label selector of your Prometheus operator instance. Example: { release: prometheus-stack } |
| serviceMonitor.namespace | string | `""` | Namespace for the ServiceMonitor. Defaults to the release namespace. |
| serviceMonitor.scrapeTimeout | string | `"10s"` | Scrape timeout. |
| tolerations | list | `[]` | Tolerations for the operator Pod. |
| webhook.certManager.enabled | bool | `false` | Use cert-manager to provision and rotate webhook TLS certificates. Requires cert-manager to be installed in the cluster. When true, a self-signed Issuer and Certificate are created automatically, and cert-manager injects the CA bundle into the webhook configurations. When false, the chart generates a self-signed certificate via Helm and stores it in a Secret. |
| webhook.enabled | bool | `true` | Enable the admission webhook (defaulting + validation). Set to false for local development or environments without cert-manager. |
| webhook.failurePolicy | string | `"Fail"` | Failure policy for the webhook. Fail means the request is rejected if the webhook is unavailable. Use Ignore for a more lenient policy during upgrades. |
| webhook.port | int | `9443` | Webhook server port. |

## Maintainers

| Name | Email | Url |
| ---- | ------ | --- |
| Harsh Vijay Thakkar |  | <https://github.com/harshvijaythakkar> |

## Source Code

* <https://github.com/harshvijaythakkar/eks-ami-operator>

## License

Apache License 2.0. See [LICENSE](https://github.com/harshvijaythakkar/eks-ami-operator/blob/main/LICENSE).