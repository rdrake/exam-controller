/*
Copyright 2026.

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
	"math"
	"reflect"
	"time"

	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupExamWebhookWithManager registers the validating webhook with the manager.
func SetupExamWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &Exam{}).
		WithValidator(&examValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-exam-otu-ca-v1alpha1-exam,mutating=false,failurePolicy=fail,sideEffects=None,groups=exam.otu.ca,resources=exams,verbs=create;update,versions=v1alpha1,name=vexam-v1alpha1.kb.io,admissionReviewVersions=v1

type examValidator struct{}

func (v *examValidator) ValidateCreate(_ context.Context, exam *Exam) (admission.Warnings, error) {
	if len(exam.Spec.Students) == 0 {
		return nil, fmt.Errorf("spec.students must have at least one entry")
	}
	if exam.Spec.Schedule.Duration.Duration <= 0 {
		return nil, fmt.Errorf("spec.schedule.duration must be > 0")
	}
	mult := exam.Spec.Schedule.TimeMultiplier
	if mult == 0 {
		mult = 1.5
	}
	if mult < 1.0 {
		return nil, fmt.Errorf("spec.schedule.timeMultiplier must be >= 1.0")
	}
	if exam.Spec.Email.InstructorEmail == "" {
		return nil, fmt.Errorf("spec.email.instructorEmail is required")
	}
	if exam.Spec.Spares < 0 {
		return nil, fmt.Errorf("spec.spares must be >= 0")
	}

	rateLimit := exam.Spec.Email.RateLimit
	if rateLimit <= 0 {
		rateLimit = 1
	}
	emailBefore := exam.Spec.Email.Before.Duration
	if emailBefore == 0 {
		emailBefore = 30 * time.Minute
	}
	minEmailTime := math.Ceil(float64(len(exam.Spec.Students))/float64(rateLimit)) * 1.5
	if emailBefore.Seconds() < minEmailTime {
		return nil, fmt.Errorf("spec.email.before (%v) is too short to send %d emails at %d/s (need %.0fs with retry buffer)",
			emailBefore, len(exam.Spec.Students), rateLimit, minEmailTime)
	}

	provisionBefore := exam.Spec.Schedule.ProvisionBefore.Duration
	if provisionBefore == 0 {
		provisionBefore = 1 * time.Hour
	}
	if provisionBefore <= emailBefore {
		return nil, fmt.Errorf("spec.schedule.provisionBefore (%v) must be greater than spec.email.before (%v)",
			provisionBefore, emailBefore)
	}

	if errs := validation.IsDNS1123Subdomain(exam.Spec.Domain); len(errs) > 0 {
		return nil, fmt.Errorf("spec.domain %q is not a valid DNS domain: %s", exam.Spec.Domain, errs[0])
	}
	for i, s := range exam.Spec.Students {
		if errs := validation.IsValidLabelValue(s.ID); len(errs) > 0 {
			return nil, fmt.Errorf("spec.students[%d].id %q is not a valid label value: %s", i, s.ID, errs[0])
		}
	}

	return nil, nil
}

func (v *examValidator) ValidateUpdate(_ context.Context, oldExam, newExam *Exam) (admission.Warnings, error) {
	phase := oldExam.Status.Phase
	if phase == ExamPhasePending || phase == "" {
		return nil, nil
	}

	// Fields immutable after Pending
	if oldExam.Spec.Template.Image != newExam.Spec.Template.Image {
		return nil, fmt.Errorf("spec.template.image is immutable after provisioning (current phase: %s)", phase)
	}
	if oldExam.Spec.Template.Port != newExam.Spec.Template.Port {
		return nil, fmt.Errorf("spec.template.port is immutable after provisioning (current phase: %s)", phase)
	}
	if !reflect.DeepEqual(oldExam.Spec.Template.Resources, newExam.Spec.Template.Resources) {
		return nil, fmt.Errorf("spec.template.resources is immutable after provisioning (current phase: %s)", phase)
	}
	if !oldExam.Spec.Schedule.Unlock.Equal(&newExam.Spec.Schedule.Unlock) {
		return nil, fmt.Errorf("spec.schedule.unlock is immutable after provisioning (current phase: %s)", phase)
	}
	if oldExam.Spec.Spares != newExam.Spec.Spares {
		return nil, fmt.Errorf("spec.spares is immutable after provisioning (current phase: %s)", phase)
	}
	if oldExam.Spec.Domain != newExam.Spec.Domain {
		return nil, fmt.Errorf("spec.domain is immutable after provisioning (current phase: %s)", phase)
	}
	if len(oldExam.Spec.Students) != len(newExam.Spec.Students) {
		return nil, fmt.Errorf("spec.students list length is immutable after provisioning")
	}
	for i := range oldExam.Spec.Students {
		if oldExam.Spec.Students[i].ID != newExam.Spec.Students[i].ID {
			return nil, fmt.Errorf("spec.students[%d].id is immutable after provisioning", i)
		}
	}

	// Duration and TimeMultiplier are immutable after Locked
	if phase == ExamPhaseLocked || phase == ExamPhaseTearingDown {
		if oldExam.Spec.Schedule.Duration != newExam.Spec.Schedule.Duration {
			return nil, fmt.Errorf("spec.schedule.duration is immutable after locking (current phase: %s)", phase)
		}
		if oldExam.Spec.Schedule.TimeMultiplier != newExam.Spec.Schedule.TimeMultiplier {
			return nil, fmt.Errorf("spec.schedule.timeMultiplier is immutable after locking (current phase: %s)", phase)
		}
	}

	// Lock time guard: if duration or multiplier changed while Unlocked,
	// the computed lock time must not be in the past.
	if phase == ExamPhaseUnlocked {
		if oldExam.Spec.Schedule.Duration != newExam.Spec.Schedule.Duration ||
			oldExam.Spec.Schedule.TimeMultiplier != newExam.Spec.Schedule.TimeMultiplier {
			mult := newExam.Spec.Schedule.TimeMultiplier
			if mult == 0 {
				mult = 1.5
			}
			newLockTime := newExam.Spec.Schedule.Unlock.Add(
				time.Duration(float64(newExam.Spec.Schedule.Duration.Duration) * mult))
			if newLockTime.Before(time.Now()) {
				return nil, fmt.Errorf("computed lockTime (%v) would be in the past", newLockTime)
			}
		}
	}

	return nil, nil
}

func (v *examValidator) ValidateDelete(_ context.Context, _ *Exam) (admission.Warnings, error) {
	return nil, nil
}
