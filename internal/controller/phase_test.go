package controller

import (
	"testing"
	"time"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func examWithSchedule(unlock time.Time, duration time.Duration, multiplier float64) *examv1alpha1.Exam {
	return &examv1alpha1.Exam{
		ObjectMeta: metav1.ObjectMeta{Name: "test-exam"},
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock:          metav1.NewTime(unlock),
				Duration:        metav1.Duration{Duration: duration},
				TimeMultiplier:  multiplier,
				ProvisionBefore: metav1.Duration{Duration: 1 * time.Hour},
				Retention:       metav1.Duration{Duration: 24 * time.Hour},
			},
			Email: examv1alpha1.ExamEmail{
				Before:          metav1.Duration{Duration: 30 * time.Minute},
				RateLimit:       1,
				InstructorEmail: "prof@test.com",
				SecretRef:       "smtp",
				From:            "noreply@test.com",
				Subject:         "Test",
			},
			Students: []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			},
		},
	}
}

func TestComputeLockTime(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	lt := computeLockTime(unlock, 2*time.Hour, 1.5)
	want := unlock.Add(3 * time.Hour)
	if !lt.Equal(want) {
		t.Errorf("lockTime = %v, want %v", lt, want)
	}
}

func TestComputeLockTime_DefaultMultiplier(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	lt := computeLockTime(unlock, 2*time.Hour, 0)
	want := unlock.Add(3 * time.Hour)
	if !lt.Equal(want) {
		t.Errorf("lockTime = %v, want %v", lt, want)
	}
}

func TestExamNamespace(t *testing.T) {
	ns := examNamespace("sofe4790u-midterm")
	if ns != "exam-sofe4790u-midterm" {
		t.Errorf("namespace = %q, want exam-sofe4790u-midterm", ns)
	}
}

func TestEffectiveMultiplier(t *testing.T) {
	if effectiveMultiplier(0) != 1.5 {
		t.Error("zero should default to 1.5")
	}
	if effectiveMultiplier(2.0) != 2.0 {
		t.Error("explicit value should pass through")
	}
}

func TestDetermineDesiredPhase_PendingBeforeProvisionTime(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	exam := examWithSchedule(now.Add(2*time.Hour), 2*time.Hour, 1.5)
	exam.Status.Phase = examv1alpha1.ExamPhasePending
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhasePending {
		t.Errorf("phase = %q, want Pending", phase)
	}
}

func TestDetermineDesiredPhase_PendingToProvisioning(t *testing.T) {
	now := time.Date(2026, 4, 10, 13, 0, 0, 0, time.UTC)
	exam := examWithSchedule(now.Add(1*time.Hour), 2*time.Hour, 1.5)
	exam.Status.Phase = examv1alpha1.ExamPhasePending
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseProvisioning {
		t.Errorf("phase = %q, want Provisioning", phase)
	}
}

func TestDetermineDesiredPhase_ReadyToUnlocked(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	now := unlock.Add(1 * time.Minute)
	exam := examWithSchedule(unlock, 2*time.Hour, 1.5)
	exam.Status.Phase = examv1alpha1.ExamPhaseReady
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseUnlocked {
		t.Errorf("phase = %q, want Unlocked", phase)
	}
}

func TestDetermineDesiredPhase_ReadyWaiting(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	now := unlock.Add(-10 * time.Minute)
	exam := examWithSchedule(unlock, 2*time.Hour, 1.5)
	exam.Status.Phase = examv1alpha1.ExamPhaseReady
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseReady {
		t.Errorf("phase = %q, want Ready", phase)
	}
}

func TestDetermineDesiredPhase_UnlockedToLocked(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	lockTime := unlock.Add(3 * time.Hour)
	now := lockTime.Add(1 * time.Minute)
	exam := examWithSchedule(unlock, 2*time.Hour, 1.5)
	exam.Status.Phase = examv1alpha1.ExamPhaseUnlocked
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseLocked {
		t.Errorf("phase = %q, want Locked", phase)
	}
}

func TestDetermineDesiredPhase_LockedToTearingDown(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	lockTime := unlock.Add(3 * time.Hour)
	retentionDeadline := lockTime.Add(24 * time.Hour)
	now := retentionDeadline.Add(1 * time.Minute)
	exam := examWithSchedule(unlock, 2*time.Hour, 1.5)
	exam.Status.Phase = examv1alpha1.ExamPhaseLocked
	exam.Status.RetentionDeadline = &metav1.Time{Time: retentionDeadline}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseTearingDown {
		t.Errorf("phase = %q, want TearingDown", phase)
	}
}

func TestDetermineDesiredPhase_LockedWaiting(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	lockTime := unlock.Add(3 * time.Hour)
	retentionDeadline := lockTime.Add(24 * time.Hour)
	now := lockTime.Add(1 * time.Hour)
	exam := examWithSchedule(unlock, 2*time.Hour, 1.5)
	exam.Status.Phase = examv1alpha1.ExamPhaseLocked
	exam.Status.RetentionDeadline = &metav1.Time{Time: retentionDeadline}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseLocked {
		t.Errorf("phase = %q, want Locked", phase)
	}
}
