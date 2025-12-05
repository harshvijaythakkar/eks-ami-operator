package finalizer

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	eksv1alpha1 "github.com/harshvijaythakkar/eks-ami-operator/api/v1alpha1"
	"github.com/harshvijaythakkar/eks-ami-operator/internal/metrics"
)

const NodeGroupFinalizer = "nodegroupupgradepolicy.eks.aws.harsh.dev/finalizer"

func HandleFinalizer(ctx context.Context, c client.Client, policy *eksv1alpha1.NodeGroupUpgradePolicy) (bool, error) {
	logger := logf.FromContext(ctx)

	if !policy.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(policy, NodeGroupFinalizer) {
			logger.Info("Handling finalizer cleanup for NodeGroupUpgradePolicy")
			metrics.DeletedPolicies.WithLabelValues(policy.Spec.ClusterName, policy.Spec.NodeGroupName).Inc()
			controllerutil.RemoveFinalizer(policy, NodeGroupFinalizer)
			if err := c.Update(ctx, policy); err != nil {
				return true, err
			}
		}
		return true, nil
	}

	if !controllerutil.ContainsFinalizer(policy, NodeGroupFinalizer) {
		controllerutil.AddFinalizer(policy, NodeGroupFinalizer)
		if err := c.Update(ctx, policy); err != nil {
			return true, err
		}
	}

	return false, nil
}
