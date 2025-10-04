package controller

import (
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SetCondition updates or adds a condition of the given type
func SetCondition(conditions *[]metav1.Condition, conditionType string, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               conditionType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now(),
	})
}


// Convenience wrapper for AMICompliance condition
func SetAMIComplianceCondition(conditions *[]metav1.Condition, status metav1.ConditionStatus, reason, message string) {
    SetCondition(conditions, "AMICompliance", status, reason, message)
}

// Convenience wrapper for UpgradeInProgress condition
func SetUpgradeCondition(conditions *[]metav1.Condition, status metav1.ConditionStatus, reason, message string) {
    SetCondition(conditions, "UpgradeInProgress", status, reason, message)
}
