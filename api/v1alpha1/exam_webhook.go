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

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// SetupExamWebhookWithManager registers the validating webhook with the manager.
func SetupExamWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &Exam{}).
		WithValidator(&examValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-exam-otu-ca-v1alpha1-exam,mutating=false,failurePolicy=fail,sideEffects=None,groups=exam.otu.ca,resources=exams,verbs=update,versions=v1alpha1,name=vexam-v1alpha1.kb.io,admissionReviewVersions=v1

type examValidator struct{}

func (v *examValidator) ValidateCreate(_ context.Context, _ *Exam) (admission.Warnings, error) {
	return nil, nil
}

func (v *examValidator) ValidateUpdate(_ context.Context, oldExam, newExam *Exam) (admission.Warnings, error) {
	phase := oldExam.Status.Phase
	if phase == ExamPhasePending || phase == "" {
		return nil, nil
	}

	if oldExam.Spec.Template.Image != newExam.Spec.Template.Image {
		return nil, fmt.Errorf("spec.template.image is immutable after provisioning (current phase: %s)", phase)
	}
	if oldExam.Spec.Template.Port != newExam.Spec.Template.Port {
		return nil, fmt.Errorf("spec.template.port is immutable after provisioning (current phase: %s)", phase)
	}

	if len(oldExam.Spec.Students) != len(newExam.Spec.Students) {
		return nil, fmt.Errorf("spec.students list length is immutable after provisioning")
	}
	for i := range oldExam.Spec.Students {
		if oldExam.Spec.Students[i].ID != newExam.Spec.Students[i].ID {
			return nil, fmt.Errorf("spec.students[%d].id is immutable after provisioning", i)
		}
	}

	if !oldExam.Spec.Schedule.Unlock.Equal(&newExam.Spec.Schedule.Unlock) {
		return nil, fmt.Errorf("spec.schedule.unlock is immutable after provisioning (current phase: %s)", phase)
	}

	return nil, nil
}

func (v *examValidator) ValidateDelete(_ context.Context, _ *Exam) (admission.Warnings, error) {
	return nil, nil
}
