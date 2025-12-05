package upgrade

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/aws-sdk-go-v2/service/eks"
	eksv1alpha1 "github.com/harshvijaythakkar/eks-ami-operator/api/v1alpha1"
	"github.com/harshvijaythakkar/eks-ami-operator/internal/metrics"
)

const (
	UpgradeStatusFailed     = "Failed"
	UpgradeStatusInProgress = "InProgress"
	UpgradeStatusSucceeded  = "Succeeded"
	UpgradeStatusOutdated   = "Outdated"
	UpgradeStatusSkipped    = "Skipped"
)

func ProcessUpgrade(ctx context.Context, c client.Client, policy *eksv1alpha1.NodeGroupUpgradePolicy, currentRelease, latestRelease string, eksClient *eks.Client) error {
	logger := logf.FromContext(ctx)

	now := float64(time.Now().Unix())
	metrics.LastCheckedTimestamp.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(now)

	if currentRelease != latestRelease {
		logger.Info("Nodegroup is outdated", "current", currentRelease, "latest", latestRelease)
		metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)
		metrics.OutdatedNodeGroups.WithLabelValues(policy.Spec.ClusterName).Inc()

		policy.Status.TargetAmi = latestRelease

		if policy.Spec.AutoUpgrade {
			_, err := eksClient.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
				ClusterName:   &policy.Spec.ClusterName,
				NodegroupName: &policy.Spec.NodeGroupName,
			})
			policy.Status.LastUpgradeAttempt = metav1.Now()

			if err != nil {
				policy.Status.UpgradeStatus = UpgradeStatusFailed
				metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "failed").Inc()
			} else {
				policy.Status.UpgradeStatus = UpgradeStatusInProgress
				metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "success").Inc()
			}
		} else {
			policy.Status.UpgradeStatus = UpgradeStatusSkipped
			metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "skipped").Inc()
		}
	} else {
		policy.Status.TargetAmi = latestRelease
		policy.Status.UpgradeStatus = UpgradeStatusSucceeded
		metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(1)
	}

	policy.Status.LastChecked = metav1.Now()
	policy.Status.CurrentAmi = currentRelease

	return c.Status().Update(ctx, policy)
}
