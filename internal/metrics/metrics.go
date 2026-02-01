package metrics

import (
	prom "github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// Number of node groups that are not using the latest AMI (per cluster)
	OutdatedNodeGroups = prom.NewGaugeVec(
		prom.GaugeOpts{
			Name: "eks_ami_operator_outdated_nodegroups",
			Help: "Number of node groups that are not using the latest AMI per cluster",
		},
		[]string{"cluster"},
	)

	// Total number of AMI upgrade attempts, labeled by result
	// result ∈ {success, failed, skipped}
	UpgradeAttempts = prom.NewCounterVec(
		prom.CounterOpts{
			Name: "eks_ami_operator_upgrade_attempts_total",
			Help: "Total number of AMI upgrade attempts by result",
		},
		[]string{"cluster", "nodegroup", "result"},
	)

	// Compliance status per node group (1 = compliant, 0 = not)
	ComplianceStatus = prom.NewGaugeVec(
		prom.GaugeOpts{
			Name: "eks_ami_operator_compliance_status",
			Help: "Compliance status of node groups with the latest AMI (1 = compliant, 0 = not)",
		},
		[]string{"cluster", "nodegroup"},
	)

	// Last checked timestamp per node group (Unix seconds)
	LastCheckedTimestamp = prom.NewGaugeVec(
		prom.GaugeOpts{
			Name: "eks_ami_operator_last_checked_timestamp_seconds",
			Help: "Unix timestamp of the last AMI compliance check per node group",
		},
		[]string{"cluster", "nodegroup"},
	)

	// Total number of NodeGroupUpgradePolicy resources deleted
	DeletedPolicies = prom.NewCounterVec(
		prom.CounterOpts{
			Name: "eks_ami_operator_deleted_policies_total",
			Help: "Total number of NodeGroupUpgradePolicy resources deleted",
		},
		[]string{"cluster", "nodegroup"},
	)

	// Time in seconds until the next scheduled reconcile for this policy
	NextRunSeconds = prom.NewGaugeVec(
		prom.GaugeOpts{
			Name: "eks_ami_operator_next_run_seconds",
			Help: "Time in seconds until the next scheduled reconcile for this NodeGroupUpgradePolicy",
		},
		[]string{"cluster", "nodegroup"},
	)
)

func init() {
	crmetrics.Registry.MustRegister(
		OutdatedNodeGroups,
		UpgradeAttempts,
		ComplianceStatus,
		LastCheckedTimestamp,
		DeletedPolicies,
		NextRunSeconds,
	)
}
