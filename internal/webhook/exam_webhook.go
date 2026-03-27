package webhook

import (
	"fmt"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
)

// ValidateUpdate checks immutability constraints based on the exam's current phase.
func ValidateUpdate(old, updated *examv1alpha1.Exam) error {
	if old.Status.Phase == examv1alpha1.ExamPhasePending || old.Status.Phase == "" {
		return nil // all changes allowed when Pending
	}

	// Template is immutable after Pending
	if old.Spec.Template.Image != updated.Spec.Template.Image {
		return fmt.Errorf("spec.template.image is immutable after provisioning (current phase: %s)", old.Status.Phase)
	}
	if old.Spec.Template.Port != updated.Spec.Template.Port {
		return fmt.Errorf("spec.template.port is immutable after provisioning (current phase: %s)", old.Status.Phase)
	}

	// Student IDs are immutable after Pending
	if len(old.Spec.Students) != len(updated.Spec.Students) {
		return fmt.Errorf("spec.students list length is immutable after provisioning")
	}
	for i := range old.Spec.Students {
		if old.Spec.Students[i].ID != updated.Spec.Students[i].ID {
			return fmt.Errorf("spec.students[%d].id is immutable after provisioning", i)
		}
	}

	// Unlock time is immutable after Pending
	if !old.Spec.Schedule.Unlock.Equal(&updated.Spec.Schedule.Unlock) {
		return fmt.Errorf("spec.schedule.unlock is immutable after provisioning (current phase: %s)", old.Status.Phase)
	}

	return nil
}
