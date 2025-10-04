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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	eksv1alpha1 "github.com/harshvijaythakkar/eks-ami-operator/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NodeGroupUpgradePolicyReconciler reconciles a NodeGroupUpgradePolicy object
type NodeGroupUpgradePolicyReconciler struct {
	client.Client
	Scheme *runtime.Scheme
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

	// TODO(user): your logic here

	// Fetch the NodeGroupUpgradePolicy resource
	var policy eksv1alpha1.NodeGroupUpgradePolicy
	if err := r.Get(ctx, req.NamespacedName, &policy); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Load AWS configuration (uses default credentials chain — can be IRSA in-cluster)
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		logger.Error(err, "unable to load AWS config")
		return ctrl.Result{}, err
	}

	// Create AWS service clients
	eksClient := eks.NewFromConfig(cfg)
	ec2Client := ec2.NewFromConfig(cfg)

	// Describe the node group to get current AMI and other metadata
	ngOutput, err := eksClient.DescribeNodegroup(ctx, &eks.DescribeNodegroupInput{
		ClusterName:   &policy.Spec.ClusterName,
		NodegroupName: &policy.Spec.NodeGroupName,
	})

	if err != nil {
		logger.Error(err, "failed to describe node group")
		return ctrl.Result{}, err
	}

	// Extract current AMI type (for managed node groups, this is usually an enum like AL2_x86_64)
	currentAmi := ngOutput.Nodegroup.AmiType

	// TODO: Fetch latest recommended AMI for the cluster version
	// TODO: Compare currentAmi with latest recommended AMI
	// TODO: If outdated and AutoUpgrade is true, trigger UpdateNodegroupVersion
	// TODO: Update CR status and Conditions

	// Update status with current AMI and timestamp
	policy.Status.LastChecked = metav1.Now()
	policy.Status.CurrentAmi = string(currentAmi)

	// Save status update to Kubernetes
	if err := r.Status().Update(ctx, &policy); err != nil {
		logger.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}

	// Requeue after specified interval (e.g., 24h) to re-check AMI compliance
	return ctrl.Result{RequeueAfter: time.Hour * 24}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodeGroupUpgradePolicyReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&eksv1alpha1.NodeGroupUpgradePolicy{}).
		Named("nodegroupupgradepolicy").
		Complete(r)
}
