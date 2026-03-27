package webhook

import (
	"testing"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func examWithPhase(phase examv1alpha1.ExamPhase) (*examv1alpha1.Exam, *examv1alpha1.Exam) {
	base := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Template: examv1alpha1.ExamTemplate{Image: "app:v1", Port: 8080},
			Schedule: examv1alpha1.ExamSchedule{
				Unlock: metav1.Now(),
				Lock:   metav1.Now(),
			},
			Students: []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			},
		},
		Status: examv1alpha1.ExamStatus{Phase: phase},
	}
	updated := base.DeepCopy()
	return base, updated
}

func TestAllowImageChangeWhenPending(t *testing.T) {
	old, updated := examWithPhase(examv1alpha1.ExamPhasePending)
	updated.Spec.Template.Image = "app:v2"
	if err := ValidateUpdate(old, updated); err != nil {
		t.Errorf("should allow image change when Pending: %v", err)
	}
}

func TestRejectImageChangeWhenProvisioning(t *testing.T) {
	old, updated := examWithPhase(examv1alpha1.ExamPhaseProvisioning)
	updated.Spec.Template.Image = "app:v2"
	if err := ValidateUpdate(old, updated); err == nil {
		t.Error("should reject image change when Provisioning")
	}
}

func TestRejectStudentIDChangeWhenReady(t *testing.T) {
	old, updated := examWithPhase(examv1alpha1.ExamPhaseReady)
	updated.Spec.Students[0].ID = "bob"
	if err := ValidateUpdate(old, updated); err == nil {
		t.Error("should reject student ID change when Ready")
	}
}

func TestAllowLockTimeChangeWhenUnlocked(t *testing.T) {
	old, updated := examWithPhase(examv1alpha1.ExamPhaseUnlocked)
	updated.Spec.Schedule.Lock = metav1.Now()
	if err := ValidateUpdate(old, updated); err != nil {
		t.Errorf("should allow lock time change when Unlocked: %v", err)
	}
}

func TestAllowEmailChangeWhenReady(t *testing.T) {
	old, updated := examWithPhase(examv1alpha1.ExamPhaseReady)
	updated.Spec.Students[0].Email = "newemail@test.com"
	if err := ValidateUpdate(old, updated); err != nil {
		t.Errorf("should allow email change: %v", err)
	}
}
