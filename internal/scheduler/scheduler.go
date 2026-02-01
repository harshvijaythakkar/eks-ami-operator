package scheduler

import (
	"fmt"
	"math/rand"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	eksv1alpha1 "github.com/harshvijaythakkar/eks-ami-operator/api/v1alpha1"
	cron "github.com/robfig/cron/v3"
)

// Reasons returned by NextRun for logging/metrics consistency.
const (
	ReasonCron     = "cron"
	ReasonInterval = "interval"
)

// package-level RNG, seeded once
var rng = rand.New(rand.NewSource(time.Now().UnixNano()))

// ShouldSkip preserves existing pause/startAfter/minInterval behavior.
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
		lastAttempt := policy.Status.LastUpgradeAttempt
		timeSinceLastAttempt := time.Since(lastAttempt.Time) // using .Time here is fine; not part of QF1008
		if timeSinceLastAttempt < minInterval {
			return true, minInterval - timeSinceLastAttempt, nil
		}
	}

	return false, 0, nil
}

// ParseInterval safely parses the CheckInterval string, defaults to 24h if invalid.
func ParseInterval(intervalStr string) time.Duration {
	interval, err := time.ParseDuration(intervalStr)
	if err != nil {
		return 24 * time.Hour
	}
	return interval
}

// NextRun decides when the next reconciliation should occur based on cron or interval.
// Precedence outside: paused -> startAfter -> (this function).
// If a run is due now, returns 0.
func NextRun(now time.Time, lastChecked *metav1.Time, policy *eksv1alpha1.NodeGroupUpgradePolicy) (time.Duration, string, error) {
	// 1) Cron schedule (if provided)
	if policy.Spec.ScheduleCron != "" {
		// Default to UTC for predictability
		loc := time.UTC
		if tz := policy.Spec.ScheduleTimezone; tz != "" {
			l, err := time.LoadLocation(tz)
			if err == nil {
				loc = l
			} else {
				// If timezone invalid, fall back to interval but report error
				return nextRunInterval(now, lastChecked, policy), ReasonInterval, fmt.Errorf("invalid scheduleTimezone: %w", err)
			}
		}

		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		schedule, err := parser.Parse(policy.Spec.ScheduleCron)
		if err != nil {
			// Fall back to interval if cron invalid
			return nextRunInterval(now, lastChecked, policy), ReasonInterval, fmt.Errorf("invalid scheduleCron: %w", err)
		}

		base := now.In(loc)
		if lastChecked != nil && !lastChecked.IsZero() {
			// QF1008 fix: use promoted method on embedded time.Time instead of .Time.In(loc)
			base = lastChecked.In(loc)
		}
		next := schedule.Next(base)
		if now.In(loc).Before(next) {
			return next.Sub(now.In(loc)), ReasonCron, nil
		}
		return 0, ReasonCron, nil // run now
	}

	// 2) Interval fallback
	return nextRunInterval(now, lastChecked, policy), ReasonInterval, nil
}

func nextRunInterval(now time.Time, lastChecked *metav1.Time, policy *eksv1alpha1.NodeGroupUpgradePolicy) time.Duration {
	d := ParseInterval(policy.Spec.CheckInterval)
	if lastChecked == nil || lastChecked.IsZero() {
		return 0 // first run
	}
	// QF1008 fix: use promoted method on embedded time.Time instead of .Time.Add(d)
	target := lastChecked.Add(d)
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
		// subtract
		return d - delta
	}
	// add
	return d + delta
}
