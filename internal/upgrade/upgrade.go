package upgrade

import (
	"context"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	eksTypes "github.com/aws/aws-sdk-go-v2/service/eks/types"
	eksv1alpha1 "github.com/harshvijaythakkar/eks-ami-operator/api/v1alpha1"
	"github.com/harshvijaythakkar/eks-ami-operator/internal/awsutils"
	"github.com/harshvijaythakkar/eks-ami-operator/internal/metrics"
	"github.com/harshvijaythakkar/eks-ami-operator/internal/scheduler"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	UpgradeStatusFailed     = "Failed"
	UpgradeStatusInProgress = "InProgress"
	UpgradeStatusSucceeded  = "Succeeded"
	UpgradeStatusOutdated   = "Outdated"
	UpgradeStatusSkipped    = "Skipped"
)

// --- logger adapter so helpers don’t import logf again ---
type logrLogger interface {
	Info(msg string, keysAndValues ...any)
	Error(err error, msg string, keysAndValues ...any)
}

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

// ProcessUpgrade performs three steps:
//
//  1. update common status fields + base metrics
//  2. if in-flight => poll DescribeUpdate and set terminal status when done
//  3. if not in-flight => decide compliant/outdated and possibly initiate an update
func ProcessUpgrade(ctx context.Context, c client.Client, policy *eksv1alpha1.NodeGroupUpgradePolicy, currentRelease, latestRelease string, eksClient *eks.Client) error {
	logger := logf.FromContext(ctx)

	// Always update "last checked" metrics / status
	nowUnix := float64(time.Now().Unix())
	metrics.LastCheckedTimestamp.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(nowUnix)

	policy.Status.LastChecked = metav1.Now()
	policy.Status.CurrentAmi = currentRelease
	policy.Status.TargetAmi = latestRelease

	// Default lifecycle state for this run unless overridden below.
	metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "idle").Set(1)
	metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)

	// 1) If update already in-flight: track it and return.
	if handled, err := trackInFlightUpdate(ctx, c, policy, eksClient, logger); handled {
		return err
	}

	// 2) Otherwise decide compliance / initiate update if needed.
	return handleNotInFlight(ctx, c, policy, currentRelease, latestRelease, eksClient, logger)
}

func trackInFlightUpdate(ctx context.Context, c client.Client, policy *eksv1alpha1.NodeGroupUpgradePolicy, eksClient *eks.Client, logger logrLogger) (bool, error) {
	if policy.Status.UpgradeStatus != UpgradeStatusInProgress || policy.Status.UpdateID == "" {
		return false, nil
	}

	outcome, derr := awsutils.DescribeNodegroupUpdate(ctx, eksClient, policy.Spec.ClusterName, policy.Spec.NodeGroupName, policy.Status.UpdateID)
	if derr != nil {
		// transient read issue; keep InProgress and let controller short-poll again
		logger.Error(derr, "DescribeUpdate failed; keeping InProgress", "updateID", policy.Status.UpdateID)

		metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "in_progress").Set(1)
		if !policy.Status.LastUpgradeAttempt.IsZero() {
			secs := time.Since(policy.Status.LastUpgradeAttempt.Time).Seconds()
			metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(secs)
		}
		return true, c.Status().Update(ctx, policy)
	}

	// Persist what EKS reports
	policy.Status.UpdateStatus = string(outcome.Status)
	policy.Status.LastProgressCheck = metav1.Now()

	switch outcome.Status {
	case eksTypes.UpdateStatusInProgress:
		logger.Info("Managed nodegroup update still InProgress", "updateID", outcome.ID)

		metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "in_progress").Set(1)
		if !policy.Status.LastUpgradeAttempt.IsZero() {
			secs := time.Since(policy.Status.LastUpgradeAttempt.Time).Seconds()
			metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(secs)
		}
		return true, c.Status().Update(ctx, policy)

	case eksTypes.UpdateStatusSuccessful:
		// Terminal success
		policy.Status.UpgradeStatus = UpgradeStatusSucceeded
		policy.Status.UpdateErrors = nil

		metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(1)
		metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "successful").Set(1)
		metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)

		logger.Info("Managed nodegroup update completed Successfully", "updateID", outcome.ID)
		return true, c.Status().Update(ctx, policy)

	case eksTypes.UpdateStatusFailed:
		return true, setTerminalFailure(ctx, c, policy, outcome, "failed", logger)

	case eksTypes.UpdateStatusCancelled:
		return true, setTerminalFailure(ctx, c, policy, outcome, "cancelled", logger)

	default:
		// Unknown/rare status -> treat as still in progress for safety
		logger.Info("Managed nodegroup update in unexpected state",
			"updateID", outcome.ID, "status", outcome.Status)

		metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "in_progress").Set(1)
		if !policy.Status.LastUpgradeAttempt.IsZero() {
			secs := time.Since(policy.Status.LastUpgradeAttempt.Time).Seconds()
			metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(secs)
		}
		return true, c.Status().Update(ctx, policy)
	}
}

func setTerminalFailure(ctx context.Context, c client.Client, policy *eksv1alpha1.NodeGroupUpgradePolicy, outcome *awsutils.UpdateOutcome, lifecycleStatus string, logger logrLogger) error {
	policy.Status.UpgradeStatus = UpgradeStatusFailed
	policy.Status.UpdateErrors = outcome.Errors

	metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)
	metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "failed").Inc()

	metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, lifecycleStatus).Set(1)
	metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)

	logger.Info("Managed nodegroup update terminal failure", "updateID", outcome.ID, "status", outcome.Status, "errors", strings.Join(outcome.Errors, "; "))

	return c.Status().Update(ctx, policy)
}

func handleNotInFlight(ctx context.Context, c client.Client, policy *eksv1alpha1.NodeGroupUpgradePolicy, currentRelease string, latestRelease string, eksClient *eks.Client, logger logrLogger) error {
	// Compliant
	if currentRelease == latestRelease {
		policy.Status.UpgradeStatus = UpgradeStatusSucceeded
		metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(1)
		metrics.OutdatedNodeGroups.WithLabelValues(policy.Spec.ClusterName).Set(0)

		metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "idle").Set(1)
		metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)

		return c.Status().Update(ctx, policy)
	}

	// Outdated
	logger.Info("Nodegroup is outdated", "current", currentRelease, "latest", latestRelease)
	metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)
	metrics.OutdatedNodeGroups.WithLabelValues(policy.Spec.ClusterName).Inc()

	// Auto-upgrade disabled -> skipped
	if !policy.Spec.AutoUpgrade {
		policy.Status.UpgradeStatus = UpgradeStatusSkipped
		metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "skipped").Inc()
		metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "idle").Set(1)
		metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)
		return c.Status().Update(ctx, policy)
	}

	// Initiate update to exact resolved release version
	upd, uerr := eksClient.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
		ClusterName:    aws.String(policy.Spec.ClusterName),
		NodegroupName:  aws.String(policy.Spec.NodeGroupName),
		ReleaseVersion: aws.String(latestRelease),
	})
	policy.Status.LastUpgradeAttempt = metav1.Now()

	if uerr != nil {
		policy.Status.UpgradeStatus = UpgradeStatusFailed
		metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "failed").Inc()

		metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "failed").Set(1)
		metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)

		logger.Error(uerr, "UpdateNodegroupVersion failed to initiate")
		return c.Status().Update(ctx, policy)
	}

	// Initiation OK -> record UpdateID and mark InProgress
	policy.Status.UpgradeStatus = UpgradeStatusInProgress
	policy.Status.UpdateID = aws.ToString(upd.Update.Id)
	policy.Status.UpdateStatus = string(eksTypes.UpdateStatusInProgress)

	metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "success").Inc()
	logger.Info("Managed nodegroup update initiated", "updateID", policy.Status.UpdateID, "targetRelease", latestRelease)
	metrics.UpdateStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "in_progress").Set(1)
	metrics.UpdateInflightSeconds.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)

	logger.Info("Managed nodegroup update initiated",
		"updateID", policy.Status.UpdateID, "targetRelease", latestRelease)

	// Option A: stamp nextScheduledTime to the next cron window (so it doesn't point at the boundary we just ran)
	stampNextCronWindow(policy)

	return c.Status().Update(ctx, policy)
}

func stampNextCronWindow(policy *eksv1alpha1.NodeGroupUpgradePolicy) {
	// Only meaningful if cron is configured
	if strings.TrimSpace(policy.Spec.ScheduleCron) == "" {
		return
	}
	now := time.Now()
	nudge := now.Add(6 * time.Second) // beyond late tolerance in scheduler.NextRun
	delay, reason, _ := scheduler.NextRun(nudge, &policy.Status.LastChecked, policy)
	if reason == scheduler.ReasonCron && delay > 0 {
		policy.Status.NextScheduledTime = metav1.NewTime(now.Add(delay))
	}
}
