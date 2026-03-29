//go:build integration

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
	"sync/atomic"
	"time"

	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	"github.com/rdrake/exam-controller/internal/metrics"
	"github.com/rdrake/exam-controller/internal/network"
	"github.com/rdrake/exam-controller/internal/notifier"
	"github.com/rdrake/exam-controller/internal/provisioner"
)

// examCRNamespace is the namespace where Exam CRs live in integration tests.
const examCRNamespace = "exam-system"

const (
	testPlatformDomain          = "exam.test.com"
	testPlatformTLSSecretName   = "test-tls"
	testPlatformSMTPSecretName  = "smtp-secret"
	testPlatformSecretNamespace = examCRNamespace
)

var testCounter atomic.Int64

// uniqueExamName returns a unique exam name with the given prefix, safe for
// concurrent use across test files.
func uniqueExamName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, testCounter.Add(1))
}

// cleanupExam removes finalizers and deletes the Exam CR.
func cleanupExam(ctx context.Context, name, namespace string) {
	resource := &examv1alpha1.Exam{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, resource)
	if err == nil {
		resource.Finalizers = nil
		_ = k8sClient.Update(ctx, resource)
		_ = k8sClient.Delete(ctx, resource)
	}
}

// createExamCR creates an Exam CR with the given name and schedule in the
// exam-system namespace.
func createExamCR(ctx context.Context, name string, unlock time.Time, students []examv1alpha1.ExamStudent, spares int) {
	resource := &examv1alpha1.Exam{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: examCRNamespace,
		},
		Spec: examv1alpha1.ExamSpec{
			Template: examv1alpha1.ExamTemplate{
				Image:     "nginx:latest",
				Port:      8080,
				Resources: corev1.ResourceRequirements{},
			},
			Schedule: examv1alpha1.ExamSchedule{
				Unlock:          metav1.NewTime(unlock),
				Duration:        metav1.Duration{Duration: 2 * time.Hour},
				TimeMultiplier:  1.5,
				ProvisionBefore: metav1.Duration{Duration: 1 * time.Hour},
				Retention:       metav1.Duration{Duration: 24 * time.Hour},
			},
			Students: students,
			Spares:   spares,
			Email: examv1alpha1.ExamEmail{
				Before:          metav1.Duration{Duration: 30 * time.Minute},
				RateLimit:       10,
				InstructorEmail: "prof@test.com",
				From:            "test@test.com",
				Subject:         "Test Exam",
			},
		},
	}
	Expect(k8sClient.Create(ctx, resource)).To(Succeed())
}

// newReconciler creates an ExamReconciler with the given clock, sender, and metrics.
func newReconciler(nowFn func() time.Time, sender notifier.Sender, m *metrics.ExamMetrics) *ExamReconciler {
	return &ExamReconciler{
		Client: k8sClient,
		Scheme: k8sClient.Scheme(),
		Platform: PlatformConfig{
			BaseDomain:           testPlatformDomain,
			IngressTLSSecretName: testPlatformTLSSecretName,
			SMTPSecretName:       testPlatformSMTPSecretName,
			SecretNamespace:      testPlatformSecretNamespace,
		},
		PolicyProvider: &network.VanillaPolicyProvider{},
		Sender:         sender,
		Now:            nowFn,
		Metrics:        m,
	}
}

func createTLSSecret(ctx context.Context, name, namespace string) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeTLS,
		Data: map[string][]byte{
			corev1.TLSCertKey:       []byte("dummy-cert"),
			corev1.TLSPrivateKeyKey: []byte("dummy-key"),
		},
	}
	err := k8sClient.Create(ctx, secret)
	if err != nil && !errors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

// preseedSlugs sets known-valid slugs (starting with a letter) in the exam's
// status before provisioning. This prevents random slugs that start with a
// digit from failing K8s DNS-1035 validation for Service names.
func preseedSlugs(ctx context.Context, nn types.NamespacedName) {
	exam := &examv1alpha1.Exam{}
	Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

	for i, student := range exam.Spec.Students {
		slug := fmt.Sprintf("s%s%02d", exam.Name[0:4], i)
		for len(exam.Status.Students) <= i {
			exam.Status.Students = append(exam.Status.Students, examv1alpha1.StudentStatus{})
		}
		exam.Status.Students[i] = examv1alpha1.StudentStatus{
			ID:   student.ID,
			Slug: slug,
		}
	}
	for i := 0; i < exam.Spec.Spares; i++ {
		slug := fmt.Sprintf("x%s%02d", exam.Name[0:4], i)
		for len(exam.Status.Spares) <= i {
			exam.Status.Spares = append(exam.Status.Spares, examv1alpha1.SpareStatus{})
		}
		exam.Status.Spares[i] = examv1alpha1.SpareStatus{
			Slug: slug,
		}
	}
	Expect(k8sClient.Status().Update(ctx, exam)).To(Succeed())
}

// patchDeploymentsReady patches all deployments in the namespace so they
// report ReadyReplicas=1.
func patchDeploymentsReady(ctx context.Context, namespace, examName string) {
	var deps appsv1.DeploymentList
	Expect(k8sClient.List(ctx, &deps,
		client.InNamespace(namespace),
		client.MatchingLabels{
			provisioner.LabelExam:          examName,
			provisioner.LabelExamNamespace: examCRNamespace,
		},
	)).To(Succeed())
	for i := range deps.Items {
		deps.Items[i].Status.Replicas = 1
		deps.Items[i].Status.ReadyReplicas = 1
		deps.Items[i].Status.AvailableReplicas = 1
		Expect(k8sClient.Status().Update(ctx, &deps.Items[i])).To(Succeed())
	}
}

// createSMTPSecret creates a Secret with SMTP credentials in the given namespace.
func createSMTPSecret(ctx context.Context, name, namespace string) {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Data: map[string][]byte{
			"host":     []byte("smtp.test.com"),
			"port":     []byte("587"),
			"username": []byte("user"),
			"password": []byte("pass"),
		},
	}
	err := k8sClient.Create(ctx, secret)
	if err != nil && !errors.IsAlreadyExists(err) {
		Expect(err).NotTo(HaveOccurred())
	}
}

// drainEmails reconciles repeatedly until the AllEmailsSent condition is set,
// ensuring the email queue is fully processed before testing other Ready-phase
// behavior like dry runs.
func drainEmails(ctx context.Context, reconciler *ExamReconciler, nn types.NamespacedName) {
	for i := 0; i < 20; i++ { // safety limit
		exam := &examv1alpha1.Exam{}
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		if meta.IsStatusConditionTrue(exam.Status.Conditions, "AllEmailsSent") {
			return
		}
		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())
	}
	// Verify AllEmailsSent was eventually set
	exam := &examv1alpha1.Exam{}
	Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
	Expect(meta.IsStatusConditionTrue(exam.Status.Conditions, "AllEmailsSent")).To(BeTrue(),
		"AllEmailsSent condition should be set after draining emails")
}

// counterValue reads a Prometheus CounterVec value for the given labels.
func counterValue(cv *prometheus.CounterVec, labels ...string) float64 {
	return testutil.ToFloat64(cv.WithLabelValues(labels...))
}

func metricLabelValuesForNN(nn types.NamespacedName) []string {
	return metrics.LabelValues(nn.Name, nn.Namespace)
}

// driveToPhase drives the reconciler through the lifecycle until the exam
// reaches the given phase. It returns the reconciler so callers can continue
// using it with the same injectable clock.
func driveToPhase(
	ctx context.Context,
	nn types.NamespacedName,
	target examv1alpha1.ExamPhase,
	unlock time.Time,
	sender *notifier.FakeSender,
	m *metrics.ExamMetrics,
) *ExamReconciler {
	lockTime := computeLockTime(unlock, 2*time.Hour, 1.5)
	retentionDeadline := lockTime.Add(24 * time.Hour)

	// Start before unlock but after provision time
	clockTime := unlock.Add(-30 * time.Minute)
	reconciler := newReconciler(func() time.Time { return clockTime }, sender, m)

	// Provisioning
	_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
	Expect(err).NotTo(HaveOccurred())
	if target == examv1alpha1.ExamPhaseProvisioning {
		return reconciler
	}

	// Ready
	patchDeploymentsReady(ctx, examNamespace(nn.Name, nn.Namespace), nn.Name)
	_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
	Expect(err).NotTo(HaveOccurred())
	if target == examv1alpha1.ExamPhaseReady {
		return reconciler
	}

	// Unlocked
	reconciler.Now = func() time.Time { return unlock.Add(5 * time.Minute) }
	_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
	Expect(err).NotTo(HaveOccurred())
	if target == examv1alpha1.ExamPhaseUnlocked {
		return reconciler
	}

	// Locked
	reconciler.Now = func() time.Time { return lockTime.Add(5 * time.Minute) }
	_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
	Expect(err).NotTo(HaveOccurred())
	if target == examv1alpha1.ExamPhaseLocked {
		return reconciler
	}

	// TearingDown
	reconciler.Now = func() time.Time { return retentionDeadline.Add(5 * time.Minute) }
	_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
	Expect(err).NotTo(HaveOccurred())
	return reconciler
}

// reconcileRequest builds a reconcile.Request from a NamespacedName.
func reconcileRequest(nn types.NamespacedName) reconcile.Request {
	return reconcile.Request{NamespacedName: nn}
}
