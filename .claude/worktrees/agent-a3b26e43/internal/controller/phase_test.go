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

// --- computeSchedule tests ---

func TestComputeSchedule_DefaultValues(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock:   metav1.NewTime(unlock),
				Duration: metav1.Duration{Duration: 2 * time.Hour},
				// ProvisionBefore, Retention, and TimeMultiplier all zero → defaults
			},
			Email: examv1alpha1.ExamEmail{
				// Before zero → default 30m
			},
		},
	}

	provisionTime, emailTime, lockTime, retentionDeadline := computeSchedule(exam)

	// Default provisionBefore = 1h
	wantProvision := unlock.Add(-1 * time.Hour)
	if !provisionTime.Equal(wantProvision) {
		t.Errorf("provisionTime = %v, want %v", provisionTime, wantProvision)
	}

	// Default emailBefore = 30m
	wantEmail := unlock.Add(-30 * time.Minute)
	if !emailTime.Equal(wantEmail) {
		t.Errorf("emailTime = %v, want %v", emailTime, wantEmail)
	}

	// Default multiplier = 1.5, lockTime = unlock + 2h*1.5 = unlock + 3h
	wantLock := unlock.Add(3 * time.Hour)
	if !lockTime.Equal(wantLock) {
		t.Errorf("lockTime = %v, want %v", lockTime, wantLock)
	}

	// Default retention = 24h
	wantRetention := wantLock.Add(24 * time.Hour)
	if !retentionDeadline.Equal(wantRetention) {
		t.Errorf("retentionDeadline = %v, want %v", retentionDeadline, wantRetention)
	}
}

func TestComputeSchedule_CustomValues(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock:          metav1.NewTime(unlock),
				Duration:        metav1.Duration{Duration: 3 * time.Hour},
				TimeMultiplier:  2.0,
				ProvisionBefore: metav1.Duration{Duration: 2 * time.Hour},
				Retention:       metav1.Duration{Duration: 48 * time.Hour},
			},
			Email: examv1alpha1.ExamEmail{
				Before: metav1.Duration{Duration: 45 * time.Minute},
			},
		},
	}

	provisionTime, emailTime, lockTime, retentionDeadline := computeSchedule(exam)

	// Custom provisionBefore = 2h
	wantProvision := unlock.Add(-2 * time.Hour)
	if !provisionTime.Equal(wantProvision) {
		t.Errorf("provisionTime = %v, want %v", provisionTime, wantProvision)
	}

	// Custom emailBefore = 45m
	wantEmail := unlock.Add(-45 * time.Minute)
	if !emailTime.Equal(wantEmail) {
		t.Errorf("emailTime = %v, want %v", emailTime, wantEmail)
	}

	// Custom multiplier = 2.0, lockTime = unlock + 3h*2.0 = unlock + 6h
	wantLock := unlock.Add(6 * time.Hour)
	if !lockTime.Equal(wantLock) {
		t.Errorf("lockTime = %v, want %v", lockTime, wantLock)
	}

	// Custom retention = 48h
	wantRetention := wantLock.Add(48 * time.Hour)
	if !retentionDeadline.Equal(wantRetention) {
		t.Errorf("retentionDeadline = %v, want %v", retentionDeadline, wantRetention)
	}
}

func TestComputeSchedule_LockTimeEqualsUnlockPlusDurationTimesMultiplier(t *testing.T) {
	unlock := time.Date(2026, 6, 15, 9, 0, 0, 0, time.UTC)
	duration := 90 * time.Minute
	multiplier := 1.75

	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock:          metav1.NewTime(unlock),
				Duration:        metav1.Duration{Duration: duration},
				TimeMultiplier:  multiplier,
				ProvisionBefore: metav1.Duration{Duration: 1 * time.Hour},
				Retention:       metav1.Duration{Duration: 24 * time.Hour},
			},
			Email: examv1alpha1.ExamEmail{
				Before: metav1.Duration{Duration: 30 * time.Minute},
			},
		},
	}

	_, _, lockTime, _ := computeSchedule(exam)

	// lockTime = unlock + duration * multiplier = 9:00 + 90m * 1.75 = 9:00 + 157.5m = 11:37:30
	want := unlock.Add(time.Duration(float64(duration) * multiplier))
	if !lockTime.Equal(want) {
		t.Errorf("lockTime = %v, want %v", lockTime, want)
	}
}
