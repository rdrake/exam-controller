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
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	"github.com/rdrake/exam-controller/internal/metrics"
	"github.com/rdrake/exam-controller/internal/network"
	"github.com/rdrake/exam-controller/internal/notifier"
	"github.com/rdrake/exam-controller/internal/provisioner"
	"github.com/rdrake/exam-controller/internal/slug"
	"github.com/rdrake/exam-controller/internal/smoketest"
)

const finalizerName = "exam.otu.ca/cleanup"

// +kubebuilder:rbac:groups=exam.otu.ca,resources=exams,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=exam.otu.ca,resources=exams/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=exam.otu.ca,resources=exams/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses;networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=cilium.io,resources=ciliumnetworkpolicies,verbs=get;list;watch;create;update;patch;delete

// ExamReconciler reconciles an Exam object.
type ExamReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	PolicyProvider network.PolicyProvider
	Sender         notifier.Sender
	Now            func() time.Time
	Metrics        *metrics.ExamMetrics
}

func (r *ExamReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// resolvedSender returns a Sender configured with SMTP credentials from the
// Exam's referenced Secret. If r.Sender is already set to a non-SMTPSender
// (e.g., FakeSender for tests), it is returned directly.
func (r *ExamReconciler) resolvedSender(ctx context.Context, exam *examv1alpha1.Exam) (notifier.Sender, error) {
	if r.Sender != nil {
		if _, ok := r.Sender.(*notifier.RetrySender); !ok {
			// Non-RetrySender (e.g., FakeSender) — use directly for testing
			return r.Sender, nil
		}
	}

	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{
		Name:      exam.Spec.Email.SecretRef,
		Namespace: exam.Namespace,
	}, &secret); err != nil {
		return nil, fmt.Errorf("reading SMTP secret %q: %w", exam.Spec.Email.SecretRef, err)
	}

	port, _ := strconv.Atoi(string(secret.Data["port"]))
	if port == 0 {
		port = 587
	}

	sender := &notifier.SMTPSender{
		Host:     string(secret.Data["host"]),
		Port:     port,
		Username: string(secret.Data["username"]),
		Password: string(secret.Data["password"]),
	}
	return notifier.NewRetrySender(sender, 3), nil
}

// sendEmail resolves SMTP credentials and sends an email. Returns nil if
// sending succeeds or if no sender is configured.
func (r *ExamReconciler) sendEmail(ctx context.Context, exam *examv1alpha1.Exam, from string, to []string, msg []byte) error {
	sender, err := r.resolvedSender(ctx, exam)
	if err != nil {
		return err
	}
	return sender.Send(from, to, msg)
}

func effectiveMultiplier(m float64) float64 {
	if m == 0 {
		return 1.5
	}
	return m
}

func computeLockTime(unlock time.Time, duration time.Duration, multiplier float64) time.Time {
	return unlock.Add(time.Duration(float64(duration) * effectiveMultiplier(multiplier)))
}

func examNamespace(examName string) string {
	return "exam-" + examName
}

func computeSchedule(exam *examv1alpha1.Exam) (provisionTime, emailTime, lockTime, retentionDeadline time.Time) {
	unlock := exam.Spec.Schedule.Unlock.Time
	dur := exam.Spec.Schedule.Duration.Duration
	mult := effectiveMultiplier(exam.Spec.Schedule.TimeMultiplier)
	provBefore := exam.Spec.Schedule.ProvisionBefore.Duration
	if provBefore == 0 {
		provBefore = 1 * time.Hour
	}
	emailBefore := exam.Spec.Email.Before.Duration
	if emailBefore == 0 {
		emailBefore = 30 * time.Minute
	}
	retention := exam.Spec.Schedule.Retention.Duration
	if retention == 0 {
		retention = 24 * time.Hour
	}

	lockTime = computeLockTime(unlock, dur, mult)
	provisionTime = unlock.Add(-provBefore)
	emailTime = unlock.Add(-emailBefore)
	retentionDeadline = lockTime.Add(retention)
	return
}

func determineDesiredPhase(exam *examv1alpha1.Exam, now time.Time) examv1alpha1.ExamPhase {
	current := exam.Status.Phase
	provisionTime, _, lockTime, _ := computeSchedule(exam)

	switch current {
	case "", examv1alpha1.ExamPhasePending:
		if now.Before(provisionTime) {
			return examv1alpha1.ExamPhasePending
		}
		return examv1alpha1.ExamPhaseProvisioning

	case examv1alpha1.ExamPhaseProvisioning:
		return examv1alpha1.ExamPhaseProvisioning

	case examv1alpha1.ExamPhaseReady:
		if now.Before(exam.Spec.Schedule.Unlock.Time) {
			return examv1alpha1.ExamPhaseReady
		}
		return examv1alpha1.ExamPhaseUnlocked

	case examv1alpha1.ExamPhaseUnlocked:
		if now.Before(lockTime) {
			return examv1alpha1.ExamPhaseUnlocked
		}
		return examv1alpha1.ExamPhaseLocked

	case examv1alpha1.ExamPhaseLocked:
		if exam.Status.RetentionDeadline != nil && !now.Before(exam.Status.RetentionDeadline.Time) {
			return examv1alpha1.ExamPhaseTearingDown
		}
		return examv1alpha1.ExamPhaseLocked

	case examv1alpha1.ExamPhaseTearingDown:
		return examv1alpha1.ExamPhaseTearingDown
	}

	return current
}

func (r *ExamReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	start := time.Now()

	var exam examv1alpha1.Exam
	if err := r.Get(ctx, req.NamespacedName, &exam); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Finalizer: handle deletion
	if !exam.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&exam, finalizerName) {
			if err := r.reconcileTeardown(ctx, &exam); err != nil {
				return ctrl.Result{}, err
			}
			controllerutil.RemoveFinalizer(&exam, finalizerName)
			if err := r.Update(ctx, &exam); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(&exam, finalizerName) {
		controllerutil.AddFinalizer(&exam, finalizerName)
		if err := r.Update(ctx, &exam); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Compute and set status times
	provisionTime, emailTime, lockTime, retentionDeadline := computeSchedule(&exam)
	exam.Status.ComputedLockTime = &metav1.Time{Time: lockTime}
	exam.Status.ProvisionTime = &metav1.Time{Time: provisionTime}
	exam.Status.EmailTime = &metav1.Time{Time: emailTime}
	exam.Status.RetentionDeadline = &metav1.Time{Time: retentionDeadline}

	// Determine desired phase and transition
	now := r.now()
	oldPhase := exam.Status.Phase
	desiredPhase := determineDesiredPhase(&exam, now)

	if oldPhase != desiredPhase {
		logger.Info("Phase transition", "from", oldPhase, "to", desiredPhase)
		exam.Status.Phase = desiredPhase
		if r.Metrics != nil {
			r.Metrics.PhaseTransitions.WithLabelValues(exam.Name, string(oldPhase), string(desiredPhase)).Inc()
		}
	}

	// Dispatch to phase handler
	var result ctrl.Result
	var err error
	switch exam.Status.Phase {
	case examv1alpha1.ExamPhasePending:
		result = ctrl.Result{RequeueAfter: time.Until(provisionTime)}
	case examv1alpha1.ExamPhaseProvisioning:
		result, err = r.reconcileProvisioning(ctx, &exam)
	case examv1alpha1.ExamPhaseReady:
		result, err = r.reconcileReady(ctx, &exam, now)
	case examv1alpha1.ExamPhaseUnlocked:
		result, err = r.reconcileUnlock(ctx, &exam, now)
	case examv1alpha1.ExamPhaseLocked:
		result, err = r.reconcileLocked(ctx, &exam, now)
	case examv1alpha1.ExamPhaseTearingDown:
		err = r.reconcileTeardown(ctx, &exam)
	}

	// Update countdown gauges
	if r.Metrics != nil {
		unlock := exam.Spec.Schedule.Unlock.Time
		secondsUntilUnlock := time.Until(unlock).Seconds()
		if secondsUntilUnlock < 0 {
			secondsUntilUnlock = 0
		}
		r.Metrics.SecondsUntilUnlock.WithLabelValues(exam.Name).Set(secondsUntilUnlock)

		secondsUntilLock := time.Until(lockTime).Seconds()
		if secondsUntilLock < 0 {
			secondsUntilLock = 0
		}
		r.Metrics.SecondsUntilLock.WithLabelValues(exam.Name).Set(secondsUntilLock)
	}

	// Update status
	r.updateMetricsSummary(&exam)
	if statusErr := r.Status().Update(ctx, &exam); statusErr != nil {
		logger.Error(statusErr, "Failed to update status")
		return ctrl.Result{}, statusErr
	}

	// Record reconcile duration
	if r.Metrics != nil {
		r.Metrics.ReconcileDuration.Observe(time.Since(start).Seconds())
		if err != nil {
			r.Metrics.ReconcileErrors.Inc()
		}
	}

	return result, err
}

// --- Phase handlers ---

func (r *ExamReconciler) reconcileProvisioning(ctx context.Context, exam *examv1alpha1.Exam) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	ns := examNamespace(exam.Name)

	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
	if err := r.Create(ctx, namespace); err != nil && !errors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}

	allHealthy := true
	for i, student := range exam.Spec.Students {
		s, err := r.findOrGenerateSlug(exam, student.ID)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("slug generation for %s: %w", student.ID, err)
		}
		if err := r.provisionInstance(ctx, exam, ns, student.ID, s); err != nil {
			logger.Error(err, "Failed to provision student", "student", student.ID)
			r.setStudentStatus(exam, i, s, examv1alpha1.StudentPhaseFailed)
			allHealthy = false
			continue
		}
		r.setStudentStatus(exam, i, s, examv1alpha1.StudentPhaseProvisioned)
	}

	for i := range exam.Spec.Spares {
		s, err := r.findOrGenerateSpareSlug(exam, i)
		if err != nil {
			return ctrl.Result{}, fmt.Errorf("slug generation for spare %d: %w", i, err)
		}
		if err := r.provisionInstance(ctx, exam, ns, "", s); err != nil {
			logger.Error(err, "Failed to provision spare", "index", i)
			allHealthy = false
			continue
		}
		r.setSpareStatus(exam, i, s, examv1alpha1.StudentPhaseProvisioned)
	}

	if !allHealthy {
		meta.SetStatusCondition(&exam.Status.Conditions, metav1.Condition{
			Type:    "ProvisioningDegraded",
			Status:  metav1.ConditionTrue,
			Reason:  "SomeInstancesFailed",
			Message: "One or more instances failed to provision",
		})
	}

	if r.allInstancesHealthy(ctx, exam, ns) {
		meta.SetStatusCondition(&exam.Status.Conditions, metav1.Condition{
			Type:   "Provisioned",
			Status: metav1.ConditionTrue,
			Reason: "AllHealthy",
		})
		exam.Status.Phase = examv1alpha1.ExamPhaseReady

		if exam.Spec.Spares > 0 {
			var urls []string
			for _, sp := range exam.Status.Spares {
				urls = append(urls, sp.URL)
			}
			msg := notifier.BuildSparesMessage(exam.Spec.Email.From, exam.Spec.Email.InstructorEmail, exam.Spec.Email.Subject, urls)
			if err := r.sendEmail(ctx, exam, exam.Spec.Email.From, []string{exam.Spec.Email.InstructorEmail}, []byte(msg)); err != nil {
				logger.Error(err, "Failed to send spare URLs to instructor")
			}
		}

		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

func (r *ExamReconciler) reconcileReady(ctx context.Context, exam *examv1alpha1.Exam, now time.Time) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	_, emailTime, _, _ := computeSchedule(exam)
	unlock := exam.Spec.Schedule.Unlock.Time

	if !meta.IsStatusConditionTrue(exam.Status.Conditions, "AllEmailsSent") && !now.Before(emailTime) {
		rateLimit := exam.Spec.Email.RateLimit
		if rateLimit <= 0 {
			rateLimit = 1
		}
		sent := r.sendNextPendingEmail(ctx, exam)
		if sent {
			return ctrl.Result{RequeueAfter: time.Second / time.Duration(rateLimit)}, nil
		}
		meta.SetStatusCondition(&exam.Status.Conditions, metav1.Condition{
			Type:   "AllEmailsSent",
			Status: metav1.ConditionTrue,
			Reason: "Complete",
		})
	}

	if exam.Spec.Schedule.DryRun != nil && !meta.IsStatusConditionTrue(exam.Status.Conditions, "DryRunComplete") {
		dryRunTime := unlock.Add(-exam.Spec.Schedule.DryRun.Before.Duration)
		if !now.Before(dryRunTime) {
			r.runDryRun(ctx, exam)
			meta.SetStatusCondition(&exam.Status.Conditions, metav1.Condition{
				Type:   "DryRunComplete",
				Status: metav1.ConditionTrue,
				Reason: "Complete",
			})
		}
	}

	r.enforcePolicies(ctx, exam, false)

	nextWake := unlock
	if !meta.IsStatusConditionTrue(exam.Status.Conditions, "AllEmailsSent") && emailTime.Before(nextWake) {
		nextWake = emailTime
	}
	if exam.Spec.Schedule.DryRun != nil && !meta.IsStatusConditionTrue(exam.Status.Conditions, "DryRunComplete") {
		dryRunTime := unlock.Add(-exam.Spec.Schedule.DryRun.Before.Duration)
		if dryRunTime.Before(nextWake) {
			nextWake = dryRunTime
		}
	}

	requeue := time.Until(nextWake)
	if requeue < 0 {
		requeue = 0
	}
	logger.Info("Ready phase waiting", "nextWake", nextWake, "requeue", requeue)
	return ctrl.Result{RequeueAfter: requeue}, nil
}

func (r *ExamReconciler) reconcileUnlock(ctx context.Context, exam *examv1alpha1.Exam, now time.Time) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	r.enforcePolicies(ctx, exam, true)

	for i := range exam.Status.Students {
		if exam.Status.Students[i].Phase != examv1alpha1.StudentPhaseFailed {
			exam.Status.Students[i].Phase = examv1alpha1.StudentPhaseUnlocked
		}
	}
	for i := range exam.Status.Spares {
		if exam.Status.Spares[i].Phase != examv1alpha1.StudentPhaseFailed {
			exam.Status.Spares[i].Phase = examv1alpha1.StudentPhaseUnlocked
		}
	}

	if !meta.IsStatusConditionTrue(exam.Status.Conditions, "InstructorNotifiedUnlock") {
		var failedEmails []string
		for _, s := range exam.Status.Students {
			if s.EmailStatus == examv1alpha1.EmailStatusFailed {
				failedEmails = append(failedEmails, s.ID)
			}
		}
		msg := notifier.BuildUnlockNotification(exam.Spec.Email.From, exam.Spec.Email.InstructorEmail,
			exam.Spec.Email.Subject, len(exam.Spec.Students), exam.Spec.Spares, failedEmails)
		if err := r.sendEmail(ctx, exam, exam.Spec.Email.From, []string{exam.Spec.Email.InstructorEmail}, []byte(msg)); err != nil {
			logger.Error(err, "Failed to send unlock notification")
		} else {
			meta.SetStatusCondition(&exam.Status.Conditions, metav1.Condition{
				Type: "InstructorNotifiedUnlock", Status: metav1.ConditionTrue, Reason: "Sent",
			})
		}
	}

	exam.Status.Message = fmt.Sprintf("Exam in progress — %d students, %d spares", len(exam.Spec.Students), exam.Spec.Spares)

	lockTime := exam.Status.ComputedLockTime.Time
	return ctrl.Result{RequeueAfter: time.Until(lockTime)}, nil
}

func (r *ExamReconciler) reconcileLocked(ctx context.Context, exam *examv1alpha1.Exam, now time.Time) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	ns := examNamespace(exam.Name)

	r.enforcePolicies(ctx, exam, false)
	if err := r.deleteIngresses(ctx, ns, exam.Name); err != nil {
		return ctrl.Result{}, err
	}

	for i := range exam.Status.Students {
		if exam.Status.Students[i].Phase != examv1alpha1.StudentPhaseFailed {
			exam.Status.Students[i].Phase = examv1alpha1.StudentPhaseLocked
		}
	}
	for i := range exam.Status.Spares {
		if exam.Status.Spares[i].Phase != examv1alpha1.StudentPhaseFailed {
			exam.Status.Spares[i].Phase = examv1alpha1.StudentPhaseLocked
		}
	}

	if !meta.IsStatusConditionTrue(exam.Status.Conditions, "InstructorNotifiedLock") {
		healthy := 0
		failed := 0
		for _, s := range exam.Status.Students {
			if s.Phase == examv1alpha1.StudentPhaseFailed {
				failed++
			} else {
				healthy++
			}
		}
		msg := notifier.BuildLockNotification(exam.Spec.Email.From, exam.Spec.Email.InstructorEmail,
			exam.Spec.Email.Subject, len(exam.Spec.Students), healthy, failed)
		if err := r.sendEmail(ctx, exam, exam.Spec.Email.From, []string{exam.Spec.Email.InstructorEmail}, []byte(msg)); err != nil {
			logger.Error(err, "Failed to send lock notification")
		} else {
			meta.SetStatusCondition(&exam.Status.Conditions, metav1.Condition{
				Type: "InstructorNotifiedLock", Status: metav1.ConditionTrue, Reason: "Sent",
			})
		}
	}

	exam.Status.Message = "Exam ended, instances retained for investigation"
	requeue := time.Until(exam.Status.RetentionDeadline.Time)
	if requeue < 0 {
		requeue = 0
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

func (r *ExamReconciler) reconcileTeardown(ctx context.Context, exam *examv1alpha1.Exam) error {
	ns := examNamespace(exam.Name)
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
	if err := r.Delete(ctx, namespace); err != nil && !errors.IsNotFound(err) {
		return err
	}
	exam.Status.Phase = examv1alpha1.ExamPhaseTearingDown
	exam.Status.Message = "Namespace deleted"
	return nil
}

// --- Helpers ---

func (r *ExamReconciler) provisionInstance(ctx context.Context, exam *examv1alpha1.Exam, ns, studentID, s string) error {
	dep := provisioner.Deployment(exam, ns, studentID, s)
	if err := r.Create(ctx, dep); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	svc := provisioner.Service(exam, ns, studentID, s)
	if err := r.Create(ctx, svc); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	// Ingress is created at unlock time, not during provisioning — the route
	// should not exist while deny-all policies are active.
	labels := provisioner.Labels(exam.Name, studentID, s)
	labels["exam.otu.ca/port"] = fmt.Sprintf("%d", exam.Spec.Template.Port)
	denyAll := r.PolicyProvider.DenyAll(ns, labels)
	if err := r.Create(ctx, denyAll); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	egressAllow := r.PolicyProvider.EgressAllowlist(ns, labels)
	if err := r.Create(ctx, egressAllow); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (r *ExamReconciler) findOrGenerateSlug(exam *examv1alpha1.Exam, studentID string) (string, error) {
	for _, s := range exam.Status.Students {
		if s.ID == studentID && s.Slug != "" {
			return s.Slug, nil
		}
	}
	return slug.Generate()
}

func (r *ExamReconciler) findOrGenerateSpareSlug(exam *examv1alpha1.Exam, index int) (string, error) {
	if index < len(exam.Status.Spares) && exam.Status.Spares[index].Slug != "" {
		return exam.Status.Spares[index].Slug, nil
	}
	return slug.Generate()
}

func (r *ExamReconciler) setStudentStatus(exam *examv1alpha1.Exam, index int, s string, phase examv1alpha1.StudentPhase) {
	for len(exam.Status.Students) <= index {
		exam.Status.Students = append(exam.Status.Students, examv1alpha1.StudentStatus{})
	}
	student := exam.Spec.Students[index]
	exam.Status.Students[index] = examv1alpha1.StudentStatus{
		ID:          student.ID,
		Slug:        s,
		URL:         fmt.Sprintf("https://%s.%s", s, exam.Spec.Domain),
		Phase:       phase,
		EmailStatus: examv1alpha1.EmailStatusPending,
	}
}

func (r *ExamReconciler) setSpareStatus(exam *examv1alpha1.Exam, index int, s string, phase examv1alpha1.StudentPhase) {
	for len(exam.Status.Spares) <= index {
		exam.Status.Spares = append(exam.Status.Spares, examv1alpha1.SpareStatus{})
	}
	exam.Status.Spares[index] = examv1alpha1.SpareStatus{
		Slug:  s,
		URL:   fmt.Sprintf("https://%s.%s", s, exam.Spec.Domain),
		Phase: phase,
	}
}

func (r *ExamReconciler) allInstancesHealthy(ctx context.Context, exam *examv1alpha1.Exam, ns string) bool {
	var deps appsv1.DeploymentList
	if err := r.List(ctx, &deps, client.InNamespace(ns), client.MatchingLabels{"exam.otu.ca/exam": exam.Name}); err != nil {
		return false
	}
	for _, d := range deps.Items {
		if d.Status.ReadyReplicas < 1 {
			return false
		}
	}
	return len(deps.Items) == len(exam.Spec.Students)+exam.Spec.Spares
}

func (r *ExamReconciler) sendNextPendingEmail(ctx context.Context, exam *examv1alpha1.Exam) bool {
	for i := range exam.Status.Students {
		if exam.Status.Students[i].EmailStatus == examv1alpha1.EmailStatusPending {
			student := exam.Spec.Students[i]
			msg := notifier.BuildStudentMessage(exam.Spec.Email.From, student.Email, exam.Spec.Email.Subject, exam.Status.Students[i].URL)
			now := metav1.Now()
			if err := r.sendEmail(ctx, exam, exam.Spec.Email.From, []string{student.Email}, []byte(msg)); err != nil {
				exam.Status.Students[i].EmailStatus = examv1alpha1.EmailStatusFailed
				if r.Metrics != nil {
					r.Metrics.EmailsFailed.WithLabelValues(exam.Name).Inc()
				}
			} else {
				exam.Status.Students[i].EmailStatus = examv1alpha1.EmailStatusSent
				exam.Status.Students[i].EmailSentAt = &now
				if r.Metrics != nil {
					r.Metrics.EmailsSent.WithLabelValues(exam.Name).Inc()
				}
			}
			return true
		}
	}
	return false
}

func (r *ExamReconciler) runDryRun(ctx context.Context, exam *examv1alpha1.Exam) {
	ns := examNamespace(exam.Name)
	var targets []smoketest.Target
	for _, s := range exam.Status.Students {
		targets = append(targets, smoketest.Target{
			StudentID: s.ID,
			URL:       fmt.Sprintf("http://%s.%s:%d", s.Slug, ns, exam.Spec.Template.Port),
		})
	}
	for _, s := range exam.Status.Spares {
		targets = append(targets, smoketest.Target{
			StudentID: "spare-" + s.Slug,
			URL:       fmt.Sprintf("http://%s.%s:%d", s.Slug, ns, exam.Spec.Template.Port),
		})
	}

	negativeURL := ""
	if len(exam.Status.Students) > 0 {
		s := exam.Status.Students[0]
		negativeURL = fmt.Sprintf("http://%s.%s:%d", s.Slug, ns, exam.Spec.Template.Port)
	}

	dr := smoketest.RunDryRun(ctx, targets, negativeURL)
	now := metav1.Now()
	exam.Status.DryRun = &examv1alpha1.DryRunStatus{
		CompletedAt: &now,
		Passed:      dr.Result.Passed,
		Failed:      dr.Result.Failed,
	}
	for _, f := range dr.Result.Failures {
		exam.Status.DryRun.Failures = append(exam.Status.DryRun.Failures, examv1alpha1.DryRunFailure{
			Student: f.Student,
			Error:   f.Error,
		})
	}

	status := metav1.ConditionTrue
	reason := "Verified"
	if !dr.PolicyEnforced {
		status = metav1.ConditionFalse
		reason = "NotEnforced"
	}
	meta.SetStatusCondition(&exam.Status.Conditions, metav1.Condition{
		Type:    "NetworkPolicyEnforced",
		Status:  status,
		Reason:  reason,
		Message: fmt.Sprintf("Negative connectivity test: policyEnforced=%v", dr.PolicyEnforced),
	})

	if dr.Result.Failed > 0 {
		meta.SetStatusCondition(&exam.Status.Conditions, metav1.Condition{
			Type:    "DryRunFailed",
			Status:  metav1.ConditionTrue,
			Reason:  "SomeFailed",
			Message: fmt.Sprintf("%d of %d checks failed", dr.Result.Failed, dr.Result.Passed+dr.Result.Failed),
		})
	}

	if r.Metrics != nil {
		r.Metrics.DryRunPassed.WithLabelValues(exam.Name).Set(float64(dr.Result.Passed))
		r.Metrics.DryRunFailed.WithLabelValues(exam.Name).Set(float64(dr.Result.Failed))
	}
}

func (r *ExamReconciler) enforcePolicies(ctx context.Context, exam *examv1alpha1.Exam, unlocked bool) {
	ns := examNamespace(exam.Name)
	allSlugs := r.collectSlugs(exam)

	for _, entry := range allSlugs {
		labels := provisioner.Labels(exam.Name, entry.studentID, entry.slug)
		labels["exam.otu.ca/port"] = fmt.Sprintf("%d", exam.Spec.Template.Port)

		denyAll := r.PolicyProvider.DenyAll(ns, labels)
		if err := r.Create(ctx, denyAll); err != nil && !errors.IsAlreadyExists(err) {
			log.FromContext(ctx).Error(err, "drift: failed to create deny-all", "slug", entry.slug)
		}
		egressAllow := r.PolicyProvider.EgressAllowlist(ns, labels)
		if err := r.Create(ctx, egressAllow); err != nil && !errors.IsAlreadyExists(err) {
			log.FromContext(ctx).Error(err, "drift: failed to create egress-allow", "slug", entry.slug)
		}

		ingressAllow := r.PolicyProvider.IngressAllow(ns, labels)
		if unlocked {
			// Create Ingress resource alongside ingress-allow policy
			ing := provisioner.Ingress(exam, ns, entry.studentID, entry.slug)
			if err := r.Create(ctx, ing); err != nil && !errors.IsAlreadyExists(err) {
				log.FromContext(ctx).Error(err, "drift: failed to create ingress", "slug", entry.slug)
			}
			if err := r.Create(ctx, ingressAllow); err != nil && !errors.IsAlreadyExists(err) {
				log.FromContext(ctx).Error(err, "drift: failed to create ingress-allow", "slug", entry.slug)
			}
		} else {
			if err := r.Delete(ctx, ingressAllow); err != nil && !errors.IsNotFound(err) {
				log.FromContext(ctx).Error(err, "drift: failed to delete ingress-allow", "slug", entry.slug)
			}
		}
	}
}

type slugEntry struct {
	studentID string
	slug      string
}

func (r *ExamReconciler) collectSlugs(exam *examv1alpha1.Exam) []slugEntry {
	var entries []slugEntry
	for _, s := range exam.Status.Students {
		entries = append(entries, slugEntry{studentID: s.ID, slug: s.Slug})
	}
	for _, s := range exam.Status.Spares {
		entries = append(entries, slugEntry{studentID: "", slug: s.Slug})
	}
	return entries
}

func (r *ExamReconciler) deleteIngresses(ctx context.Context, ns, examName string) error {
	var ingresses networkingv1.IngressList
	if err := r.List(ctx, &ingresses, client.InNamespace(ns), client.MatchingLabels{"exam.otu.ca/exam": examName}); err != nil {
		return err
	}
	for i := range ingresses.Items {
		if err := r.Delete(ctx, &ingresses.Items[i]); err != nil && !errors.IsNotFound(err) {
			return err
		}
	}
	return nil
}

func (r *ExamReconciler) updateMetricsSummary(exam *examv1alpha1.Exam) {
	healthy, failed, emailsSent, emailsFailed := 0, 0, 0, 0
	for _, s := range exam.Status.Students {
		switch s.Phase {
		case examv1alpha1.StudentPhaseFailed:
			failed++
		default:
			healthy++
		}
		switch s.EmailStatus {
		case examv1alpha1.EmailStatusSent:
			emailsSent++
		case examv1alpha1.EmailStatusFailed:
			emailsFailed++
		}
	}
	for _, s := range exam.Status.Spares {
		if s.Phase == examv1alpha1.StudentPhaseFailed {
			failed++
		} else {
			healthy++
		}
	}
	exam.Status.Metrics = &examv1alpha1.MetricsSummary{
		TotalStudents:    len(exam.Spec.Students),
		TotalSpares:      exam.Spec.Spares,
		EmailsSent:       emailsSent,
		EmailsFailed:     emailsFailed,
		InstancesHealthy: healthy,
		InstancesFailed:  failed,
	}

	if r.Metrics != nil {
		name := exam.Name
		r.Metrics.InstancesTotal.WithLabelValues(name).Set(float64(len(exam.Spec.Students) + exam.Spec.Spares))
		r.Metrics.InstancesHealthy.WithLabelValues(name).Set(float64(healthy))
		r.Metrics.InstancesFailed.WithLabelValues(name).Set(float64(failed))
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ExamReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapToExam := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			labels := obj.GetLabels()
			examName := labels["exam.otu.ca/exam"]
			if examName == "" {
				return nil
			}
			return []reconcile.Request{{
				NamespacedName: types.NamespacedName{
					Name:      examName,
					Namespace: "exam-system",
				},
			}}
		},
	)

	return ctrl.NewControllerManagedBy(mgr).
		For(&examv1alpha1.Exam{}).
		Watches(&appsv1.Deployment{}, mapToExam).
		Watches(&corev1.Service{}, mapToExam).
		Watches(&networkingv1.Ingress{}, mapToExam).
		Watches(&networkingv1.NetworkPolicy{}, mapToExam).
		Complete(r)
}
