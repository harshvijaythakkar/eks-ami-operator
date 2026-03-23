package scheduler

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	eksv1alpha1 "github.com/harshvijaythakkar/eks-ami-operator/api/v1alpha1"
	cron "github.com/robfig/cron/v3"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
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

	// minInterval := 6 * time.Hour
	// if !policy.Status.LastUpgradeAttempt.IsZero() {
	// 	lastAttempt := policy.Status.LastUpgradeAttempt
	// 	timeSinceLastAttempt := time.Since(lastAttempt.Time)
	// 	if timeSinceLastAttempt < minInterval {
	// 		return true, minInterval - timeSinceLastAttempt, nil
	// 	}
	// }

	// Apply 6h cooldown (minInterval) ONLY when using interval scheduling.
	// If a cron expression is configured (even if timezone omitted), we must not
	// throttle via cooldown because cron is the source of truth for cadence.
	usingCron := strings.TrimSpace(policy.Spec.ScheduleCron) != ""
	if !usingCron {
		minInterval := 6 * time.Hour
		if !policy.Status.LastUpgradeAttempt.IsZero() {
			lastAttempt := policy.Status.LastUpgradeAttempt
			timeSinceLastAttempt := time.Since(lastAttempt.Time)
			if timeSinceLastAttempt < minInterval {
				return true, minInterval - timeSinceLastAttempt, nil
			}
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
//   - For cron:   compute strictly from "now" (UTC default or requested TZ), return ReasonCron.
//   - For interval: use lastChecked (if present), return ReasonInterval.
func NextRun(now time.Time, lastChecked *metav1.Time, policy *eksv1alpha1.NodeGroupUpgradePolicy) (time.Duration, string, error) {
	// 1) Cron schedule (if provided)
	if policy.Spec.ScheduleCron != "" {
		// Default to UTC
		loc := time.UTC
		if tz := policy.Spec.ScheduleTimezone; tz != "" {
			l, err := time.LoadLocation(tz)
			if err == nil {
				loc = l
			} else {
				// If timezone invalid, fall back to interval but report error
				return nextRunInterval(now, lastChecked, policy), ReasonInterval, fmt.Errorf("invalid scheduleTimezone: %w", err)
			}
			// loc = l
		}

		parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
		schedule, err := parser.Parse(policy.Spec.ScheduleCron)
		if err != nil {
			// Fall back to interval if cron invalid
			return nextRunInterval(now, lastChecked, policy), ReasonInterval, fmt.Errorf("invalid scheduleCron: %w", err)
		}

		base := now.In(loc)

		// Tolerances
		const earlyTolerance = 1 * time.Second // allow firing up to 1s *before* boundary
		const lateTolerance = 5 * time.Second  // allow firing up to 5s  *after*  boundary
		const eps = time.Nanosecond            // to handle "strictly after" semantics

		// EARLY boundary: if we're just before the minute boundary
		next := schedule.Next(base.Add(-eps))
		logger := logf.Log.WithName("scheduler-debug")
		logger.Info("cron-eval",
			"now", now.UTC(), "loc", loc.String(),
			"base", base,
			"next", next, "nextDelta", next.Sub(base),
			"cron", policy.Spec.ScheduleCron, "tz", policy.Spec.ScheduleTimezone,
		)
		if next.Equal(base) || next.Sub(base) <= earlyTolerance {
			return 0, ReasonCron, nil
		}

		// LATE boundary: if we're just *after* the minute boundary
		// Trick to compute the previous occurrence: ask "next" from one minute earlier.
		prev := schedule.Next(base.Add(-time.Minute)).In(loc)
		// 'prev' is the cron time within the last minute window (if any).
		logger.Info("cron-eval",
			"now", now.UTC(), "loc", loc.String(),
			"base", base,
			"next", next, "nextDelta", next.Sub(base),
			"prev", prev, "prevDelta", base.Sub(prev),
			"cron", policy.Spec.ScheduleCron, "tz", policy.Spec.ScheduleTimezone,
		)
		if base.After(prev) && base.Sub(prev) <= lateTolerance {
			return 0, ReasonCron, nil
		}

		// Otherwise: schedule to the next cron slot
		return next.Sub(base), ReasonCron, nil
	}

	// 2) Interval fallback
	return nextRunInterval(now, lastChecked, policy), ReasonInterval, nil
}

func nextRunInterval(now time.Time, lastChecked *metav1.Time, policy *eksv1alpha1.NodeGroupUpgradePolicy) time.Duration {
	d := ParseInterval(policy.Spec.CheckInterval)
	if lastChecked == nil || lastChecked.IsZero() {
		return 0 // first run
	}
	target := lastChecked.Add(d)
	if now.Before(target) {
		return target.Sub(now)
	}
	return 0
}

// Jitter adds ±10% randomization to a duration to avoid thundering herd.
// NOTE: The controller will apply jitter ONLY for ReasonInterval (not for cron).
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
