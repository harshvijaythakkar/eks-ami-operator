package scheduler

import (
	"fmt"
	"math/rand"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	eksv1alpha1 "github.com/harshvijaythakkar/eks-ami-operator/api/v1alpha1"
	cron "github.com/robfig/cron/v3"
)

// package-level RNG, seeded once
var rng = rand.New(rand.NewSource(time.Now().UnixNano()))

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

// NextRun decides when the next reconciliation should occur based on cron or interval.
// Precedence: paused > startAfter > cron > interval. If a run is due now, returns 0.
func NextRun(now time.Time, lastChecked *metav1.Time, policy *eksv1alpha1.NodeGroupUpgradePolicy) (time.Duration, string, error) {
	// Cron schedule (if provided)
	if policy.Spec.ScheduleCron != "" {
		// Load timezone
		// loc := time.Local
		loc := time.UTC
		if tz := policy.Spec.ScheduleTimezone; tz != "" {
			l, err := time.LoadLocation(tz)
			if err == nil {
				loc = l
			} else {
				// If timezone invalid, fall back to interval but report error
				return nextRunInterval(now, lastChecked, policy), "interval", fmt.Errorf("invalid scheduleTimezone: %w", err)
			}
		}

		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		schedule, err := parser.Parse(policy.Spec.ScheduleCron)
		if err != nil {
			// Fall back to interval if cron invalid
			return nextRunInterval(now, lastChecked, policy), "interval", fmt.Errorf("invalid scheduleCron: %w", err)
		}

		base := now.In(loc)
		if lastChecked != nil && !lastChecked.IsZero() {
			base = lastChecked.Time.In(loc)
		}
		next := schedule.Next(base)
		if now.In(loc).Before(next) {
			return next.Sub(now.In(loc)), "cron", nil
		}
		return 0, "cron", nil // run now
	}

	// Interval fallback
	return nextRunInterval(now, lastChecked, policy), "interval", nil
}

func nextRunInterval(now time.Time, lastChecked *metav1.Time, policy *eksv1alpha1.NodeGroupUpgradePolicy) time.Duration {
	d := ParseInterval(policy.Spec.CheckInterval)
	if lastChecked == nil || lastChecked.IsZero() {
		return 0 // first run
	}
	target := lastChecked.Time.Add(d)
	if now.Before(target) {
		return target.Sub(now)
	}
	return 0
}

// Jitter adds ±10% randomization to a duration to avoid thundering herd.
func Jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	// max jitter 10% of d
	max := int64(d) / 10
	if max <= 0 {
		return d
	}
	delta := time.Duration(rng.Int63n(max))
	if rng.Intn(2) == 0 {
		return d - delta // subtract
	}
	return d + delta // add
}
