//go:build e2e
// +build e2e

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

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/rdrake/exam-controller/test/utils"
)

// namespace where the project is deployed in
const namespace = "exam-controller-system"

// examCRNamespace is where Exam CRs are created (controller watches this namespace)
const examCRNamespace = "exam-system"

// serviceAccountName created for the project
const serviceAccountName = "exam-controller-controller-manager"

// metricsServiceName is the name of the metrics service of the project
const metricsServiceName = "exam-controller-controller-manager-metrics-service"

// metricsRoleBindingName is the name of the RBAC that will be created to allow get the metrics data
const metricsRoleBindingName = "exam-controller-metrics-binding"

var _ = Describe("Manager", Ordered, func() {
	var controllerPodName string

	// Before running the tests, set up the environment by creating the namespace,
	// enforce the restricted security policy to the namespace, installing CRDs,
	// and deploying the controller.
	BeforeAll(func() {
		By("creating manager namespace")
		cmd := exec.Command("kubectl", "create", "ns", namespace)
		_, err := utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to create namespace")

		By("labeling the namespace to enforce the restricted security policy")
		cmd = exec.Command("kubectl", "label", "--overwrite", "ns", namespace,
			"pod-security.kubernetes.io/enforce=restricted")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to label namespace with restricted policy")

		By("installing CRDs")
		cmd = exec.Command("make", "install")
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to install CRDs")

		By("creating the exam CR namespace")
		cmd = exec.Command("kubectl", "create", "ns", examCRNamespace)
		_, _ = utils.Run(cmd) // ignore if already exists

		By("deploying the controller-manager")
		cmd = exec.Command("make", "deploy", fmt.Sprintf("IMG=%s", managerImage))
		_, err = utils.Run(cmd)
		Expect(err).NotTo(HaveOccurred(), "Failed to deploy the controller-manager")
	})

	// After all tests have been executed, clean up by undeploying the controller, uninstalling CRDs,
	// and deleting the namespace.
	AfterAll(func() {
		By("cleaning up the curl pod for metrics")
		cmd := exec.Command("kubectl", "delete", "pod", "curl-metrics", "-n", namespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("undeploying the controller-manager")
		cmd = exec.Command("make", "undeploy")
		_, _ = utils.Run(cmd)

		By("uninstalling CRDs")
		cmd = exec.Command("make", "uninstall")
		_, _ = utils.Run(cmd)

		By("removing exam CR namespace")
		cmd = exec.Command("kubectl", "delete", "ns", examCRNamespace, "--ignore-not-found")
		_, _ = utils.Run(cmd)

		By("removing manager namespace")
		cmd = exec.Command("kubectl", "delete", "ns", namespace)
		_, _ = utils.Run(cmd)
	})

	// After each test, check for failures and collect logs, events,
	// and pod descriptions for debugging.
	AfterEach(func() {
		specReport := CurrentSpecReport()
		if specReport.Failed() {
			By("Fetching controller manager pod logs")
			cmd := exec.Command("kubectl", "logs", controllerPodName, "-n", namespace)
			controllerLogs, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Controller logs:\n %s", controllerLogs)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Controller logs: %s", err)
			}

			By("Fetching Kubernetes events")
			cmd = exec.Command("kubectl", "get", "events", "-n", namespace, "--sort-by=.lastTimestamp")
			eventsOutput, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Kubernetes events:\n%s", eventsOutput)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to get Kubernetes events: %s", err)
			}

			By("Fetching controller manager pod description")
			cmd = exec.Command("kubectl", "describe", "pod", controllerPodName, "-n", namespace)
			podDescription, err := utils.Run(cmd)
			if err == nil {
				_, _ = fmt.Fprintf(GinkgoWriter, "Pod description:\n %s", podDescription)
			} else {
				_, _ = fmt.Fprintf(GinkgoWriter, "Failed to describe controller pod: %s", err)
			}
		}
	})

	SetDefaultEventuallyTimeout(2 * time.Minute)
	SetDefaultEventuallyPollingInterval(5 * time.Second)

	Context("Manager", func() {
		// Scenario 1: Controller health and infrastructure
		It("controller boots and becomes healthy", func() {
			By("validating that the controller-manager pod is running")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods",
					"-l", "control-plane=controller-manager",
					"-o", "go-template={{ range .items }}"+
						"{{ if not .metadata.deletionTimestamp }}"+
						"{{ .metadata.name }}"+
						"{{ \"\\n\" }}{{ end }}{{ end }}",
					"-n", namespace,
				)
				podOutput, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred(), "Failed to retrieve controller-manager pod information")
				podNames := utils.GetNonEmptyLines(podOutput)
				g.Expect(podNames).To(HaveLen(1), "expected 1 controller pod running")
				controllerPodName = podNames[0]
				g.Expect(controllerPodName).To(ContainSubstring("controller-manager"))

				cmd = exec.Command("kubectl", "get", "pods", controllerPodName,
					"-o", "jsonpath={.status.phase}", "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Running"), "Incorrect controller-manager pod status")
			}).Should(Succeed())

			By("verifying healthz and readyz via pod conditions")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pod", controllerPodName, "-n", namespace,
					"-o", "jsonpath={.status.conditions[?(@.type=='Ready')].status}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("True"), "Controller pod not ready")
			}, 3*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying the CRD is registered")
			cmd := exec.Command("kubectl", "get", "crd", "exams.exam.otu.ca")
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "CRD exams.exam.otu.ca should be registered")

			By("creating a ClusterRoleBinding for metrics access")
			cmd = exec.Command("kubectl", "create", "clusterrolebinding", metricsRoleBindingName,
				"--clusterrole=exam-controller-metrics-reader",
				fmt.Sprintf("--serviceaccount=%s:%s", namespace, serviceAccountName),
			)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create ClusterRoleBinding")

			By("getting the service account token")
			token, err := serviceAccountToken()
			Expect(err).NotTo(HaveOccurred())
			Expect(token).NotTo(BeEmpty())

			By("verifying metrics endpoint is reachable via curl pod")
			cmd = exec.Command("kubectl", "run", "curl-metrics", "--restart=Never",
				"--namespace", namespace,
				"--image=curlimages/curl:latest",
				"--overrides",
				fmt.Sprintf(`{
					"spec": {
						"containers": [{
							"name": "curl",
							"image": "curlimages/curl:latest",
							"command": ["/bin/sh", "-c"],
							"args": [
								"for i in $(seq 1 30); do curl -v -k -H 'Authorization: Bearer %s' https://%s.%s.svc.cluster.local:8443/metrics && exit 0 || sleep 2; done; exit 1"
							],
							"securityContext": {
								"readOnlyRootFilesystem": true,
								"allowPrivilegeEscalation": false,
								"capabilities": {
									"drop": ["ALL"]
								},
								"runAsNonRoot": true,
								"runAsUser": 1000,
								"seccompProfile": {
									"type": "RuntimeDefault"
								}
							}
						}],
						"serviceAccountName": "%s"
					}
				}`, token, metricsServiceName, namespace, serviceAccountName))
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create curl-metrics pod")

			By("waiting for the curl-metrics pod to succeed")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "pods", "curl-metrics",
					"-o", "jsonpath={.status.phase}", "-n", namespace)
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Succeeded"), "curl pod in wrong status")
			}, 5*time.Minute, 5*time.Second).Should(Succeed())

			By("confirming metrics response contains HTTP 200")
			cmd = exec.Command("kubectl", "logs", "curl-metrics", "-n", namespace)
			metricsOutput, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to retrieve logs from curl pod")
			Expect(metricsOutput).To(ContainSubstring("< HTTP/1.1 200 OK"))
		})

		// Scenario 2: Full exam lifecycle
		It("exam completes full lifecycle", func() {
			const examName = "e2e-lifecycle"
			examNS := "exam-" + examName

			unlock := time.Now().UTC().Add(5 * time.Minute).Format(time.RFC3339)

			By("creating the SMTP secret")
			cmd := exec.Command("kubectl", "create", "secret", "generic",
				"exam-smtp-credentials",
				"--namespace", examCRNamespace,
				"--from-literal=host=smtp.example.com",
				"--from-literal=port=587",
				"--from-literal=username=user",
				"--from-literal=password=pass",
			)
			_, _ = utils.Run(cmd) // ignore if already exists

			By("creating the Exam CR")
			examYAML := fmt.Sprintf(`apiVersion: exam.otu.ca/v1alpha1
kind: Exam
metadata:
  name: %s
  namespace: %s
spec:
  template:
    image: nginxinc/nginx-unprivileged:latest
    port: 80
  schedule:
    unlock: "%s"
    duration: "1h"
    timeMultiplier: 1.5
    provisionBefore: "10m"
  students:
    - id: test-student
      email: test@example.com
  spares: 0
  email:
    before: "3m"
    rateLimit: 10
    instructorEmail: "instructor@example.com"
    secretRef: exam-smtp-credentials
    from: "noreply@example.com"
    subject: "E2E Lifecycle Test"
  ingressTLS:
    secretName: test-tls
  domain: exam.test.local`, examName, examCRNamespace, unlock)

			tmpFile := filepath.Join(os.TempDir(), "exam-lifecycle.yaml")
			Expect(os.WriteFile(tmpFile, []byte(examYAML), 0644)).To(Succeed())
			defer os.Remove(tmpFile)

			cmd = exec.Command("kubectl", "apply", "-f", tmpFile)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create Exam CR")

			By("waiting for at least Provisioning phase")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "exam", examName,
					"-n", examCRNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(BeElementOf("Provisioning", "Ready"),
					"expected phase to be at least Provisioning")
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("waiting for Ready phase")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "exam", examName,
					"-n", examCRNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Ready"))
			}, 5*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying student namespace exists")
			cmd = exec.Command("kubectl", "get", "ns", examNS)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "exam namespace %s should exist during Ready phase", examNS)

			By("cleaning up the Exam CR")
			cmd = exec.Command("kubectl", "delete", "exam", examName,
				"-n", examCRNamespace, "--ignore-not-found", "--timeout=60s")
			_, _ = utils.Run(cmd)

			By("waiting for exam namespace to be cleaned up")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ns", examNS)
				_, err := utils.Run(cmd)
				if err != nil {
					return // namespace gone -- success
				}
				g.Expect("namespace still exists").To(BeEmpty())
			}, 3*time.Minute, 5*time.Second).Should(Succeed())
		})

		// Scenario 3: Webhook validation (requires --enable-webhooks + cert-manager)
		PIt("webhook rejects invalid exam", func() {
			unlock := time.Now().UTC().Add(10 * time.Minute).Format(time.RFC3339)

			By("rejecting an Exam with empty students")
			zeroStudentsYAML := fmt.Sprintf(`apiVersion: exam.otu.ca/v1alpha1
kind: Exam
metadata:
  name: e2e-webhook-no-students
  namespace: %s
spec:
  template:
    image: nginxinc/nginx-unprivileged:latest
    port: 80
  schedule:
    unlock: "%s"
    duration: "1h"
    timeMultiplier: 1.5
    provisionBefore: "2h"
  students: []
  spares: 0
  email:
    before: "30m"
    rateLimit: 1
    instructorEmail: "instructor@example.com"
    secretRef: exam-smtp-credentials
    from: "noreply@example.com"
    subject: "Webhook Test"
  ingressTLS:
    secretName: test-tls
  domain: exam.test.local`, examCRNamespace, unlock)

			tmpFile := filepath.Join(os.TempDir(), "exam-webhook-no-students.yaml")
			Expect(os.WriteFile(tmpFile, []byte(zeroStudentsYAML), 0644)).To(Succeed())
			defer os.Remove(tmpFile)

			cmd := exec.Command("kubectl", "apply", "-f", tmpFile)
			output, err := utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "expected kubectl apply to fail for 0 students")
			Expect(output).To(ContainSubstring("at least one entry"),
				"expected error about requiring at least one student")

			By("rejecting an Exam with zero duration")
			zeroDurationYAML := fmt.Sprintf(`apiVersion: exam.otu.ca/v1alpha1
kind: Exam
metadata:
  name: e2e-webhook-zero-duration
  namespace: %s
spec:
  template:
    image: nginxinc/nginx-unprivileged:latest
    port: 80
  schedule:
    unlock: "%s"
    duration: "0s"
    timeMultiplier: 1.5
    provisionBefore: "2h"
  students:
    - id: test-student
      email: test@example.com
  spares: 0
  email:
    before: "30m"
    rateLimit: 10
    instructorEmail: "instructor@example.com"
    secretRef: exam-smtp-credentials
    from: "noreply@example.com"
    subject: "Webhook Test"
  ingressTLS:
    secretName: test-tls
  domain: exam.test.local`, examCRNamespace, unlock)

			tmpFile2 := filepath.Join(os.TempDir(), "exam-webhook-zero-duration.yaml")
			Expect(os.WriteFile(tmpFile2, []byte(zeroDurationYAML), 0644)).To(Succeed())
			defer os.Remove(tmpFile2)

			cmd = exec.Command("kubectl", "apply", "-f", tmpFile2)
			output, err = utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "expected kubectl apply to fail for duration 0")
			Expect(output).To(ContainSubstring("duration must be > 0"),
				"expected error about positive duration")

			By("rejecting an Exam with timeMultiplier < 1.0")
			lowMultiplierYAML := fmt.Sprintf(`apiVersion: exam.otu.ca/v1alpha1
kind: Exam
metadata:
  name: e2e-webhook-low-multiplier
  namespace: %s
spec:
  template:
    image: nginxinc/nginx-unprivileged:latest
    port: 80
  schedule:
    unlock: "%s"
    duration: "1h"
    timeMultiplier: 0.5
    provisionBefore: "2h"
  students:
    - id: test-student
      email: test@example.com
  spares: 0
  email:
    before: "30m"
    rateLimit: 10
    instructorEmail: "instructor@example.com"
    secretRef: exam-smtp-credentials
    from: "noreply@example.com"
    subject: "Webhook Test"
  ingressTLS:
    secretName: test-tls
  domain: exam.test.local`, examCRNamespace, unlock)

			tmpFile3 := filepath.Join(os.TempDir(), "exam-webhook-low-multiplier.yaml")
			Expect(os.WriteFile(tmpFile3, []byte(lowMultiplierYAML), 0644)).To(Succeed())
			defer os.Remove(tmpFile3)

			cmd = exec.Command("kubectl", "apply", "-f", tmpFile3)
			output, err = utils.Run(cmd)
			Expect(err).To(HaveOccurred(), "expected kubectl apply to fail for timeMultiplier < 1.0")
			Expect(output).To(ContainSubstring("timeMultiplier must be >= 1.0"),
				"expected error about timeMultiplier minimum")
		})

		// Scenario 4: Unlock and lock transitions
		It("unlock and lock transitions create and remove ingresses", func() {
			const examName = "e2e-phases"
			examNS := "exam-" + examName

			unlock := time.Now().UTC().Add(30 * time.Second).Format(time.RFC3339)

			By("creating the SMTP secret")
			cmd := exec.Command("kubectl", "create", "secret", "generic",
				"exam-smtp-phases",
				"--namespace", examCRNamespace,
				"--from-literal=host=smtp.invalid.example.com",
				"--from-literal=port=587",
				"--from-literal=username=dummy",
				"--from-literal=password=dummy",
			)
			_, _ = utils.Run(cmd) // ignore if already exists

			By("creating the Exam CR with short timings")
			examYAML := fmt.Sprintf(`apiVersion: exam.otu.ca/v1alpha1
kind: Exam
metadata:
  name: %s
  namespace: %s
spec:
  template:
    image: nginxinc/nginx-unprivileged:latest
    port: 80
  schedule:
    unlock: "%s"
    duration: "30s"
    timeMultiplier: 1.0
    provisionBefore: "2m"
  students:
    - id: test-student
      email: test@example.com
  spares: 0
  email:
    before: "1m30s"
    rateLimit: 10
    instructorEmail: "instructor@example.com"
    secretRef: exam-smtp-phases
    from: "noreply@example.com"
    subject: "E2E Phase Test"
  ingressTLS:
    secretName: test-tls
  domain: exam.test.local`, examName, examCRNamespace, unlock)

			tmpFile := filepath.Join(os.TempDir(), "exam-phases.yaml")
			Expect(os.WriteFile(tmpFile, []byte(examYAML), 0644)).To(Succeed())
			defer os.Remove(tmpFile)

			cmd = exec.Command("kubectl", "apply", "-f", tmpFile)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create Exam CR")

			By("waiting for Unlocked phase")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "exam", examName,
					"-n", examCRNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Unlocked"))
			}, 3*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying at least one Ingress exists in exam namespace")
			cmd = exec.Command("kubectl", "get", "ingress", "-n", examNS,
				"-o", "jsonpath={.items[*].metadata.name}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(output)).NotTo(BeEmpty(), "expected Ingress resources while Unlocked")

			By("waiting for Locked phase")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "exam", examName,
					"-n", examCRNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Locked"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying Ingresses are deleted after lock")
			cmd = exec.Command("kubectl", "get", "ingress", "-n", examNS,
				"-o", "jsonpath={.items[*].metadata.name}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(output)).To(BeEmpty(), "expected no Ingress resources after Locked")

			By("cleaning up the Exam CR")
			cmd = exec.Command("kubectl", "delete", "exam", examName,
				"-n", examCRNamespace, "--ignore-not-found", "--timeout=60s")
			_, _ = utils.Run(cmd)

			By("waiting for exam namespace cleanup")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ns", examNS)
				_, err := utils.Run(cmd)
				if err != nil {
					return // namespace gone -- success
				}
				g.Expect("namespace still exists").To(BeEmpty())
			}, 3*time.Minute, 5*time.Second).Should(Succeed())

			By("cleaning up the SMTP secret")
			cmd = exec.Command("kubectl", "delete", "secret", "exam-smtp-phases",
				"-n", examCRNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})

		// Scenario 5: Full 6-phase lifecycle with compressed timeline
		It("exercises the full 6-phase lifecycle with compressed timeline", func() {
			const examName = "e2e-full-lifecycle"
			examNS := "exam-" + examName

			unlock := time.Now().UTC().Add(60 * time.Second).Format(time.RFC3339)

			By("creating the SMTP secret with non-routable host for fast failure")
			cmd := exec.Command("kubectl", "create", "secret", "generic",
				"exam-smtp-lifecycle",
				"--namespace", examCRNamespace,
				"--from-literal=host=smtp.invalid",
				"--from-literal=port=587",
				"--from-literal=username=dummy",
				"--from-literal=password=dummy",
			)
			_, _ = utils.Run(cmd) // ignore if already exists

			By("creating the Exam CR with compressed timings")
			examYAML := fmt.Sprintf(`apiVersion: exam.otu.ca/v1alpha1
kind: Exam
metadata:
  name: %s
  namespace: %s
spec:
  template:
    image: nginxinc/nginx-unprivileged:latest
    port: 80
  schedule:
    unlock: "%s"
    duration: "20s"
    timeMultiplier: 1.0
    provisionBefore: "90s"
    retention: "10s"
  students:
    - id: student-one
      email: student1@example.com
    - id: student-two
      email: student2@example.com
  spares: 1
  email:
    before: "15s"
    rateLimit: 10
    instructorEmail: "instructor@example.com"
    secretRef: exam-smtp-lifecycle
    from: "noreply@example.com"
    subject: "E2E Full Lifecycle Test"
  ingressTLS:
    secretName: test-tls
  domain: exam.test.local`, examName, examCRNamespace, unlock)

			tmpFile := filepath.Join(os.TempDir(), "exam-full-lifecycle.yaml")
			Expect(os.WriteFile(tmpFile, []byte(examYAML), 0644)).To(Succeed())
			defer os.Remove(tmpFile)

			cmd = exec.Command("kubectl", "apply", "-f", tmpFile)
			_, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "Failed to create Exam CR")

			// --- Phase 1: Provisioning ---
			By("waiting for Provisioning phase")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "exam", examName,
					"-n", examCRNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(BeElementOf("Provisioning", "Ready"),
					"expected phase to be at least Provisioning")
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying exam namespace exists")
			cmd = exec.Command("kubectl", "get", "ns", examNS)
			_, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred(), "exam namespace %s should exist", examNS)

			By("verifying 3 deployments exist (2 students + 1 spare)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "deployments",
					"-n", examNS,
					"-l", "exam.otu.ca/exam="+examName,
					"-o", "jsonpath={.items[*].metadata.name}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				names := strings.Fields(strings.TrimSpace(output))
				g.Expect(names).To(HaveLen(3), "expected 3 deployments, got: %v", names)
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying 3 services exist")
			cmd = exec.Command("kubectl", "get", "services",
				"-n", examNS,
				"-l", "exam.otu.ca/exam="+examName,
				"-o", "jsonpath={.items[*].metadata.name}")
			output, err := utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Fields(strings.TrimSpace(output))).To(HaveLen(3), "expected 3 services")

			By("verifying 6 network policies exist (deny-all + egress-allow per instance)")
			cmd = exec.Command("kubectl", "get", "networkpolicies",
				"-n", examNS,
				"-l", "exam.otu.ca/exam="+examName,
				"-o", "jsonpath={.items[*].metadata.name}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			npNames := strings.Fields(strings.TrimSpace(output))
			Expect(npNames).To(HaveLen(6), "expected 6 network policies, got: %v", npNames)

			By("verifying student statuses have slugs and URLs")
			cmd = exec.Command("kubectl", "get", "exam", examName,
				"-n", examCRNamespace,
				"-o", "jsonpath={.status.students[*].slug}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Fields(strings.TrimSpace(output))).To(HaveLen(2), "expected 2 student slugs")

			cmd = exec.Command("kubectl", "get", "exam", examName,
				"-n", examCRNamespace,
				"-o", "jsonpath={.status.spares[0].slug}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(output)).NotTo(BeEmpty(), "expected spare slug to be populated")

			// --- Phase 2: Ready ---
			By("waiting for Ready phase (pods become healthy)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "exam", examName,
					"-n", examCRNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Ready"))
			}, 3*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying Provisioned condition is True")
			cmd = exec.Command("kubectl", "get", "exam", examName,
				"-n", examCRNamespace,
				"-o", "jsonpath={.status.conditions[?(@.type=='Provisioned')].status}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("True"), "Provisioned condition should be True")

			By("verifying metrics summary is populated")
			cmd = exec.Command("kubectl", "get", "exam", examName,
				"-n", examCRNamespace,
				"-o", "jsonpath={.status.metrics.instancesHealthy}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("3"), "expected 3 healthy instances")

			// --- Phase 3: Unlocked ---
			By("waiting for Unlocked phase")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "exam", examName,
					"-n", examCRNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Unlocked"))
			}, 3*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying 3 ingresses exist")
			cmd = exec.Command("kubectl", "get", "ingress",
				"-n", examNS,
				"-o", "jsonpath={.items[*].metadata.name}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.Fields(strings.TrimSpace(output))).To(HaveLen(3),
				"expected 3 ingresses (2 students + 1 spare)")

			By("verifying student phases are Unlocked")
			cmd = exec.Command("kubectl", "get", "exam", examName,
				"-n", examCRNamespace,
				"-o", "jsonpath={.status.students[*].phase}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			for _, p := range strings.Fields(strings.TrimSpace(output)) {
				Expect(p).To(Equal("Unlocked"))
			}

			By("verifying spare phases are Unlocked")
			cmd = exec.Command("kubectl", "get", "exam", examName,
				"-n", examCRNamespace,
				"-o", "jsonpath={.status.spares[*].phase}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			for _, p := range strings.Fields(strings.TrimSpace(output)) {
				Expect(p).To(Equal("Unlocked"))
			}

			// --- Phase 4: Locked ---
			By("waiting for Locked phase")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "exam", examName,
					"-n", examCRNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("Locked"))
			}, 2*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying ingresses are deleted after lock")
			cmd = exec.Command("kubectl", "get", "ingress",
				"-n", examNS,
				"-o", "jsonpath={.items[*].metadata.name}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(strings.TrimSpace(output)).To(BeEmpty(), "expected no ingresses after Locked")

			By("verifying student phases are Locked")
			cmd = exec.Command("kubectl", "get", "exam", examName,
				"-n", examCRNamespace,
				"-o", "jsonpath={.status.students[*].phase}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			for _, p := range strings.Fields(strings.TrimSpace(output)) {
				Expect(p).To(Equal("Locked"))
			}

			By("verifying spare phases are Locked")
			cmd = exec.Command("kubectl", "get", "exam", examName,
				"-n", examCRNamespace,
				"-o", "jsonpath={.status.spares[*].phase}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			for _, p := range strings.Fields(strings.TrimSpace(output)) {
				Expect(p).To(Equal("Locked"))
			}

			By("verifying status message during Locked phase")
			cmd = exec.Command("kubectl", "get", "exam", examName,
				"-n", examCRNamespace,
				"-o", "jsonpath={.status.message}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("Exam ended, instances retained for investigation"))

			// --- Phase 5: TearingDown ---
			By("waiting for TearingDown phase (after retention deadline)")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "exam", examName,
					"-n", examCRNamespace,
					"-o", "jsonpath={.status.phase}")
				output, err := utils.Run(cmd)
				g.Expect(err).NotTo(HaveOccurred())
				g.Expect(output).To(Equal("TearingDown"))
			}, 3*time.Minute, 5*time.Second).Should(Succeed())

			By("verifying status message is 'Namespace deleted'")
			cmd = exec.Command("kubectl", "get", "exam", examName,
				"-n", examCRNamespace,
				"-o", "jsonpath={.status.message}")
			output, err = utils.Run(cmd)
			Expect(err).NotTo(HaveOccurred())
			Expect(output).To(Equal("Namespace deleted"))

			By("waiting for exam namespace to be deleted")
			Eventually(func(g Gomega) {
				cmd := exec.Command("kubectl", "get", "ns", examNS)
				_, err := utils.Run(cmd)
				if err != nil {
					return // namespace gone -- success
				}
				g.Expect("namespace still exists").To(BeEmpty())
			}, 3*time.Minute, 5*time.Second).Should(Succeed())

			// --- Cleanup ---
			By("cleaning up the Exam CR")
			cmd = exec.Command("kubectl", "delete", "exam", examName,
				"-n", examCRNamespace, "--ignore-not-found", "--timeout=60s")
			_, _ = utils.Run(cmd)

			By("cleaning up the SMTP secret")
			cmd = exec.Command("kubectl", "delete", "secret", "exam-smtp-lifecycle",
				"-n", examCRNamespace, "--ignore-not-found")
			_, _ = utils.Run(cmd)
		})
	})
})

// serviceAccountToken returns a token for the specified service account in the given namespace.
// It uses the Kubernetes TokenRequest API to generate a token by directly sending a request
// and parsing the resulting token from the API response.
func serviceAccountToken() (string, error) {
	const tokenRequestRawString = `{
		"apiVersion": "authentication.k8s.io/v1",
		"kind": "TokenRequest"
	}`

	// Temporary file to store the token request
	secretName := fmt.Sprintf("%s-token-request", serviceAccountName)
	tokenRequestFile := filepath.Join("/tmp", secretName)
	err := os.WriteFile(tokenRequestFile, []byte(tokenRequestRawString), os.FileMode(0o644))
	if err != nil {
		return "", err
	}

	var out string
	verifyTokenCreation := func(g Gomega) {
		// Execute kubectl command to create the token
		cmd := exec.Command("kubectl", "create", "--raw", fmt.Sprintf(
			"/api/v1/namespaces/%s/serviceaccounts/%s/token",
			namespace,
			serviceAccountName,
		), "-f", tokenRequestFile)

		output, err := cmd.CombinedOutput()
		g.Expect(err).NotTo(HaveOccurred())

		// Parse the JSON output to extract the token
		var token tokenRequest
		err = json.Unmarshal(output, &token)
		g.Expect(err).NotTo(HaveOccurred())

		out = token.Status.Token
	}
	Eventually(verifyTokenCreation).Should(Succeed())

	return out, err
}

// tokenRequest is a simplified representation of the Kubernetes TokenRequest API response,
// containing only the token field that we need to extract.
type tokenRequest struct {
	Status struct {
		Token string `json:"token"`
	} `json:"status"`
}
