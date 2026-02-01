/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package controller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	// "sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/aws/aws-sdk-go-v2/aws"
	// "github.com/aws/aws-sdk-go-v2/service/eks"

	eksv1alpha1 "github.com/harshvijaythakkar/eks-ami-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/harshvijaythakkar/eks-ami-operator/internal/awsutils"
	"github.com/harshvijaythakkar/eks-ami-operator/internal/finalizer"
	"github.com/harshvijaythakkar/eks-ami-operator/internal/metrics"
	"github.com/harshvijaythakkar/eks-ami-operator/internal/scheduler"
	"github.com/harshvijaythakkar/eks-ami-operator/internal/upgrade"
	"github.com/harshvijaythakkar/eks-ami-operator/pkg/awsclient"
)

// NodeGroupUpgradePolicyReconciler reconciles a NodeGroupUpgradePolicy object
type NodeGroupUpgradePolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// const (
// 	UpgradeStatusFailed     = "Failed"
// 	UpgradeStatusInProgress = "InProgress"
// 	UpgradeStatusSucceeded  = "Succeeded"
// 	UpgradeStatusOutdated   = "Outdated"
// 	UpgradeStatusSkipped    = "Skipped"
// )

// +kubebuilder:rbac:groups=eks.aws.harsh.dev,resources=nodegroupupgradepolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=eks.aws.harsh.dev,resources=nodegroupupgradepolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=eks.aws.harsh.dev,resources=nodegroupupgradepolicies/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the NodeGroupUpgradePolicy object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.22.1/pkg/reconcile
func (r *NodeGroupUpgradePolicyReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	// const nodeGroupFinalizer = "nodegroupupgradepolicy.eks.aws.harsh.dev/finalizer"

	// Fetch the NodeGroupUpgradePolicy resource
	var policy eksv1alpha1.NodeGroupUpgradePolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Finalizer handling
	// done, err := r.handleFinalizer(ctx, &policy, nodeGroupFinalizer)
	done, err := finalizer.HandleFinalizer(ctx, r.Client, &policy)
	if done || err != nil {
		return ctrl.Result{}, err
	}

	// Hard guards: paused / startAfter / min upgrade interval
	skip, requeueAfter, err := scheduler.ShouldSkip(&policy)
	if err != nil {
		// ShouldSkip errored -> compute cron-aware next delay anyway
		nd := r.computeNextDelay(ctx, &policy)
		logger.Error(err, "scheduler.ShouldSkip returned error; requeueing via schedule",
			"cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName, "requeueAfter", nd)
		return ctrl.Result{RequeueAfter: nd}, err
	}

	if skip {
		// Respect skip decision (e.g., startAfter future); jitter to avoid herd
		jittered := scheduler.Jitter(requeueAfter)
		logger.Info("Skipping due to scheduling guard (paused/startAfter/minInterval)",
			"cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName, "requeueAfter", jittered)
		return ctrl.Result{RequeueAfter: jittered}, nil
	}

	// Cron/interval decision — if not due, exit early without AWS work (with proper logging)
	if nd, due := r.whenToRunNext(ctx, &policy); !due {
		return ctrl.Result{RequeueAfter: nd}, nil
	}

	// Due now: proceed with logic
	// AWS clients
	clients, err := getAWSClients(ctx, policy.Spec.Region)
	if err != nil {
		nd := r.computeNextDelay(ctx, &policy)
		logger.Error(err, "AWS client creation failed; requeueing via schedule",
			"cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName, "requeueAfter", nd)
		return ctrl.Result{RequeueAfter: nd}, err
	}

	// Describe the node group to get current AMI and other metadata
	ngOutput, err := awsutils.DescribeNodegroup(ctx, clients.EKS, policy.Spec.ClusterName, policy.Spec.NodeGroupName)
	if err != nil {
		nd := r.computeNextDelay(ctx, &policy)
		logger.Error(err, "DescribeNodegroup failed; requeueing via schedule",
			"cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName, "requeueAfter", nd)
		return ctrl.Result{RequeueAfter: nd}, nil
	}

	// Extract current AMI type (for managed node groups, this is usually an enum like AL2_x86_64)
	amiType := ngOutput.Nodegroup.AmiType
	releaseVersion := aws.ToString(ngOutput.Nodegroup.ReleaseVersion)
	logger.Info("Nodegroup metadata", "amiType", amiType, "releaseVersion", releaseVersion)

	// Fetch latest recommended AMI for the cluster version
	eksVersion, err := awsutils.DescribeCluster(ctx, clients.EKS, policy.Spec.ClusterName)
	if err != nil {
		nd := r.computeNextDelay(ctx, &policy)
		logger.Error(err, "DescribeCluster failed; requeueing via schedule",
			"cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName, "requeueAfter", nd)
		return ctrl.Result{RequeueAfter: nd}, err
	}

	latestAmi, latestReleaseVersion, err := awsutils.ResolveLatestAMI(ctx, clients.SSM, ngOutput.Nodegroup.AmiType, eksVersion)
	if err != nil {
		nd := r.computeNextDelay(ctx, &policy)
		logger.Error(err, "ResolveLatestAMI failed; requeueing via schedule",
			"cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName, "requeueAfter", nd)
		return ctrl.Result{RequeueAfter: nd}, nil
	}

	logger.Info("Fetched latest AMI metadata", "latestAmi", latestAmi, "latestReleaseVersion", latestReleaseVersion)

	// now := float64(time.Now().Unix())
	// metrics.LastCheckedTimestamp.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(now)

	// Reset outdated count for this cluster at the start of reconciliation
	metrics.OutdatedNodeGroups.WithLabelValues(policy.Spec.ClusterName).Set(0)

	// Upgrade logic
	// err = r.processUpgrade(ctx, &policy, aws.ToString(ngOutput.Nodegroup.ReleaseVersion), latestReleaseVersion, clients.EKS)
	err = upgrade.ProcessUpgrade(ctx, r.Client, &policy, aws.ToString(ngOutput.Nodegroup.ReleaseVersion), latestReleaseVersion, clients.EKS)
	if err != nil {
		nd := r.computeNextDelay(ctx, &policy)
		logger.Error(err, "processUpgrade failed; requeueing via schedule",
			"cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName, "requeueAfter", nd)
		return ctrl.Result{RequeueAfter: nd}, err
	}

	// Check if enough time has passed since lastChecked
	// if !policy.Status.LastChecked.IsZero() {
	// 	lastCheckedTime := policy.Status.LastChecked.Time
	// 	timeSinceLastCheck := time.Since(lastCheckedTime)
	// 	if timeSinceLastCheck < interval {
	// 		remaining := interval - timeSinceLastCheck
	// 		logger.Info("Requeueing after remaining interval", "remaining", remaining)
	// 		return ctrl.Result{RequeueAfter: remaining}, nil
	// 	}
	// }

	// If no lastChecked or interval has passed, proceed and requeue after full interval
	// logger.Info("Requeueing after full interval", "interval", interval)

	// After a successful run, set observable schedule for next time
	nextDelay := r.computeNextDelay(ctx, &policy)
	policy.Status.LastScheduledTime = metav1.Now()
	policy.Status.NextScheduledTime = metav1.NewTime(time.Now().Add(nextDelay))
	if uerr := r.Status().Update(ctx, &policy); uerr != nil {
		logger.Error(uerr, "failed to update scheduled status after successful run",
			"cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName)
	}
	logger.Info("Completed; requeueing for next schedule",
		"cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName, "requeueAfter", nextDelay)
	return ctrl.Result{RequeueAfter: nextDelay}, nil

	// return ctrl.Result{RequeueAfter: scheduler.ParseInterval(policy.Spec.CheckInterval)}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeGroupUpgradePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	logger := logf.Log.WithName("SetupWithManager")
	go func() {
		<-mgr.Elected()
		logger.Info("This operator instance has become the leader")
	}()
	r.Recorder = mgr.GetEventRecorderFor("nodegroupupgradepolicy-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&eksv1alpha1.NodeGroupUpgradePolicy{}).
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		Named("nodegroupupgradepolicy").
		Complete(r)
}

// func retryUpdateNodegroupVersion(ctx context.Context, eksClient *eks.Client, input *eks.UpdateNodegroupVersionInput) error {
//     operation := func() error {
//         _, err := eksClient.UpdateNodegroupVersion(ctx, input)
//         return err
//     }

//     expBackoff := backoff.NewExponentialBackOff()
//     expBackoff.InitialInterval = 2 * time.Second
//     expBackoff.MaxElapsedTime = 30 * time.Second // You can tune this

//     return backoff.Retry(operation, expBackoff)
// }

// getAWSClients creates AWS clients for EKS and SSM using the region from the CRD.
func getAWSClients(ctx context.Context, region string) (*awsclient.AWSClients, error) {
	return awsclient.NewAWSClients(ctx, region)
}

// func (r *NodeGroupUpgradePolicyReconciler) handleFinalizer(ctx context.Context, policy *eksv1alpha1.NodeGroupUpgradePolicy, finalizerName string) (bool, error) {
// 	logger := logf.FromContext(ctx)

// 	// If deletion timestamp is set, handle cleanup
// 	if !policy.DeletionTimestamp.IsZero() {
// 		if controllerutil.ContainsFinalizer(policy, finalizerName) {
// 			logger.Info("Handling finalizer cleanup for NodeGroupUpgradePolicy")
// 			metrics.DeletedPolicies.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Inc()
// 			r.Recorder.Event(policy, corev1.EventTypeNormal, "FinalizerCleanup", "Cleanup completed before deletion")
// 			controllerutil.RemoveFinalizer(policy, finalizerName)
// 			if err := r.Update(ctx, policy); err != nil {
// 				logger.Error(err, "Failed to remove finalizer")
// 				return true, err
// 			}
// 		}
// 		return true, nil // Done with reconciliation
// 	}

// 	// Add finalizer if not present
// 	if !controllerutil.ContainsFinalizer(policy, finalizerName) {
// 		controllerutil.AddFinalizer(policy, finalizerName)
// 		if err := r.Update(ctx, policy); err != nil {
// 			logger.Error(err, "Failed to add finalizer")
// 			return true, err
// 		}
// 	}

// 	return false, nil // Continue reconciliation
// }

// func (r *NodeGroupUpgradePolicyReconciler) processUpgrade(ctx context.Context, policy *eksv1alpha1.NodeGroupUpgradePolicy, currentRelease, latestRelease string, eksClient *eks.Client) error {
// 	logger := logf.FromContext(ctx)

// 	now := float64(time.Now().Unix())
// 	metrics.LastCheckedTimestamp.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(now)

// 	if currentRelease != latestRelease {
// 		logger.Info("Nodegroup is outdated", "current", currentRelease, "latest", latestRelease)
// 		metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)
// 		metrics.OutdatedNodeGroups.WithLabelValues(policy.Spec.ClusterName).Inc()

// 		policy.Status.TargetAmi = latestRelease

// 		if policy.Spec.AutoUpgrade {
// 			logger.Info("AutoUpgrade enabled, initiating update")
// 			_, err := eksClient.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
// 				ClusterName:   &policy.Spec.ClusterName,
// 				NodegroupName: &policy.Spec.NodeGroupName,
// 			})
// 			policy.Status.LastUpgradeAttempt = metav1.Now()

// 			if err != nil {
// 				logger.Error(err, "Failed to initiate nodegroup update")
// 				policy.Status.UpgradeStatus = UpgradeStatusFailed
// 				SetUpgradeCondition(&policy.Status.Conditions, metav1.ConditionFalse, "UpgradeFailed", "Failed to upgrade node group.")
// 				metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "failed").Inc()
// 			} else {
// 				policy.Status.UpgradeStatus = UpgradeStatusInProgress
// 				SetUpgradeCondition(&policy.Status.Conditions, metav1.ConditionTrue, "UpgradeInitiated", "Upgrade initiated.")
// 				metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "success").Inc()
// 			}
// 		} else {
// 			logger.Info("AutoUpgrade disabled, skipping upgrade")
// 			policy.Status.UpgradeStatus = UpgradeStatusSkipped
// 			SetUpgradeCondition(&policy.Status.Conditions, metav1.ConditionFalse, "UpgradeSkipped", "AutoUpgrade disabled.")
// 			SetAMIComplianceCondition(&policy.Status.Conditions, metav1.ConditionFalse, "OutdatedAMI", "Node group is outdated.")
// 			metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "skipped").Inc()
// 		}
// 	} else {
// 		logger.Info("Nodegroup is compliant", "version", latestRelease)
// 		metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(1)
// 		policy.Status.TargetAmi = latestRelease
// 		policy.Status.UpgradeStatus = UpgradeStatusSucceeded
// 		SetUpgradeCondition(&policy.Status.Conditions, metav1.ConditionTrue, "UpgradeSucceeded", "Node group is up-to-date.")
// 		SetAMIComplianceCondition(&policy.Status.Conditions, metav1.ConditionTrue, "UpToDate", "Node group is using latest AMI.")
// 	}

// 	policy.Status.LastChecked = metav1.Now()
// 	policy.Status.CurrentAmi = currentRelease

// 	return r.Status().Update(ctx, policy)
// }

// whenToRunNext computes the next delay and returns (delay, dueNow bool).
// If dueNow = false -> it logs reason, sets metrics, updates status (best-effort), and returns a jittered delay.
// If dueNow = true  -> logs reason and allows AWS work to proceed.
func (r *NodeGroupUpgradePolicyReconciler) whenToRunNext(ctx context.Context, policy *eksv1alpha1.NodeGroupUpgradePolicy) (time.Duration, bool) {
	logger := logf.FromContext(ctx)
	now := time.Now()

	delay, reason, err := scheduler.NextRun(now, &policy.Status.LastChecked, policy)
	if err != nil {
		// NextRun error means cron/timezone invalid → it already fell back to interval.
		logger.Error(err, "scheduler.NextRun returned error; using fallback delay",
			"reason", reason, "cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName)
	}

	// Jitter and metric
	jittered := scheduler.Jitter(delay)

	if jittered > 0 {
		// Not due yet → update status (best-effort) & log reason
		policy.Status.LastScheduledTime = metav1.Now()
		policy.Status.NextScheduledTime = metav1.NewTime(now.Add(jittered))
		if uerr := r.Status().Update(ctx, policy); uerr != nil {
			logger.Error(uerr, "failed to update scheduled status",
				"reason", reason, "cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName)
		}

		logger.Info("Not due yet; requeueing",
			"reason", reason, "cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName, "requeueAfter", jittered)
		return jittered, false
	}

	// Due now
	logger.Info("Due now; proceeding;",
		"reason", reason, "cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName)
	return 0, true
}

// computeNextDelay returns the next cron/interval-based delay with jitter,
// logs the reason, and emits the metric. Used in all error branches and post-success.
func (r *NodeGroupUpgradePolicyReconciler) computeNextDelay(ctx context.Context, policy *eksv1alpha1.NodeGroupUpgradePolicy) time.Duration {
	logger := logf.FromContext(ctx)
	now := time.Now()

	delay, reason, err := scheduler.NextRun(now, &policy.Status.LastChecked, policy)
	if err != nil {
		// Keep going — NextRun returns interval fallback delay when cron/timezone invalid.
		logger.Error(err, "scheduler.NextRun error while computing next delay",
			"reason", reason, "cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName)
	}

	// If NextRun says “run now” (delay <= 0), use interval fallback to avoid tight loop.
	if delay <= 0 {
		delay = scheduler.ParseInterval(policy.Spec.CheckInterval)
		// Make the reason explicit in logs: interval fallback used
		reason = "interval-fallback-after-immediate"
	}

	jittered := scheduler.Jitter(delay)

	logger.Info("Computed next delay",
		"reason", reason, "cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName, "requeueAfter", jittered)
	return jittered
}
