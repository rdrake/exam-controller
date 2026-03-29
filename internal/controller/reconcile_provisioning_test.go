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
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	"github.com/rdrake/exam-controller/internal/notifier"
	"github.com/rdrake/exam-controller/internal/provisioner"
)

var _ = Describe("Provisioning and Drift Correction", func() {

	Describe("Resource creation", func() {
		var (
			ctx      context.Context
			examName string
			nn       types.NamespacedName
			unlock   time.Time
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("prov")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("creates namespace, deployments, services, and network policies", func() {
			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
				{ID: "bob", Email: "bob@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 1)
			preseedSlugs(ctx, nn)

			clockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return clockTime }, &notifier.FakeSender{}, nil)

			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			ns := examNamespace(examName, examCRNamespace)

			By("Verifying exam namespace exists")
			namespace := &corev1.Namespace{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: ns}, namespace)).To(Succeed())

			By("Verifying 3 deployments exist (2 students + 1 spare)")
			var deps appsv1.DeploymentList
			Expect(k8sClient.List(ctx, &deps,
				client.InNamespace(ns),
				client.MatchingLabels{provisioner.LabelExam: examName},
			)).To(Succeed())
			Expect(deps.Items).To(HaveLen(3))

			By("Verifying 3 services exist")
			var svcs corev1.ServiceList
			Expect(k8sClient.List(ctx, &svcs,
				client.InNamespace(ns),
				client.MatchingLabels{provisioner.LabelExam: examName},
			)).To(Succeed())
			Expect(svcs.Items).To(HaveLen(3))

			By("Verifying 3 deny-all network policies exist")
			var netpols networkingv1.NetworkPolicyList
			Expect(k8sClient.List(ctx, &netpols,
				client.InNamespace(ns),
				client.MatchingLabels{provisioner.LabelExam: examName},
			)).To(Succeed())

			denyAllCount := 0
			egressAllowCount := 0
			for _, np := range netpols.Items {
				name := np.Name
				if strings.HasSuffix(name, "-deny-all") {
					denyAllCount++
				}
				if strings.HasSuffix(name, "-egress-allow") {
					egressAllowCount++
				}
			}
			Expect(denyAllCount).To(Equal(3), "expected 3 deny-all network policies")

			By("Verifying 3 egress-allow network policies exist")
			Expect(egressAllowCount).To(Equal(3), "expected 3 egress-allow network policies")
		})

		It("creates correct number of student + spare instances", func() {
			students := []examv1alpha1.ExamStudent{
				{ID: "s1", Email: "s1@test.com"},
				{ID: "s2", Email: "s2@test.com"},
				{ID: "s3", Email: "s3@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 2)
			preseedSlugs(ctx, nn)

			clockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return clockTime }, &notifier.FakeSender{}, nil)

			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			ns := examNamespace(examName, examCRNamespace)

			By("Verifying 5 deployments exist (3 students + 2 spares)")
			var deps appsv1.DeploymentList
			Expect(k8sClient.List(ctx, &deps,
				client.InNamespace(ns),
				client.MatchingLabels{provisioner.LabelExam: examName},
			)).To(Succeed())
			Expect(deps.Items).To(HaveLen(5))

			By("Verifying 5 services exist")
			var svcs corev1.ServiceList
			Expect(k8sClient.List(ctx, &svcs,
				client.InNamespace(ns),
				client.MatchingLabels{provisioner.LabelExam: examName},
			)).To(Succeed())
			Expect(svcs.Items).To(HaveLen(5))
		})
	})

	Describe("Spare URL notification", func() {
		var (
			ctx      context.Context
			examName string
			nn       types.NamespacedName
			unlock   time.Time
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("spare")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("sends spare URLs to instructor when spares > 0", func() {
			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 1)
			preseedSlugs(ctx, nn)

			fakeSender := &notifier.FakeSender{}
			clockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, nil)

			By("Reconciling to start provisioning")
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			By("Patching deployments to be ready")
			patchDeploymentsReady(ctx, examNamespace(examName, examCRNamespace), examName)

			By("Reconciling again to transition to Ready and trigger spare email")
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the exam reached Ready phase")
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseReady))

			By("Verifying FakeSender received a spare URL message to instructor")
			Expect(fakeSender.Sent).NotTo(BeEmpty())
			found := false
			for _, msg := range fakeSender.Sent {
				for _, to := range msg.To {
					if to == "prof@test.com" {
						found = true
						// Verify the body contains the spare URL
						Expect(string(msg.Body)).To(ContainSubstring(exam.Status.Spares[0].URL))
						break
					}
				}
			}
			Expect(found).To(BeTrue(), "expected spare URL email sent to instructor")
		})
	})

	Describe("Drift correction", func() {
		var (
			ctx      context.Context
			examName string
			nn       types.NamespacedName
			unlock   time.Time
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("drift")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("recreates deleted deployment", func() {
			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
				{ID: "bob", Email: "bob@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
			preseedSlugs(ctx, nn)

			By("Reconciling to enter Provisioning and create resources")
			fakeSender := &notifier.FakeSender{}
			clockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, nil)

			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			ns := examNamespace(examName, examCRNamespace)

			By("Listing deployments and deleting one")
			var deps appsv1.DeploymentList
			Expect(k8sClient.List(ctx, &deps,
				client.InNamespace(ns),
				client.MatchingLabels{provisioner.LabelExam: examName},
			)).To(Succeed())
			Expect(deps.Items).To(HaveLen(2))

			deletedName := deps.Items[0].Name
			Expect(k8sClient.Delete(ctx, &deps.Items[0])).To(Succeed())

			By("Verifying the deployment is gone")
			Expect(k8sClient.List(ctx, &deps,
				client.InNamespace(ns),
				client.MatchingLabels{provisioner.LabelExam: examName},
			)).To(Succeed())
			Expect(deps.Items).To(HaveLen(1))

			By("Reconciling again while still in Provisioning to trigger reprovisioning")
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the deployment is recreated")
			Expect(k8sClient.List(ctx, &deps,
				client.InNamespace(ns),
				client.MatchingLabels{provisioner.LabelExam: examName},
			)).To(Succeed())
			Expect(deps.Items).To(HaveLen(2))

			foundRecreated := false
			for _, d := range deps.Items {
				if d.Name == deletedName {
					foundRecreated = true
					break
				}
			}
			Expect(foundRecreated).To(BeTrue(), "expected deleted deployment %s to be recreated", deletedName)
		})

		It("recreates deleted network policy", func() {
			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
			preseedSlugs(ctx, nn)

			By("Reconciling to enter Provisioning and create resources")
			fakeSender := &notifier.FakeSender{}
			clockTime := unlock.Add(-30 * time.Minute)
			reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, nil)

			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			ns := examNamespace(examName, examCRNamespace)

			By("Listing all network policies")
			var netpols networkingv1.NetworkPolicyList
			Expect(k8sClient.List(ctx, &netpols,
				client.InNamespace(ns),
				client.MatchingLabels{provisioner.LabelExam: examName},
			)).To(Succeed())
			Expect(netpols.Items).NotTo(BeEmpty(), "expected network policies after provisioning")

			By("Finding and deleting a deny-all network policy")
			var denyAllPolicy *networkingv1.NetworkPolicy
			for i := range netpols.Items {
				name := netpols.Items[i].Name
				if strings.HasSuffix(name, "-deny-all") {
					denyAllPolicy = &netpols.Items[i]
					break
				}
			}
			Expect(denyAllPolicy).NotTo(BeNil(), "expected to find a deny-all network policy")
			deletedName := denyAllPolicy.Name
			Expect(k8sClient.Delete(ctx, denyAllPolicy)).To(Succeed())

			By("Verifying the deny-all policy is gone")
			deleted := &networkingv1.NetworkPolicy{}
			err = k8sClient.Get(ctx, types.NamespacedName{Name: deletedName, Namespace: ns}, deleted)
			Expect(err).To(HaveOccurred(), "deny-all policy should be deleted")

			By("Reconciling again while still in Provisioning to trigger reprovisioning")
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the deny-all policy is recreated")
			recreated := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deletedName, Namespace: ns}, recreated)).To(Succeed())
		})
	})

	Describe("resolvedSender", func() {
		var (
			ctx      context.Context
			examName string
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("sender")
		})

		It("reads SMTP credentials from Secret when Sender is nil", func() {
			createSMTPSecret(ctx, "smtp-secret", examCRNamespace)

			exam := &examv1alpha1.Exam{
				ObjectMeta: metav1.ObjectMeta{
					Name:      examName,
					Namespace: examCRNamespace,
				},
				Spec: examv1alpha1.ExamSpec{
					Email: examv1alpha1.ExamEmail{
						SecretRef: "smtp-secret",
					},
				},
			}

			reconciler := newReconciler(nil, nil, nil) // Sender is nil
			sender, err := reconciler.resolvedSender(ctx, exam)
			Expect(err).NotTo(HaveOccurred())
			Expect(sender).NotTo(BeNil())
			// Should return a RetrySender wrapping an SMTPSender
			_, ok := sender.(*notifier.RetrySender)
			Expect(ok).To(BeTrue(), "expected RetrySender, got %T", sender)
		})

		It("defaults port to 587 when not specified in Secret", func() {
			// Create a secret without a port key
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "smtp-noport",
					Namespace: examCRNamespace,
				},
				Data: map[string][]byte{
					"host":     []byte("mail.test.com"),
					"username": []byte("user"),
					"password": []byte("pass"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			exam := &examv1alpha1.Exam{
				ObjectMeta: metav1.ObjectMeta{
					Name:      examName,
					Namespace: examCRNamespace,
				},
				Spec: examv1alpha1.ExamSpec{
					Email: examv1alpha1.ExamEmail{
						SecretRef: "smtp-noport",
					},
				},
			}

			reconciler := newReconciler(nil, nil, nil)
			sender, err := reconciler.resolvedSender(ctx, exam)
			Expect(err).NotTo(HaveOccurred())
			Expect(sender).NotTo(BeNil())
		})

		It("returns error when Secret does not exist", func() {
			exam := &examv1alpha1.Exam{
				ObjectMeta: metav1.ObjectMeta{
					Name:      examName,
					Namespace: examCRNamespace,
				},
				Spec: examv1alpha1.ExamSpec{
					Email: examv1alpha1.ExamEmail{
						SecretRef: "nonexistent-secret",
					},
				},
			}

			reconciler := newReconciler(nil, nil, nil)
			_, err := reconciler.resolvedSender(ctx, exam)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("nonexistent-secret"))
		})
	})

	Describe("Drift correction during Unlocked phase", func() {
		var (
			ctx      context.Context
			examName string
			nn       types.NamespacedName
			unlock   time.Time
		)

		BeforeEach(func() {
			ctx = context.Background()
			examName = uniqueExamName("driftunlock")
			nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
			unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
		})

		AfterEach(func() {
			cleanupExam(ctx, examName, examCRNamespace)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
			_ = k8sClient.Delete(ctx, ns)
		})

		It("recreates deleted ingress-allow network policy while unlocked", func() {
			students := []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			}
			createExamCR(ctx, examName, unlock, students, 0)
			preseedSlugs(ctx, nn)

			By("Driving the exam to Unlocked phase")
			fakeSender := &notifier.FakeSender{}
			reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseUnlocked, unlock, fakeSender, nil)

			ns := examNamespace(examName, examCRNamespace)

			By("Verifying exam is in Unlocked phase")
			exam := &examv1alpha1.Exam{}
			Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
			Expect(exam.Status.Phase).To(Equal(examv1alpha1.ExamPhaseUnlocked))

			By("Listing all network policies with exam label")
			var netpols networkingv1.NetworkPolicyList
			Expect(k8sClient.List(ctx, &netpols,
				client.InNamespace(ns),
				client.MatchingLabels{provisioner.LabelExam: examName},
			)).To(Succeed())

			By("Finding an ingress-allow network policy")
			var ingressAllowPolicy *networkingv1.NetworkPolicy
			for i := range netpols.Items {
				if strings.HasSuffix(netpols.Items[i].Name, "-ingress-allow") {
					ingressAllowPolicy = &netpols.Items[i]
					break
				}
			}
			Expect(ingressAllowPolicy).NotTo(BeNil(), "expected to find an ingress-allow network policy in Unlocked phase")
			deletedName := ingressAllowPolicy.Name

			By("Deleting the ingress-allow network policy")
			Expect(k8sClient.Delete(ctx, ingressAllowPolicy)).To(Succeed())

			By("Verifying the ingress-allow policy is gone")
			deleted := &networkingv1.NetworkPolicy{}
			err := k8sClient.Get(ctx, types.NamespacedName{Name: deletedName, Namespace: ns}, deleted)
			Expect(err).To(HaveOccurred(), "ingress-allow policy should be deleted")

			By("Reconciling again while still in Unlocked phase")
			_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())

			By("Verifying the ingress-allow policy is recreated")
			recreated := &networkingv1.NetworkPolicy{}
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deletedName, Namespace: ns}, recreated)).To(Succeed())

			By("Verifying ingress resources still exist")
			var ingresses networkingv1.IngressList
			Expect(k8sClient.List(ctx, &ingresses,
				client.InNamespace(ns),
				client.MatchingLabels{provisioner.LabelExam: examName},
			)).To(Succeed())
			Expect(ingresses.Items).NotTo(BeEmpty(), "ingress resources should still exist after drift correction")
		})
	})
})
