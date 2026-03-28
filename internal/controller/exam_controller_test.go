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
	"strings"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
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
)

var testCounter atomic.Int64

func uniqueExamName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, testCounter.Add(1))
}

func cleanupExam(ctx context.Context, name, namespace string) {
	resource := &examv1alpha1.Exam{}
	err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, resource)
	if err == nil {
		resource.Finalizers = nil
		_ = k8sClient.Update(ctx, resource)
		_ = k8sClient.Delete(ctx, resource)
	}
}

// createExamCR creates an Exam CR with the given name and schedule.
func createExamCR(ctx context.Context, name string, unlock time.Time, students []examv1alpha1.ExamStudent, spares int) {
	resource := &examv1alpha1.Exam{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
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
			Students:   students,
			Spares:     spares,
			Email: examv1alpha1.ExamEmail{
				Before:          metav1.Duration{Duration: 30 * time.Minute},
				RateLimit:       10,
				InstructorEmail: "prof@test.com",
				SecretRef:       "smtp-secret",
				From:            "test@test.com",
				Subject:         "Test Exam",
			},
			IngressTLS: examv1alpha1.ExamIngressTLS{SecretName: "test-tls"},
			Domain:     "exam.test.com",
		},
	}
	Expect(k8sClient.Create(ctx, resource)).To(Succeed())
}

// newReconciler creates an ExamReconciler with the given clock and sender.
func newReconciler(nowFn func() time.Time, sender notifier.Sender, m *metrics.ExamMetrics) *ExamReconciler {
	return &ExamReconciler{
		Client:         k8sClient,
		Scheme:         k8sClient.Scheme(),
		PolicyProvider: &network.VanillaPolicyProvider{},
		Sender:         sender,
		Now:            nowFn,
		Metrics:        m,
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

// patchDeploymentsReady patches all deployments in the namespace so they report ReadyReplicas=1.
func patchDeploymentsReady(ctx context.Context, namespace, examName string) {
	var deps appsv1.DeploymentList
	Expect(k8sClient.List(ctx, &deps,
		client.InNamespace(namespace),
		client.MatchingLabels{"exam.otu.ca/exam": examName},
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

// gaugeValue reads a Prometheus GaugeVec value for the given label.
func gaugeValue(g *prometheus.GaugeVec, label string) float64 {
	m := &dto.Metric{}
	gauge, err := g.GetMetricWithLabelValues(label)
	Expect(err).NotTo(HaveOccurred())
	Expect(gauge.Write(m)).To(Succeed())
	return m.GetGauge().GetValue()
}

var _ = Describe("Exam Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-exam"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: "default",
		}

		BeforeEach(func() {
			By("creating the custom resource for the Kind Exam")
			exam := &examv1alpha1.Exam{}
			err := k8sClient.Get(ctx, typeNamespacedName, exam)
			if err != nil && errors.IsNotFound(err) {
				now := time.Now()
				resource := &examv1alpha1.Exam{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "default",
					},
					Spec: examv1alpha1.ExamSpec{
						Template: examv1alpha1.ExamTemplate{
							Image:     "nginx:latest",
							Port:      8080,
							Resources: corev1.ResourceRequirements{},
						},
						Schedule: examv1alpha1.ExamSchedule{
							Unlock:          metav1.NewTime(now.Add(1 * time.Hour)),
							Duration:        metav1.Duration{Duration: 2 * time.Hour},
							TimeMultiplier:  1.5,
							ProvisionBefore: metav1.Duration{Duration: 30 * time.Minute},
							Retention:       metav1.Duration{Duration: 24 * time.Hour},
						},
						Students: []examv1alpha1.ExamStudent{
							{ID: "alice", Email: "alice@test.com"},
						},
						Email: examv1alpha1.ExamEmail{
							Before:          metav1.Duration{Duration: 15 * time.Minute},
							RateLimit:       10,
							InstructorEmail: "prof@test.com",
							SecretRef:       "smtp-secret",
							From:            "test@test.com",
							Subject:         "Test Exam",
						},
						IngressTLS: examv1alpha1.ExamIngressTLS{SecretName: "test-tls"},
						Domain:     "exam.test.com",
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			resource := &examv1alpha1.Exam{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				By("Cleanup the specific resource instance Exam")
				resource.Finalizers = nil
				Expect(k8sClient.Update(ctx, resource)).To(Succeed())
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}
		})

		It("should add finalizer and stay Pending when before provision time", func() {
			controllerReconciler := &ExamReconciler{
				Client:         k8sClient,
				Scheme:         k8sClient.Scheme(),
				PolicyProvider: &network.VanillaPolicyProvider{},
				Sender:         &notifier.FakeSender{},
				Now:            func() time.Time { return time.Now() },
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, exam)).To(Succeed())
			Expect(exam.Finalizers).To(ContainElement("exam.otu.ca/cleanup"))
		})

		It("should compute and set status times", func() {
			controllerReconciler := &ExamReconciler{
				Client:         k8sClient,
				Scheme:         k8sClient.Scheme(),
				PolicyProvider: &network.VanillaPolicyProvider{},
				Sender:         &notifier.FakeSender{},
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, typeNamespacedName, exam)).To(Succeed())
			Expect(exam.Status.ComputedLockTime).NotTo(BeNil())
			Expect(exam.Status.ProvisionTime).NotTo(BeNil())
			Expect(exam.Status.EmailTime).NotTo(BeNil())
			Expect(exam.Status.RetentionDeadline).NotTo(BeNil())
		})
	})

	Context("Full provisioning lifecycle", func() {
		var (
			ctx      context.Context
			examName string
			nn       types.NamespacedName
			unlock   time.Time
			fakeSender *notifier.FakeSender
			reconciler *ExamReconciler
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("lifecycle")
			nn = types.NamespacedName{Name: examName, Namespace: "default"}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			fakeSender = &notifier.FakeSender{}

			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
				{ID: "bob", Email: "bob@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
			preseedSlugs(ctx, nn)

			// Set clock AFTER provisionTime but BEFORE unlock
			// provisionBefore=1h, so provisionTime = unlock - 1h
			// Set now to 30 min before unlock (well after provisionTime)
			clockTime := unlock.Add(-30 * time.Minute)
			reconciler = newReconciler(func() time.Time { return clockTime }, fakeSender, nil)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, "default")
			// Clean up the namespace
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should progress through Pending -> Provisioning -> Ready", func() {
			By("First reconcile: adds finalizer, computes times, provisions instances")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Finalizers).To(ContainElement(finalizerName))
			Expect(exam.Status.ComputedLockTime).NotTo(BeNil())
			Expect(exam.Status.ProvisionTime).NotTo(BeNil())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseProvisioning))

			By("Verifying student statuses have slugs, URLs, phase=Provisioned")
			Expect(exam.Status.Students).To(HaveLen(2))
			for _, s := range exam.Status.Students {
				Expect(s.Slug).NotTo(BeEmpty())
				Expect(s.URL).To(HavePrefix("https://"))
				Expect(s.URL).To(ContainSubstring("exam.test.com"))
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseProvisioned))
			}

			By("Verifying namespace was created")
			ns := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: examNamespace(examName)}, ns)).To(Succeed())

			By("Patching deployments to be ready")
			patchDeploymentsReady(ctx, examNamespace(examName), examName)

			By("Second reconcile: transitions to Ready")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))

			By("Verifying Provisioned condition is True")
			cond := meta.FindStatusCondition(exam.Status.Conditions, "Provisioned")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("Email sending", func() {
		var (
			ctx        context.Context
			examName   string
			nn         types.NamespacedName
			unlock     time.Time
			fakeSender *notifier.FakeSender
			reconciler *ExamReconciler
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("email")
			nn = types.NamespacedName{Name: examName, Namespace: "default"}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			fakeSender = &notifier.FakeSender{}

			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
				{ID: "bob", Email: "bob@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
			preseedSlugs(ctx, nn)

			// Clock is after emailTime (30m before unlock), before unlock
			emailTime := unlock.Add(-30 * time.Minute)
			clockTime := emailTime.Add(5 * time.Minute)
			reconciler = newReconciler(func() time.Time { return clockTime }, fakeSender, nil)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, "default")
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should send emails and set AllEmailsSent condition", func() {
			By("Running reconcile: adds finalizer, provisions instances")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			By("Patching deployments to be ready")
			patchDeploymentsReady(ctx, examNamespace(examName), examName)

			By("Running reconcile to transition to Ready")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))

			By("Running reconcile in Ready phase to send first email")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			// First email sent
			Expect(fakeSender.Sent).To(HaveLen(1))
			Expect(fakeSender.Sent[0].To).To(ContainElement("alice@test.com"))

			// Verify first student emailStatus = Sent
			Expect(exam.Status.Students[0].EmailStatus).To(Equal(examv1alpha1.EmailStatusSent))
			Expect(exam.Status.Students[1].EmailStatus).To(Equal(examv1alpha1.EmailStatusPending))

			By("Running reconcile again to send second email")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(fakeSender.Sent).To(HaveLen(2))
			Expect(fakeSender.Sent[1].To).To(ContainElement("bob@test.com"))
			Expect(exam.Status.Students[1].EmailStatus).To(Equal(examv1alpha1.EmailStatusSent))

			By("Running reconcile once more — all emails sent, condition set")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			cond := meta.FindStatusCondition(exam.Status.Conditions, "AllEmailsSent")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("resolvedSender", func() {
		var (
			ctx      context.Context
			examName string
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("sender")
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, "default")
		})

		It("should return FakeSender directly when Sender is set", func() {
			fake := &notifier.FakeSender{}
			r := newReconciler(nil, fake, nil)

			exam := &examv1alpha1.Exam{
				Spec: examv1alpha1.ExamSpec{
					Email: examv1alpha1.ExamEmail{SecretRef: "smtp-secret"},
				},
				ObjectMeta: metav1.ObjectMeta{Name: examName, Namespace: "default"},
			}
			sender, err := r.resolvedSender(ctx, exam)
			Expect(err).NotTo(HaveOccurred())
			Expect(sender).To(BeIdenticalTo(fake))
		})

		It("should read SMTP credentials from Secret when Sender is nil", func() {
			By("Creating SMTP secret")
			createSMTPSecret(ctx, "smtp-sender-test", "default")

			r := newReconciler(nil, nil, nil)

			exam := &examv1alpha1.Exam{
				Spec: examv1alpha1.ExamSpec{
					Email: examv1alpha1.ExamEmail{SecretRef: "smtp-sender-test"},
				},
				ObjectMeta: metav1.ObjectMeta{Name: examName, Namespace: "default"},
			}
			sender, err := r.resolvedSender(ctx, exam)
			Expect(err).NotTo(HaveOccurred())
			Expect(sender).NotTo(BeNil())
			// Should be a RetrySender wrapping an SMTPSender
			_, ok := sender.(*notifier.RetrySender)
			Expect(ok).To(BeTrue())
		})

		It("should return error when Sender is nil and Secret is missing", func() {
			r := newReconciler(nil, nil, nil)

			exam := &examv1alpha1.Exam{
				Spec: examv1alpha1.ExamSpec{
					Email: examv1alpha1.ExamEmail{SecretRef: "nonexistent-secret"},
				},
				ObjectMeta: metav1.ObjectMeta{Name: examName, Namespace: "default"},
			}
			_, err := r.resolvedSender(ctx, exam)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("reading SMTP secret"))
		})
	})

	Context("Unlock phase", func() {
		var (
			ctx        context.Context
			examName   string
			nn         types.NamespacedName
			unlock     time.Time
			fakeSender *notifier.FakeSender
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("unlock")
			nn = types.NamespacedName{Name: examName, Namespace: "default"}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			fakeSender = &notifier.FakeSender{}

			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
				{ID: "bob", Email: "bob@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
			preseedSlugs(ctx, nn)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, "default")
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should transition to Unlocked, update student phases, and notify instructor", func() {
			By("Provisioning the exam to Ready state")
			preUnlockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return preUnlockTime }, fakeSender, nil)

			// Provision (first reconcile adds finalizer + provisions)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			// Patch deployments
			patchDeploymentsReady(ctx, examNamespace(examName), examName)
			// Transition to Ready
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))

			By("Setting clock to after unlock and reconciling")
			postUnlockTime := unlock.Add(5 * time.Minute)
			reconciler.Now = func() time.Time { return postUnlockTime }

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseUnlocked))

			By("Verifying student phases are Unlocked")
			for _, s := range exam.Status.Students {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseUnlocked))
			}

			By("Verifying instructor was notified about unlock")
			hasUnlockNotification := false
			for _, msg := range fakeSender.Sent {
				for _, to := range msg.To {
					if to == "prof@test.com" && strings.Contains(string(msg.Body), "Exam is live") {
						hasUnlockNotification = true
					}
				}
			}
			Expect(hasUnlockNotification).To(BeTrue())

			By("Verifying InstructorNotifiedUnlock condition")
			cond := meta.FindStatusCondition(exam.Status.Conditions, "InstructorNotifiedUnlock")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("Lock phase", func() {
		var (
			ctx        context.Context
			examName   string
			nn         types.NamespacedName
			unlock     time.Time
			lockTime   time.Time
			fakeSender *notifier.FakeSender
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("lock")
			nn = types.NamespacedName{Name: examName, Namespace: "default"}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			// lockTime = unlock + 2h * 1.5 = unlock + 3h
			lockTime = unlock.Add(3 * time.Hour)
			fakeSender = &notifier.FakeSender{}

			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
			preseedSlugs(ctx, nn)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, "default")
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should transition to Locked, update student phases, and notify instructor", func() {
			By("Getting exam to Unlocked state")
			preUnlockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return preUnlockTime }, fakeSender, nil)

			// Provision (first reconcile adds finalizer + provisions)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			// Patch deployments
			patchDeploymentsReady(ctx, examNamespace(examName), examName)
			// Transition to Ready
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			// Transition to Unlocked
			reconciler.Now = func() time.Time { return unlock.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseUnlocked))

			By("Setting clock to after lock time and reconciling")
			// Reset sender to track only lock-phase notifications
			fakeSender.Sent = nil
			reconciler.Now = func() time.Time { return lockTime.Add(5 * time.Minute) }

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseLocked))

			By("Verifying student phases are Locked")
			for _, s := range exam.Status.Students {
				Expect(s.Phase).To(Equal(examv1alpha1.StudentPhaseLocked))
			}

			By("Verifying instructor was notified about lock")
			hasLockNotification := false
			for _, msg := range fakeSender.Sent {
				for _, to := range msg.To {
					if to == "prof@test.com" && strings.Contains(string(msg.Body), "Exam has ended") {
						hasLockNotification = true
					}
				}
			}
			Expect(hasLockNotification).To(BeTrue())

			By("Verifying InstructorNotifiedLock condition")
			cond := meta.FindStatusCondition(exam.Status.Conditions, "InstructorNotifiedLock")
			Expect(cond).NotTo(BeNil())
			Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		})
	})

	Context("Teardown phase", func() {
		var (
			ctx        context.Context
			examName   string
			nn         types.NamespacedName
			unlock     time.Time
			fakeSender *notifier.FakeSender
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("teardown")
			nn = types.NamespacedName{Name: examName, Namespace: "default"}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			fakeSender = &notifier.FakeSender{}

			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
			preseedSlugs(ctx, nn)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, "default")
		})

		It("should transition to TearingDown and delete namespace", func() {
			// lockTime = unlock + 3h, retentionDeadline = lockTime + 24h
			lockTime := unlock.Add(3 * time.Hour)
			retentionDeadline := lockTime.Add(24 * time.Hour)

			By("Getting exam to Locked state")
			preUnlockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return preUnlockTime }, fakeSender, nil)

			// Provision (first reconcile adds finalizer + provisions)
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			// Patch deployments
			patchDeploymentsReady(ctx, examNamespace(examName), examName)
			// Ready
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			// Unlocked
			reconciler.Now = func() time.Time { return unlock.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			// Locked
			reconciler.Now = func() time.Time { return lockTime.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseLocked))

			By("Setting clock to after retention deadline and reconciling")
			reconciler.Now = func() time.Time { return retentionDeadline.Add(5 * time.Minute) }

			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseTearingDown))
			Expect(exam.Status.Message).To(Equal("Namespace deleted"))
		})
	})

	Context("updateMetricsSummary", func() {
		It("should populate Prometheus gauges correctly", func() {
			By("Creating a registry and metrics")
			reg := prometheus.NewRegistry()
			m := metrics.NewExamMetrics(reg)

			r := &ExamReconciler{
				Metrics: m,
			}

			exam := &examv1alpha1.Exam{
				ObjectMeta: metav1.ObjectMeta{Name: "metrics-exam"},
				Spec: examv1alpha1.ExamSpec{
					Students: []examv1alpha1.ExamStudent{
						{ID: "alice"}, {ID: "bob"}, {ID: "charlie"},
					},
					Spares: 2,
				},
				Status: examv1alpha1.ExamStatus{
					Students: []examv1alpha1.StudentStatus{
						{ID: "alice", Phase: examv1alpha1.StudentPhaseProvisioned, EmailStatus: examv1alpha1.EmailStatusSent},
						{ID: "bob", Phase: examv1alpha1.StudentPhaseFailed, EmailStatus: examv1alpha1.EmailStatusFailed},
						{ID: "charlie", Phase: examv1alpha1.StudentPhaseUnlocked, EmailStatus: examv1alpha1.EmailStatusPending},
					},
					Spares: []examv1alpha1.SpareStatus{
						{Slug: "spare1", Phase: examv1alpha1.StudentPhaseProvisioned},
						{Slug: "spare2", Phase: examv1alpha1.StudentPhaseFailed},
					},
				},
			}

			r.updateMetricsSummary(exam)

			By("Verifying status metrics summary")
			Expect(exam.Status.Metrics).NotTo(BeNil())
			Expect(exam.Status.Metrics.TotalStudents).To(Equal(3))
			Expect(exam.Status.Metrics.TotalSpares).To(Equal(2))
			Expect(exam.Status.Metrics.InstancesHealthy).To(Equal(3))  // alice + charlie + spare1
			Expect(exam.Status.Metrics.InstancesFailed).To(Equal(2))   // bob + spare2
			Expect(exam.Status.Metrics.EmailsSent).To(Equal(1))        // alice
			Expect(exam.Status.Metrics.EmailsFailed).To(Equal(1))      // bob

			By("Verifying Prometheus gauge values")
			Expect(gaugeValue(m.InstancesTotal, "metrics-exam")).To(Equal(5.0))    // 3 students + 2 spares
			Expect(gaugeValue(m.InstancesHealthy, "metrics-exam")).To(Equal(3.0))
			Expect(gaugeValue(m.InstancesFailed, "metrics-exam")).To(Equal(2.0))
		})
	})

	Context("Countdown gauges", func() {
		var (
			ctx      context.Context
			examName string
			nn       types.NamespacedName
			unlock   time.Time
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("countdown")
			nn = types.NamespacedName{Name: examName, Namespace: "default"}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)

			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
			preseedSlugs(ctx, nn)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, "default")
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should use injectable clock for countdown gauge values", func() {
			reg := prometheus.NewRegistry()
			m := metrics.NewExamMetrics(reg)

			// Set Now to exactly 1 hour before unlock
			clockTime := unlock.Add(-1 * time.Hour)
			reconciler := newReconciler(func() time.Time { return clockTime }, &notifier.FakeSender{}, m)

			// lockTime = unlock + 3h (2h * 1.5)
			lockTime := unlock.Add(3 * time.Hour)

			By("Reconciling: adds finalizer, provisions, sets gauges")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying countdown values relative to injected time")
			secondsUntilUnlock := gaugeValue(m.SecondsUntilUnlock, examName)
			secondsUntilLock := gaugeValue(m.SecondsUntilLock, examName)

			// Clock is 1 hour before unlock → 3600 seconds
			Expect(secondsUntilUnlock).To(BeNumerically("~", unlock.Sub(clockTime).Seconds(), 1.0))
			// Clock is 1h before unlock, lock is 3h after unlock → 4h = 14400 seconds
			Expect(secondsUntilLock).To(BeNumerically("~", lockTime.Sub(clockTime).Seconds(), 1.0))
		})

		It("should report zero for countdown after the event has passed", func() {
			reg := prometheus.NewRegistry()
			m := metrics.NewExamMetrics(reg)

			// lockTime = unlock + 3h
			lockTime := unlock.Add(3 * time.Hour)

			// Set Now to after lock time
			clockTime := lockTime.Add(30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return clockTime }, &notifier.FakeSender{}, m)

			By("Getting exam through lifecycle to Locked state")
			// First reconcile: adds finalizer + provisions
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			// Patch deployments
			patchDeploymentsReady(ctx, examNamespace(examName), examName)

			// Ready -> Unlocked (clock is past unlock)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			// Unlocked -> Locked (clock is past lock time)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying countdown gauges are zero after events passed")
			Expect(gaugeValue(m.SecondsUntilUnlock, examName)).To(Equal(0.0))
			Expect(gaugeValue(m.SecondsUntilLock, examName)).To(Equal(0.0))
		})
	})

	Context("Teardown with metrics cleanup", func() {
		It("should clean up metrics when transitioning to TearingDown", func() {
			ctx := context.Background()
			examName := uniqueExamName("teardown-metrics")
			nn := types.NamespacedName{Name: examName, Namespace: "default"}
			unlock := time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			lockTime := unlock.Add(3 * time.Hour)
			retentionDeadline := lockTime.Add(24 * time.Hour)
			fakeSender := &notifier.FakeSender{}

			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
			preseedSlugs(ctx, nn)
			defer cleanupExam(ctx, examName, "default")

			reg := prometheus.NewRegistry()
			m := metrics.NewExamMetrics(reg)

			preUnlockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return preUnlockTime }, fakeSender, m)

			// Progress through lifecycle: first reconcile adds finalizer + provisions
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
			patchDeploymentsReady(ctx, examNamespace(examName), examName)
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			// Verify metrics are set
			Expect(gaugeValue(m.InstancesTotal, examName)).To(BeNumerically(">", 0))

			// Unlock
			reconciler.Now = func() time.Time { return unlock.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			// Lock
			reconciler.Now = func() time.Time { return lockTime.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			// Teardown
			reconciler.Now = func() time.Time { return retentionDeadline.Add(5 * time.Minute) }
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseTearingDown))
		})
	})

	Context("Provisioning with spares", func() {
		var (
			ctx        context.Context
			examName   string
			nn         types.NamespacedName
			unlock     time.Time
			fakeSender *notifier.FakeSender
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("spares")
			nn = types.NamespacedName{Name: examName, Namespace: "default"}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
			fakeSender = &notifier.FakeSender{}

			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 2)
			preseedSlugs(ctx, nn)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, "default")
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("should provision spare instances and send spare URLs to instructor", func() {
			clockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, nil)

			By("Provision (first reconcile adds finalizer + provisions)")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Spares).To(HaveLen(2))
			for _, sp := range exam.Status.Spares {
				Expect(sp.Slug).NotTo(BeEmpty())
				Expect(sp.URL).To(HavePrefix("https://"))
				Expect(sp.Phase).To(Equal(examv1alpha1.StudentPhaseProvisioned))
			}

			By("Patching all deployments (students + spares) to be ready")
			patchDeploymentsReady(ctx, examNamespace(examName), examName)

			By("Transition to Ready — instructor should receive spare URLs")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))

			// Check that instructor received spare URLs email
			hasSpareEmail := false
			for _, msg := range fakeSender.Sent {
				for _, to := range msg.To {
					if to == "prof@test.com" && strings.Contains(string(msg.Body), "Spare instances") {
						hasSpareEmail = true
					}
				}
			}
			Expect(hasSpareEmail).To(BeTrue())
		})
	})

	Context("Deletion with finalizer", func() {
		var (
			ctx      context.Context
			examName string
			nn       types.NamespacedName
			unlock   time.Time
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("delete")
			nn = types.NamespacedName{Name: examName, Namespace: "default"}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)

			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
		})

		It("should handle deletion and remove finalizer", func() {
			fakeSender := &notifier.FakeSender{}
			clockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, nil)

			By("Add finalizer through reconcile")
			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			By("Verify finalizer is present")
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Finalizers).To(ContainElement(finalizerName))

			By("Deleting the exam CR")
			Expect(k8sClient.Delete(ctx, exam)).To(Succeed())

			By("Reconciling after deletion — should remove finalizer")
			_, err = reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the object is gone")
			err = k8sClient.Get(ctx, nn, exam)
			Expect(errors.IsNotFound(err)).To(BeTrue())
		})
	})

	Context("Reconcile for non-existent resource", func() {
		It("should return no error for a missing resource", func() {
			ctx := context.Background()
			nn := types.NamespacedName{Name: "does-not-exist", Namespace: "default"}
			reconciler := newReconciler(nil, &notifier.FakeSender{}, nil)

			_, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nn})
			Expect(err).NotTo(HaveOccurred())
		})
	})
})
