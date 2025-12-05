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
	// "time"

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
	// metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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

	// Scheduling checks
	skip, requeueAfter, err := scheduler.ShouldSkip(&policy)
	if err != nil {
		logger.Error(err, "Error in scheduling checks")
		return ctrl.Result{}, err
	}

	if skip {
		logger.Info("Skipping reconciliation based on scheduling logic")
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}

	// AWS clients
	clients, err := getAWSClients(ctx, policy.Spec.Region)
	if err != nil {
		logger.Error(err, "Unable to create AWS clients")
		return ctrl.Result{RequeueAfter: scheduler.ParseInterval(policy.Spec.CheckInterval)}, err
	}

	// Describe the node group to get current AMI and other metadata
	ngOutput, err := awsutils.DescribeNodegroup(ctx, clients.EKS, policy.Spec.ClusterName, policy.Spec.NodeGroupName)
	if err != nil {
		return ctrl.Result{RequeueAfter: scheduler.ParseInterval(policy.Spec.CheckInterval)}, nil
	}

	// Extract current AMI type (for managed node groups, this is usually an enum like AL2_x86_64)
	amiType := ngOutput.Nodegroup.AmiType
	releaseVersion := aws.ToString(ngOutput.Nodegroup.ReleaseVersion)
	logger.Info("Nodegroup metadata", "amiType", amiType, "releaseVersion", releaseVersion)

	// Fetch latest recommended AMI for the cluster version
	eksVersion, err := awsutils.DescribeCluster(ctx, clients.EKS, policy.Spec.ClusterName)
	if err != nil {
		return ctrl.Result{RequeueAfter: scheduler.ParseInterval(policy.Spec.CheckInterval)}, err
	}

	latestAmi, latestReleaseVersion, err := awsutils.ResolveLatestAMI(ctx, clients.SSM, ngOutput.Nodegroup.AmiType, eksVersion)
	if err != nil {
		return ctrl.Result{RequeueAfter: scheduler.ParseInterval(policy.Spec.CheckInterval)}, nil
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
		return ctrl.Result{RequeueAfter: scheduler.ParseInterval(policy.Spec.CheckInterval)}, err
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
	return ctrl.Result{RequeueAfter: scheduler.ParseInterval(policy.Spec.CheckInterval)}, nil
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
