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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
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

// PlatformConfig carries cluster-level settings shared by all exams managed by
// this controller instance.
type PlatformConfig struct {
	BaseDomain           string
	IngressTLSSecretName string
	SMTPSecretName       string
	SecretNamespace      string
}

// +kubebuilder:rbac:groups=exam.otu.ca,resources=exams,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=exam.otu.ca,resources=exams/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=exam.otu.ca,resources=exams/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=services;namespaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses;networkpolicies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=cilium.io,resources=ciliumnetworkpolicies,verbs=get;list;watch;create;update;patch;delete

// ExamReconciler reconciles an Exam object.
type ExamReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	Platform       PlatformConfig
	PolicyProvider network.PolicyProvider
	Sender         notifier.Sender
	Now            func() time.Time
	Metrics        *metrics.ExamMetrics
	Checker        smoketest.HealthChecker // nil defaults to HTTPChecker
}

func (r *ExamReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *ExamReconciler) platformSecretNamespace(exam *examv1alpha1.Exam) string {
	if r.Platform.SecretNamespace != "" {
		return r.Platform.SecretNamespace
	}
	return exam.Namespace
}

func (r *ExamReconciler) validatePlatformConfig() error {
	switch {
	case r.Platform.BaseDomain == "":
		return fmt.Errorf("platform config missing base domain")
	case r.Platform.IngressTLSSecretName == "":
		return fmt.Errorf("platform config missing ingress TLS secret name")
	case r.Platform.SMTPSecretName == "":
		return fmt.Errorf("platform config missing SMTP secret name")
	default:
		return nil
	}
}

func (r *ExamReconciler) instanceURL(s string) string {
	return fmt.Sprintf("https://%s.%s", s, r.Platform.BaseDomain)
}

// resolvedSender returns a Sender configured with SMTP credentials from the
// controller's platform Secret. If r.Sender is already set to a non-SMTPSender
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
		Name:      r.Platform.SMTPSecretName,
		Namespace: r.platformSecretNamespace(exam),
	}, &secret); err != nil {
		return nil, fmt.Errorf("reading SMTP secret %q from namespace %q: %w",
			r.Platform.SMTPSecretName, r.platformSecretNamespace(exam), err)
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

// sendEmail resolves SMTP credentials and sends an email.
func (r *ExamReconciler) sendEmail(ctx context.Context, exam *examv1alpha1.Exam, to []string, msg []byte) error {
	sender, err := r.resolvedSender(ctx, exam)
	if err != nil {
		return err
	}
	return sender.Send(exam.Spec.Email.From, to, msg)
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

func examNamespace(examName, examCRNamespace string) string {
	const (
		prefix      = "exam-"
		hashLength  = 8
		maxLength   = 63
		minBaseName = "instance"
	)

	sum := sha256.Sum256([]byte(examCRNamespace + "/" + examName))
	hashSuffix := hex.EncodeToString(sum[:])[:hashLength]

	maxBaseLen := maxLength - len(prefix) - len(hashSuffix) - 1
	base := examName
	if maxBaseLen <= 0 {
		base = minBaseName
	} else if len(base) > maxBaseLen {
		base = base[:maxBaseLen]
		for len(base) > 0 && base[len(base)-1] == '-' {
			base = base[:len(base)-1]
		}
		if base == "" {
			base = minBaseName
		}
	}

	return fmt.Sprintf("%s%s-%s", prefix, base, hashSuffix)
}

func setCondition(exam *examv1alpha1.Exam, c metav1.Condition) {
	c.ObservedGeneration = exam.Generation
	meta.SetStatusCondition(&exam.Status.Conditions, c)
}

func examResourceLabels(exam *examv1alpha1.Exam) map[string]string {
	return provisioner.OwnerLabels(exam)
}

func examResourceSelector(exam *examv1alpha1.Exam) client.MatchingLabels {
	return client.MatchingLabels(examResourceLabels(exam))
}

func examStudentSelector(exam *examv1alpha1.Exam, studentID string) client.MatchingLabels {
	labels := examResourceLabels(exam)
	labels[provisioner.LabelStudent] = studentID
	return client.MatchingLabels(labels)
}

func examMetricLabelValues(exam *examv1alpha1.Exam) []string {
	return metrics.LabelValues(exam.Name, exam.Namespace)
}

func phaseTransitionMetricLabelValues(exam *examv1alpha1.Exam, from, to examv1alpha1.ExamPhase) []string {
	return append(examMetricLabelValues(exam), string(from), string(to))
}

func requestsForOwnedObject(obj client.Object) []reconcile.Request {
	labels := obj.GetLabels()
	examName := labels[provisioner.LabelExam]
	examNamespace := labels[provisioner.LabelExamNamespace]
	if examName == "" || examNamespace == "" {
		return nil
	}

	return []reconcile.Request{{
		NamespacedName: types.NamespacedName{
			Name:      examName,
			Namespace: examNamespace,
		},
	}}
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
	start := r.now()

	var exam examv1alpha1.Exam
	if err := r.Get(ctx, req.NamespacedName, &exam); err != nil {
		if apierrors.IsNotFound(err) {
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
			r.Metrics.PhaseTransitions.WithLabelValues(
				phaseTransitionMetricLabelValues(&exam, oldPhase, desiredPhase)...,
			).Inc()
		}
	}

	// Dispatch to phase handler
	var result ctrl.Result
	var err error
	switch exam.Status.Phase {
	case examv1alpha1.ExamPhasePending:
		result = ctrl.Result{RequeueAfter: max(provisionTime.Sub(now), 0)}
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

	// Update countdown gauges and metrics summary (skip after teardown — CleanupExam already ran)
	if exam.Status.Phase != examv1alpha1.ExamPhaseTearingDown {
		if r.Metrics != nil {
			unlock := exam.Spec.Schedule.Unlock.Time
			metricLabels := examMetricLabelValues(&exam)
			r.Metrics.SecondsUntilUnlock.WithLabelValues(metricLabels...).Set(max(unlock.Sub(now).Seconds(), 0))
			r.Metrics.SecondsUntilLock.WithLabelValues(metricLabels...).Set(max(lockTime.Sub(now).Seconds(), 0))
		}
		r.updateMetricsSummary(&exam)
	}
	if statusErr := r.Status().Update(ctx, &exam); statusErr != nil {
		logger.Error(statusErr, "Failed to update status")
		err = errors.Join(err, statusErr)
	}

	// Record reconcile duration
	if r.Metrics != nil {
		r.Metrics.ReconcileDuration.Observe(r.now().Sub(start).Seconds())
		if err != nil {
			r.Metrics.ReconcileErrors.Inc()
		}
	}

	return result, err
}

// --- Phase handlers ---

func (r *ExamReconciler) reconcileProvisioning(ctx context.Context, exam *examv1alpha1.Exam) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	ns := examNamespace(exam.Name, exam.Namespace)

	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ns,
			Labels: examResourceLabels(exam),
		},
	}
	if err := r.Create(ctx, namespace); err != nil && !apierrors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}
	if err := r.reconcilePlatformTLSSecret(ctx, exam, ns); err != nil {
		return ctrl.Result{}, err
	}

	allHealthy := true
	for i, student := range exam.Spec.Students {
		s, err := r.findOrGenerateSlug(ctx, exam, ns, student.ID)
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
		s, err := r.findOrGenerateSpareSlug(ctx, exam, ns, i)
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
		setCondition(exam, metav1.Condition{
			Type:    "ProvisioningDegraded",
			Status:  metav1.ConditionTrue,
			Reason:  "SomeInstancesFailed",
			Message: "One or more instances failed to provision",
		})
	}

	// Clean up orphaned resources left by concurrent reconcile races.
	// Two simultaneous reconciles can each generate a different slug for the
	// same student, creating duplicate deployments/services/policies. Remove
	// any resources whose slug doesn't match the now-canonical set.
	if err := r.cleanupOrphanedResources(ctx, exam, ns); err != nil {
		logger.Error(err, "Failed to clean up orphaned resources")
	}

	if r.allInstancesHealthy(ctx, exam, ns) {
		setCondition(exam, metav1.Condition{
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
			if err := r.sendEmail(ctx, exam, []string{exam.Spec.Email.InstructorEmail}, []byte(msg)); err != nil {
				logger.Error(err, "Failed to send spare URLs to instructor")
			}
		}

		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

//nolint:unparam // error return kept for consistency with other reconcile methods
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
		setCondition(exam, metav1.Condition{
			Type:   "AllEmailsSent",
			Status: metav1.ConditionTrue,
			Reason: "Complete",
		})
	}

	if exam.Spec.Schedule.DryRun != nil && !meta.IsStatusConditionTrue(exam.Status.Conditions, "DryRunComplete") {
		dryRunTime := unlock.Add(-exam.Spec.Schedule.DryRun.Before.Duration)
		if !now.Before(dryRunTime) {
			r.runDryRun(ctx, exam)
			setCondition(exam, metav1.Condition{
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

	requeue := max(nextWake.Sub(now), 0)
	logger.Info("Ready phase waiting", "nextWake", nextWake, "requeue", requeue)
	return ctrl.Result{RequeueAfter: requeue}, nil
}

//nolint:unparam // error return kept for consistency with other reconcile methods
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
		if err := r.sendEmail(ctx, exam, []string{exam.Spec.Email.InstructorEmail}, []byte(msg)); err != nil {
			logger.Error(err, "Failed to send unlock notification")
		} else {
			setCondition(exam, metav1.Condition{
				Type: "InstructorNotifiedUnlock", Status: metav1.ConditionTrue, Reason: "Sent",
			})
		}
	}

	exam.Status.Message = fmt.Sprintf("Exam in progress — %d students, %d spares", len(exam.Spec.Students), exam.Spec.Spares)

	lockTime := exam.Status.ComputedLockTime.Time
	return ctrl.Result{RequeueAfter: max(lockTime.Sub(now), 0)}, nil
}

func (r *ExamReconciler) reconcileLocked(ctx context.Context, exam *examv1alpha1.Exam, now time.Time) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	ns := examNamespace(exam.Name, exam.Namespace)

	r.enforcePolicies(ctx, exam, false)
	if err := r.deleteIngresses(ctx, exam, ns); err != nil {
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
		if err := r.sendEmail(ctx, exam, []string{exam.Spec.Email.InstructorEmail}, []byte(msg)); err != nil {
			logger.Error(err, "Failed to send lock notification")
		} else {
			setCondition(exam, metav1.Condition{
				Type: "InstructorNotifiedLock", Status: metav1.ConditionTrue, Reason: "Sent",
			})
		}
	}

	exam.Status.Message = "Exam ended, instances retained for investigation"
	return ctrl.Result{RequeueAfter: max(exam.Status.RetentionDeadline.Sub(now), 0)}, nil
}

func (r *ExamReconciler) reconcileTeardown(ctx context.Context, exam *examv1alpha1.Exam) error {
	ns := examNamespace(exam.Name, exam.Namespace)
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
	if err := r.Delete(ctx, namespace); err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if r.Metrics != nil {
		r.Metrics.CleanupExam(exam.Name, exam.Namespace)
	}
	exam.Status.Phase = examv1alpha1.ExamPhaseTearingDown
	exam.Status.Message = "Namespace deleted"
	return nil
}

// --- Helpers ---

func (r *ExamReconciler) provisionInstance(ctx context.Context, exam *examv1alpha1.Exam, ns, studentID, s string) error {
	dep := provisioner.Deployment(exam, ns, studentID, s)
	if err := r.Create(ctx, dep); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	svc := provisioner.Service(exam, ns, studentID, s)
	if err := r.Create(ctx, svc); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	// Ingress is created at unlock time, not during provisioning — the route
	// should not exist while deny-all policies are active.
	labels := provisioner.Labels(exam, studentID, s)
	labels[provisioner.LabelPort] = fmt.Sprintf("%d", exam.Spec.Template.Port)
	denyAll := r.PolicyProvider.DenyAll(ns, labels)
	if err := r.Create(ctx, denyAll); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	egressAllow := r.PolicyProvider.EgressAllowlist(ns, labels)
	if err := r.Create(ctx, egressAllow); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func (r *ExamReconciler) reconcilePlatformTLSSecret(ctx context.Context, exam *examv1alpha1.Exam, examNS string) error {
	var source corev1.Secret
	sourceNN := types.NamespacedName{
		Name:      r.Platform.IngressTLSSecretName,
		Namespace: r.platformSecretNamespace(exam),
	}
	if err := r.Get(ctx, sourceNN, &source); err != nil {
		return fmt.Errorf("reading ingress TLS secret %q from namespace %q: %w",
			sourceNN.Name, sourceNN.Namespace, err)
	}

	target := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      r.Platform.IngressTLSSecretName,
			Namespace: examNS,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, target, func() error {
		target.Labels = examResourceLabels(exam)
		target.Type = source.Type
		target.Data = make(map[string][]byte, len(source.Data))
		for k, v := range source.Data {
			target.Data[k] = append([]byte(nil), v...)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("copying ingress TLS secret %q into namespace %q: %w",
			r.Platform.IngressTLSSecretName, examNS, err)
	}
	return nil
}

func (r *ExamReconciler) findOrGenerateSlug(ctx context.Context, exam *examv1alpha1.Exam, ns, studentID string) (string, error) {
	for _, s := range exam.Status.Students {
		if s.ID == studentID && s.Slug != "" {
			return s.Slug, nil
		}
	}
	// Check for an existing deployment created by a concurrent reconcile whose
	// status update lost the race.  Reusing that slug prevents orphaned resources.
	if r.Client != nil {
		var deps appsv1.DeploymentList
		if err := r.List(ctx, &deps, client.InNamespace(ns),
			examStudentSelector(exam, studentID)); err == nil && len(deps.Items) > 0 {
			if s := deps.Items[0].Labels[provisioner.LabelSlug]; s != "" {
				return s, nil
			}
		}
	}
	return slug.Generate()
}

func (r *ExamReconciler) findOrGenerateSpareSlug(ctx context.Context, exam *examv1alpha1.Exam, ns string, index int) (string, error) {
	if index < len(exam.Status.Spares) && exam.Status.Spares[index].Slug != "" {
		return exam.Status.Spares[index].Slug, nil
	}
	// Check for existing spare deployments (no student label) to reuse slugs
	// from concurrent reconcile races.
	if r.Client != nil {
		var deps appsv1.DeploymentList
		if err := r.List(ctx, &deps, client.InNamespace(ns),
			examResourceSelector(exam)); err == nil {
			spareDeployments := make([]string, 0)
			for _, d := range deps.Items {
				if _, hasStudent := d.Labels[provisioner.LabelStudent]; !hasStudent {
					if s := d.Labels[provisioner.LabelSlug]; s != "" {
						spareDeployments = append(spareDeployments, s)
					}
				}
			}
			sort.Strings(spareDeployments)
			if index < len(spareDeployments) {
				return spareDeployments[index], nil
			}
		}
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
		URL:         r.instanceURL(s),
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
		URL:   r.instanceURL(s),
		Phase: phase,
	}
}

func (r *ExamReconciler) allInstancesHealthy(ctx context.Context, exam *examv1alpha1.Exam, ns string) bool {
	var deps appsv1.DeploymentList
	if err := r.List(ctx, &deps, client.InNamespace(ns), examResourceSelector(exam)); err != nil {
		return false
	}
	for _, d := range deps.Items {
		if d.Status.ReadyReplicas < 1 {
			return false
		}
	}
	return len(deps.Items) == len(exam.Spec.Students)+exam.Spec.Spares
}

func (r *ExamReconciler) validSlugs(exam *examv1alpha1.Exam) map[string]bool {
	slugs := make(map[string]bool)
	for _, s := range exam.Status.Students {
		if s.Slug != "" {
			slugs[s.Slug] = true
		}
	}
	for _, s := range exam.Status.Spares {
		if s.Slug != "" {
			slugs[s.Slug] = true
		}
	}
	return slugs
}

func (r *ExamReconciler) cleanupOrphanedResources(ctx context.Context, exam *examv1alpha1.Exam, ns string) error {
	logger := log.FromContext(ctx)
	slugs := r.validSlugs(exam)

	var deps appsv1.DeploymentList
	if err := r.List(ctx, &deps, client.InNamespace(ns), examResourceSelector(exam)); err != nil {
		return err
	}
	for i := range deps.Items {
		if s := deps.Items[i].Labels[provisioner.LabelSlug]; s != "" && !slugs[s] {
			logger.Info("Deleting orphaned deployment", "slug", s)
			if err := r.Delete(ctx, &deps.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}

	var svcs corev1.ServiceList
	if err := r.List(ctx, &svcs, client.InNamespace(ns), examResourceSelector(exam)); err != nil {
		return err
	}
	for i := range svcs.Items {
		if s := svcs.Items[i].Labels[provisioner.LabelSlug]; s != "" && !slugs[s] {
			logger.Info("Deleting orphaned service", "slug", s)
			if err := r.Delete(ctx, &svcs.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}

	var pols networkingv1.NetworkPolicyList
	if err := r.List(ctx, &pols, client.InNamespace(ns), examResourceSelector(exam)); err != nil {
		return err
	}
	for i := range pols.Items {
		if s := pols.Items[i].Labels[provisioner.LabelSlug]; s != "" && !slugs[s] {
			logger.Info("Deleting orphaned network policy", "slug", s)
			if err := r.Delete(ctx, &pols.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
		}
	}

	if _, ok := r.PolicyProvider.(*network.CiliumPolicyProvider); ok {
		policies := network.NewCiliumPolicyList()
		if err := r.List(ctx, policies, client.InNamespace(ns), examResourceSelector(exam)); err != nil {
			return err
		}
		for i := range policies.Items {
			if s := policies.Items[i].GetLabels()[provisioner.LabelSlug]; s != "" && !slugs[s] {
				logger.Info("Deleting orphaned CiliumNetworkPolicy", "slug", s)
				if err := r.Delete(ctx, &policies.Items[i]); err != nil && !apierrors.IsNotFound(err) {
					return err
				}
			}
		}
	}

	return nil
}

func (r *ExamReconciler) sendNextPendingEmail(ctx context.Context, exam *examv1alpha1.Exam) bool {
	for i := range exam.Status.Students {
		if exam.Status.Students[i].EmailStatus == examv1alpha1.EmailStatusPending {
			student := exam.Spec.Students[i]
			msg := notifier.BuildStudentMessage(exam.Spec.Email.From, student.Email, exam.Spec.Email.Subject, exam.Status.Students[i].URL)
			now := metav1.Now()
			if err := r.sendEmail(ctx, exam, []string{student.Email}, []byte(msg)); err != nil {
				exam.Status.Students[i].EmailStatus = examv1alpha1.EmailStatusFailed
				if r.Metrics != nil {
					r.Metrics.EmailsFailed.WithLabelValues(examMetricLabelValues(exam)...).Inc()
				}
			} else {
				exam.Status.Students[i].EmailStatus = examv1alpha1.EmailStatusSent
				exam.Status.Students[i].EmailSentAt = &now
				if r.Metrics != nil {
					r.Metrics.EmailsSent.WithLabelValues(examMetricLabelValues(exam)...).Inc()
				}
			}
			return true
		}
	}
	return false
}

func (r *ExamReconciler) runDryRun(ctx context.Context, exam *examv1alpha1.Exam) {
	checker := r.Checker
	if checker == nil {
		checker = &smoketest.HTTPChecker{}
	}

	ns := examNamespace(exam.Name, exam.Namespace)
	targets := make([]smoketest.Target, 0, len(exam.Status.Students)+len(exam.Status.Spares))
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

	dr := smoketest.RunDryRun(ctx, checker, targets, negativeURL)
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
	setCondition(exam, metav1.Condition{
		Type:    "NetworkPolicyEnforced",
		Status:  status,
		Reason:  reason,
		Message: fmt.Sprintf("Negative connectivity test: policyEnforced=%v", dr.PolicyEnforced),
	})

	if dr.Result.Failed > 0 {
		setCondition(exam, metav1.Condition{
			Type:    "DryRunFailed",
			Status:  metav1.ConditionTrue,
			Reason:  "SomeFailed",
			Message: fmt.Sprintf("%d of %d checks failed", dr.Result.Failed, dr.Result.Passed+dr.Result.Failed),
		})
	}

	if r.Metrics != nil {
		metricLabels := examMetricLabelValues(exam)
		r.Metrics.DryRunPassed.WithLabelValues(metricLabels...).Set(float64(dr.Result.Passed))
		r.Metrics.DryRunFailed.WithLabelValues(metricLabels...).Set(float64(dr.Result.Failed))
	}
}

func (r *ExamReconciler) enforcePolicies(ctx context.Context, exam *examv1alpha1.Exam, unlocked bool) {
	ns := examNamespace(exam.Name, exam.Namespace)
	allSlugs := r.collectSlugs(exam)

	if err := r.reconcilePlatformTLSSecret(ctx, exam, ns); err != nil {
		log.FromContext(ctx).Error(err, "drift: failed to reconcile ingress TLS secret", "namespace", ns)
	}

	for _, entry := range allSlugs {
		labels := provisioner.Labels(exam, entry.studentID, entry.slug)
		labels[provisioner.LabelPort] = fmt.Sprintf("%d", exam.Spec.Template.Port)

		denyAll := r.PolicyProvider.DenyAll(ns, labels)
		if err := r.Create(ctx, denyAll); err != nil && !apierrors.IsAlreadyExists(err) {
			log.FromContext(ctx).Error(err, "drift: failed to create deny-all", "slug", entry.slug)
		}
		egressAllow := r.PolicyProvider.EgressAllowlist(ns, labels)
		if err := r.Create(ctx, egressAllow); err != nil && !apierrors.IsAlreadyExists(err) {
			log.FromContext(ctx).Error(err, "drift: failed to create egress-allow", "slug", entry.slug)
		}

		ingressAllow := r.PolicyProvider.IngressAllow(ns, labels)
		if unlocked {
			ing := provisioner.Ingress(
				exam,
				ns,
				entry.studentID,
				entry.slug,
				r.Platform.BaseDomain,
				r.Platform.IngressTLSSecretName,
			)
			if err := r.Create(ctx, ing); err != nil && !apierrors.IsAlreadyExists(err) {
				log.FromContext(ctx).Error(err, "drift: failed to create ingress", "slug", entry.slug)
			}
			if err := r.Create(ctx, ingressAllow); err != nil && !apierrors.IsAlreadyExists(err) {
				log.FromContext(ctx).Error(err, "drift: failed to create ingress-allow", "slug", entry.slug)
			}
		} else {
			if err := r.Delete(ctx, ingressAllow); err != nil && !apierrors.IsNotFound(err) {
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
	entries := make([]slugEntry, 0, len(exam.Status.Students)+len(exam.Status.Spares))
	for _, s := range exam.Status.Students {
		entries = append(entries, slugEntry{studentID: s.ID, slug: s.Slug})
	}
	for _, s := range exam.Status.Spares {
		entries = append(entries, slugEntry{studentID: "", slug: s.Slug})
	}
	return entries
}

func (r *ExamReconciler) deleteIngresses(ctx context.Context, exam *examv1alpha1.Exam, ns string) error {
	var ingresses networkingv1.IngressList
	if err := r.List(ctx, &ingresses, client.InNamespace(ns), examResourceSelector(exam)); err != nil {
		return err
	}
	for i := range ingresses.Items {
		if err := r.Delete(ctx, &ingresses.Items[i]); err != nil && !apierrors.IsNotFound(err) {
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
		metricLabels := examMetricLabelValues(exam)
		r.Metrics.InstancesTotal.WithLabelValues(metricLabels...).Set(float64(len(exam.Spec.Students) + exam.Spec.Spares))
		r.Metrics.InstancesHealthy.WithLabelValues(metricLabels...).Set(float64(healthy))
		r.Metrics.InstancesFailed.WithLabelValues(metricLabels...).Set(float64(failed))
	}
}

// SetupWithManager sets up the controller with the Manager.
func (r *ExamReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.validatePlatformConfig(); err != nil {
		return err
	}

	mapToExam := handler.EnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []reconcile.Request {
			return requestsForOwnedObject(obj)
		},
	)

	builder := ctrl.NewControllerManagedBy(mgr).
		For(&examv1alpha1.Exam{}).
		Watches(&appsv1.Deployment{}, mapToExam).
		Watches(&corev1.Service{}, mapToExam).
		Watches(&networkingv1.Ingress{}, mapToExam).
		Watches(&networkingv1.NetworkPolicy{}, mapToExam)

	if _, ok := r.PolicyProvider.(*network.CiliumPolicyProvider); ok {
		builder = builder.Watches(network.NewCiliumPolicyObject(), mapToExam)
	}

	return builder.Complete(r)
}
