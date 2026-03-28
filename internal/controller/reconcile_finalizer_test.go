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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	"github.com/rdrake/exam-controller/internal/notifier"
)

var _ = Describe("Finalizer and Deletion", func() {
	var (
		ctx        context.Context
		examName   string
		nn         types.NamespacedName
		unlock     time.Time
		fakeSender *notifier.FakeSender
	)

	BeforeEach(func() {
		ctx = context.Background()
		examName = uniqueExamName("finalizer")
		nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}
		unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
		fakeSender = &notifier.FakeSender{}
	})

	AfterEach(func() {
		cleanupExam(ctx, examName, examCRNamespace)
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName)}}
		_ = k8sClient.Delete(ctx, ns)
	})

	It("adds finalizer on first reconcile", func() {
		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		clockTime := unlock.Add(-30 * time.Minute)
		reconciler := newReconciler(func() time.Time { return clockTime }, fakeSender, nil)

		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		exam := &examv1alpha1.Exam{}
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		Expect(exam.Finalizers).To(ContainElement(finalizerName))
	})

	It("deletion triggers namespace cleanup and finalizer removal", func() {
		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		// Drive to Provisioning so namespace and resources exist.
		reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseProvisioning, unlock, fakeSender, nil)

		// Verify namespace exists before deletion.
		ns := &corev1.Namespace{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: examNamespace(examName)}, ns)).To(Succeed())

		// Delete the Exam object — this sets DeletionTimestamp but finalizer holds it.
		exam := &examv1alpha1.Exam{}
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		Expect(k8sClient.Delete(ctx, exam)).To(Succeed())

		// Reconcile to trigger finalizer cleanup.
		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		// Verify the Exam object is gone (finalizer removed, K8s garbage-collected it).
		err = k8sClient.Get(ctx, nn, exam)
		Expect(errors.IsNotFound(err)).To(BeTrue(), "expected Exam to be deleted")

		// Verify namespace is gone or being deleted.
		nsCheck := &corev1.Namespace{}
		err = k8sClient.Get(ctx, types.NamespacedName{Name: examNamespace(examName)}, nsCheck)
		if err == nil {
			// Namespace may still exist but should be terminating.
			Expect(nsCheck.DeletionTimestamp).NotTo(BeNil())
		} else {
			Expect(errors.IsNotFound(err)).To(BeTrue())
		}
	})

	It("handles already-deleted namespace gracefully", func() {
		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		// Drive to Provisioning so namespace exists.
		reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseProvisioning, unlock, fakeSender, nil)

		// Manually delete the exam namespace before deleting the Exam CR.
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName)}}
		Expect(k8sClient.Delete(ctx, ns)).To(Succeed())

		// Delete the Exam object.
		exam := &examv1alpha1.Exam{}
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		Expect(k8sClient.Delete(ctx, exam)).To(Succeed())

		// Reconcile should succeed even though namespace is already gone.
		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		// Verify the Exam object is gone.
		err = k8sClient.Get(ctx, nn, exam)
		Expect(errors.IsNotFound(err)).To(BeTrue(), "expected Exam to be deleted")
	})

	It("reconcile for non-existent resource returns no error", func() {
		nonExistent := types.NamespacedName{
			Name:      "does-not-exist",
			Namespace: examCRNamespace,
		}
		reconciler := newReconciler(func() time.Time { return time.Now() }, fakeSender, nil)

		result, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: nonExistent})
		Expect(err).NotTo(HaveOccurred())
		Expect(result).To(Equal(reconcile.Result{}))
	})
})
