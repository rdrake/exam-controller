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

package controller

import (
	"context"
	"fmt"
	"time"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	"github.com/rdrake/exam-controller/internal/network"
	"github.com/rdrake/exam-controller/internal/notifier"
	"github.com/rdrake/exam-controller/internal/provisioner"
	"github.com/rdrake/exam-controller/internal/slug"
	"github.com/rdrake/exam-controller/internal/smoketest"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// ExamReconciler reconciles an Exam object.
type ExamReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Sender notifier.Sender
	Now    func() time.Time // injectable clock for testing
}

// +kubebuilder:rbac:groups=exam.otu.ca,resources=exams,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=exam.otu.ca,resources=exams/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=exam.otu.ca,resources=exams/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses;networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *ExamReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *ExamReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var exam examv1alpha1.Exam
	if err := r.Get(ctx, req.NamespacedName, &exam); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	now := r.now()
	desired := determineDesiredPhase(&exam, now)
	current := exam.Status.Phase
	if current == "" {
		current = examv1alpha1.ExamPhasePending
	}

	log.Info("reconciling", "current", current, "desired", desired)

	var requeueAfter time.Duration
	var err error

	switch desired {
	case examv1alpha1.ExamPhaseProvisioning:
		err = r.reconcileProvisioning(ctx, &exam)
	case examv1alpha1.ExamPhaseReady:
		requeueAfter = r.requeueForDryRun(&exam, now)
	case examv1alpha1.ExamPhaseDryRun:
		err = r.reconcileDryRun(ctx, &exam)
	case examv1alpha1.ExamPhaseVerified:
		requeueAfter = time.Until(exam.Spec.Schedule.Unlock.Time)
	case examv1alpha1.ExamPhaseUnlocked:
		err = r.reconcileUnlock(ctx, &exam, now)
		if err == nil {
			requeueAfter = r.requeueForLock(&exam, now)
		}
	case examv1alpha1.ExamPhaseLocking:
		err = r.reconcileLocking(ctx, &exam, now)
		if err == nil {
			requeueAfter = r.requeueForNextLock(&exam, now)
		}
	case examv1alpha1.ExamPhaseLocked:
		err = r.reconcileLocked(ctx, &exam, now)
	case examv1alpha1.ExamPhaseTearingDown:
		err = r.reconcileTeardown(ctx, &exam)
	}

	if err != nil {
		return ctrl.Result{}, err
	}

	if exam.Status.Phase != desired {
		exam.Status.Phase = desired
		if err := r.Status().Update(ctx, &exam); err != nil {
			return ctrl.Result{}, err
		}
	}

	if requeueAfter > 0 {
		return ctrl.Result{RequeueAfter: requeueAfter}, nil
	}
	return ctrl.Result{}, nil
}

// determineDesiredPhase computes the target phase based on current status and time.
func determineDesiredPhase(exam *examv1alpha1.Exam, now time.Time) examv1alpha1.ExamPhase {
	current := exam.Status.Phase
	if current == "" || current == examv1alpha1.ExamPhasePending {
		return examv1alpha1.ExamPhaseProvisioning
	}

	// Check for teardown annotation
	if exam.Annotations != nil && exam.Annotations["exam.otu.ca/teardown"] == "confirmed" {
		return examv1alpha1.ExamPhaseTearingDown
	}

	schedule := exam.Spec.Schedule
	unlockTime := schedule.Unlock.Time
	latest := latestLockTime(exam)

	// All students past their lock time?
	if current == examv1alpha1.ExamPhaseLocking || current == examv1alpha1.ExamPhaseUnlocked {
		if !now.Before(latest) {
			return examv1alpha1.ExamPhaseLocked
		}
	}

	// Some students past lock, some not?
	if current == examv1alpha1.ExamPhaseUnlocked || current == examv1alpha1.ExamPhaseLocking {
		earliest := earliestLockTime(exam)
		if !now.Before(earliest) && now.Before(latest) {
			return examv1alpha1.ExamPhaseLocking
		}
	}

	// Past unlock time?
	if !now.Before(unlockTime) {
		if current == examv1alpha1.ExamPhaseVerified || current == examv1alpha1.ExamPhaseReady {
			return examv1alpha1.ExamPhaseUnlocked
		}
	}

	// Dry run completed?
	if current == examv1alpha1.ExamPhaseDryRun {
		if exam.Status.DryRun != nil && exam.Status.DryRun.CompletedAt != nil {
			return examv1alpha1.ExamPhaseVerified
		}
	}

	// Dry run window?
	if current == examv1alpha1.ExamPhaseReady && schedule.DryRun != nil {
		dryRunStart := unlockTime.Add(-schedule.DryRun.Before.Duration)
		if !now.Before(dryRunStart) {
			return examv1alpha1.ExamPhaseDryRun
		}
	}

	// Provisioning complete?
	if current == examv1alpha1.ExamPhaseProvisioning {
		if len(exam.Status.Students) == len(exam.Spec.Students) {
			allReady := true
			for _, s := range exam.Status.Students {
				if s.Phase != examv1alpha1.StudentPhaseHealthy && s.Phase != examv1alpha1.StudentPhaseProvisioned {
					allReady = false
					break
				}
			}
			if allReady {
				return examv1alpha1.ExamPhaseReady
			}
		}
	}

	return current
}

func earliestLockTime(exam *examv1alpha1.Exam) time.Time {
	earliest := exam.Spec.Schedule.Lock.Time
	for _, s := range exam.Spec.Students {
		lt := effectiveLockTime(exam, &s)
		if lt.Before(earliest) {
			earliest = lt
		}
	}
	return earliest
}

func latestLockTime(exam *examv1alpha1.Exam) time.Time {
	latest := exam.Spec.Schedule.Lock.Time
	for _, s := range exam.Spec.Students {
		lt := effectiveLockTime(exam, &s)
		if lt.After(latest) {
			latest = lt
		}
	}
	return latest
}

func effectiveLockTime(exam *examv1alpha1.Exam, student *examv1alpha1.ExamStudent) time.Time {
	if student.LockOverride != nil {
		return student.LockOverride.Time
	}
	return exam.Spec.Schedule.Lock.Time
}

func examNamespace(exam *examv1alpha1.Exam) string {
	return fmt.Sprintf("exam-%s", exam.Name)
}

func (r *ExamReconciler) reconcileProvisioning(ctx context.Context, exam *examv1alpha1.Exam) error {
	ns := examNamespace(exam)

	// Ensure namespace exists
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
			Labels: map[string]string{
				"exam.otu.ca/exam":                   exam.Name,
				"pod-security.kubernetes.io/enforce": "baseline",
			},
		},
	}
	if err := r.Client.Create(ctx, namespace); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}

	for i, student := range exam.Spec.Students {
		studentSlug := findOrGenerateSlug(exam, student.ID)

		// Create Deployment
		dep := provisioner.Deployment(exam, ns, student.ID, studentSlug)
		if err := controllerutil.SetOwnerReference(exam, dep, r.Scheme); err != nil {
			return err
		}
		if err := r.Client.Create(ctx, dep); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}

		// Create Service
		svc := provisioner.Service(exam, ns, student.ID, studentSlug)
		if err := controllerutil.SetOwnerReference(exam, svc, r.Scheme); err != nil {
			return err
		}
		if err := r.Client.Create(ctx, svc); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}

		// Create Ingress
		ing := provisioner.Ingress(exam, ns, student.ID, studentSlug)
		if err := controllerutil.SetOwnerReference(exam, ing, r.Scheme); err != nil {
			return err
		}
		if err := r.Client.Create(ctx, ing); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}

		// Create deny-all and egress-allow policies
		denyAll := network.DenyAllPolicy(ns, exam.Name, student.ID)
		if err := controllerutil.SetOwnerReference(exam, denyAll, r.Scheme); err != nil {
			return err
		}
		if err := r.Client.Create(ctx, denyAll); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}

		egressAllow := network.EgressAllowlistPolicy(ns, exam.Name, student.ID, "kube-system")
		if err := controllerutil.SetOwnerReference(exam, egressAllow, r.Scheme); err != nil {
			return err
		}
		if err := r.Client.Create(ctx, egressAllow); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}

		// Update student status
		if i >= len(exam.Status.Students) {
			exam.Status.Students = append(exam.Status.Students, examv1alpha1.StudentStatus{
				ID:                student.ID,
				Slug:              studentSlug,
				URL:               fmt.Sprintf("https://%s.%s", studentSlug, exam.Spec.Domain),
				Phase:             examv1alpha1.StudentPhaseProvisioned,
				EmailStatus:       examv1alpha1.EmailStatusPending,
				EffectiveLockTime: metav1.NewTime(effectiveLockTime(exam, &student)),
			})
		}
	}

	return nil
}

func findOrGenerateSlug(exam *examv1alpha1.Exam, studentID string) string {
	for _, s := range exam.Status.Students {
		if s.ID == studentID && s.Slug != "" {
			return s.Slug
		}
	}
	s, _ := slug.Generate()
	return s
}

func (r *ExamReconciler) reconcileDryRun(ctx context.Context, exam *examv1alpha1.Exam) error {
	if exam.Status.DryRun != nil && exam.Status.DryRun.CompletedAt != nil {
		return nil // already completed
	}

	var targets []smoketest.Target
	ns := examNamespace(exam)
	for _, s := range exam.Status.Students {
		targets = append(targets, smoketest.Target{
			StudentID: s.ID,
			URL:       fmt.Sprintf("http://%s.%s:%d", s.Slug, ns, exam.Spec.Template.Port),
		})
	}

	result := smoketest.RunAll(ctx, targets)
	now := metav1.Now()
	exam.Status.DryRun = &examv1alpha1.DryRunStatus{
		CompletedAt: &now,
		Passed:      result.Passed,
		Failed:      result.Failed,
	}
	for _, f := range result.Failures {
		exam.Status.DryRun.Failures = append(exam.Status.DryRun.Failures, examv1alpha1.DryRunFailure{
			Student: f.Student,
			Error:   f.Error,
		})
	}

	return nil
}

func (r *ExamReconciler) reconcileUnlock(ctx context.Context, exam *examv1alpha1.Exam, now time.Time) error {
	ns := examNamespace(exam)

	for i, student := range exam.Spec.Students {
		lt := effectiveLockTime(exam, &student)
		if now.Before(lt) {
			// Student should be unlocked — add ingress-allow policy
			ingressAllow := network.IngressAllowPolicy(ns, exam.Name, student.ID, "ingress-nginx", map[string]string{
				"app.kubernetes.io/name": "ingress-nginx",
			})
			if err := controllerutil.SetOwnerReference(exam, ingressAllow, r.Scheme); err != nil {
				return err
			}
			if err := r.Client.Create(ctx, ingressAllow); err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}

			if i < len(exam.Status.Students) {
				exam.Status.Students[i].Phase = examv1alpha1.StudentPhaseUnlocked
			}
		}
	}

	// Send emails if not yet sent
	r.sendEmailsIfNeeded(exam)

	return nil
}

func (r *ExamReconciler) sendEmailsIfNeeded(exam *examv1alpha1.Exam) {
	if r.Sender == nil {
		return
	}
	for i := range exam.Status.Students {
		ss := &exam.Status.Students[i]
		if ss.EmailStatus != examv1alpha1.EmailStatusPending {
			continue
		}
		var studentEmail string
		for _, spec := range exam.Spec.Students {
			if spec.ID == ss.ID {
				studentEmail = spec.Email
				break
			}
		}
		msg := notifier.BuildMessage(exam.Spec.SMTP.From, studentEmail, exam.Spec.SMTP.Subject, ss.URL)
		if err := r.Sender.Send(exam.Spec.SMTP.From, []string{studentEmail}, []byte(msg)); err != nil {
			ss.EmailStatus = examv1alpha1.EmailStatusFailed
		} else {
			ss.EmailStatus = examv1alpha1.EmailStatusSent
			now := metav1.Now()
			ss.EmailSentAt = &now
		}
	}
}

func (r *ExamReconciler) reconcileLocking(ctx context.Context, exam *examv1alpha1.Exam, now time.Time) error {
	ns := examNamespace(exam)

	for i, student := range exam.Spec.Students {
		lt := effectiveLockTime(exam, &student)
		if !now.Before(lt) {
			if i < len(exam.Status.Students) && exam.Status.Students[i].Phase == examv1alpha1.StudentPhaseLocked {
				continue // already locked
			}

			// Remove ingress-allow policy
			ingressAllow := &networkingv1.NetworkPolicy{
				ObjectMeta: metav1.ObjectMeta{
					Name:      student.ID + "-ingress-allow",
					Namespace: ns,
				},
			}
			if err := r.Client.Delete(ctx, ingressAllow); err != nil && !apierrors.IsNotFound(err) {
				return err
			}

			// Delete Ingress to kill established connections
			if i < len(exam.Status.Students) {
				ingress := &networkingv1.Ingress{
					ObjectMeta: metav1.ObjectMeta{
						Name:      exam.Status.Students[i].Slug,
						Namespace: ns,
					},
				}
				if err := r.Client.Delete(ctx, ingress); err != nil && !apierrors.IsNotFound(err) {
					return err
				}

				exam.Status.Students[i].Phase = examv1alpha1.StudentPhaseLocked
				lockedAt := metav1.Now()
				exam.Status.Students[i].LockedAt = &lockedAt
			}
		}
	}

	return nil
}

func (r *ExamReconciler) reconcileLocked(ctx context.Context, exam *examv1alpha1.Exam, now time.Time) error {
	// Ensure all students are locked (drift correction)
	if err := r.reconcileLocking(ctx, exam, now); err != nil {
		return err
	}

	// Set retention deadline
	if exam.Status.RetentionDeadline == nil {
		deadline := metav1.NewTime(latestLockTime(exam).Add(7 * 24 * time.Hour))
		exam.Status.RetentionDeadline = &deadline
	}

	if now.After(exam.Status.RetentionDeadline.Time) {
		exam.Status.Message = "WARNING: Retention deadline exceeded. Consider triggering teardown."
	}

	return nil
}

func (r *ExamReconciler) reconcileTeardown(ctx context.Context, exam *examv1alpha1.Exam) error {
	ns := examNamespace(exam)
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}
	return client.IgnoreNotFound(r.Client.Delete(ctx, namespace))
}

func (r *ExamReconciler) requeueForDryRun(exam *examv1alpha1.Exam, now time.Time) time.Duration {
	if exam.Spec.Schedule.DryRun == nil {
		return time.Until(exam.Spec.Schedule.Unlock.Time)
	}
	dryRunStart := exam.Spec.Schedule.Unlock.Time.Add(-exam.Spec.Schedule.DryRun.Before.Duration)
	d := dryRunStart.Sub(now)
	if d < 0 {
		return 0
	}
	return d
}

func (r *ExamReconciler) requeueForLock(exam *examv1alpha1.Exam, now time.Time) time.Duration {
	earliest := earliestLockTime(exam)
	d := earliest.Sub(now)
	if d < 0 {
		return 0
	}
	return d
}

func (r *ExamReconciler) requeueForNextLock(exam *examv1alpha1.Exam, now time.Time) time.Duration {
	var next time.Time
	for _, s := range exam.Spec.Students {
		lt := effectiveLockTime(exam, &s)
		if lt.After(now) && (next.IsZero() || lt.Before(next)) {
			next = lt
		}
	}
	if next.IsZero() {
		return 0
	}
	return next.Sub(now)
}

// SetupWithManager sets up the controller with the Manager.
func (r *ExamReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&examv1alpha1.Exam{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Named("exam").
		Complete(r)
}
