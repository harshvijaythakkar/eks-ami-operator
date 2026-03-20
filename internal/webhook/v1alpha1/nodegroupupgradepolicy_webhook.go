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

package v1alpha1

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	cron "github.com/robfig/cron/v3"

	"github.com/harshvijaythakkar/eks-ami-operator/pkg/awsclient"

	eksv1alpha1 "github.com/harshvijaythakkar/eks-ami-operator/api/v1alpha1"
)

// nolint:unused
// log is for logging in this package.
var nodegroupupgradepolicylog = logf.Log.WithName("nodegroupupgradepolicy-resource")

// SetupNodeGroupUpgradePolicyWebhookWithManager registers the webhook for NodeGroupUpgradePolicy in the manager.
func SetupNodeGroupUpgradePolicyWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&eksv1alpha1.NodeGroupUpgradePolicy{}).
		WithDefaulter(&eksv1alpha1.NodeGroupUpgradePolicy{}).
		WithValidator(&NodeGroupUpgradePolicyCustomValidator{}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-eks-aws-harsh-dev-v1alpha1-nodegroupupgradepolicy,mutating=false,failurePolicy=fail,sideEffects=None,groups=eks.aws.harsh.dev,resources=nodegroupupgradepolicies,verbs=create;update,versions=v1alpha1,name=vnodegroupupgradepolicy-v1alpha1.kb.io,admissionReviewVersions=v1

// NodeGroupUpgradePolicyCustomValidator struct is responsible for validating the NodeGroupUpgradePolicy resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type NodeGroupUpgradePolicyCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &NodeGroupUpgradePolicyCustomValidator{}

// ---- Shared validation helpers ----
func (v *NodeGroupUpgradePolicyCustomValidator) validateCommonSpec(spec *eksv1alpha1.NodeGroupUpgradePolicySpec) error {
	// Required fields
	if spec.ClusterName == "" {
		return fmt.Errorf("spec.clusterName must not be empty")
	}
	if spec.NodeGroupName == "" {
		return fmt.Errorf("spec.nodeGroupName must not be empty")
	}
	if spec.Region == "" {
		return fmt.Errorf("spec.region must not be empty")
	}

	// checkInterval (if provided) must be a valid Go duration
	if spec.CheckInterval != "" {
		if _, err := time.ParseDuration(spec.CheckInterval); err != nil {
			return fmt.Errorf("spec.checkInterval must be a valid duration string (e.g., '24h'): %w", err)
		}
	}

	// startAfter (if provided) must be RFC3339
	if spec.StartAfter != "" {
		if _, err := time.Parse(time.RFC3339, spec.StartAfter); err != nil {
			return fmt.Errorf("spec.startAfter must be a valid RFC3339 timestamp: %w", err)
		}
	}

	// scheduleTimezone (if provided) must be valid IANA
	if tz := spec.ScheduleTimezone; tz != "" {
		if _, err := time.LoadLocation(tz); err != nil {
			return fmt.Errorf("spec.scheduleTimezone must be a valid IANA timezone: %w", err)
		}
	}

	// scheduleCron (if provided) must be a valid 5-field cron expression
	if spec.ScheduleCron != "" {
		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		if _, err := parser.Parse(spec.ScheduleCron); err != nil {
			return fmt.Errorf("spec.scheduleCron is invalid (expected 5-field cron): %w", err)
		}
	}

	return nil
}

func (v *NodeGroupUpgradePolicyCustomValidator) validateRegion(ctx context.Context, region string) error {
	clients, err := awsclient.NewAWSClients(ctx, region)
	if err != nil {
		nodegroupupgradepolicylog.Error(err, "failed to init AWS clients for region validation")
		return fmt.Errorf("unable to validate spec.region: %w", err)
	}
	ec2Client := clients.EC2

	resp, err := ec2Client.DescribeRegions(ctx, &ec2.DescribeRegionsInput{})
	if err != nil {
		return fmt.Errorf("unable to validate spec.region: %w", err)
	}

	for _, r := range resp.Regions {
		if aws.ToString(r.RegionName) == region {
			return nil
		}
	}
	return fmt.Errorf("spec.region must be a valid AWS region")
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type NodeGroupUpgradePolicy.
func (v *NodeGroupUpgradePolicyCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	nodegroupupgradepolicy, ok := obj.(*eksv1alpha1.NodeGroupUpgradePolicy)
	if !ok {
		return nil, fmt.Errorf("expected a NodeGroupUpgradePolicy object but got %T", obj)
	}
	nodegroupupgradepolicylog.Info("Validation for NodeGroupUpgradePolicy upon creation", "name", nodegroupupgradepolicy.GetName())

	// Spec validations
	if err := v.validateCommonSpec(&nodegroupupgradepolicy.Spec); err != nil {
		return nil, err
	}
	// Region validation via EC2
	if err := v.validateRegion(ctx, nodegroupupgradepolicy.Spec.Region); err != nil {
		return nil, err
	}

	return nil, nil
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type NodeGroupUpgradePolicy.
func (v *NodeGroupUpgradePolicyCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	newnodegroupupgradepolicy, ok := newObj.(*eksv1alpha1.NodeGroupUpgradePolicy)
	if !ok {
		return nil, fmt.Errorf("expected a NodeGroupUpgradePolicy object for the newObj but got %T", newObj)
	}

	oldnodegroupupgradepolicy, ok := oldObj.(*eksv1alpha1.NodeGroupUpgradePolicy)
	if !ok {
		return nil, fmt.Errorf("expected a NodeGroupUpgradePolicy object for the oldObj but got %T", oldObj)
	}

	nodegroupupgradepolicylog.Info("Validation for NodeGroupUpgradePolicy upon update", "name", newnodegroupupgradepolicy.GetName())
	// nodegroupupgradepolicylog.Info("Object received", "old", oldnodegroupupgradepolicy, "new", newnodegroupupgradepolicy)

	if newnodegroupupgradepolicy.Spec.ClusterName != oldnodegroupupgradepolicy.Spec.ClusterName {
		nodegroupupgradepolicylog.Info("ClusterName changed", "old", oldnodegroupupgradepolicy.Spec.ClusterName, "new", newnodegroupupgradepolicy.Spec.ClusterName)
		return nil, fmt.Errorf("spec.clusterName cannot be changed after creation")
	}

	if newnodegroupupgradepolicy.Spec.NodeGroupName != oldnodegroupupgradepolicy.Spec.NodeGroupName {
		nodegroupupgradepolicylog.Info("NodeGroupName changed", "old", oldnodegroupupgradepolicy.Spec.NodeGroupName, "new", newnodegroupupgradepolicy.Spec.NodeGroupName)
		return nil, fmt.Errorf("spec.nodeGroupName cannot be changed after creation")
	}

	if newnodegroupupgradepolicy.Spec.Region != oldnodegroupupgradepolicy.Spec.Region {
		nodegroupupgradepolicylog.Info("Region changed", "old", oldnodegroupupgradepolicy.Spec.Region, "new", newnodegroupupgradepolicy.Spec.Region)
		return nil, fmt.Errorf("spec.region cannot be changed after creation")
	}

	// Reuse create validations for the new spec (cron/timezone/etc.)
	if err := v.validateCommonSpec(&newnodegroupupgradepolicy.Spec); err != nil {
		return nil, err
	}

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type NodeGroupUpgradePolicy.
func (v *NodeGroupUpgradePolicyCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	nodegroupupgradepolicy, ok := obj.(*eksv1alpha1.NodeGroupUpgradePolicy)
	if !ok {
		return nil, fmt.Errorf("expected a NodeGroupUpgradePolicy object but got %T", obj)
	}
	nodegroupupgradepolicylog.Info("Validation for NodeGroupUpgradePolicy upon deletion", "name", nodegroupupgradepolicy.GetName())

	// No delete restrictions for now

	return nil, nil
}
