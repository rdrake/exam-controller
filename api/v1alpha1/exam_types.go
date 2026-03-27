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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ExamPhase represents the current state of an Exam.
type ExamPhase string

const (
	ExamPhasePending      ExamPhase = "Pending"
	ExamPhaseProvisioning ExamPhase = "Provisioning"
	ExamPhaseReady        ExamPhase = "Ready"
	ExamPhaseDryRun       ExamPhase = "DryRun"
	ExamPhaseVerified     ExamPhase = "Verified"
	ExamPhaseUnlocked     ExamPhase = "Unlocked"
	ExamPhaseLocking      ExamPhase = "Locking"
	ExamPhaseLocked       ExamPhase = "Locked"
	ExamPhaseTearingDown  ExamPhase = "TearingDown"
)

// StudentPhase represents the current state of a student's instance.
type StudentPhase string

const (
	StudentPhaseProvisioned StudentPhase = "Provisioned"
	StudentPhaseHealthy     StudentPhase = "Healthy"
	StudentPhaseUnlocked    StudentPhase = "Unlocked"
	StudentPhaseLocked      StudentPhase = "Locked"
	StudentPhaseFailed      StudentPhase = "Failed"
)

// EmailStatus represents the email delivery state.
type EmailStatus string

const (
	EmailStatusPending EmailStatus = "Pending"
	EmailStatusSent    EmailStatus = "Sent"
	EmailStatusFailed  EmailStatus = "Failed"
)

// ExamTemplate defines the pod template for student instances.
type ExamTemplate struct {
	Image     string                      `json:"image"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	Port      int32                       `json:"port"`
}

// ExamSchedule defines the timing for unlock/lock and dry run.
type ExamSchedule struct {
	Unlock metav1.Time     `json:"unlock"`
	Lock   metav1.Time     `json:"lock"`
	DryRun *ExamDryRunSpec `json:"dryRun,omitempty"`
}

// ExamDryRunSpec configures the pre-exam smoke test window.
type ExamDryRunSpec struct {
	Before   metav1.Duration `json:"before"`
	Duration metav1.Duration `json:"duration"`
}

// ExamStudent defines a student participating in the exam.
type ExamStudent struct {
	ID           string       `json:"id"`
	Email        string       `json:"email"`
	LockOverride *metav1.Time `json:"lockOverride,omitempty"`
}

// ExamIngressTLS configures TLS for student Ingress resources.
type ExamIngressTLS struct {
	SecretName string `json:"secretName"`
}

// ExamSMTP configures email delivery.
type ExamSMTP struct {
	SecretRef string `json:"secretRef"`
	From      string `json:"from"`
	Subject   string `json:"subject"`
}

// ExamSpec defines the desired state of Exam.
type ExamSpec struct {
	Template   ExamTemplate   `json:"template"`
	Schedule   ExamSchedule   `json:"schedule"`
	Students   []ExamStudent  `json:"students"`
	IngressTLS ExamIngressTLS `json:"ingressTLS"`
	Domain     string         `json:"domain"`
	SMTP       ExamSMTP       `json:"smtp"`
}

// DryRunFailure records a smoke test failure for a student.
type DryRunFailure struct {
	Student string `json:"student"`
	Error   string `json:"error"`
}

// DryRunStatus records the results of the dry run.
type DryRunStatus struct {
	CompletedAt *metav1.Time    `json:"completedAt,omitempty"`
	Passed      int             `json:"passed"`
	Failed      int             `json:"failed"`
	Failures    []DryRunFailure `json:"failures,omitempty"`
}

// StudentStatus records the state of a single student's resources.
type StudentStatus struct {
	ID                string       `json:"id"`
	Slug              string       `json:"slug,omitempty"`
	URL               string       `json:"url,omitempty"`
	Phase             StudentPhase `json:"phase"`
	EmailStatus       EmailStatus  `json:"emailStatus"`
	EmailSentAt       *metav1.Time `json:"emailSentAt,omitempty"`
	LockedAt          *metav1.Time `json:"lockedAt,omitempty"`
	EffectiveLockTime metav1.Time  `json:"effectiveLockTime"`
}

// ExamStatus defines the observed state of Exam.
type ExamStatus struct {
	Phase             ExamPhase          `json:"phase,omitempty"`
	Message           string             `json:"message,omitempty"`
	Conditions        []metav1.Condition `json:"conditions,omitempty"`
	DryRun            *DryRunStatus      `json:"dryRun,omitempty"`
	Students          []StudentStatus    `json:"students,omitempty"`
	RetentionDeadline *metav1.Time       `json:"retentionDeadline,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
//+kubebuilder:printcolumn:name="Unlock",type=string,JSONPath=`.spec.schedule.unlock`
//+kubebuilder:printcolumn:name="Lock",type=string,JSONPath=`.spec.schedule.lock`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Exam is the Schema for the exams API
type Exam struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ExamSpec   `json:"spec,omitempty"`
	Status ExamStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ExamList contains a list of Exam
type ExamList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Exam `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Exam{}, &ExamList{})
}
