# eks-ami-operator

[![Go Report Card](https://goreportcard.com/badge/github.com/harshvijaythakkar/eks-ami-operator)](https://goreportcard.com/report/github.com/harshvijaythakkar/eks-ami-operator)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue)](LICENSE)

A Kubernetes operator to **automate and manage EKS managed node group AMI upgrades**—production‑grade, observable, and easy to operate.

---

## Table of Contents

- [Overview](#overview)
- [Features](#features)
- [Prerequisites](#prerequisites)
- [Build & Deploy](#build--deploy)
- [Quick start (sample CRs)](#quick-start-sample-crs)
- [Scheduling semantics](#scheduling-semantics)
- [Metrics](#metrics)
- [Upgrade Lifecycle & Status Fields](#Upgrade-Lifecycle--Status-Fields)
- [Status & Conditions](#status--conditions)
- [Security & RBAC](#security--rbac)
- [Development](#development)
- [Troubleshooting](#troubleshooting)
- [Roadmap](#roadmap)
- [Contributing](#contributing)
- [License](#license)

---

## Overview

`eks-ami-operator` keeps your **EKS managed node groups** aligned to the **latest recommended AMIs** (via AWS SSM) and can optionally trigger **managed upgrades** when out of date. It’s designed for **safety**, **clarity**, and **operational control**.

**What it does:**
- Resolves the latest EKS‑optimized AMI using **AWS SSM Parameter Store** (based on EKS version & AMI type).
- Compares with the node group’s **current release version**.
- Optionally triggers **`UpdateNodegroupVersion`** to upgrade.
- Exposes **Prometheus metrics** and sets clear **status conditions**.
- Schedules reconciles using **cron** (with timezone) or **interval**, plus **jitter** to avoid thundering herds.
- Cleans up safely using **Kubernetes finalizers**.

---

## Features

- **Dynamic AMI lookup (SSM)** by **AMI type** and **EKS cluster version**
- **AutoUpgrade** for hands‑off managed upgrades
- **Scheduling**
  - `scheduleCron` + `scheduleTimezone` (**default = UTC** if timezone omitted)
  - Fallback `checkInterval` (Go duration)
  - **Precedence**: `paused` → `startAfter` → `scheduleCron` → `checkInterval`
  - **Cron = exact (no jitter)**, **Interval = jittered**
- **Status & Conditions**: `UpgradeStatus`, `CurrentAmi`, `TargetAmi`, `LastChecked`,
  `LastScheduledTime`, `NextScheduledTime`, plus standardized conditions
- **Metrics** for compliance, attempts, timestamps, and next scheduled run
- **Robust retries** with **exponential backoff**
- **Finalizers** for safe deletion
- **Refactored, testable structure** (`internal/` packages for scheduler, awsutils, upgrade, finalizer)

---

## Prerequisites
- Go `v1.24.0+`
- Docker `v17.03+`
- kubectl `v1.11.3+`
- Access to a Kubernetes cluster `v1.11.3+`
- AWS credentials configured for EKS and SSM access (IRSA recommended)

### Required AWS permissions
```json
{
  "Version": "2012-10-17",
  "Statement": [
    { "Effect": "Allow", "Action": ["eks:DescribeCluster"], "Resource": "*" },
    { "Effect": "Allow", "Action": ["eks:DescribeNodegroup", "eks:UpdateNodegroupVersion"], "Resource": "*" },
    { "Effect": "Allow", "Action": ["ssm:GetParameter"], "Resource": "arn:aws:ssm:<region>:*:parameter/aws/service/eks/*" }
  ]
}
```

---

## Build & Deploy

**Build and push the image:**
```sh
make docker-build docker-push IMG=<registry>/eks-ami-operator:<tag>
```

**Install CRDs:**
```sh
make install
```

**Deploy the operator:**
```sh
make deploy IMG=<registry>/eks-ami-operator:<tag>
```

### **Create a NodeGroupUpgradePolicy**
Apply a sample CR:
```sh
kubectl apply -f config/samples/eks.aws.harsh.dev_v1alpha1_nodegroupupgradepolicy.yaml
```

---

## Quick start (sample CRs)

We include ready-to-use examples in examples/

1. AutoUpgrade = true
```yaml
# examples/nodegroupupgradepolicy_autoupgrade_true.yaml
apiVersion: eks.aws.harsh.dev/v1alpha1
kind: NodeGroupUpgradePolicy
metadata:
  name: sample-ng-upgrade-autoupgrade-true
  namespace: eks-ami-operator-system
spec:
  region: us-west-2
  clusterName: my-eks-cluster
  nodeGroupName: my-managed-nodegroup
  checkInterval: 24h
  paused: false
  autoUpgrade: true
```

2. AutoUpgrade = false
```yaml
# examples/nodegroupupgradepolicy_autoupgrade_false.yaml
apiVersion: eks.aws.harsh.dev/v1alpha1
kind: NodeGroupUpgradePolicy
metadata:
  name: sample-ng-upgrade-autoupgrade-false
  namespace: eks-ami-operator-system
spec:
  region: us-west-2
  clusterName: my-eks-cluster
  nodeGroupName: my-managed-nodegroup
  checkInterval: 24h
  paused: false
  autoUpgrade: false
```

3. Cron-based scheduling (with timezone)
```yaml
# examples/nodegroupupgradepolicy_cron.yaml
apiVersion: eks.aws.harsh.dev/v1alpha1
kind: NodeGroupUpgradePolicy
metadata:
  name: sample-ng-upgrade-cron
  namespace: eks-ami-operator-system
spec:
  region: us-west-2
  clusterName: my-eks-cluster
  nodeGroupName: my-managed-nodegroup

  # Fallback interval if cron is omitted/invalid
  checkInterval: 24h

  # Run at 03:00 Mon–Fri in Asia/Kolkata (default is UTC if timezone omitted)
  scheduleCron: "0 3 * * 1-5"
  scheduleTimezone: "Asia/Kolkata"

  paused: false
  autoUpgrade: true
```

### Apply & observe
```bash
kubectl apply -f examples/nodegroupupgradepolicy_autoupgrade_true.yaml
kubectl get nodegroupupgradepolicies -n eks-ami-operator-system
kubectl describe nodegroupupgradepolicy sample-ng-upgrade-autoupgrade-true -n eks-ami-operator-system
```

### Delete
```bash
kubectl delete -f examples/nodegroupupgradepolicy_autoupgrade_true.yaml
```

---

## Scheduling semantics

- Precedence (most restrictive first):
  1. paused: true → no work, reconciles exit
  2. startAfter: <RFC3339> in the future → requeue until that moment
  3. scheduleCron + scheduleTimezone (default = UTC when timezone omitted)
  4. checkInterval (fallback)
- Defaults (admission/controller)
  - If both `scheduleCron` and `checkInterval` are omitted → `checkInterval` defaults to **`24h`**.
  - If `scheduleCron` is set and `scheduleTimezone` is omitted → timezone defaults to **`UTC`**.
- Jitter: a small ±10% randomization is applied to all future requeues to avoid synchronized spikes.
- Common patterns:
  - "Every night at 02:00 UTC"
  ```yaml
    scheduleCron: "0 2 * * *"
    # scheduleTimezone omitted → defaults to UTC
  ```
  - "Weekdays at 03:00 IST"
  ```yaml
    scheduleCron: "0 3 * * 1-5"
    scheduleTimezone: "Asia/Kolkata"
  ```

---

## Metrics

The controller exposes the following Prometheus metrics (names and labels match the code):

- eks_ami_operator_outdated_nodegroups (gauge) — number of node groups using outdated AMIs
Labels: cluster

- eks_ami_operator_upgrade_attempts_total (counter) — total AMI upgrade attempts
Labels: cluster, nodegroup, result (success | failed | skipped)

- eks_ami_operator_compliance_status (gauge) — compliance per node group (1 = compliant, 0 = not)
Labels: cluster, nodegroup

- eks_ami_operator_last_checked_timestamp_seconds (gauge) — Unix timestamp of the last compliance check
Labels: cluster, nodegroup

- eks_ami_operator_deleted_policies_total (counter) — total NodeGroupUpgradePolicy deletions
Labels: cluster, nodegroup

- eks_ami_operator_next_run_seconds (gauge) — computed delay until next scheduled reconcile
Labels: cluster, nodegroup

### Additional lifecycle metrics

- `eks_ami_operator_update_status` *(gauge)* — lifecycle of the managed node group update  
  Labels: `cluster`, `nodegroup`, `status`  
  status values: `idle`, `in_progress`, `successful`, `failed`, `cancelled`  
  Examples:
  - `…{status="in_progress"} == 1` while an update is running
  - `…{status="successful"} == 1` after EKS reports *Successful*
  - `…{status="failed"}` or `…{status="cancelled"} == 1` after terminal failure/cancel


- `eks_ami_operator_update_inflight_seconds` *(gauge)* — seconds since the last upgrade attempt while the update is InProgress, `0` otherwise
Labels: `cluster`, `nodegroup`

### Example PromQL queries

- Outdated nodegroups by cluster:
```
sum by (cluster) (eks_ami_operator_outdated_nodegroups)
```

- Compliance status over time for a specific nodegroup:
```
eks_ami_operator_compliance_status{cluster="my-eks-cluster", nodegroup="my-managed-nodegroup"}
```

- Upgrade attempts by result (last 1h):
```
sum by (result) (increase(eks_ami_operator_upgrade_attempts_total[1h]))
```

- Last checked timestamp (most recent value):
```
max by (cluster, nodegroup) (eks_ami_operator_last_checked_timestamp_seconds)
```

- Time to next run:
```
max by (cluster, nodegroup) (eks_ami_operator_next_run_seconds)
```

- Terminal failed/cancelled
```
max by (cluster, nodegroup)
```

- Upgrade in progress too long (e.g., > 8h)
```
eks_ami_operator_update_inflight_seconds > 8 * 60 * 60
```

- Initiation failures (API errors)
```
increase(eks_ami_operator_upgrade_attempts_total{result="failed"}[15m]) > 0
```

- Nodegroup non-compliant for too long (e.g., > 6h)
```
max_over_time(1 - eks_ami_operator_compliance_status[6h]) == 1
```

- Operator stalled (no checks) (24h)
```
(time() - eks_ami_operator_last_checked_timestamp_seconds) > 24*60*60
```

- Suspiciously distant next run (> 5 days)
```
eks_ami_operator_next_run_seconds > 5 * 24 * 60 * 60
```

---

## Upgrade Lifecycle & Status Fields

This operator exposes **two complementary status fields** on `NodeGroupUpgradePolicy`:

*   **`status.upgradeStatus`** — *Operator’s high‑level lifecycle* of the nodegroup upgrade.
    *   Values: `InProgress`, `Succeeded`, `Failed`, `Skipped`.
*   **`status.updateStatus`** — *EKS authoritative status* for the specific **`status.updateID`** (from `DescribeUpdate`).
    *   Values (typed by AWS): `InProgress`, `Successful`, `Failed`, `Cancelled`.

> **Mental model**  
> `upgradeStatus` = “**Operator’s story** about the whole upgrade”  
> `updateStatus` = “**EKS’s story** about this specific UpdateID\*\*”

### When each field changes

| Scenario                                               | Operator action   | `upgradeStatus` | `updateID` | `updateStatus`       |
| ------------------------------------------------------ | ----------------- | --------------- | ---------- | -------------------- |
| **Compliant already**                                  | Do nothing        | `Succeeded`     | —          | —                    |
| **Outdated & autoUpgrade=false**                       | Do nothing        | `Skipped`       | —          | —                    |
| **Unsupported AMI** (e.g., AL2 on EKS ≥ 1.33)          | Do nothing        | `Skipped`       | —          | —                    |
| **Initiation failed** (`UpdateNodegroupVersion` error) | Stop              | `Failed`        | —          | —                    |
| **Initiation succeeded**                               | Track             | `InProgress`    | set        | `InProgress`         |
| **Polling: EKS says InProgress**                       | Keep polling \~2m | `InProgress`    | keep       | `InProgress`         |
| **Polling: EKS says Successful**                       | Finish            | `Succeeded`     | keep       | `Successful`         |
| **Polling: EKS says Failed/Cancelled**                 | Finish            | `Failed`        | keep       | `Failed`/`Cancelled` |

> While **in progress**, the controller **short‑requeues \~2 minutes** for live progress.  
> After **terminal** (Successful/Failed/Cancelled), it returns to the **user’s cron/interval** cadence.

### Field glossary (selected)

*   `status.updateID`: The EKS **UpdateID** returned by `UpdateNodegroupVersion`. Used with `DescribeUpdate`.
*   `status.updateStatus`: What EKS reports for the above UpdateID (`InProgress|Successful|Failed|Cancelled`).
*   `status.upgradeStatus`: The operator’s high‑level lifecycle (`InProgress|Succeeded|Failed|Skipped`).
*   `status.lastUpgradeAttempt`: Timestamp when we last initiated an EKS update.
*   `status.lastProgressCheck`: Timestamp of the latest `DescribeUpdate` poll.
*   `status.nextScheduledTime`: The **next cron window** (Option A behavior).
    *   During initiation we compute the *next* cron (using a small nudge) and stamp it, so the CR shows a **future** time while we short‑poll.
    *   If an upgrade was already in flight before this behavior was added, the value may still point to the **boundary that just fired** until the next initiation/terminal state.

### Example statuses

**New initiation (outdated + autoUpgrade=true)**

```yaml
status:
  upgradeStatus: InProgress
  updateID: 58efb0dc-09c6-3834-88be-e84c9112d5c5
  updateStatus: InProgress
  lastUpgradeAttempt: "2026-03-23T10:15:00Z"
  nextScheduledTime: "2026-03-24T10:15:00Z"   # next cron window (computed at initiation)
```

**Terminal success**

```yaml
status:
  upgradeStatus: Succeeded
  updateID: 58efb0dc-09c6-3834-88be-e84c9112d5c5
  updateStatus: Successful
  lastProgressCheck: "2026-03-23T10:42:12Z"
```

**Terminal failure**

```yaml
status:
  upgradeStatus: Failed
  updateID: 58efb0dc-09c6-3834-88be-e84c9112d5c5
  updateStatus: Failed
  updateErrors:
    - "PodEvictionFailure: Reached max retries while trying to evict pods from nodes in node group ..."
```

> We always surface **all error messages** returned by EKS in `status.updateErrors[]` (no special‑casing). If your policy allows, you may re‑try with `force=true` on a later cron window for eviction‑blocked cases.

***

## Metrics for Alerting & Dashboards

### Lifecycle (one‑hot) and in‑flight timer

*   **`eks_ami_operator_update_status{cluster,nodegroup,status}`** *(gauge)*  
    One‑hot lifecycle: `status ∈ {idle, in_progress, successful, failed, cancelled}`. Exactly one value is `1` per nodegroup.
*   **`eks_ami_operator_update_inflight_seconds{cluster,nodegroup}`** *(gauge)*  
    Seconds since `status.lastUpgradeAttempt` **while InProgress**, otherwise `0`.

### Existing metrics (recap)

*   **`eks_ami_operator_compliance_status{cluster,nodegroup}`** *(gauge)* — `1` compliant, `0` not.
*   **`eks_ami_operator_outdated_nodegroups{cluster}`** *(gauge)* — count of outdated nodegroups.
*   **`eks_ami_operator_upgrade_attempts_total{cluster,nodegroup,result}`** *(counter)* — `result ∈ success|failed|skipped` (initiation attempts).
*   **`eks_ami_operator_last_checked_timestamp_seconds{cluster,nodegroup}`** *(gauge)*
*   **`eks_ami_operator_next_run_seconds{cluster,nodegroup}`** *(gauge)*

***

## Suggested PromQL Alerts

> Adjust label filters (e.g., `cluster=~"prod-.*"`) and severities for your environment.

**Terminal failed/cancelled**

```promql
max by (cluster, nodegroup) (eks_ami_operator_update_status{status=~"failed|cancelled"}) == 1
```

**Upgrade in progress too long (e.g., > 8h)**

```promql
eks_ami_operator_update_inflight_seconds > 8 * 60 * 60
```

**Initiation failures in last 15 minutes**

```promql
increase(eks_ami_operator_upgrade_attempts_total{result="failed"}[15m]) > 0
```

**Nodegroup non‑compliant for too long (e.g., 6h)**

```promql
max_over_time(1 - eks_ami_operator_compliance_status[6h]) == 1
```

**Operator stalled: last check too old (e.g., 24h)**

```promql
(time() - eks_ami_operator_last_checked_timestamp_seconds) > 24*60*60
```

---

## Status & Conditions

Selected fields on status:

- UpgradeStatus: InProgress | Succeeded | Failed | Skipped
- CurrentAmi, TargetAmi
- LastChecked, LastUpgradeAttempt
- LastScheduledTime, NextScheduledTime
- conditions[]:
  - Upgrade conditions: `UpgradeInitiated`, `UpgradeFailed`, `UpgradeSucceeded`
  - AMI compliance: `OutdatedAMI`, `UpToDate`
  - (When applicable) `UnsupportedAMI` (e.g., AL2 on EKS ≥ 1.33)

---

## Security & RBAC

- IRSA on EKS strongly recommended
- Minimal pod security:
  - `runAsNonRoot: true`
  - `readOnlyRootFilesystem: true`
  - Drop all capabilities
- Scope AWS IAM permissions to specific clusters/nodegroups where feasible.

---

## Development

### Run locally
```sh
make install
make run
```

### Lint & test
```sh
golangci-lint run ./...
go test ./... -race
```

### Generate & manifests
```sh
make generate && make manifests
```

### Uninstall
```sh
kubectl delete -k config/samples/
make uninstall
make undeploy
```

---

## Roadmap
- Custom launch template support
- Enhanced status with EKS update ARN/ID & progress
- Helm chart for simplified install
- Global cooldown across nodegroups in a cluster

---

## Contributing

Contributions are welcome—PRs, issues, and docs improvements. Please include:
- Clear problem statements
- Repro steps or unit tests
- Impacted components (scheduler, awsutils, upgrade, finalizer, controller)

---

## **License**
Apache License 2.0  
See http://www.apache.org/licenses/LICENSE-2.0 for details.
