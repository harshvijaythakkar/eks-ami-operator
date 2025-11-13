# eks-ami-operator

[![License](https://img.shields.io/badge/License-Apache_2.0-blue)](LICENSE)

A Kubernetes operator to **automate and manage EKS node group AMI upgrades** in a production-grade, flexible, and observable way.

---

## **Overview**
`eks-ami-operator` helps platform teams keep their EKS managed node groups compliant with the latest recommended AMIs. It supports:

- **Dynamic AMI resolution** via AWS SSM Parameter Store.
- **AutoUpgrade** capability for seamless AMI updates.
- **Customizable scheduling** using `checkInterval` and `startAfter`.
- **Prometheus metrics** for compliance and upgrade observability.
- **Safe cleanup** using Kubernetes finalizers.

This operator is designed for **flexibility**, **scalability**, and **future extensibility** (e.g., custom launch templates, cron-based scheduling).

---

## **Features**
- ✅ Dynamic AMI lookup based on EKS version and AMI type.
- ✅ AutoUpgrade with cooldown logic (`LastUpgradeAttempt`).
- ✅ CRD-driven scheduling (`checkInterval`, `startAfter`, `paused`).
- ✅ Detailed status tracking (`UpgradeStatus`, `LastChecked`, `TargetAmi`).
- ✅ Prometheus metrics for compliance and upgrade attempts.
- ✅ Robust error handling with exponential backoff for AWS API calls.

---

## **Getting Started**

### **Prerequisites**
- Go `v1.24.0+`
- Docker `v17.03+`
- kubectl `v1.11.3+`
- Access to a Kubernetes cluster `v1.11.3+`
- AWS credentials configured for EKS and SSM access (IRSA recommended)

---

### **Deploy the Operator**

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

---

### **Create a NodeGroupUpgradePolicy**
Apply a sample CR:
```sh
kubectl apply -f config/samples/eks.aws.harsh.dev_v1alpha1_nodegroupupgradepolicy.yaml
```

Example CR:
```yaml
apiVersion: eks.aws.harsh.dev/v1alpha1
kind: NodeGroupUpgradePolicy
metadata:
  name: my-node-group-policy-name
  namespace: my-namespace
spec:
  clusterName: my-cluster
  nodeGroupName: my-node-group
  region: my-aws-region
  autoUpgrade: true
  checkInterval: 24h
  startAfter: "2025-11-02T10:15:00Z"
  paused: false
```

---

### **Uninstall**
```sh
kubectl delete -k config/samples/
make uninstall
make undeploy
```

---

## **Metrics**
The operator exposes Prometheus metrics such as:
- `ComplianceStatus`
- `UpgradeAttempts`
- `LastCheckedTimestamp`
- `OutdatedNodeGroups`

---

## **Roadmap**
- Cron-based scheduling for precise control.
- Support for custom launch templates.
- Helm chart for easy deployment.

---

## **Contributing**
We welcome contributions! Please open issues or PRs for:
- Bug fixes
- Feature requests
- Documentation improvements

---

## **License**
Apache License 2.0  
See http://www.apache.org/licenses/LICENSE-2.0 for details.
