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
	ExamPhaseUnlocked     ExamPhase = "Unlocked"
	ExamPhaseLocked       ExamPhase = "Locked"
	ExamPhaseTearingDown  ExamPhase = "TearingDown"
)

// StudentPhase represents the current state of a student's instance.
type StudentPhase string

const (
	StudentPhaseProvisioned StudentPhase = "Provisioned"
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

// ExamDryRunSpec configures the pre-exam smoke test window.
type ExamDryRunSpec struct {
	Before metav1.Duration `json:"before"`
}

// ExamSchedule defines the timing for the exam lifecycle.
type ExamSchedule struct {
	Unlock   metav1.Time     `json:"unlock"`
	Duration metav1.Duration `json:"duration"`
	// +kubebuilder:default=1.5
	TimeMultiplier float64 `json:"timeMultiplier,omitempty"`
	// +kubebuilder:default="1h"
	ProvisionBefore metav1.Duration `json:"provisionBefore,omitempty"`
	// +kubebuilder:default="24h"
	Retention metav1.Duration `json:"retention,omitempty"`
	DryRun    *ExamDryRunSpec `json:"dryRun,omitempty"`
}

// ExamStudent defines a student participating in the exam.
type ExamStudent struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// ExamEmail configures email delivery.
type ExamEmail struct {
	// +kubebuilder:default="30m"
	Before metav1.Duration `json:"before,omitempty"`
	// +kubebuilder:default="1s"
	SendInterval    metav1.Duration `json:"sendInterval,omitempty"`
	InstructorEmail string          `json:"instructorEmail"`
	From            string          `json:"from"`
	Subject         string          `json:"subject"`
}

// ExamSpec defines the desired state of Exam.
type ExamSpec struct {
	Template ExamTemplate  `json:"template"`
	Schedule ExamSchedule  `json:"schedule"`
	Students []ExamStudent `json:"students"`
	Email    ExamEmail     `json:"email"`
	Spares   int           `json:"spares,omitempty"`
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

// SpareStatus records the state of a spare instance.
type SpareStatus struct {
	Slug  string       `json:"slug"`
	URL   string       `json:"url,omitempty"`
	Phase StudentPhase `json:"phase"`
}

// StudentStatus records the state of a single student's resources.
type StudentStatus struct {
	ID          string       `json:"id"`
	Slug        string       `json:"slug,omitempty"`
	URL         string       `json:"url,omitempty"`
	Phase       StudentPhase `json:"phase"`
	EmailStatus EmailStatus  `json:"emailStatus"`
	EmailSentAt *metav1.Time `json:"emailSentAt,omitempty"`
}

// MetricsSummary provides quick counts for dashboard queries.
type MetricsSummary struct {
	TotalStudents    int `json:"totalStudents"`
	TotalSpares      int `json:"totalSpares"`
	EmailsSent       int `json:"emailsSent"`
	EmailsFailed     int `json:"emailsFailed"`
	InstancesHealthy int `json:"instancesHealthy"`
	InstancesFailed  int `json:"instancesFailed"`
}

// ExamStatus defines the observed state of Exam.
type ExamStatus struct {
	Phase             ExamPhase          `json:"phase,omitempty"`
	Message           string             `json:"message,omitempty"`
	Conditions        []metav1.Condition `json:"conditions,omitempty"`
	ComputedLockTime  *metav1.Time       `json:"computedLockTime,omitempty"`
	ProvisionTime     *metav1.Time       `json:"provisionTime,omitempty"`
	EmailTime         *metav1.Time       `json:"emailTime,omitempty"`
	RetentionDeadline *metav1.Time       `json:"retentionDeadline,omitempty"`
	DryRun            *DryRunStatus      `json:"dryRun,omitempty"`
	Students          []StudentStatus    `json:"students,omitempty"`
	Spares            []SpareStatus      `json:"spares,omitempty"`
	Metrics           *MetricsSummary    `json:"metrics,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Unlock",type=string,JSONPath=`.spec.schedule.unlock`
// +kubebuilder:printcolumn:name="Lock",type=string,JSONPath=`.status.computedLockTime`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Exam is the Schema for the exams API
type Exam struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ExamSpec   `json:"spec,omitempty"`
	Status ExamStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ExamList contains a list of Exam
type ExamList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Exam `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Exam{}, &ExamList{})
}
