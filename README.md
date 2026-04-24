# eks-ami-operator

<p align="center">
  <a href="https://github.com/harshvijaythakkar/eks-ami-operator/actions/workflows/lint.yml">
    <img src="https://github.com/harshvijaythakkar/eks-ami-operator/actions/workflows/lint.yml/badge.svg" alt="Lint">
  </a>
  <a href="https://github.com/harshvijaythakkar/eks-ami-operator/actions/workflows/test.yml">
    <img src="https://github.com/harshvijaythakkar/eks-ami-operator/actions/workflows/test.yml/badge.svg" alt="Tests">
  </a>
  <a href="https://github.com/harshvijaythakkar/eks-ami-operator/actions/workflows/security.yml">
    <img src="https://github.com/harshvijaythakkar/eks-ami-operator/actions/workflows/security.yml/badge.svg" alt="Security">
  </a>
  <a href="https://github.com/harshvijaythakkar/eks-ami-operator/releases/latest">
    <img src="https://img.shields.io/github/v/release/harshvijaythakkar/eks-ami-operator" alt="Latest Release">
  </a>
  <a href="https://goreportcard.com/report/github.com/harshvijaythakkar/eks-ami-operator">
    <img src="https://goreportcard.com/badge/github.com/harshvijaythakkar/eks-ami-operator" alt="Go Report Card">
  </a>
  <a href="https://github.com/harshvijaythakkar/eks-ami-operator/blob/main/LICENSE">
    <img src="https://img.shields.io/badge/License-Apache_2.0-blue.svg" alt="License">
  </a>
  <a href="https://go.dev/doc/go1.24">
    <img src="https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go" alt="Go Version">
  </a>
  <a href="https://kubernetes.io/">
    <img src="https://img.shields.io/badge/Kubernetes-1.11+-326CE5?logo=kubernetes&logoColor=white" alt="Kubernetes">
  </a>
  <img src="https://img.shields.io/badge/kubebuilder-v3-blueviolet" alt="kubebuilder">
</p>

<p align="center">
  A Kubernetes operator for automating EKS managed node group AMI upgrades.
</p>

---

## Table of contents

- [Overview](#overview)
- [Features](#features)
- [How it works](#how-it-works)
- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Quick start](#quick-start)
- [CRD reference](#crd-reference)
  - [Spec fields](#spec-fields)
  - [Status fields](#status-fields)
- [Scheduling semantics](#scheduling-semantics)
- [Upgrade lifecycle](#upgrade-lifecycle)
- [Status & conditions](#status--conditions)
- [Metrics](#metrics)
- [Alerting](#alerting)
- [Security & RBAC](#security--rbac)
- [Development](#development)
- [Architecture](#architecture)
- [Troubleshooting](#troubleshooting)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License](#license)

---

## Overview

`eks-ami-operator` keeps your EKS managed node groups on the latest recommended AMIs published by AWS via SSM Parameter Store.

What it does:

1. Resolves the latest EKS-optimized AMI release version from AWS SSM Parameter Store, keyed by EKS cluster version and AMI type.
2. Compares it against the node group's current release version.
3. Optionally calls `UpdateNodegroupVersion` when the node group is outdated and `autoUpgrade: true`.
4. Tracks the EKS update to completion by polling `DescribeUpdate`.
5. Exposes Prometheus metrics and sets Kubernetes status conditions.
6. Schedules reconciles via cron (with timezone) or interval, with jitter to avoid thundering herds.
7. Cleans up safely using Kubernetes finalizers.

---

## Features

| Feature | Details |
|---|---|
| Dynamic AMI lookup | Resolves latest AMI via AWS SSM by AMI type and EKS version |
| AutoUpgrade | Automated upgrades via `UpdateNodegroupVersion` |
| Update tracking | Polls `DescribeUpdate` until terminal state (Successful / Failed / Cancelled) |
| Cron scheduling | 5-field cron with IANA timezone support (default UTC) |
| Interval scheduling | Go duration fallback with ±10% jitter |
| Scheduling precedence | `paused` → `startAfter` → `scheduleCron` → `checkInterval` |
| AL2 deprecation guard | Detects AL2 AMIs unsupported on EKS >= 1.33 and surfaces a clear condition |
| Prometheus metrics | Compliance, attempts, lifecycle, in-flight timer, next run |
| Status conditions | `AMICompliance`, `UpgradeInProgress` Kubernetes conditions |
| Exponential backoff | All AWS API calls use jittered exponential backoff |
| Finalizers | Safe cleanup on CR deletion |
| Admission webhook | Defaulting + validation (immutable fields, cron/timezone/region validation) |
| Leader election | Safe multi-replica deployment |
| Helm chart | Helm chart with ServiceMonitor support |

---

## How it works

```
NodeGroupUpgradePolicy CR
         │
         ▼
┌─────────────────────────────────────────────────────────────┐
│                     Reconcile loop                          │
│                                                             │
│  1. Finalizer add/remove                                    │
│  2. Guard checks: paused? startAfter? cooldown?             │
│  3. Schedule gate: cron/interval due?                       │
│     └─ Bypass if upgrade is InProgress (poll mode)         │
│  4. AWS: DescribeNodegroup → current release version        │
│  5. AWS: DescribeCluster  → EKS version                    │
│  6. AWS: SSM GetParameter → latest release version         │
│  7. ProcessUpgrade:                                         │
│     ├─ InProgress + UpdateID → DescribeUpdate (poll)       │
│     ├─ Compliant             → Succeeded                   │
│     ├─ Outdated + autoUpgrade=false → Skipped              │
│     └─ Outdated + autoUpgrade=true  → UpdateNodegroupVersion│
│  8. Requeue: 2min (in-flight) or cron/interval (terminal)  │
└─────────────────────────────────────────────────────────────┘
```

---

## Prerequisites

| Requirement | Version |
|---|---|
| Go | `v1.24.0+` |
| Docker | `v17.03+` |
| kubectl | `v1.11.3+` |
| Kubernetes cluster | `v1.11.3+` |
| Helm | `v3.0+` |
| AWS credentials | IRSA recommended |

### Required AWS IAM permissions

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": ["eks:DescribeCluster"],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": ["eks:DescribeNodegroup", "eks:UpdateNodegroupVersion", "eks:DescribeUpdate"],
      "Resource": "*"
    },
    {
      "Effect": "Allow",
      "Action": ["ssm:GetParameter"],
      "Resource": "arn:aws:ssm:<region>:*:parameter/aws/service/eks/*"
    },
    {
      "Effect": "Allow",
      "Action": ["ec2:DescribeRegions"],
      "Resource": "*"
    }
  ]
}
```

> Tip: Scope `eks:UpdateNodegroupVersion` to specific cluster/nodegroup ARNs in production.

---

## Installation

Both the container image and Helm chart are published to GHCR. No separate registry setup is needed.

| Artifact | Location |
|---|---|
| Container image | `ghcr.io/harshvijaythakkar/eks-ami-operator:<tag>` |
| Helm chart (OCI) | `oci://ghcr.io/harshvijaythakkar/charts/eks-ami-operator` |

### Basic install

```bash
helm install eks-ami-operator \
  oci://ghcr.io/harshvijaythakkar/charts/eks-ami-operator \
  --version <tag> \
  --namespace eks-ami-operator-system \
  --create-namespace
```

### With IRSA (recommended for production)

```bash
helm install eks-ami-operator \
  oci://ghcr.io/harshvijaythakkar/charts/eks-ami-operator \
  --version <tag> \
  --namespace eks-ami-operator-system \
  --create-namespace \
  --set serviceAccount.annotations."eks\.amazonaws\.com/role-arn"=arn:aws:iam::<account-id>:role/<irsa-role-name>
```

### With Prometheus ServiceMonitor

```bash
helm install eks-ami-operator \
  oci://ghcr.io/harshvijaythakkar/charts/eks-ami-operator \
  --version <tag> \
  --namespace eks-ami-operator-system \
  --create-namespace \
  --set serviceMonitor.enabled=true \
  --set serviceMonitor.labels.release=prometheus-stack
```

### Helm values reference

| Value | Default | Description |
|---|---|---|
| `image.repository` | `ghcr.io/harshvijaythakkar/eks-ami-operator` | Container image repository |
| `image.tag` | `latest` | Container image tag |
| `image.pullPolicy` | `IfNotPresent` | Image pull policy |
| `replicaCount` | `1` | Number of replicas |
| `serviceAccount.create` | `true` | Create a ServiceAccount |
| `serviceAccount.name` | `eks-ami-operator` | ServiceAccount name |
| `serviceAccount.annotations` | `{}` | Annotations (e.g., IRSA role ARN) |
| `rbac.create` | `true` | Create RBAC resources |
| `leaderElection.enabled` | `true` | Enable leader election |
| `resources.limits.cpu` | `200m` | CPU limit |
| `resources.limits.memory` | `256Mi` | Memory limit |
| `resources.requests.cpu` | `100m` | CPU request |
| `resources.requests.memory` | `128Mi` | Memory request |
| `metrics.enabled` | `true` | Enable metrics endpoint |
| `metrics.port` | `8080` | Metrics port |
| `serviceMonitor.enabled` | `true` | Create Prometheus ServiceMonitor |
| `serviceMonitor.interval` | `30s` | Scrape interval |
| `serviceMonitor.scrapeTimeout` | `10s` | Scrape timeout |
| `serviceMonitor.labels` | `{release: prometheus-stack}` | Labels for ServiceMonitor |
| `nodeSelector` | `{}` | Node selector |
| `tolerations` | `[]` | Tolerations |
| `affinity` | `{}` | Affinity rules |

---

## Quick start

### Interval-based, AutoUpgrade enabled

```yaml
# examples/nodegroupupgradepolicy_autoupgrade_true.yaml
apiVersion: eks.aws.harsh.dev/v1alpha1
kind: NodeGroupUpgradePolicy
metadata:
  name: my-nodegroup-policy
  namespace: eks-ami-operator-system
spec:
  region: us-west-2
  clusterName: my-eks-cluster
  nodeGroupName: my-managed-nodegroup
  checkInterval: 24h
  autoUpgrade: true
  paused: false
```

### Interval-based, AutoUpgrade disabled (observe only)

```yaml
# examples/nodegroupupgradepolicy_autoupgrade_false.yaml
apiVersion: eks.aws.harsh.dev/v1alpha1
kind: NodeGroupUpgradePolicy
metadata:
  name: my-nodegroup-policy-observe
  namespace: eks-ami-operator-system
spec:
  region: us-west-2
  clusterName: my-eks-cluster
  nodeGroupName: my-managed-nodegroup
  checkInterval: 12h
  autoUpgrade: false
  paused: false
```

### Cron-based scheduling with timezone

```yaml
# examples/nodegroupupgradepolicy_cron.yaml
apiVersion: eks.aws.harsh.dev/v1alpha1
kind: NodeGroupUpgradePolicy
metadata:
  name: my-nodegroup-policy-cron
  namespace: eks-ami-operator-system
spec:
  region: us-west-2
  clusterName: my-eks-cluster
  nodeGroupName: my-managed-nodegroup

  # Fallback if cron is omitted or invalid
  checkInterval: 24h

  # Run at 03:00 Mon-Fri in Asia/Kolkata
  scheduleCron: "0 3 * * 1-5"
  scheduleTimezone: "Asia/Kolkata"

  autoUpgrade: true
  paused: false
```

### Delayed start

```yaml
apiVersion: eks.aws.harsh.dev/v1alpha1
kind: NodeGroupUpgradePolicy
metadata:
  name: my-nodegroup-policy-delayed
  namespace: eks-ami-operator-system
spec:
  region: us-west-2
  clusterName: my-eks-cluster
  nodeGroupName: my-managed-nodegroup
  checkInterval: 24h
  startAfter: "2026-06-01T02:00:00Z"   # RFC3339; operator waits until this time
  autoUpgrade: true
  paused: false
```

### Apply and observe

```bash
kubectl apply -f examples/nodegroupupgradepolicy_autoupgrade_true.yaml

# List all policies
kubectl get nodegroupupgradepolicies -n eks-ami-operator-system

# Describe a policy (shows status, conditions, events)
kubectl describe nodegroupupgradepolicy my-nodegroup-policy -n eks-ami-operator-system

# Watch status
kubectl get nodegroupupgradepolicy my-nodegroup-policy -n eks-ami-operator-system -w

# Delete
kubectl delete -f examples/nodegroupupgradepolicy_autoupgrade_true.yaml
```

---

## CRD reference

### Spec fields

| Field | Type | Required | Default | Description |
|---|---|---|---|---|
| `clusterName` | string | yes | — | EKS cluster name. Immutable after creation. |
| `nodeGroupName` | string | yes | — | Managed node group name. Immutable after creation. |
| `region` | string | yes | — | AWS region. Immutable after creation. Validated against EC2 DescribeRegions on admission. |
| `autoUpgrade` | bool | — | `false` | If `true`, calls `UpdateNodegroupVersion` when the node group is outdated. |
| `paused` | bool | — | `false` | Suspend all reconciliation. No AWS calls are made. |
| `startAfter` | string (RFC3339) | — | — | Delay first run until this timestamp. |
| `checkInterval` | string (duration) | — | `24h` | Interval-based scheduling fallback (e.g., `6h`, `12h`, `24h`). Defaults to `24h` if both `scheduleCron` and `checkInterval` are omitted. |
| `scheduleCron` | string (5-field cron) | — | — | Cron expression for scheduling (e.g., `"0 2 * * *"`). Takes precedence over `checkInterval`. |
| `scheduleTimezone` | string (IANA) | — | `UTC` | Timezone for cron evaluation (e.g., `"Asia/Kolkata"`, `"America/New_York"`). Defaults to `UTC` if `scheduleCron` is set but timezone is omitted. |

### Status fields

| Field | Type | Description |
|---|---|---|
| `upgradeStatus` | string | Operator's lifecycle: `InProgress`, `Succeeded`, `Failed`, or `Skipped` |
| `updateID` | string | EKS UpdateID returned by `UpdateNodegroupVersion` |
| `updateStatus` | string | EKS's status for the update: `InProgress`, `Successful`, `Failed`, or `Cancelled` |
| `updateErrors[]` | []string | Error messages from EKS `DescribeUpdate` (format: `"<code>: <message>"`) |
| `currentAmi` | string | Current node group release version |
| `targetAmi` | string | Latest resolved release version from SSM |
| `lastChecked` | timestamp | Last time a compliance check ran |
| `lastUpgradeAttempt` | timestamp | Last time `UpdateNodegroupVersion` was called |
| `lastProgressCheck` | timestamp | Last time `DescribeUpdate` was polled |
| `lastScheduledTime` | timestamp | Last time the schedule was evaluated |
| `nextScheduledTime` | timestamp | Computed next scheduled run time |
| `conditions[]` | []Condition | Kubernetes conditions (see [Status & conditions](#status--conditions)) |

---

## Scheduling semantics

### Precedence (most restrictive first)

1. `paused: true` — no work, reconcile exits immediately.
2. `startAfter: <RFC3339>` — requeue until that moment.
3. `scheduleCron` + `scheduleTimezone` — cron-based scheduling, exact, no jitter.
4. `checkInterval` — interval-based scheduling, ±10% jitter applied.

### Defaults

| Scenario | Behavior |
|---|---|
| Both `scheduleCron` and `checkInterval` omitted | `checkInterval` defaults to `24h` |
| `scheduleCron` set, `scheduleTimezone` omitted | `scheduleTimezone` defaults to `UTC` |

### Jitter

A ±10% randomization is applied to interval-based requeues only. Cron schedules are always exact. This prevents multiple policies from reconciling at the same time.

### Cooldown

When using interval scheduling (no cron), a 6-hour minimum cooldown is enforced between upgrade attempts. This guard does not apply when using cron scheduling, since cron is the authoritative cadence.

### Common patterns

```yaml
# Every night at 02:00 UTC
scheduleCron: "0 2 * * *"
# scheduleTimezone omitted, defaults to UTC

# Weekdays at 03:00 IST
scheduleCron: "0 3 * * 1-5"
scheduleTimezone: "Asia/Kolkata"

# Every Sunday at 01:00 US Eastern
scheduleCron: "0 1 * * 0"
scheduleTimezone: "America/New_York"

# Every 12 hours (interval, no cron)
checkInterval: 12h
```

---

## Upgrade lifecycle

The operator tracks two separate status fields:

- `status.upgradeStatus` — the operator's view of the overall upgrade.
- `status.updateStatus` — EKS's status for the specific `updateID`.

Think of it this way: `upgradeStatus` is what the operator knows, `updateStatus` is what EKS reports.

### State transitions

| Scenario | `upgradeStatus` | `updateID` | `updateStatus` |
|---|---|---|---|
| Compliant (current == latest) | `Succeeded` | — | — |
| Outdated + `autoUpgrade: false` | `Skipped` | — | — |
| AL2 AMI unsupported (EKS >= 1.33) | `Skipped` | — | — |
| `UpdateNodegroupVersion` failed | `Failed` | — | — |
| Initiation succeeded | `InProgress` | set | `InProgress` |
| Polling: EKS says InProgress | `InProgress` | keep | `InProgress` |
| Polling: EKS says Successful | `Succeeded` | keep | `Successful` |
| Polling: EKS says Failed/Cancelled | `Failed` | keep | `Failed`/`Cancelled` |

While in-progress, the controller requeues every ~2 minutes to poll EKS. After a terminal state (Successful/Failed/Cancelled), it returns to the normal cron/interval cadence.

### Example status outputs

Upgrade initiated (outdated + `autoUpgrade: true`):

```yaml
status:
  upgradeStatus: InProgress
  updateID: 58efb0dc-09c6-3834-88be-e84c9112d5c5
  updateStatus: InProgress
  currentAmi: 1.29.3-20240322
  targetAmi: 1.29.6-20240531
  lastUpgradeAttempt: "2026-03-23T10:15:00Z"
  nextScheduledTime: "2026-03-24T10:15:00Z"
```

Terminal success:

```yaml
status:
  upgradeStatus: Succeeded
  updateID: 58efb0dc-09c6-3834-88be-e84c9112d5c5
  updateStatus: Successful
  currentAmi: 1.29.6-20240531
  targetAmi: 1.29.6-20240531
  lastProgressCheck: "2026-03-23T10:42:12Z"
```

Terminal failure:

```yaml
status:
  upgradeStatus: Failed
  updateID: 58efb0dc-09c6-3834-88be-e84c9112d5c5
  updateStatus: Failed
  updateErrors:
    - "PodEvictionFailure: Reached max retries while trying to evict pods from nodes in node group ..."
```

Compliant (no upgrade needed):

```yaml
status:
  upgradeStatus: Succeeded
  currentAmi: 1.29.6-20240531
  targetAmi: 1.29.6-20240531
  lastChecked: "2026-03-23T10:00:00Z"
  nextScheduledTime: "2026-03-24T10:00:00Z"
```

Skipped (autoUpgrade disabled):

```yaml
status:
  upgradeStatus: Skipped
  currentAmi: 1.29.3-20240322
  targetAmi: 1.29.6-20240531
  lastChecked: "2026-03-23T10:00:00Z"
```

Unsupported AMI (AL2 on EKS >= 1.33):

```yaml
status:
  upgradeStatus: Skipped
  conditions:
    - type: AMICompliance
      status: "False"
      reason: UnsupportedAMI
      message: "AL2 AMI not published for this EKS version; create a new node group on AL2023 and migrate."
```

---

## Status & conditions

The operator sets two condition types on `status.conditions`:

### AMICompliance

| Status | Reason | Meaning |
|---|---|---|
| `True` | `UpToDate` | Node group is using the latest AMI |
| `False` | `OutdatedAMI` | Node group is outdated |
| `False` | `UnsupportedAMI` | AL2 AMI not published for this EKS version |

### UpgradeInProgress

| Status | Reason | Meaning |
|---|---|---|
| `True` | `UpgradeInitiated` | Upgrade was successfully initiated |
| `False` | `UpgradeFailed` | Upgrade initiation or execution failed |
| `True` | `UpgradeSucceeded` | Upgrade completed successfully |

---

## Metrics

All metrics are exposed on the `/metrics` endpoint (default port `8080`).

### Metric reference

| Metric | Type | Labels | Description |
|---|---|---|---|
| `eks_ami_operator_outdated_nodegroups` | Gauge | `cluster` | Number of node groups not using the latest AMI |
| `eks_ami_operator_upgrade_attempts_total` | Counter | `cluster`, `nodegroup`, `result` | Total upgrade initiation attempts. `result` is one of `success`, `failed`, `skipped` |
| `eks_ami_operator_compliance_status` | Gauge | `cluster`, `nodegroup` | `1` = compliant, `0` = not compliant |
| `eks_ami_operator_last_checked_timestamp_seconds` | Gauge | `cluster`, `nodegroup` | Unix timestamp of the last compliance check |
| `eks_ami_operator_deleted_policies_total` | Counter | `cluster`, `nodegroup` | Total `NodeGroupUpgradePolicy` CR deletions |
| `eks_ami_operator_next_run_seconds` | Gauge | `cluster`, `nodegroup` | Seconds until the next scheduled reconcile |
| `eks_ami_operator_update_status` | Gauge (one-hot) | `cluster`, `nodegroup`, `status` | Lifecycle status. `status` is one of `idle`, `in_progress`, `successful`, `failed`, `cancelled`. Exactly one value is `1` per nodegroup. |
| `eks_ami_operator_update_inflight_seconds` | Gauge | `cluster`, `nodegroup` | Seconds since `lastUpgradeAttempt` while update is `InProgress`; `0` otherwise |

### Example PromQL queries

```promql
# Outdated nodegroups by cluster
sum by (cluster) (eks_ami_operator_outdated_nodegroups)

# Compliance status for a specific nodegroup
eks_ami_operator_compliance_status{cluster="my-eks-cluster", nodegroup="my-managed-nodegroup"}

# Upgrade attempts by result (last 1h)
sum by (result) (increase(eks_ami_operator_upgrade_attempts_total[1h]))

# Last checked timestamp
max by (cluster, nodegroup) (eks_ami_operator_last_checked_timestamp_seconds)

# Time to next run
max by (cluster, nodegroup) (eks_ami_operator_next_run_seconds)

# Upgrade in progress too long (> 8h)
eks_ami_operator_update_inflight_seconds > 8 * 3600

# Terminal failed or cancelled
max by (cluster, nodegroup) (eks_ami_operator_update_status{status=~"failed|cancelled"}) == 1
```

---

## Alerting

Suggested Prometheus alerting rules. Adjust thresholds and label selectors for your environment.

```yaml
groups:
  - name: eks-ami-operator
    rules:

      - alert: EKSNodeGroupUpgradeFailed
        expr: max by (cluster, nodegroup) (eks_ami_operator_update_status{status=~"failed|cancelled"}) == 1
        for: 0m
        labels:
          severity: critical
        annotations:
          summary: "EKS node group upgrade failed or was cancelled"
          description: "Cluster {{ $labels.cluster }}, nodegroup {{ $labels.nodegroup }} upgrade is in a terminal failure state."

      - alert: EKSNodeGroupUpgradeStuck
        expr: eks_ami_operator_update_inflight_seconds > 8 * 3600
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "EKS node group upgrade has been in-progress for over 8 hours"
          description: "Cluster {{ $labels.cluster }}, nodegroup {{ $labels.nodegroup }} upgrade has been running for {{ $value | humanizeDuration }}."

      - alert: EKSNodeGroupUpgradeInitiationFailed
        expr: increase(eks_ami_operator_upgrade_attempts_total{result="failed"}[15m]) > 0
        for: 0m
        labels:
          severity: warning
        annotations:
          summary: "EKS node group upgrade initiation failed"
          description: "Cluster {{ $labels.cluster }}, nodegroup {{ $labels.nodegroup }} failed to initiate an upgrade."

      - alert: EKSNodeGroupNonCompliant
        expr: max_over_time(1 - eks_ami_operator_compliance_status[6h]) == 1
        for: 0m
        labels:
          severity: warning
        annotations:
          summary: "EKS node group has been non-compliant for over 6 hours"
          description: "Cluster {{ $labels.cluster }}, nodegroup {{ $labels.nodegroup }} is using an outdated AMI."

      - alert: EKSAMIOperatorStalled
        expr: (time() - eks_ami_operator_last_checked_timestamp_seconds) > 24 * 3600
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "eks-ami-operator has not checked a nodegroup in over 24 hours"
          description: "Cluster {{ $labels.cluster }}, nodegroup {{ $labels.nodegroup }} has not been checked since {{ $value | humanizeTimestamp }}."

      - alert: EKSAMIOperatorNextRunTooFar
        expr: eks_ami_operator_next_run_seconds > 5 * 24 * 3600
        for: 0m
        labels:
          severity: info
        annotations:
          summary: "eks-ami-operator next run is more than 5 days away"
          description: "Cluster {{ $labels.cluster }}, nodegroup {{ $labels.nodegroup }} next run is in {{ $value | humanizeDuration }}."
```

---

## Security & RBAC

### IRSA (recommended)

Use IAM Roles for Service Accounts to grant the operator AWS permissions without static credentials:

```bash
eksctl create iamserviceaccount \
  --name eks-ami-operator \
  --namespace eks-ami-operator-system \
  --cluster <cluster-name> \
  --attach-policy-arn arn:aws:iam::<account-id>:policy/EKSAMIOperatorPolicy \
  --approve
```

Then set the annotation on the ServiceAccount via Helm:

```yaml
serviceAccount:
  annotations:
    eks.amazonaws.com/role-arn: arn:aws:iam::<account-id>:role/<irsa-role-name>
```

### Pod security

The operator runs with minimal privileges:

- `runAsNonRoot: true`
- `readOnlyRootFilesystem: true`
- All Linux capabilities dropped

### Kubernetes RBAC

The operator needs the following Kubernetes permissions:

| Resource | Verbs |
|---|---|
| `nodegroupupgradepolicies` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` |
| `nodegroupupgradepolicies/status` | `get`, `update`, `patch` |
| `nodegroupupgradepolicies/finalizers` | `update` |
| `events` | `create`, `patch` |
| `leases` (leader election) | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` |

### Immutable fields

The admission webhook blocks changes to the following fields after a `NodeGroupUpgradePolicy` is created:

- `spec.clusterName`
- `spec.nodeGroupName`
- `spec.region`

---

## Development

### Prerequisites

```bash
# Install controller-gen, envtest
make setup-envtest
```

### Run locally

```bash
# Install CRDs into the cluster pointed to by ~/.kube/config
make install

# Run the operator locally (uses local kubeconfig)
make run
```

### Run tests

```bash
# Unit + integration tests (uses envtest)
go test ./... -race

# With coverage
make test
```

### Lint

```bash
golangci-lint run ./...
```

### Code generation

After modifying `api/v1alpha1/nodegroupupgradepolicy_types.go`:

```bash
# Regenerate DeepCopy methods
make generate

# Regenerate CRD manifests, RBAC, webhook configs
make manifests
```

### Build and push the image

Single-arch (current platform only):

```bash
make docker-build docker-push IMG=<registry>/eks-ami-operator:<tag>
```

Multi-arch (linux/amd64 + linux/arm64):

```bash
make docker-buildx IMG=<registry>/eks-ami-operator:<tag>
```

The `docker-buildx` target creates a `docker-container` buildx builder named `eks-ami-operator-builder` on first use and reuses it on subsequent runs. In CI, if your pipeline already sets up a buildx builder (e.g. via `docker/setup-buildx-action`), pass its name to avoid creating a duplicate:

```bash
make docker-buildx IMG=<registry>/eks-ami-operator:<tag> BUILDX_BUILDER=<ci-builder-name>
```

To build for different platforms:

```bash
make docker-buildx IMG=<registry>/eks-ami-operator:<tag> PLATFORMS=linux/amd64,linux/arm64,linux/s390x
```

### Helm chart development

```bash
# Lint the chart (strict mode)
make helm-lint

# Regenerate charts/eks-ami-operator/README.md after changing values.yaml or README.md.gotmpl
make helm-docs

# Package the chart into dist/
make helm-package
```

### Project structure

```
api/v1alpha1/
  nodegroupupgradepolicy_types.go     <- CRD types + defaulting webhook
  groupversion_info.go                <- API group/version registration
  zz_generated.deepcopy.go            <- Auto-generated DeepCopy methods

cmd/
  main.go                             <- Manager bootstrap

internal/
  controller/
    nodegroupupgradepolicy_controller.go  <- Main reconcile loop
    conditions.go                         <- Condition helper functions
    nodegroupupgradepolicy_controller_test.go
    suite_test.go
  scheduler/
    scheduler.go                      <- ShouldSkip, NextRun, Jitter, ParseInterval
  upgrade/
    upgrade.go                        <- ProcessUpgrade (initiation + polling)
  awsutils/
    aws_helpers.go                    <- DescribeNodegroup, ResolveLatestAMI, DescribeNodegroupUpdate
    version.go                        <- EKS version parsing
  finalizer/
    finalizer.go                      <- HandleFinalizer
  metrics/
    metrics.go                        <- Prometheus metric registrations
  webhook/v1alpha1/
    nodegroupupgradepolicy_webhook.go <- Validation webhook
    nodegroupupgradepolicy_webhook_test.go
    webhook_suite_test.go

pkg/
  awsclient/
    awsclient.go                      <- AWS SDK v2 client factory

charts/
  eks-ami-operator/                   <- Helm chart

config/
  crd/                                <- CRD manifests
  rbac/                               <- RBAC manifests
  manager/                            <- Deployment manifests
  webhook/                            <- Webhook manifests
  certmanager/                        <- cert-manager integration
  prometheus/                         <- ServiceMonitor
  default/                            <- Kustomize default overlay
  samples/                            <- Sample CRs
```

---

## Architecture

### AMI resolution

The operator queries AWS SSM Parameter Store using a path built from the AMI type and EKS cluster version:

| AMI type | SSM path |
|---|---|
| `AL2_x86_64` | `/aws/service/eks/optimized-ami/{version}/amazon-linux-2/recommended` |
| `AL2_ARM_64` | `/aws/service/eks/optimized-ami/{version}/amazon-linux-2-arm64/recommended` |
| `AL2023_x86_64_STANDARD` | `/aws/service/eks/optimized-ami/{version}/amazon-linux-2023/x86_64/standard/recommended` |
| `AL2023_ARM_64_STANDARD` | `/aws/service/eks/optimized-ami/{version}/amazon-linux-2023/arm64/standard/recommended` |
| `CUSTOM` | No-op (skipped) |

The SSM parameter value is a JSON object:

```json
{
  "image_id": "ami-0abcdef1234567890",
  "release_version": "1.29.6-20240531"
}
```

The `release_version` is used for comparison, not the AMI ID. This matches what EKS reports in `DescribeNodegroup`.

### AL2 deprecation handling

AWS stopped publishing AL2-optimized AMIs for EKS versions >= 1.33. When the operator detects this:

1. Sets `upgradeStatus: Skipped`
2. Sets the `AMICompliance` condition to `False` with reason `UnsupportedAMI`
3. Logs a message recommending migration to AL2023
4. Requeues per the normal schedule (not treated as an error)

### Retry strategy

All AWS API calls use jittered exponential backoff:

- Initial interval: 2s + up to 500ms random jitter
- Max elapsed time: 30s
- Permanent errors (no retry): `ResourceNotFoundException`, `InvalidParameterException`, `ParameterNotFound`, `InvalidParameter`

### Scheduling implementation

`scheduler.NextRun` implements cron scheduling with boundary tolerances to handle clock skew and controller startup timing:

- Early tolerance: 1 second before the cron boundary is treated as "due now"
- Late tolerance: 5 seconds after the cron boundary is treated as "due now"

After a successful cron run, `computeNextDelay` nudges the reference time 6 seconds forward before recomputing, so the next requeue targets the next cron occurrence rather than the boundary that just fired.

---

## Troubleshooting

### Operator not reconciling

```bash
# Check operator logs
kubectl logs -n eks-ami-operator-system deployment/eks-ami-operator -f

# Check if policy is paused
kubectl get nodegroupupgradepolicy <name> -n eks-ami-operator-system -o jsonpath='{.spec.paused}'

# Check nextScheduledTime
kubectl get nodegroupupgradepolicy <name> -n eks-ami-operator-system -o jsonpath='{.status.nextScheduledTime}'
```

### Upgrade not triggering

```bash
# Check upgradeStatus and conditions
kubectl describe nodegroupupgradepolicy <name> -n eks-ami-operator-system

# Verify autoUpgrade is enabled
kubectl get nodegroupupgradepolicy <name> -n eks-ami-operator-system -o jsonpath='{.spec.autoUpgrade}'

# Check if AMI type is unsupported (AL2 on EKS >= 1.33)
kubectl get nodegroupupgradepolicy <name> -n eks-ami-operator-system -o jsonpath='{.status.conditions}'
```

### AWS permission errors

```bash
# Check operator logs for AWS errors
kubectl logs -n eks-ami-operator-system deployment/eks-ami-operator | grep -i "error\|failed\|denied"

# Verify IRSA annotation
kubectl get serviceaccount eks-ami-operator -n eks-ami-operator-system -o yaml
```

### Upgrade stuck in InProgress

```bash
# Check updateID and updateStatus
kubectl get nodegroupupgradepolicy <name> -n eks-ami-operator-system \
  -o jsonpath='{.status.updateID} {.status.updateStatus} {.status.updateErrors}'

# Check EKS directly
aws eks describe-update \
  --name <cluster-name> \
  --nodegroup-name <nodegroup-name> \
  --update-id <updateID> \
  --region <region>
```

### Webhook errors on CR creation

```bash
# Check webhook pod logs
kubectl logs -n eks-ami-operator-system deployment/eks-ami-operator | grep -i webhook

# Disable webhooks for local development
export ENABLE_WEBHOOKS=false
make run
```

### Common issues

| Symptom | Likely cause | Fix |
|---|---|---|
| `upgradeStatus: Skipped` with `UnsupportedAMI` | AL2 node group on EKS >= 1.33 | Migrate node group to AL2023 |
| `upgradeStatus: Failed` with no `updateErrors` | `UpdateNodegroupVersion` API error | Check operator logs for the error |
| Policy never reconciles | `paused: true` or `startAfter` in the future | Check spec fields |
| Cron not firing at expected time | Wrong timezone or cron expression | Validate with `crontab.guru`, check `scheduleTimezone` |
| `spec.region must be a valid AWS region` on create | Invalid region or missing EC2 permissions | Verify region name and IAM permissions |

---

## Roadmap

- [ ] Custom launch template support
- [ ] Helm chart published to a public registry
- [ ] Global cooldown across nodegroups in a cluster
- [ ] Dry-run mode (check compliance without upgrading)

---

## Contributing

Contributions are welcome. Here's how to get started:

1. Fork the repository
2. Create a feature branch: `git checkout -b feat/my-feature`
3. Make your changes with tests
4. Run `make test` and `golangci-lint run ./...`
5. Commit with a clear message
6. Open a pull request

### Pull request requirements

- Clear problem statement in the PR description
- Unit tests for new logic (especially in `internal/scheduler`, `internal/upgrade`, `internal/awsutils`)
- Updated documentation if behavior changes
- No regressions in existing tests

### Reporting issues

Please include:
- Operator version and Kubernetes version
- `kubectl describe nodegroupupgradepolicy <name>` output
- Relevant operator logs
- Steps to reproduce

### Code style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Use structured logging (`logf.FromContext(ctx).Info(...)`)
- Keep AWS API calls in `internal/awsutils`
- Keep scheduling logic in `internal/scheduler`
- Keep upgrade state machine in `internal/upgrade`

---

## License

Copyright 2025 Harsh Vijay Thakkar

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE) for the full license text.

```
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.