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
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/aws/aws-sdk-go-v2/aws"
	// "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/smithy-go"

	// "github.com/aws/aws-sdk-go-v2/service/eks/types"
	eksv1alpha1 "github.com/harshvijaythakkar/eks-ami-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/harshvijaythakkar/eks-ami-operator/internal/metrics"
	"github.com/harshvijaythakkar/eks-ami-operator/pkg/awsclient"

	"github.com/cenkalti/backoff/v4"
)

// NodeGroupUpgradePolicyReconciler reconciles a NodeGroupUpgradePolicy object
type NodeGroupUpgradePolicyReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

const (
	UpgradeStatusFailed     = "Failed"
	UpgradeStatusInProgress = "InProgress"
	UpgradeStatusSucceeded  = "Succeeded"
	UpgradeStatusOutdated   = "Outdated"
	UpgradeStatusSkipped    = "Skipped"
)

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

	// Scheduling checks
	skip, requeueAfter, err := shouldSkip(&policy)
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
		return ctrl.Result{RequeueAfter: parseInterval(policy.Spec.CheckInterval)}, err
	}

	// Use clients
	eksClient := clients.EKS
	ssmClient := clients.SSM

	// Describe the node group to get current AMI and other metadata
	ngOutput, err := retryDescribeNodegroup(ctx, eksClient, &eks.DescribeNodegroupInput{
		ClusterName:   &policy.Spec.ClusterName,
		NodegroupName: &policy.Spec.NodeGroupName,
	})

	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "ResourceNotFoundException" || apiErr.ErrorCode() == "InvalidParameterException") {
			logger.Info("Skipping reconciliation due to permanent nodegroup error", "errorCode", apiErr.ErrorCode(), "nodegroupName", policy.Spec.NodeGroupName)

			// Requeue after a longer interval to retry later in case the config is fixed
			return ctrl.Result{RequeueAfter: 6 * time.Hour}, nil

		}
		logger.Error(err, "failed to describe node group")
		interval, parseErr := time.ParseDuration(policy.Spec.CheckInterval)
		if parseErr != nil {
			interval = 24 * time.Hour
		}
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	// Extract current AMI type (for managed node groups, this is usually an enum like AL2_x86_64)
	// currentAmi := ngOutput.Nodegroup.AmiType
	amiType := ngOutput.Nodegroup.AmiType
	releaseVersion := aws.ToString(ngOutput.Nodegroup.ReleaseVersion)
	logger.Info("Nodegroup metadata", "amiType", amiType, "releaseVersion", releaseVersion)

	// Fetch latest recommended AMI for the cluster version
	// Step 1: Get EKS cluster version
	clusterOutput, err := eksClient.DescribeCluster(ctx, &eks.DescribeClusterInput{
		Name: &policy.Spec.ClusterName,
	})
	if err != nil {
		logger.Error(err, "failed to describe EKS cluster")
		interval, err := time.ParseDuration(policy.Spec.CheckInterval)
		return ctrl.Result{RequeueAfter: interval}, err
	}

	eksVersion := *clusterOutput.Cluster.Version
	eksVersion = strings.TrimPrefix(eksVersion, "v") // SSM expects version without 'v'

	// Step 2: Build dynamic SSM parameter path
	var ssmPath string
	switch amiType {
	case "AL2_x86_64":
		ssmPath = fmt.Sprintf("/aws/service/eks/optimized-ami/%s/amazon-linux-2/recommended", eksVersion)
	case "AL2_ARM_64":
		ssmPath = fmt.Sprintf("/aws/service/eks/optimized-ami/%s/amazon-linux-2-arm64/recommended", eksVersion)
	case "AL2023_x86_64_STANDARD":
		ssmPath = fmt.Sprintf("/aws/service/eks/optimized-ami/%s/amazon-linux-2023/x86_64/standard/recommended", eksVersion)
	case "AL2023_ARM_64_STANDARD":
		ssmPath = fmt.Sprintf("/aws/service/eks/optimized-ami/%s/amazon-linux-2023/arm64/standard/recommended", eksVersion)
	case "CUSTOM":
		logger.Info("Custom AMI detected, skipping SSM lookup")
		// For custom AMIs, we might want to skip or handle differently
		// For now, we'll just log and continue
		return ctrl.Result{}, nil
	default:
		ssmPath = fmt.Sprintf("/aws/service/eks/optimized-ami/%s/amazon-linux-2/recommended", eksVersion)
	}
	logger.Info("Using SSM path", "ssmPath", ssmPath)

	// Step 3: Fetch latest recommended AMI from SSM
	ssmOutput, err := retryGetParameter(ctx, ssmClient, &ssm.GetParameterInput{
		Name:           &ssmPath,
		WithDecryption: aws.Bool(false),
	})

	if err != nil {
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) && (apiErr.ErrorCode() == "ParameterNotFound" || apiErr.ErrorCode() == "InvalidParameter") {
			logger.Info("Skipping reconciliation due to permanent ssm error", "errorCode", apiErr.ErrorCode(), "ssmPath", ssmPath)

			// Requeue after a longer interval to retry later in case the config is fixed
			return ctrl.Result{RequeueAfter: 6 * time.Hour}, nil
		}
		logger.Error(err, "failed to get recommended AMI from SSM")
		interval, parseErr := time.ParseDuration(policy.Spec.CheckInterval)
		if parseErr != nil {
			interval = 24 * time.Hour
		}
		return ctrl.Result{RequeueAfter: interval}, nil
	}

	// latestAmi := *ssmOutput.Parameter.Value

	// Step 4: Parse JSON from SSM parameter
	var amiMetadata struct {
		ImageID        string `json:"image_id"`
		ReleaseVersion string `json:"release_version"`
	}
	if err := json.Unmarshal([]byte(*ssmOutput.Parameter.Value), &amiMetadata); err != nil {
		logger.Error(err, "failed to parse SSM parameter JSON")
		interval, parseErr := time.ParseDuration(policy.Spec.CheckInterval)
		if parseErr != nil {
			interval = 24 * time.Hour
		}
		return ctrl.Result{RequeueAfter: interval}, err
	}

	latestReleaseVersion := amiMetadata.ReleaseVersion
	latestAmi := amiMetadata.ImageID

	logger.Info("Fetched latest AMI metadata", "latestAmi", latestAmi, "latestReleaseVersion", latestReleaseVersion)

	now := float64(time.Now().Unix())
	metrics.LastCheckedTimestamp.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(now)

	// Reset outdated count for this cluster at the start of reconciliation
	metrics.OutdatedNodeGroups.WithLabelValues(policy.Spec.ClusterName).Set(0)

	// Compare currentAmi with latest recommended AMI
	// currentAmiStr := types.AMITypes(latestAmi)
	if releaseVersion != latestReleaseVersion {
		logger.Info("Nodegroup is not using the latest AMI release version", "current", releaseVersion, "latest", latestReleaseVersion)

		metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(0)
		metrics.OutdatedNodeGroups.WithLabelValues(policy.Spec.ClusterName).Inc()

		// policy.Status.TargetAmi = latestAmi
		policy.Status.TargetAmi = latestReleaseVersion

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
				var apiError smithy.APIError
				if errors.As(err, &apiError) {
					logger.Error(err, "AWS API error during node group update", "code", apiError.ErrorCode(), "message", apiError.ErrorMessage())
				} else {
					logger.Error(err, "failed to initiate node group update")
				}
				policy.Status.UpgradeStatus = UpgradeStatusFailed
				policy.Status.LastUpgradeAttempt = metav1.Now()
				SetUpgradeCondition(&policy.Status.Conditions, metav1.ConditionFalse, "UpgradeFailed", "Failed to upgrade node group to latest AMI.")
				// Metric for failed upgrade attempt
				metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "failed").Inc()

			} else {
				policy.Status.UpgradeStatus = UpgradeStatusInProgress
				policy.Status.LastUpgradeAttempt = metav1.Now()
				SetUpgradeCondition(&policy.Status.Conditions, metav1.ConditionTrue, "UpgradeInitiated", "Upgrade to latest AMI initiated.")
				// Metric for successful upgrade initiation
				metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "success").Inc()
			}
		} else {
			logger.Info("AutoUpgrade is disabled, upgrade will not be triggered", "name", policy.Name)
			metrics.UpgradeAttempts.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName, "skipped").Inc()
			policy.Status.UpgradeStatus = UpgradeStatusSkipped
			policy.Status.LastChecked = metav1.Now()
			policy.Status.CurrentAmi = releaseVersion
			SetUpgradeCondition(&policy.Status.Conditions, metav1.ConditionFalse, "UpgradeSkipped", "AutoUpgrade is disabled; upgrade was skipped.")
			SetAMIComplianceCondition(&policy.Status.Conditions, metav1.ConditionFalse, "OutdatedAMI", "Node group is using an outdated AMI.")
		}
	} else {
		logger.Info("Node group is compliant with latest AMI release version", "version", latestReleaseVersion)
		metrics.ComplianceStatus.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Set(1)
		// policy.Status.TargetAmi = latestAmi
		policy.Status.TargetAmi = latestReleaseVersion
		policy.Status.UpgradeStatus = UpgradeStatusSucceeded
		SetUpgradeCondition(&policy.Status.Conditions, metav1.ConditionTrue, "UpgradeSucceeded", "Node group upgrade completed successfully.")
		SetAMIComplianceCondition(&policy.Status.Conditions, metav1.ConditionTrue, "UpToDate", "Node group is using the latest recommended AMI.")
		policy.Status.LastChecked = metav1.Now()
		policy.Status.CurrentAmi = releaseVersion
	}

	// Save status update to Kubernetes
	if err := r.Status().Update(ctx, &policy); err != nil {
		logger.Error(err, "failed to update status")
		interval, parseErr := time.ParseDuration(policy.Spec.CheckInterval)
		if parseErr != nil {
			interval = 24 * time.Hour
		}
		return ctrl.Result{RequeueAfter: interval}, err
	}

	// Requeue after specified interval (e.g., 24h) to re-check AMI compliance
	interval, err := time.ParseDuration(policy.Spec.CheckInterval)
	if err != nil {
		logger.Error(err, "invalid checkInterval format, defaulting to 24h")
		interval = 24 * time.Hour
	}

	// Check if enough time has passed since lastChecked
	if !policy.Status.LastChecked.IsZero() {
		lastCheckedTime := policy.Status.LastChecked.Time
		timeSinceLastCheck := time.Since(lastCheckedTime)
		if timeSinceLastCheck < interval {
			remaining := interval - timeSinceLastCheck
			logger.Info("Requeueing after remaining interval", "remaining", remaining)
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
	}

	// If no lastChecked or interval has passed, proceed and requeue after full interval
	logger.Info("Requeueing after full interval", "interval", interval)
	return ctrl.Result{RequeueAfter: interval}, nil
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

func retryDescribeNodegroup(ctx context.Context, eksClient *eks.Client, input *eks.DescribeNodegroupInput) (*eks.DescribeNodegroupOutput, error) {
	var output *eks.DescribeNodegroupOutput

	operation := func() error {
		var err error
		output, err = eksClient.DescribeNodegroup(ctx, input)
		if err != nil {
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				switch apiErr.ErrorCode() {
				case "ResourceNotFoundException", "InvalidParameterException":
					logf.FromContext(ctx).Error(err, "Permanent error during DescribeNodegroup, skipping retries", "errorCode", apiErr.ErrorCode(), "clusterName", *input.ClusterName, "nodegroupName", *input.NodegroupName)
					// Return backoff.Permanent to stop retrying
					return backoff.Permanent(err)
				}
			}
		}
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
		if err != nil {
			var apiErr smithy.APIError
			if errors.As(err, &apiErr) {
				switch apiErr.ErrorCode() {
				case "ParameterNotFound", "InvalidParameter":
					logf.FromContext(ctx).Error(err, "Permanent error during GetParameter, skipping retries", "errorCode", apiErr.ErrorCode(), "parameterName", *input.Name)
					return backoff.Permanent(err)
				}
			}
		}
		return err
	}

	expBackoff := backoff.NewExponentialBackOff()
	expBackoff.InitialInterval = 2 * time.Second
	expBackoff.MaxElapsedTime = 30 * time.Second

	err := backoff.Retry(operation, expBackoff)
	return output, err
}

// shouldSkip checks if reconciliation should be skipped based on Paused, StartAfter, and minInterval logic.
func shouldSkip(policy *eksv1alpha1.NodeGroupUpgradePolicy) (bool, time.Duration, error) {
	if policy.Spec.Paused {
		return true, 0, nil
	}

	if policy.Spec.StartAfter != "" {
		startAfterTime, err := time.Parse(time.RFC3339, policy.Spec.StartAfter)
		if err != nil {
			return false, 0, fmt.Errorf("invalid StartAfter format: %w", err)
		}
		if time.Now().Before(startAfterTime) {
			return true, time.Until(startAfterTime), nil
		}
	}

	minInterval := 6 * time.Hour
	if !policy.Status.LastUpgradeAttempt.IsZero() {
		lastAttempt := policy.Status.LastUpgradeAttempt.Time
		timeSinceLastAttempt := time.Since(lastAttempt)
		if timeSinceLastAttempt < minInterval {
			return true, minInterval - timeSinceLastAttempt, nil
		}
	}

	return false, 0, nil
}

// getAWSClients creates AWS clients for EKS and SSM using the region from the CRD.
func getAWSClients(ctx context.Context, region string) (*awsclient.AWSClients, error) {
	return awsclient.NewAWSClients(ctx, region)
}

// parseInterval safely parses the CheckInterval string, defaults to 24h if invalid.
func parseInterval(intervalStr string) time.Duration {
	interval, err := time.ParseDuration(intervalStr)
	if err != nil {
		return 24 * time.Hour
	}
	return interval
}
