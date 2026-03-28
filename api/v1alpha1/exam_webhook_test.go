package v1alpha1

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func baseExam() *Exam {
	return &Exam{
		ObjectMeta: metav1.ObjectMeta{Name: "test-exam"},
		Spec: ExamSpec{
			Template: ExamTemplate{Image: "vuln-app:v1", Port: 8080},
			Schedule: ExamSchedule{
				Unlock:          metav1.NewTime(time.Now().Add(2 * time.Hour)),
				Duration:        metav1.Duration{Duration: 2 * time.Hour},
				TimeMultiplier:  1.5,
				ProvisionBefore: metav1.Duration{Duration: 1 * time.Hour},
			},
			Students: []ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			},
			Email: ExamEmail{
				Before:          metav1.Duration{Duration: 30 * time.Minute},
				RateLimit:       1,
				InstructorEmail: "prof@test.com",
				SecretRef:       "smtp",
				From:            "noreply@test.com",
				Subject:         "Test",
			},
			IngressTLS: ExamIngressTLS{SecretName: "tls"},
			Domain:     "exam.test.com",
		},
	}
}

func TestValidateCreate_Valid(t *testing.T) {
	v := &examValidator{}
	_, err := v.ValidateCreate(context.Background(), baseExam())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateCreate_MissingInstructorEmail(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	e.Spec.Email.InstructorEmail = ""
	_, err := v.ValidateCreate(context.Background(), e)
	if err == nil {
		t.Error("expected error for missing instructorEmail")
	}
}

func TestValidateCreate_DurationZero(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	e.Spec.Schedule.Duration = metav1.Duration{}
	_, err := v.ValidateCreate(context.Background(), e)
	if err == nil {
		t.Error("expected error for zero duration")
	}
}

func TestValidateCreate_MultiplierTooLow(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	e.Spec.Schedule.TimeMultiplier = 0.5
	_, err := v.ValidateCreate(context.Background(), e)
	if err == nil {
		t.Error("expected error for multiplier < 1.0")
	}
}

func TestValidateCreate_NoStudents(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	e.Spec.Students = nil
	_, err := v.ValidateCreate(context.Background(), e)
	if err == nil {
		t.Error("expected error for empty students")
	}
}

func TestValidateCreate_EmailTimingInsufficient(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	e.Spec.Students = make([]ExamStudent, 100)
	for i := range e.Spec.Students {
		e.Spec.Students[i] = ExamStudent{ID: "s", Email: "s@test.com"}
	}
	e.Spec.Email.Before = metav1.Duration{Duration: 2 * time.Minute}
	_, err := v.ValidateCreate(context.Background(), e)
	if err == nil {
		t.Error("expected error for insufficient email timing")
	}
}

func TestValidateCreate_ProvisionBeforeEmailBefore(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	e.Spec.Schedule.ProvisionBefore = metav1.Duration{Duration: 10 * time.Minute}
	e.Spec.Email.Before = metav1.Duration{Duration: 30 * time.Minute}
	_, err := v.ValidateCreate(context.Background(), e)
	if err == nil {
		t.Error("expected error: provisionBefore must be > emailBefore")
	}
}

func TestValidateUpdate_ImmutableImageAfterPending(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseProvisioning
	updated := old.DeepCopy()
	updated.Spec.Template.Image = "app:v2"
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Error("expected error for image change after Pending")
	}
}

func TestValidateUpdate_AllowImageChangeWhenPending(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhasePending
	updated := old.DeepCopy()
	updated.Spec.Template.Image = "app:v2"
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err != nil {
		t.Errorf("should allow image change when Pending: %v", err)
	}
}

func TestValidateUpdate_DurationMutableWhenUnlocked(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseUnlocked
	updated := old.DeepCopy()
	updated.Spec.Schedule.Duration = metav1.Duration{Duration: 3 * time.Hour}
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err != nil {
		t.Errorf("should allow duration change when Unlocked: %v", err)
	}
}

func TestValidateUpdate_DurationImmutableWhenLocked(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseLocked
	updated := old.DeepCopy()
	updated.Spec.Schedule.Duration = metav1.Duration{Duration: 3 * time.Hour}
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Error("expected error for duration change when Locked")
	}
}

func TestValidateUpdate_SparesImmutableAfterPending(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseReady
	old.Spec.Spares = 2
	updated := old.DeepCopy()
	updated.Spec.Spares = 5
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Error("expected error for spares change after Pending")
	}
}

func TestValidateUpdate_ResourcesImmutableAfterPending(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseProvisioning
	old.Spec.Template.Resources = corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("100m"),
		},
	}
	updated := old.DeepCopy()
	updated.Spec.Template.Resources.Requests = nil
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Error("expected error for resources change after Pending")
	}
}

func TestValidateUpdate_DomainImmutableAfterPending(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseReady
	updated := old.DeepCopy()
	updated.Spec.Domain = "other.com"
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Error("expected error for domain change after Pending")
	}
}

func TestValidateUpdate_LockTimeGuardWhenUnlocked(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseUnlocked
	old.Spec.Schedule.Unlock = metav1.NewTime(time.Now().Add(-1 * time.Hour))
	updated := old.DeepCopy()
	updated.Spec.Schedule.Duration = metav1.Duration{Duration: 10 * time.Minute}
	updated.Spec.Schedule.TimeMultiplier = 1.0
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Error("expected error: computed lockTime would be in the past")
	}
}

// --- ValidateCreate edge cases ---

func TestValidateCreate_InvalidDomain(t *testing.T) {
	v := &examValidator{}
	tests := []struct {
		name   string
		domain string
	}{
		{"domain with spaces", "exam test.com"},
		{"domain with underscores", "exam_test.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := baseExam()
			e.Spec.Domain = tc.domain
			_, err := v.ValidateCreate(context.Background(), e)
			if err == nil {
				t.Errorf("expected error for invalid domain %q", tc.domain)
			}
		})
	}
}

func TestValidateCreate_InvalidStudentID(t *testing.T) {
	v := &examValidator{}
	tests := []struct {
		name string
		id   string
	}{
		{"contains slash", "alice/bob"},
		{"too long", string(make([]byte, 64))}, // 64 chars exceeds 63-char label limit
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := baseExam()
			e.Spec.Students = []ExamStudent{{ID: tc.id, Email: "x@test.com"}}
			_, err := v.ValidateCreate(context.Background(), e)
			if err == nil {
				t.Errorf("expected error for invalid student ID %q", tc.id)
			}
		})
	}
}

func TestValidateCreate_ZeroSpares(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	e.Spec.Spares = 0
	_, err := v.ValidateCreate(context.Background(), e)
	if err != nil {
		t.Errorf("spares=0 should be valid, got error: %v", err)
	}
}

func TestValidateCreate_NegativeSpares(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	e.Spec.Spares = -1
	_, err := v.ValidateCreate(context.Background(), e)
	if err == nil {
		t.Error("expected error for negative spares")
	}
}

func TestValidateCreate_MultiplierExactlyOne(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	e.Spec.Schedule.TimeMultiplier = 1.0
	_, err := v.ValidateCreate(context.Background(), e)
	if err != nil {
		t.Errorf("multiplier=1.0 should be valid, got error: %v", err)
	}
}

func TestValidateCreate_EmailTimingEdge(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	// 1 student, rateLimit=1 → minEmailTime = ceil(1/1) * 1.5 = 1.5s
	// Set emailBefore to exactly 2s (>= 1.5s boundary) to pass.
	e.Spec.Students = []ExamStudent{{ID: "alice", Email: "alice@test.com"}}
	e.Spec.Email.RateLimit = 1
	e.Spec.Email.Before = metav1.Duration{Duration: 2 * time.Second}
	e.Spec.Schedule.ProvisionBefore = metav1.Duration{Duration: 10 * time.Second}
	_, err := v.ValidateCreate(context.Background(), e)
	if err != nil {
		t.Errorf("emailBefore at boundary should be accepted, got error: %v", err)
	}
}

// --- ValidateUpdate edge cases ---

func TestValidateUpdate_StudentAddAfterPending(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseReady
	updated := old.DeepCopy()
	updated.Spec.Students = append(updated.Spec.Students, ExamStudent{ID: "bob", Email: "bob@test.com"})
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Error("expected error for adding a student when phase is not Pending")
	}
}

func TestValidateUpdate_AllowedFieldChange(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseProvisioning
	updated := old.DeepCopy()
	// Email subject is not in the immutable list, so it should be mutable.
	updated.Spec.Email.Subject = "Updated Subject"
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err != nil {
		t.Errorf("changing a mutable field (email subject) should be allowed, got error: %v", err)
	}
}

// --- ValidateDelete ---

func TestValidateDelete_AlwaysAllowed(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	warnings, err := v.ValidateDelete(context.Background(), e)
	if err != nil {
		t.Errorf("ValidateDelete should always return nil error, got: %v", err)
	}
	if warnings != nil {
		t.Errorf("ValidateDelete should return nil warnings, got: %v", warnings)
	}
}
