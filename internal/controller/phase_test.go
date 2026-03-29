package controller

import (
	"context"
	"testing"
	"time"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	"github.com/rdrake/exam-controller/internal/provisioner"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func examWithSchedule(unlock time.Time) *examv1alpha1.Exam {
	return &examv1alpha1.Exam{
		ObjectMeta: metav1.ObjectMeta{Name: "test-exam", Namespace: "exam-system"},
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock:          metav1.NewTime(unlock),
				Duration:        metav1.Duration{Duration: 2 * time.Hour},
				TimeMultiplier:  1.5,
				ProvisionBefore: metav1.Duration{Duration: 1 * time.Hour},
				Retention:       metav1.Duration{Duration: 24 * time.Hour},
			},
			Email: examv1alpha1.ExamEmail{
				Before:          metav1.Duration{Duration: 30 * time.Minute},
				RateLimit:       1,
				InstructorEmail: "prof@test.com",
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
	ns := examNamespace("sofe4790u-midterm", "exam-system")
	if len(ns) > 63 {
		t.Fatalf("namespace %q exceeds 63 characters", ns)
	}
	if got, want := ns[:len("exam-")], "exam-"; got != want {
		t.Fatalf("namespace prefix = %q, want %q", got, want)
	}

	other := examNamespace("sofe4790u-midterm", "another-system")
	if ns == other {
		t.Fatalf("expected namespace hash to differ across Exam namespaces, both were %q", ns)
	}
}

func TestExamNamespace_Deterministic(t *testing.T) {
	first := examNamespace("midterm", "exam-system")
	second := examNamespace("midterm", "exam-system")
	if first != second {
		t.Fatalf("namespace generation should be deterministic: %q != %q", first, second)
	}
}

func TestRequestsForOwnedObject(t *testing.T) {
	obj := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				provisioner.LabelExam:          "midterm",
				provisioner.LabelExamNamespace: "exam-system",
			},
		},
	}

	requests := requestsForOwnedObject(obj)
	if len(requests) != 1 {
		t.Fatalf("requests len = %d, want 1", len(requests))
	}
	if requests[0].Name != "midterm" || requests[0].Namespace != "exam-system" {
		t.Fatalf("request = %+v, want NamespacedName{Name: midterm, Namespace: exam-system}", requests[0].NamespacedName)
	}
}

func TestRequestsForOwnedObject_MissingLabels(t *testing.T) {
	obj := &corev1.Service{ObjectMeta: metav1.ObjectMeta{}}
	if requests := requestsForOwnedObject(obj); requests != nil {
		t.Fatalf("requests = %+v, want nil", requests)
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
	exam := examWithSchedule(now.Add(2 * time.Hour))
	exam.Status.Phase = examv1alpha1.ExamPhasePending
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhasePending {
		t.Errorf("phase = %q, want Pending", phase)
	}
}

func TestDetermineDesiredPhase_PendingToProvisioning(t *testing.T) {
	now := time.Date(2026, 4, 10, 13, 0, 0, 0, time.UTC)
	exam := examWithSchedule(now.Add(1 * time.Hour))
	exam.Status.Phase = examv1alpha1.ExamPhasePending
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseProvisioning {
		t.Errorf("phase = %q, want Provisioning", phase)
	}
}

func TestDetermineDesiredPhase_ReadyToUnlocked(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	now := unlock.Add(1 * time.Minute)
	exam := examWithSchedule(unlock)
	exam.Status.Phase = examv1alpha1.ExamPhaseReady
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseUnlocked {
		t.Errorf("phase = %q, want Unlocked", phase)
	}
}

func TestDetermineDesiredPhase_ReadyWaiting(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	now := unlock.Add(-10 * time.Minute)
	exam := examWithSchedule(unlock)
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
	exam := examWithSchedule(unlock)
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
	exam := examWithSchedule(unlock)
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
	exam := examWithSchedule(unlock)
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

// --- now() fallback ---

func TestNow_NilFallback(t *testing.T) {
	r := &ExamReconciler{} // Now is nil
	before := time.Now()
	got := r.now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Errorf("now() with nil Now should return time.Now(), got %v", got)
	}
}

// --- findOrGenerateSlug ---

func TestFindOrGenerateSlug_ExistingSlug(t *testing.T) {
	r := &ExamReconciler{}
	exam := examWithSchedule(time.Now().Add(2 * time.Hour))
	exam.Status.Students = []examv1alpha1.StudentStatus{
		{ID: "alice", Slug: "abcd1234"},
	}
	slug, err := r.findOrGenerateSlug(context.Background(), exam, "test-ns", "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slug != "abcd1234" {
		t.Errorf("slug = %q, want %q", slug, "abcd1234")
	}
}

func TestFindOrGenerateSlug_NewSlug(t *testing.T) {
	r := &ExamReconciler{}
	exam := examWithSchedule(time.Now().Add(2 * time.Hour))
	exam.Status.Students = nil
	// nil client means List will fail, falling through to slug.Generate()
	slug, err := r.findOrGenerateSlug(context.Background(), exam, "test-ns", "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slug) != 8 {
		t.Errorf("generated slug length = %d, want 8", len(slug))
	}
}

// --- findOrGenerateSpareSlug ---

func TestFindOrGenerateSpareSlug_ExistingSlug(t *testing.T) {
	r := &ExamReconciler{}
	exam := examWithSchedule(time.Now().Add(2 * time.Hour))
	exam.Status.Spares = []examv1alpha1.SpareStatus{
		{Slug: "xyzw5678"},
	}
	slug, err := r.findOrGenerateSpareSlug(context.Background(), exam, "test-ns", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if slug != "xyzw5678" {
		t.Errorf("slug = %q, want %q", slug, "xyzw5678")
	}
}

func TestFindOrGenerateSpareSlug_NewSlug(t *testing.T) {
	r := &ExamReconciler{}
	exam := examWithSchedule(time.Now().Add(2 * time.Hour))
	exam.Status.Spares = nil
	// nil client means List will fail, falling through to slug.Generate()
	slug, err := r.findOrGenerateSpareSlug(context.Background(), exam, "test-ns", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(slug) != 8 {
		t.Errorf("generated slug length = %d, want 8", len(slug))
	}
}

// --- setStudentStatus / setSpareStatus slice growth ---

func TestSetStudentStatus_GrowsSlice(t *testing.T) {
	r := &ExamReconciler{Platform: PlatformConfig{BaseDomain: "exam.test.com"}}
	exam := examWithSchedule(time.Now().Add(2 * time.Hour))
	exam.Status.Students = nil // empty slice
	r.setStudentStatus(exam, 0, "abcd1234", examv1alpha1.StudentPhaseProvisioned)
	if len(exam.Status.Students) != 1 {
		t.Fatalf("students len = %d, want 1", len(exam.Status.Students))
	}
	if exam.Status.Students[0].Slug != "abcd1234" {
		t.Errorf("slug = %q, want %q", exam.Status.Students[0].Slug, "abcd1234")
	}
	if exam.Status.Students[0].Phase != examv1alpha1.StudentPhaseProvisioned {
		t.Errorf("phase = %q, want Provisioned", exam.Status.Students[0].Phase)
	}
}

func TestSetSpareStatus_GrowsSlice(t *testing.T) {
	r := &ExamReconciler{Platform: PlatformConfig{BaseDomain: "exam.test.com"}}
	exam := examWithSchedule(time.Now().Add(2 * time.Hour))
	exam.Status.Spares = nil // empty slice
	r.setSpareStatus(exam, 0, "xyzw5678", examv1alpha1.StudentPhaseProvisioned)
	if len(exam.Status.Spares) != 1 {
		t.Fatalf("spares len = %d, want 1", len(exam.Status.Spares))
	}
	if exam.Status.Spares[0].Slug != "xyzw5678" {
		t.Errorf("slug = %q, want %q", exam.Status.Spares[0].Slug, "xyzw5678")
	}
	if exam.Status.Spares[0].Phase != examv1alpha1.StudentPhaseProvisioned {
		t.Errorf("phase = %q, want Provisioned", exam.Status.Spares[0].Phase)
	}
}

// --- determineDesiredPhase additional cases ---

func TestDetermineDesiredPhase_EmptyPhaseToProvisioning(t *testing.T) {
	now := time.Date(2026, 4, 10, 13, 30, 0, 0, time.UTC)
	exam := examWithSchedule(now.Add(30 * time.Minute)) // provision time = unlock - 1h = now - 30m
	exam.Status.Phase = ""                              // empty/initial
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseProvisioning {
		t.Errorf("phase = %q, want Provisioning", phase)
	}
}

func TestDetermineDesiredPhase_TearingDownStays(t *testing.T) {
	now := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	exam := examWithSchedule(now)
	exam.Status.Phase = examv1alpha1.ExamPhaseTearingDown
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseTearingDown {
		t.Errorf("phase = %q, want TearingDown", phase)
	}
}
