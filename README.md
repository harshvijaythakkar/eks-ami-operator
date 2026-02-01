# eks-ami-operator

[![Go Report Card](https://goreportcard.com/badge/github.com/harshvijaythakkar/eks-ami-operator)](https://goreportcard.com/report/github.com/harshvijaythakkar/eks-ami-operator)
[![License](https://img.shields.io/badge/License-Apache_2.0-blue)](LICENSE)

A Kubernetes operator to **automate and manage EKS managed node group AMI upgrades**—production‑grade, observable, and easy to operate.

---

## Table of Contents

- [Overview](#overview)
- [Features](#features)
- [Prerequisites](#prerequisites)
- [build--deploy](#build--deploy)
- [quick-start-sample-crs](#quick-start-sample-crs)
- [scheduling-semantics](#scheduling-semantics)
- [metrics](#metrics)
- [status--conditions](#status--conditions)
- [security--rbac](#security--rbac)
- [development](#development)
- [roadmap](#roadmap)
- [contributing](#contributing)
- [license](#license)

---

## **Overview**

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
  - **Jitter** on requeues to avoid synchronized spikes
- **Status & Conditions**: `UpgradeStatus`, `CurrentAmi`, `TargetAmi`, `LastChecked`,  
  `LastScheduledTime`, `NextScheduledTime`, plus standardized conditions
- **Metrics** for compliance, attempts, timestamps, scheduler next run
- **Robust retries** with **exponential backoff** (+ slight jitter)
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
- Jitter: a small ±10% randomization is applied to all future requeues to avoid synchronized spikes.
- Common patterns:
  - "Every night at 02:00 UTC"
  ```
    scheduleCron: "0 2 * * *"
    # scheduleTimezone omitted → defaults to UTC
  ```
  - "Weekdays at 03:00 IST"
  ```
    scheduleCron: "0 3 * * 1-5"
    scheduleTimezone: "Asia/Kolkata"
  ```

---

## Metrics

Exposed via Prometheus client:

- eks_ami_operator_compliance_status{cluster,nodegroup} – 1 compliant, 0 outdated
- eks_ami_operator_upgrade_attempts_total{cluster,nodegroup,result} – counter (result = success|failed|skipped)
- eks_ami_operator_last_checked_timestamp{cluster,nodegroup} – unix seconds (gauge)
- eks_ami_operator_outdated_nodegroups{cluster} – number of outdated nodegroups in last check (gauge)
- eks_ami_operator_next_run_seconds{cluster,nodegroup} – computed delay to next run (gauge)

---

## Status & Conditions

Selected fields on status:

- UpgradeStatus: InProgress | Succeeded | Failed | Skipped
- CurrentAmi, TargetAmi
- LastChecked, LastUpgradeAttempt
- LastScheduledTime, NextScheduledTime
- conditions[]:
  - Upgrade conditions: UpgradeInitiated, UpgradeFailed, UpgradeSucceeded
  - AMI compliance: OutdatedAMI, UpToDate

---

## Security & RBAC

- IRSA on EKS strongly recommended
- Minimal pod security:
  - runAsNonRoot: true
  - readOnlyRootFilesystem: true
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
