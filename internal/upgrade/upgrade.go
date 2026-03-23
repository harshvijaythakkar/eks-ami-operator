package upgrade

import (
	"context"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	eksTypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	eksv1alpha1 "github.com/harshvijaythakkar/eks-ami-operator/api/v1alpha1"
	"github.com/harshvijaythakkar/eks-ami-operator/internal/awsutils"
	"github.com/harshvijaythakkar/eks-ami-operator/internal/metrics"
	"github.com/harshvijaythakkar/eks-ami-operator/internal/scheduler"
)

const (
	UpgradeStatusFailed     = "Failed"
	UpgradeStatusInProgress = "InProgress"
	UpgradeStatusSucceeded  = "Succeeded"
	UpgradeStatusOutdated   = "Outdated"
	UpgradeStatusSkipped    = "Skipped"
)

// func ProcessUpgrade(ctx context.Context, c client.Client, policy *eksv1alpha1.NodeGroupUpgradePolicy, currentRelease, latestRelease string, eksClient *eks.Client) error {
// 	logger := logf.FromContext(ctx)

// 	now := float64(time.Now().Unix())
// 	metrics.LastCheckedTimestamp.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(now)

// 	if currentRelease != latestRelease {
// 		logger.Info("Nodegroup is outdated", "current", currentRelease, "latest", latestRelease)
// 		metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)
// 		metrics.OutdatedNodeGroups.WithLabelValues(policy.Spec.ClusterName).Inc()

// 		policy.Status.TargetAmi = latestRelease

// 		if policy.Spec.AutoUpgrade {
// 			_, err := eksClient.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
// 				ClusterName:   &policy.Spec.ClusterName,
// 				NodegroupName: &policy.Spec.NodeGroupName,
// 			})
// 			policy.Status.LastUpgradeAttempt = metav1.Now()

// 			if err != nil {
// 				policy.Status.UpgradeStatus = UpgradeStatusFailed
// 				metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "failed").Inc()
// 			} else {
// 				policy.Status.UpgradeStatus = UpgradeStatusInProgress
// 				metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "success").Inc()
// 			}
// 		} else {
// 			policy.Status.UpgradeStatus = UpgradeStatusSkipped
// 			metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "skipped").Inc()
// 		}
// 	} else {
// 		policy.Status.TargetAmi = latestRelease
// 		policy.Status.UpgradeStatus = UpgradeStatusSucceeded
// 		metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(1)
// 	}

// 	policy.Status.LastChecked = metav1.Now()
// 	policy.Status.CurrentAmi = currentRelease

// 	return c.Status().Update(ctx, policy)
// }

func ProcessUpgrade(ctx context.Context, c client.Client, policy *eksv1alpha1.NodeGroupUpgradePolicy, currentRelease, latestRelease string, eksClient *eks.Client) error {
	logger := logf.FromContext(ctx)

	now := float64(time.Now().Unix())
	metrics.LastCheckedTimestamp.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(now)

	// Keep these fresh each reconcile
	policy.Status.LastChecked = metav1.Now()
	policy.Status.CurrentAmi = currentRelease
	policy.Status.TargetAmi = latestRelease

	// If nothing else happens, lifecycle is "idle" by default for this run; we'll override below as needed.
	metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "idle").Set(1)
	metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)

	// 1) If an update is already in-flight, poll DescribeUpdate
	if policy.Status.UpgradeStatus == UpgradeStatusInProgress && policy.Status.UpdateID != "" {
		outcome, derr := awsutils.DescribeNodegroupUpdate(ctx, eksClient, policy.Spec.ClusterName, policy.Spec.NodeGroupName, policy.Status.UpdateID)
		if derr != nil {
			// Transient read issue; persist the time and keep InProgress
			logger.Error(derr, "DescribeUpdate failed; keeping InProgress", "updateID", policy.Status.UpdateID)
			// Keep lifecycle as in_progress to reflect real state, inflight timer since LastUpgradeAttempt
			if !policy.Status.LastUpgradeAttempt.IsZero() {
				secs := time.Since(policy.Status.LastUpgradeAttempt.Time).Seconds()
				metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(secs)
			}
			metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "in_progress").Set(1)
			return c.Status().Update(ctx, policy)
		}
		policy.Status.UpdateStatus = string(outcome.Status)
		policy.Status.LastProgressCheck = metav1.Now()

		switch outcome.Status {
		case eksTypes.UpdateStatusInProgress:
			// Keep polling on subsequent reconciles
			logger.Info("Managed nodegroup update still InProgress", "updateID", outcome.ID)
			if !policy.Status.LastUpgradeAttempt.IsZero() {
				secs := time.Since(policy.Status.LastUpgradeAttempt.Time).Seconds()
				metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(secs)
			}
			metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "in_progress").Set(1)
			return c.Status().Update(ctx, policy)
		case eksTypes.UpdateStatusSuccessful:
			// Terminal success → mark Succeeded and compliant
			policy.Status.UpgradeStatus = UpgradeStatusSucceeded
			policy.Status.UpdateErrors = nil
			metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(1)
			logger.Info("Managed nodegroup update completed Successfully", "updateID", outcome.ID)
			metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "successful").Set(1)
			metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)
			return c.Status().Update(ctx, policy)
		case eksTypes.UpdateStatusFailed, eksTypes.UpdateStatusCancelled:
			// Terminal failure → capture all AWS errors generically
			policy.Status.UpgradeStatus = UpgradeStatusFailed
			policy.Status.UpdateErrors = outcome.Errors
			metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)
			metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "failed").Inc()
			logger.Info("Managed nodegroup update Failed or Cancelled", "updateID", outcome.ID, "status", outcome.Status, "errors", strings.Join(outcome.Errors, "; "))
			if outcome.Status == eksTypes.UpdateStatusCancelled {
				metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "cancelled").Set(1)
			} else {
				metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "failed").Set(1)
			}
			metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)
			return c.Status().Update(ctx, policy)
		default:
			// Unexpected/rare states → preserve InProgress but record last seen status
			logger.Info("Managed nodegroup update in unexpected state", "updateID", outcome.ID, "status", outcome.Status)
			metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "in_progress").Set(1)
			if !policy.Status.LastUpgradeAttempt.IsZero() {
				secs := time.Since(policy.Status.LastUpgradeAttempt.Time).Seconds()
				metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(secs)
			}
			return c.Status().Update(ctx, policy)
		}
	}

	// 2) No in-flight update → decide compliant vs initiation
	if currentRelease == latestRelease {
		// Compliant
		policy.Status.UpgradeStatus = UpgradeStatusSucceeded
		metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(1)
		metrics.OutdatedNodeGroups.WithLabelValues(policy.Spec.ClusterName).Set(0)
		return c.Status().Update(ctx, policy)
	}

	// Outdated
	logger.Info("Nodegroup is outdated", "current", currentRelease, "latest", latestRelease)
	metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)
	metrics.OutdatedNodeGroups.WithLabelValues(policy.Spec.ClusterName).Inc()
	metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "idle").Set(1)
	metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)

	if !policy.Spec.AutoUpgrade {
		policy.Status.UpgradeStatus = UpgradeStatusSkipped
		metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "skipped").Inc()
		metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "idle").Set(1)
		metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)
		return c.Status().Update(ctx, policy)
	}

	// Initiate managed nodegroup update to the resolved release
	upd, uerr := eksClient.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
		ClusterName:    &policy.Spec.ClusterName,
		NodegroupName:  &policy.Spec.NodeGroupName,
		ReleaseVersion: &latestRelease, // target exact AMI release (official API supports this)
	})
	policy.Status.LastUpgradeAttempt = metav1.Now()
	if uerr != nil {
		policy.Status.UpgradeStatus = UpgradeStatusFailed
		metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "failed").Inc()
		logger.Error(uerr, "UpdateNodegroupVersion failed to initiate")
		metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "failed").Set(1)
		metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)
		return c.Status().Update(ctx, policy)
	}

	// Initiation OK → record UpdateID and mark InProgress
	policy.Status.UpgradeStatus = UpgradeStatusInProgress
	policy.Status.UpdateID = aws.ToString(upd.Update.Id)
	policy.Status.UpdateStatus = "InProgress"
	metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "success").Inc()
	logger.Info("Managed nodegroup update initiated", "updateID", policy.Status.UpdateID, "targetRelease", latestRelease)
	metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "in_progress").Set(1)
	metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)

	// stamp the *next* cron window now so status doesn't point at the boundary we just ran ---
	{
		// Nudge beyond late tolerance (5s) so NextRun returns the next occurrence, not "run now"
		now := time.Now()
		nudge := now.Add(6 * time.Second)
		delay, reason, _ := scheduler.NextRun(nudge, &policy.Status.LastChecked, policy)
		switch reason {
		case scheduler.ReasonCron:
			if delay > 0 {
				policy.Status.NextScheduledTime = metav1.NewTime(now.Add(delay))
			}
		case scheduler.ReasonInterval:
			// Defensive fallback: for valid cron we don't expect this branch
			if delay > 0 {
				policy.Status.NextScheduledTime = metav1.NewTime(now.Add(delay))
			}
		}
	}

	return c.Status().Update(ctx, policy)
}
