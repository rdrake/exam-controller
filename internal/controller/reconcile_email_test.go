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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	"github.com/rdrake/exam-controller/internal/notifier"
)

var _ = Describe("Email Sending", func() {
	var (
		ctx        context.Context
		examName   string
		nn         types.NamespacedName
		unlock     time.Time
		fakeSender *notifier.FakeSender
	)

	BeforeEach(func() {
		ctx = context.Background()
		unlock = time.Date(2026, 6, 15, 14, 0, 0, 0, time.UTC)
		fakeSender = &notifier.FakeSender{}
	})

	AfterEach(func() {
		cleanupExam(ctx, examName, examCRNamespace)
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: examNamespace(examName, examCRNamespace)}}
		_ = k8sClient.Delete(ctx, ns)
	})

	It("sends student emails during Ready phase", func() {
		examName = uniqueExamName("email-ready")
		nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}

		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
			{ID: "bob", Email: "bob@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		// Drive to Ready phase (clock at unlock-30m, which is emailTime)
		reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, nil)

		// Clear any emails sent during provisioning (e.g. spare notifications)
		fakeSender.Sent = nil

		// Advance clock past emailTime so emails get sent.
		// emailTime = unlock - 30m. Clock is already at unlock-30m from driveToPhase.
		// Reconcile once — should send exactly 1 email (rate limit per reconcile).
		reconciler.Now = func() time.Time { return unlock.Add(-29 * time.Minute) }
		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeSender.Sent).To(HaveLen(1))

		// Reconcile again — should send the second email.
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeSender.Sent).To(HaveLen(2))
	})

	It("respects rate limit", func() {
		examName = uniqueExamName("email-rate")
		nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}

		students := []examv1alpha1.ExamStudent{
			{ID: "s1", Email: "s1@test.com"},
			{ID: "s2", Email: "s2@test.com"},
			{ID: "s3", Email: "s3@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		// Patch rateLimit to 1 on the CR
		exam := &examv1alpha1.Exam{}
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		exam.Spec.Email.RateLimit = 1
		Expect(k8sClient.Update(ctx, exam)).To(Succeed())

		reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, nil)
		fakeSender.Sent = nil

		reconciler.Now = func() time.Time { return unlock.Add(-29 * time.Minute) }

		// Each reconcile should send exactly 1 email
		for i := 1; i <= 3; i++ {
			_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
			Expect(err).NotTo(HaveOccurred())
			Expect(fakeSender.Sent).To(HaveLen(i))
		}
	})

	It("does not re-send already-sent emails", func() {
		examName = uniqueExamName("email-nosend")
		nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}

		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
			{ID: "bob", Email: "bob@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, nil)
		fakeSender.Sent = nil

		reconciler.Now = func() time.Time { return unlock.Add(-29 * time.Minute) }

		// Send all emails (2 students)
		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeSender.Sent).To(HaveLen(2))

		// One more reconcile sets AllEmailsSent condition
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		sentCount := len(fakeSender.Sent)

		// Reconcile 2 more times — count should not grow
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		Expect(fakeSender.Sent).To(HaveLen(sentCount))
	})

	It("skips students with Failed email status", func() {
		examName = uniqueExamName("email-skip")
		nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}

		students := []examv1alpha1.ExamStudent{
			{ID: "s1", Email: "s1@test.com"},
			{ID: "s2", Email: "s2@test.com"},
			{ID: "s3", Email: "s3@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, nil)
		fakeSender.Sent = nil

		reconciler.Now = func() time.Time { return unlock.Add(-29 * time.Minute) }

		// First reconcile sends email to s1
		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeSender.Sent).To(HaveLen(1))

		// Manually patch s2's EmailStatus to Failed
		exam := &examv1alpha1.Exam{}
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		exam.Status.Students[1].EmailStatus = examv1alpha1.EmailStatusFailed
		Expect(k8sClient.Status().Update(ctx, exam)).To(Succeed())

		// Next reconcile should skip s2 (Failed) and send to s3
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeSender.Sent).To(HaveLen(2))

		// Verify the second email went to s3, not s2
		Expect(fakeSender.Sent[1].To).To(ContainElement("s3@test.com"))
	})

	It("sets AllEmailsSent condition after all students attempted", func() {
		examName = uniqueExamName("email-done")
		nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}

		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
			{ID: "bob", Email: "bob@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 0)
		preseedSlugs(ctx, nn)

		reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseReady, unlock, fakeSender, nil)

		reconciler.Now = func() time.Time { return unlock.Add(-29 * time.Minute) }

		// Send email to alice
		_, err := reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		// Send email to bob
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		// Next reconcile: no more pending, should set AllEmailsSent
		_, err = reconciler.Reconcile(ctx, reconcileRequest(nn))
		Expect(err).NotTo(HaveOccurred())

		exam := &examv1alpha1.Exam{}
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())

		cond := meta.FindStatusCondition(exam.Status.Conditions, "AllEmailsSent")
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		Expect(cond.Reason).To(Equal("Complete"))
	})

	It("sends unlock notification to instructor", func() {
		examName = uniqueExamName("email-unlock")
		nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}

		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 1)
		preseedSlugs(ctx, nn)

		reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseUnlocked, unlock, fakeSender, nil)
		_ = reconciler

		// Check that an unlock notification was sent to instructor
		var unlockMsg *notifier.SentMessage
		for i := range fakeSender.Sent {
			for _, to := range fakeSender.Sent[i].To {
				if to == "prof@test.com" && strings.Contains(string(fakeSender.Sent[i].Body), "Exam is live") {
					unlockMsg = &fakeSender.Sent[i]
					break
				}
			}
		}
		Expect(unlockMsg).NotTo(BeNil(), "expected unlock notification to instructor")
		Expect(string(unlockMsg.Body)).To(ContainSubstring("Exam Unlocked"))

		// Verify condition is set
		exam := &examv1alpha1.Exam{}
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		cond := meta.FindStatusCondition(exam.Status.Conditions, "InstructorNotifiedUnlock")
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
	})

	It("sends lock notification to instructor", func() {
		examName = uniqueExamName("email-lock")
		nn = types.NamespacedName{Name: examName, Namespace: examCRNamespace}

		students := []examv1alpha1.ExamStudent{
			{ID: "alice", Email: "alice@test.com"},
		}
		createExamCR(ctx, examName, unlock, students, 1)
		preseedSlugs(ctx, nn)

		reconciler := driveToPhase(ctx, nn, examv1alpha1.ExamPhaseLocked, unlock, fakeSender, nil)
		_ = reconciler

		// Check that a lock notification was sent to instructor
		var lockMsg *notifier.SentMessage
		for i := range fakeSender.Sent {
			for _, to := range fakeSender.Sent[i].To {
				if to == "prof@test.com" && strings.Contains(string(fakeSender.Sent[i].Body), "Exam has ended") {
					lockMsg = &fakeSender.Sent[i]
					break
				}
			}
		}
		Expect(lockMsg).NotTo(BeNil(), "expected lock notification to instructor")
		Expect(string(lockMsg.Body)).To(ContainSubstring("Exam Locked"))

		// Verify condition is set
		exam := &examv1alpha1.Exam{}
		Expect(k8sClient.Get(ctx, nn, exam)).To(Succeed())
		cond := meta.FindStatusCondition(exam.Status.Conditions, "InstructorNotifiedLock")
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionTrue))
	})
})
