package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	// Total number of node groups using outdated AMIs
	OutdatedNodeGroups = prometheus.NewGaugeVec(
        prometheus.GaugeOpts{
            Name: "eks_nodegroup_outdated_total",
            Help: "Number of node groups that are not using the latest AMI per cluster",
        },
        []string{"cluster"}, // 👈 This enables label support
    )


	// Total number of upgrade attempts, labeled by cluster, nodegroup, and status
	UpgradeAttempts = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "ami_upgrade_attempts_total",
			Help: "Total number of AMI upgrade attempts",
		},
		[]string{"cluster", "nodegroup", "status"},
	)

	// Compliance status per node group (1 = compliant, 0 = not)
	ComplianceStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ami_compliance_status",
			Help: "Compliance status of node groups with latest AMI (1 = compliant, 0 = not)",
		},
		[]string{"cluster", "nodegroup"},
	)

	// Last checked timestamp per node group
	LastCheckedTimestamp = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "ami_last_checked_timestamp_seconds",
			Help: "Unix timestamp of the last AMI compliance check",
		},
		[]string{"cluster", "nodegroup"},
	)
)

func init() {
	metrics.Registry.MustRegister(
		OutdatedNodeGroups,
		UpgradeAttempts,
		ComplianceStatus,
		LastCheckedTimestamp,
	)
}
