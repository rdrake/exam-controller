package controller

import (
	"testing"
	"time"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDetermineDesiredPhase_Pending(t *testing.T) {
	exam := &examv1alpha1.Exam{
		Status: examv1alpha1.ExamStatus{Phase: ""},
	}
	phase := determineDesiredPhase(exam, time.Now())
	if phase != examv1alpha1.ExamPhaseProvisioning {
		t.Errorf("expected Provisioning, got %s", phase)
	}
}

func TestDetermineDesiredPhase_ReadyWaitingForDryRun(t *testing.T) {
	now := time.Now()
	unlock := metav1.NewTime(now.Add(1 * time.Hour))
	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock: unlock,
				Lock:   metav1.NewTime(now.Add(3 * time.Hour)),
				DryRun: &examv1alpha1.ExamDryRunSpec{
					Before:   metav1.Duration{Duration: 15 * time.Minute},
					Duration: metav1.Duration{Duration: 5 * time.Minute},
				},
			},
		},
		Status: examv1alpha1.ExamStatus{Phase: examv1alpha1.ExamPhaseReady},
	}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseReady {
		t.Errorf("expected Ready (not yet dry run time), got %s", phase)
	}
}

func TestDetermineDesiredPhase_DryRunTime(t *testing.T) {
	now := time.Now()
	unlock := metav1.NewTime(now.Add(10 * time.Minute))
	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock: unlock,
				Lock:   metav1.NewTime(now.Add(2 * time.Hour)),
				DryRun: &examv1alpha1.ExamDryRunSpec{
					Before:   metav1.Duration{Duration: 15 * time.Minute},
					Duration: metav1.Duration{Duration: 5 * time.Minute},
				},
			},
		},
		Status: examv1alpha1.ExamStatus{Phase: examv1alpha1.ExamPhaseReady},
	}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseDryRun {
		t.Errorf("expected DryRun, got %s", phase)
	}
}

func TestDetermineDesiredPhase_Verified(t *testing.T) {
	now := time.Now()
	completedAt := metav1.NewTime(now.Add(-1 * time.Minute))
	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock: metav1.NewTime(now.Add(5 * time.Minute)),
				Lock:   metav1.NewTime(now.Add(2 * time.Hour)),
			},
		},
		Status: examv1alpha1.ExamStatus{
			Phase: examv1alpha1.ExamPhaseDryRun,
			DryRun: &examv1alpha1.DryRunStatus{
				CompletedAt: &completedAt,
			},
		},
	}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseVerified {
		t.Errorf("expected Verified, got %s", phase)
	}
}

func TestDetermineDesiredPhase_Unlocked(t *testing.T) {
	now := time.Now()
	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock: metav1.NewTime(now.Add(-30 * time.Minute)),
				Lock:   metav1.NewTime(now.Add(90 * time.Minute)),
			},
		},
		Status: examv1alpha1.ExamStatus{Phase: examv1alpha1.ExamPhaseVerified},
	}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseUnlocked {
		t.Errorf("expected Unlocked, got %s", phase)
	}
}

func TestDetermineDesiredPhase_Locking(t *testing.T) {
	now := time.Now()
	pastLock := metav1.NewTime(now.Add(-10 * time.Minute))
	futureLock := metav1.NewTime(now.Add(30 * time.Minute))
	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock: metav1.NewTime(now.Add(-2 * time.Hour)),
				Lock:   pastLock,
			},
			Students: []examv1alpha1.ExamStudent{
				{ID: "alice"},
				{ID: "bob", LockOverride: &futureLock},
			},
		},
		Status: examv1alpha1.ExamStatus{Phase: examv1alpha1.ExamPhaseUnlocked},
	}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseLocking {
		t.Errorf("expected Locking, got %s", phase)
	}
}

func TestDetermineDesiredPhase_Locked(t *testing.T) {
	now := time.Now()
	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock: metav1.NewTime(now.Add(-3 * time.Hour)),
				Lock:   metav1.NewTime(now.Add(-1 * time.Hour)),
			},
			Students: []examv1alpha1.ExamStudent{
				{ID: "alice"},
			},
		},
		Status: examv1alpha1.ExamStatus{Phase: examv1alpha1.ExamPhaseLocking},
	}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseLocked {
		t.Errorf("expected Locked, got %s", phase)
	}
}

func TestDetermineDesiredPhase_TearingDown(t *testing.T) {
	now := time.Now()
	exam := &examv1alpha1.Exam{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				"exam.otu.ca/teardown": "confirmed",
			},
		},
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock: metav1.NewTime(now.Add(-3 * time.Hour)),
				Lock:   metav1.NewTime(now.Add(-1 * time.Hour)),
			},
		},
		Status: examv1alpha1.ExamStatus{Phase: examv1alpha1.ExamPhaseLocked},
	}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseTearingDown {
		t.Errorf("expected TearingDown, got %s", phase)
	}
}

func TestEffectiveLockTime_Default(t *testing.T) {
	now := time.Now()
	lockTime := metav1.NewTime(now.Add(2 * time.Hour))
	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{Lock: lockTime},
		},
	}
	student := &examv1alpha1.ExamStudent{ID: "alice"}
	got := effectiveLockTime(exam, student)
	if !got.Equal(lockTime.Time) {
		t.Errorf("expected %v, got %v", lockTime.Time, got)
	}
}

func TestEffectiveLockTime_Override(t *testing.T) {
	now := time.Now()
	override := metav1.NewTime(now.Add(3 * time.Hour))
	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{Lock: metav1.NewTime(now.Add(2 * time.Hour))},
		},
	}
	student := &examv1alpha1.ExamStudent{ID: "bob", LockOverride: &override}
	got := effectiveLockTime(exam, student)
	if !got.Equal(override.Time) {
		t.Errorf("expected %v, got %v", override.Time, got)
	}
}

func TestExamNamespace(t *testing.T) {
	exam := &examv1alpha1.Exam{
		ObjectMeta: metav1.ObjectMeta{Name: "sofe4790u-midterm"},
	}
	ns := examNamespace(exam)
	if ns != "exam-sofe4790u-midterm" {
		t.Errorf("expected exam-sofe4790u-midterm, got %s", ns)
	}
}
