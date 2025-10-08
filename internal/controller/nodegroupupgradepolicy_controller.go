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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/eks/types"
	eksv1alpha1 "github.com/harshvijaythakkar/eks-ami-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/harshvijaythakkar/eks-ami-operator/internal/metrics"

	"github.com/cenkalti/backoff/v4"
)

// NodeGroupUpgradePolicyReconciler reconciles a NodeGroupUpgradePolicy object
type NodeGroupUpgradePolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

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

	const nodeGroupFinalizer = "nodegroupupgradepolicy.eks.aws.harsh.dev/finalizer"

	// Fetch the NodeGroupUpgradePolicy resource
	var policy eksv1alpha1.NodeGroupUpgradePolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Handle deletion and finalizer cleanup
	if !policy.ObjectMeta.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&policy, nodeGroupFinalizer) {
			logger.Info("Handling finalizer cleanup for NodeGroupUpgradePolicy")

			// Emit metric
			metrics.DeletedPolicies.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Inc()

			// Emit Kubernetes event
			r.Recorder.Event(&policy, corev1.EventTypeNormal, "FinalizerCleanup", "Cleanup completed before deletion")

			// Log audit info
			logger.Info("Finalizer cleanup completed", "cluster", policy.Spec.ClusterName, "nodegroup", policy.Spec.NodeGroupName)

			controllerutil.RemoveFinalizer(&policy, nodeGroupFinalizer)
			if err := r.Update(ctx, &policy); err != nil {
				logger.Error(err, "failed to remove finalizer")
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&policy, nodeGroupFinalizer) {
		controllerutil.AddFinalizer(&policy, nodeGroupFinalizer)
		if err := r.Update(ctx, &policy); err != nil {
			logger.Error(err, "failed to add finalizer")
			return ctrl.Result{}, err
		}
	}

	// Load AWS configuration (uses default credentials chain — can be IRSA in-cluster)
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		logger.Error(err, "unable to load AWS config")
		return ctrl.Result{}, err
	}

	// Create AWS service clients
	eksClient := eks.NewFromConfig(cfg)
	ssmClient := ssm.NewFromConfig(cfg)

	// Describe the node group to get current AMI and other metadata
	ngOutput, err := retryDescribeNodegroup(ctx, eksClient, &eks.DescribeNodegroupInput{
		ClusterName:   &policy.Spec.ClusterName,
		NodegroupName: &policy.Spec.NodeGroupName,
	})

	if err != nil {
		logger.Error(err, "failed to describe node group")
		return ctrl.Result{}, err
	}

	// Extract current AMI type (for managed node groups, this is usually an enum like AL2_x86_64)
	currentAmi := ngOutput.Nodegroup.AmiType

	// Fetch latest recommended AMI for the cluster version
	// Step 1: Get EKS cluster version
	clusterOutput, err := eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: &policy.Spec.ClusterName,
	})
	if err != nil {
		logger.Error(err, "failed to describe EKS cluster")
		return ctrl.Result{}, err
	}

	eksVersion := *clusterOutput.Cluster.Version
	eksVersion = strings.TrimPrefix(eksVersion, "v") // SSM expects version without 'v'

	// Step 2: Build SSM parameter path
	ssmParam := fmt.Sprintf("/aws/service/eks/optimized-ami/%s/amazon-linux-2/recommended/image_id", eksVersion)

	// Step 3: Fetch latest recommended AMI from SSM
	ssmOutput, err := retryGetParameter(ctx, ssmClient, &ssm.GetParameterInput{
		Name:           &ssmParam,
		WithDecryption: aws.Bool(false),
	})

	if err != nil {
		logger.Error(err, "failed to get recommended AMI from SSM")
		return ctrl.Result{}, err
	}

	latestAmi := *ssmOutput.Parameter.Value

	now := float64(time.Now().Unix())
	metrics.LastCheckedTimestamp.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(now)

	// Reset outdated count for this cluster at the start of reconciliation
	metrics.OutdatedNodeGroups.WithLabelValues(policy.Spec.ClusterName).Set(0)

	// Compare currentAmi with latest recommended AMI
	currentAmiStr := types.AMITypes(latestAmi)
	if currentAmi != currentAmiStr {
		logger.Info("AMI mismatch detected", "currentAmi", currentAmiStr, "latestAmi", latestAmi)

		metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)
		metrics.OutdatedNodeGroups.WithLabelValues(policy.Spec.ClusterName).Inc()

		policy.Status.TargetAmi = latestAmi

		// If outdated and AutoUpgrade is true, trigger UpdateNodegroupVersion
		if policy.Spec.AutoUpgrade {
			logger.Info("AutoUpgrade is enabled, initiating node group update")

			// Trigger node group update to latest AMI version
			_, err := eksClient.UpdateNodegroupVersion(ctx, &eks.UpdateNodegroupVersionInput{
				ClusterName:   &policy.Spec.ClusterName,
				NodegroupName: &policy.Spec.NodeGroupName,
				// No need to specify AMI directly; AWS will use latest recommended AMI
			})
			// err := retryUpdateNodegroupVersion(ctx, eksClient, &eks.UpdateNodegroupVersionInput{
			// 	ClusterName:   &policy.Spec.ClusterName,
			// 	NodegroupName: &policy.Spec.NodeGroupName,
			// 	// No need to specify AMI directly; AWS will use latest recommended AMI
			// })

			if err != nil {
				logger.Error(err, "failed to initiate node group update")
				policy.Status.UpgradeStatus = "Failed"
				SetUpgradeCondition(&policy.Status.Conditions, metav1.ConditionFalse, "UpgradeFailed", "Failed to upgrade node group to latest AMI.")
				// Metric for failed upgrade attempt
				metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "failed").Inc()

			} else {
				policy.Status.UpgradeStatus = "InProgress"
				SetUpgradeCondition(&policy.Status.Conditions, metav1.ConditionTrue, "UpgradeInitiated", "Upgrade to latest AMI initiated.")
				// Metric for successful upgrade initiation
				metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "success").Inc()
			}
		} else {
			logger.Info("AutoUpgrade is disabled, skipping update")
			metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "skipped").Inc()
			policy.Status.UpgradeStatus = "Outdated"
			SetAMIComplianceCondition(&policy.Status.Conditions, metav1.ConditionFalse, "OutdatedAMI", "Node group is using an outdated AMI.")
		}
	} else {
		logger.Info("Node group is using the latest AMI", "ami", currentAmiStr)
		metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(1)
		policy.Status.TargetAmi = latestAmi
		policy.Status.UpgradeStatus = "Succeeded"
		SetUpgradeCondition(&policy.Status.Conditions, metav1.ConditionTrue, "UpgradeSucceeded", "Node group upgrade completed successfully.")
	}

	// Update CR status and Conditions
	SetAMIComplianceCondition(&policy.Status.Conditions, metav1.ConditionTrue, "UpToDate", "Node group is using the latest recommended AMI.")

	// Update status with current AMI and timestamp
	policy.Status.LastChecked = metav1.Now()
	policy.Status.CurrentAmi = string(currentAmi)

	// Save status update to Kubernetes
	if err := r.Status().Update(ctx, &policy); err != nil {
		logger.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}

	// Requeue after specified interval (e.g., 24h) to re-check AMI compliance
	interval, err := time.ParseDuration(policy.Spec.CheckInterval)
	if err != nil {
		logger.Error(err, "invalid checkInterval format, defaulting to 24h")
		interval = 24 * time.Hour
	}
	return ctrl.Result{RequeueAfter: interval}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeGroupUpgradePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Recorder = mgr.GetEventRecorderFor("nodegroupupgradepolicy-controller")
	return ctrl.NewControllerManagedBy(mgr).
		For(&eksv1alpha1.NodeGroupUpgradePolicy{}).
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

func retryDescribeNodegroup(ctx context.Context, eksClient *eks.Client, input *eks.DescribeNodegroupInput) (*eks.DescribeNodegroupOutput, error) {
	var output *eks.DescribeNodegroupOutput

	operation := func() error {
		var err error
		output, err = eksClient.DescribeNodegroup(ctx, input)
		return err
	}

	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.InitialInterval = 2 * time.Second
	expBackoff.MaxElapsedTime = 30 * time.Second

	err := backoff.Retry(operation, expBackoff)
	return output, err
}

func retryGetParameter(ctx context.Context, ssmClient *ssm.Client, input *ssm.GetParameterInput) (*ssm.GetParameterOutput, error) {
	var output *ssm.GetParameterOutput

	operation := func() error {
		var err error
		output, err = ssmClient.GetParameter(ctx, input)
		return err
	}

	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.InitialInterval = 2 * time.Second
	expBackoff.MaxElapsedTime = 30 * time.Second

	err := backoff.Retry(operation, expBackoff)
	return output, err
}
