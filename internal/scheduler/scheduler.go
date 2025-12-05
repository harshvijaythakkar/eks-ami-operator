package scheduler

import (
	"fmt"
	"time"

	eksv1alpha1 "github.com/harshvijaythakkar/eks-ami-operator/api/v1alpha1"
)

// shouldSkip checks if reconciliation should be skipped based on Paused, StartAfter, and minInterval logic.
func ShouldSkip(policy *eksv1alpha1.NodeGroupUpgradePolicy) (bool, time.Duration, error) {
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

// parseInterval safely parses the CheckInterval string, defaults to 24h if invalid.
func ParseInterval(intervalStr string) time.Duration {
	interval, err := time.ParseDuration(intervalStr)
	if err != nil {
		return 24 * time.Hour
	}
	return interval
}
