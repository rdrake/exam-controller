# Exam Controller Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Build a Kubernetes CRD controller that manages per-student pen-testing exam instances with scheduled network-level lock/unlock, smoke tests, and email notification.

**Architecture:** A Kubebuilder-scaffolded operator with an `Exam` CRD. The controller reconciles through a time-driven state machine (Pending → Provisioning → Ready → DryRun → Verified → Unlocked → Locking → Locked → TearingDown). Each concern (provisioning, network policies, smoke tests, email) is a separate internal package called by the reconciler.

**Tech Stack:** Go 1.26, Kubebuilder 4, controller-runtime, `crypto/rand`, `net/smtp`, envtest for integration tests.

---

### Task 1: Install Kubebuilder and Scaffold Project

**Files:**
- Create: `go.mod`, `go.sum`, `Makefile`, `PROJECT`, `cmd/main.go`, `Dockerfile`
- Create: `config/` directory tree (crd, rbac, manager, default)

**Step 1: Install Kubebuilder**

```bash
go install sigs.k8s.io/kubebuilder/v4/cmd/kubebuilder@latest
```

Verify: `kubebuilder version` prints v4.x.

**Step 2: Initialize the project**

```bash
cd /Users/rdrake/workspace/exam-controller
kubebuilder init --domain otu.ca --repo github.com/rdrake/exam-controller
```

**Step 3: Create the Exam API**

```bash
kubebuilder create api --group exam --version v1alpha1 --kind Exam --resource --controller
```

**Step 4: Verify scaffold compiles**

Run: `make generate && make manifests && go build ./...`
Expected: Clean build, no errors.

**Step 5: Commit**

```bash
git add -A
git commit -m "chore: scaffold kubebuilder project with Exam CRD"
```

---

### Task 2: Define CRD Types

**Files:**
- Modify: `api/v1alpha1/exam_types.go`

**Step 1: Write the ExamSpec and ExamStatus types**

Replace the scaffolded types in `api/v1alpha1/exam_types.go` with the full CRD schema:

```go
package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ExamPhase represents the current state of an Exam.
type ExamPhase string

const (
	ExamPhasePending      ExamPhase = "Pending"
	ExamPhaseProvisioning ExamPhase = "Provisioning"
	ExamPhaseReady        ExamPhase = "Ready"
	ExamPhaseDryRun       ExamPhase = "DryRun"
	ExamPhaseVerified     ExamPhase = "Verified"
	ExamPhaseUnlocked     ExamPhase = "Unlocked"
	ExamPhaseLocking      ExamPhase = "Locking"
	ExamPhaseLocked       ExamPhase = "Locked"
	ExamPhaseTearingDown  ExamPhase = "TearingDown"
)

// StudentPhase represents the current state of a student's instance.
type StudentPhase string

const (
	StudentPhaseProvisioned StudentPhase = "Provisioned"
	StudentPhaseHealthy     StudentPhase = "Healthy"
	StudentPhaseUnlocked    StudentPhase = "Unlocked"
	StudentPhaseLocked      StudentPhase = "Locked"
	StudentPhaseFailed      StudentPhase = "Failed"
)

// EmailStatus represents the email delivery state.
type EmailStatus string

const (
	EmailStatusPending EmailStatus = "Pending"
	EmailStatusSent    EmailStatus = "Sent"
	EmailStatusFailed  EmailStatus = "Failed"
)

// ExamTemplate defines the pod template for student instances.
type ExamTemplate struct {
	Image     string                      `json:"image"`
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	Port      int32                       `json:"port"`
}

// ExamSchedule defines the timing for unlock/lock and dry run.
type ExamSchedule struct {
	Unlock metav1.Time      `json:"unlock"`
	Lock   metav1.Time      `json:"lock"`
	DryRun *ExamDryRunSpec  `json:"dryRun,omitempty"`
}

// ExamDryRunSpec configures the pre-exam smoke test window.
type ExamDryRunSpec struct {
	Before   metav1.Duration `json:"before"`
	Duration metav1.Duration `json:"duration"`
}

// ExamStudent defines a student participating in the exam.
type ExamStudent struct {
	ID           string       `json:"id"`
	Email        string       `json:"email"`
	LockOverride *metav1.Time `json:"lockOverride,omitempty"`
}

// ExamIngressTLS configures TLS for student Ingress resources.
type ExamIngressTLS struct {
	SecretName string `json:"secretName"`
}

// ExamSMTP configures email delivery.
type ExamSMTP struct {
	SecretRef string `json:"secretRef"`
	From      string `json:"from"`
	Subject   string `json:"subject"`
}

// ExamSpec defines the desired state of Exam.
type ExamSpec struct {
	Template   ExamTemplate   `json:"template"`
	Schedule   ExamSchedule   `json:"schedule"`
	Students   []ExamStudent  `json:"students"`
	IngressTLS ExamIngressTLS `json:"ingressTLS"`
	Domain     string         `json:"domain"`
	SMTP       ExamSMTP       `json:"smtp"`
}

// DryRunFailure records a smoke test failure for a student.
type DryRunFailure struct {
	Student string `json:"student"`
	Error   string `json:"error"`
}

// DryRunStatus records the results of the dry run.
type DryRunStatus struct {
	CompletedAt *metav1.Time    `json:"completedAt,omitempty"`
	Passed      int             `json:"passed"`
	Failed      int             `json:"failed"`
	Failures    []DryRunFailure `json:"failures,omitempty"`
}

// StudentStatus records the state of a single student's resources.
type StudentStatus struct {
	ID                string       `json:"id"`
	Slug              string       `json:"slug,omitempty"`
	URL               string       `json:"url,omitempty"`
	Phase             StudentPhase `json:"phase"`
	EmailStatus       EmailStatus  `json:"emailStatus"`
	EmailSentAt       *metav1.Time `json:"emailSentAt,omitempty"`
	LockedAt          *metav1.Time `json:"lockedAt,omitempty"`
	EffectiveLockTime metav1.Time  `json:"effectiveLockTime"`
}

// ExamStatus defines the observed state of Exam.
type ExamStatus struct {
	Phase             ExamPhase              `json:"phase,omitempty"`
	Message           string                 `json:"message,omitempty"`
	Conditions        []metav1.Condition     `json:"conditions,omitempty"`
	DryRun            *DryRunStatus          `json:"dryRun,omitempty"`
	Students          []StudentStatus        `json:"students,omitempty"`
	RetentionDeadline *metav1.Time           `json:"retentionDeadline,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
//+kubebuilder:printcolumn:name="Students",type=integer,JSONPath=`.status.students[*].id`
//+kubebuilder:printcolumn:name="Unlock",type=string,JSONPath=`.spec.schedule.unlock`
//+kubebuilder:printcolumn:name="Lock",type=string,JSONPath=`.spec.schedule.lock`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Exam is the Schema for the exams API.
type Exam struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ExamSpec   `json:"spec,omitempty"`
	Status ExamStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ExamList contains a list of Exam.
type ExamList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Exam `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Exam{}, &ExamList{})
}
```

**Step 2: Generate deepcopy and manifests**

Run: `make generate && make manifests`
Expected: `zz_generated.deepcopy.go` updated, CRD YAML generated in `config/crd/bases/`.

**Step 3: Verify it compiles**

Run: `go build ./...`
Expected: Clean build.

**Step 4: Commit**

```bash
git add -A
git commit -m "feat: define Exam CRD types with full spec and status schema"
```

---

### Task 3: Slug Generation Utility

**Files:**
- Create: `internal/slug/slug.go`
- Create: `internal/slug/slug_test.go`

**Step 1: Write the failing test**

```go
package slug

import (
	"regexp"
	"testing"
)

func TestGenerate_Length(t *testing.T) {
	s, err := Generate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(s) != 8 {
		t.Errorf("expected length 8, got %d: %q", len(s), s)
	}
}

func TestGenerate_DNSSafe(t *testing.T) {
	s, err := Generate()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	matched, _ := regexp.MatchString(`^[a-z0-9]{8}$`, s)
	if !matched {
		t.Errorf("slug %q is not DNS-safe lowercase alphanumeric", s)
	}
}

func TestGenerate_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		s, err := Generate()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if seen[s] {
			t.Errorf("duplicate slug %q after %d generations", s, i)
		}
		seen[s] = true
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/slug/ -v`
Expected: FAIL — `Generate` not defined.

**Step 3: Write the implementation**

```go
package slug

import (
	"crypto/rand"
	"math/big"
)

const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"

// Generate returns a cryptographically random 8-character DNS-safe slug.
func Generate() (string, error) {
	b := make([]byte, 8)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		b[i] = alphabet[n.Int64()]
	}
	return string(b), nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/slug/ -v`
Expected: PASS — all three tests green.

**Step 5: Commit**

```bash
git add internal/slug/
git commit -m "feat: add crypto/rand slug generator"
```

---

### Task 4: Network Policy Builder

**Files:**
- Create: `internal/network/policy.go`
- Create: `internal/network/policy_test.go`

**Step 1: Write the failing tests**

```go
package network

import (
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDenyAllPolicy(t *testing.T) {
	p := DenyAllPolicy("exam-ns", "exam-midterm", "john.smith")
	if p.Name != "john.smith-deny-all" {
		t.Errorf("unexpected name: %s", p.Name)
	}
	if p.Namespace != "exam-ns" {
		t.Errorf("unexpected namespace: %s", p.Namespace)
	}
	if len(p.Spec.PolicyTypes) != 2 {
		t.Errorf("expected 2 policy types (Ingress+Egress), got %d", len(p.Spec.PolicyTypes))
	}
	// Should have no ingress/egress rules (deny all)
	if len(p.Spec.Ingress) != 0 {
		t.Errorf("expected no ingress rules, got %d", len(p.Spec.Ingress))
	}
	if len(p.Spec.Egress) != 0 {
		t.Errorf("expected no egress rules, got %d", len(p.Spec.Egress))
	}
}

func TestEgressAllowlistPolicy(t *testing.T) {
	p := EgressAllowlistPolicy("exam-ns", "exam-midterm", "john.smith", "kube-system")
	if p.Name != "john.smith-egress-allow" {
		t.Errorf("unexpected name: %s", p.Name)
	}
	// Should allow DNS egress to kube-system
	if len(p.Spec.Egress) == 0 {
		t.Fatal("expected at least one egress rule")
	}
}

func TestIngressAllowPolicy(t *testing.T) {
	ingressNS := "ingress-nginx"
	ingressLabels := map[string]string{"app.kubernetes.io/name": "ingress-nginx"}
	p := IngressAllowPolicy("exam-ns", "exam-midterm", "john.smith", ingressNS, ingressLabels)
	if p.Name != "john.smith-ingress-allow" {
		t.Errorf("unexpected name: %s", p.Name)
	}
	if len(p.Spec.Ingress) != 1 {
		t.Fatalf("expected 1 ingress rule, got %d", len(p.Spec.Ingress))
	}
	// Should select ingress controller namespace
	from := p.Spec.Ingress[0].From
	if len(from) != 1 {
		t.Fatalf("expected 1 from peer, got %d", len(from))
	}
	if from[0].NamespaceSelector == nil {
		t.Error("expected namespace selector")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/network/ -v`
Expected: FAIL — functions not defined.

**Step 3: Write the implementation**

```go
package network

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func studentSelector(examName, studentID string) metav1.LabelSelector {
	return metav1.LabelSelector{
		MatchLabels: map[string]string{
			"exam.otu.ca/exam":    examName,
			"exam.otu.ca/student": studentID,
		},
	}
}

// DenyAllPolicy returns a NetworkPolicy that denies all ingress and egress for a student's pods.
func DenyAllPolicy(namespace, examName, studentID string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      studentID + "-deny-all",
			Namespace: namespace,
			Labels: map[string]string{
				"exam.otu.ca/exam":    examName,
				"exam.otu.ca/student": studentID,
				"exam.otu.ca/policy":  "deny-all",
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: studentSelector(examName, studentID),
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
		},
	}
}

// EgressAllowlistPolicy returns a NetworkPolicy that permits DNS egress to kube-dns.
func EgressAllowlistPolicy(namespace, examName, studentID, dnsNamespace string) *networkingv1.NetworkPolicy {
	dnsPort := intstr.FromInt32(53)
	udp := corev1.ProtocolUDP
	tcp := corev1.ProtocolTCP
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      studentID + "-egress-allow",
			Namespace: namespace,
			Labels: map[string]string{
				"exam.otu.ca/exam":    examName,
				"exam.otu.ca/student": studentID,
				"exam.otu.ca/policy":  "egress-allow",
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: studentSelector(examName, studentID),
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeEgress,
			},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": dnsNamespace,
								},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &udp, Port: &dnsPort},
						{Protocol: &tcp, Port: &dnsPort},
					},
				},
			},
		},
	}
}

// IngressAllowPolicy returns a NetworkPolicy that permits ingress from the ingress controller.
func IngressAllowPolicy(namespace, examName, studentID, ingressNamespace string, ingressPodLabels map[string]string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      studentID + "-ingress-allow",
			Namespace: namespace,
			Labels: map[string]string{
				"exam.otu.ca/exam":    examName,
				"exam.otu.ca/student": studentID,
				"exam.otu.ca/policy":  "ingress-allow",
			},
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: studentSelector(examName, studentID),
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
			},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{
									"kubernetes.io/metadata.name": ingressNamespace,
								},
							},
							PodSelector: &metav1.LabelSelector{
								MatchLabels: ingressPodLabels,
							},
						},
					},
				},
			},
		},
	}
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/network/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/network/
git commit -m "feat: add three-policy NetworkPolicy builder"
```

---

### Task 5: Provisioner — Per-Student Resources

**Files:**
- Create: `internal/provisioner/resources.go`
- Create: `internal/provisioner/resources_test.go`

**Step 1: Write the failing tests**

```go
package provisioner

import (
	"testing"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func testExam() *examv1alpha1.Exam {
	return &examv1alpha1.Exam{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "midterm",
			Namespace: "default",
			UID:       "test-uid",
		},
		Spec: examv1alpha1.ExamSpec{
			Template: examv1alpha1.ExamTemplate{
				Image: "vuln-app:v1",
				Port:  8080,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("250m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("500m"),
						corev1.ResourceMemory: resource.MustParse("512Mi"),
					},
				},
			},
			IngressTLS: examv1alpha1.ExamIngressTLS{SecretName: "tls-secret"},
			Domain:     "exam.otu.ca",
		},
	}
}

func TestDeployment(t *testing.T) {
	exam := testExam()
	d := Deployment(exam, "exam-ns", "john.smith", "a3f9b2c1")
	if d.Name != "a3f9b2c1" {
		t.Errorf("expected deployment name 'a3f9b2c1', got %q", d.Name)
	}
	if d.Namespace != "exam-ns" {
		t.Errorf("expected namespace 'exam-ns', got %q", d.Namespace)
	}
	container := d.Spec.Template.Spec.Containers[0]
	if container.Image != "vuln-app:v1" {
		t.Errorf("expected image 'vuln-app:v1', got %q", container.Image)
	}
	if *d.Spec.Template.Spec.AutomountServiceAccountToken != false {
		t.Error("expected automountServiceAccountToken=false")
	}
	sc := d.Spec.Template.Spec.Containers[0].SecurityContext
	if sc == nil || !*sc.RunAsNonRoot {
		t.Error("expected runAsNonRoot=true")
	}
}

func TestService(t *testing.T) {
	exam := testExam()
	s := Service(exam, "exam-ns", "john.smith", "a3f9b2c1")
	if s.Spec.Ports[0].Port != 8080 {
		t.Errorf("expected port 8080, got %d", s.Spec.Ports[0].Port)
	}
}

func TestIngress(t *testing.T) {
	exam := testExam()
	i := Ingress(exam, "exam-ns", "john.smith", "a3f9b2c1")
	if i.Spec.TLS[0].SecretName != "tls-secret" {
		t.Errorf("unexpected TLS secret: %s", i.Spec.TLS[0].SecretName)
	}
	host := i.Spec.Rules[0].Host
	expected := "a3f9b2c1.exam.otu.ca"
	if host != expected {
		t.Errorf("expected host %q, got %q", expected, host)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/provisioner/ -v`
Expected: FAIL — functions not defined.

**Step 3: Write the implementation**

```go
package provisioner

import (
	"fmt"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
)

func labels(examName, studentID, slug string) map[string]string {
	return map[string]string{
		"exam.otu.ca/exam":    examName,
		"exam.otu.ca/student": studentID,
		"exam.otu.ca/slug":    slug,
	}
}

// Deployment creates a Deployment for a student's exam instance.
func Deployment(exam *examv1alpha1.Exam, namespace, studentID, slug string) *appsv1.Deployment {
	l := labels(exam.Name, studentID, slug)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      slug,
			Namespace: namespace,
			Labels:    l,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To(int32(1)),
			Selector: &metav1.LabelSelector{MatchLabels: l},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: l},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: ptr.To(false),
					Containers: []corev1.Container{
						{
							Name:      "app",
							Image:     exam.Spec.Template.Image,
							Ports:     []corev1.ContainerPort{{ContainerPort: exam.Spec.Template.Port}},
							Resources: exam.Spec.Template.Resources,
							SecurityContext: &corev1.SecurityContext{
								RunAsNonRoot:             ptr.To(true),
								AllowPrivilegeEscalation: ptr.To(false),
								Capabilities: &corev1.Capabilities{
									Drop: []corev1.Capability{"ALL"},
								},
								SeccompProfile: &corev1.SeccompProfile{
									Type: corev1.SeccompProfileTypeRuntimeDefault,
								},
							},
						},
					},
				},
			},
		},
	}
}

// Service creates a ClusterIP Service for a student's exam instance.
func Service(exam *examv1alpha1.Exam, namespace, studentID, slug string) *corev1.Service {
	l := labels(exam.Name, studentID, slug)
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      slug,
			Namespace: namespace,
			Labels:    l,
		},
		Spec: corev1.ServiceSpec{
			Selector: l,
			Ports: []corev1.ServicePort{
				{
					Port:       exam.Spec.Template.Port,
					TargetPort: intstr.FromInt32(exam.Spec.Template.Port),
				},
			},
		},
	}
}

// Ingress creates an Ingress for a student's exam instance.
func Ingress(exam *examv1alpha1.Exam, namespace, studentID, slug string) *networkingv1.Ingress {
	l := labels(exam.Name, studentID, slug)
	host := fmt.Sprintf("%s.%s", slug, exam.Spec.Domain)
	pathType := networkingv1.PathTypePrefix
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      slug,
			Namespace: namespace,
			Labels:    l,
		},
		Spec: networkingv1.IngressSpec{
			TLS: []networkingv1.IngressTLS{
				{
					Hosts:      []string{host},
					SecretName: exam.Spec.IngressTLS.SecretName,
				},
			},
			Rules: []networkingv1.IngressRule{
				{
					Host: host,
					IngressRuleValue: networkingv1.IngressRuleValue{
						HTTP: &networkingv1.HTTPIngressRuleValue{
							Paths: []networkingv1.HTTPIngressPath{
								{
									Path:     "/",
									PathType: &pathType,
									Backend: networkingv1.IngressBackend{
										Service: &networkingv1.IngressServiceBackend{
											Name: slug,
											Port: networkingv1.ServiceBackendPort{
												Number: exam.Spec.Template.Port,
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/provisioner/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/provisioner/
git commit -m "feat: add per-student Deployment/Service/Ingress builder"
```

---

### Task 6: Email Notifier

**Files:**
- Create: `internal/notifier/email.go`
- Create: `internal/notifier/email_test.go`

**Step 1: Write the failing tests**

```go
package notifier

import (
	"strings"
	"testing"
)

func TestBuildMessage(t *testing.T) {
	msg := BuildMessage("noreply@otu.ca", "student@ontariotechu.net", "Your Exam Instance", "https://a3f9b2c1.exam.otu.ca")
	if !strings.Contains(msg, "From: noreply@otu.ca") {
		t.Error("missing From header")
	}
	if !strings.Contains(msg, "To: student@ontariotechu.net") {
		t.Error("missing To header")
	}
	if !strings.Contains(msg, "https://a3f9b2c1.exam.otu.ca") {
		t.Error("missing URL in body")
	}
}

func TestSender_Interface(t *testing.T) {
	// Verify the Sender interface is implementable
	var _ Sender = &SMTPSender{}
	var _ Sender = &FakeSender{}
}

func TestFakeSender_Records(t *testing.T) {
	f := &FakeSender{}
	err := f.Send("noreply@otu.ca", []string{"student@test.com"}, []byte("test"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Sent) != 1 {
		t.Fatalf("expected 1 sent message, got %d", len(f.Sent))
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/notifier/ -v`
Expected: FAIL — types not defined.

**Step 3: Write the implementation**

```go
package notifier

import (
	"fmt"
	"net/smtp"
)

// Sender is the interface for sending email.
type Sender interface {
	Send(from string, to []string, msg []byte) error
}

// SMTPSender sends email via SMTP.
type SMTPSender struct {
	Host     string
	Port     int
	Username string
	Password string
}

func (s *SMTPSender) Send(from string, to []string, msg []byte) error {
	addr := fmt.Sprintf("%s:%d", s.Host, s.Port)
	auth := smtp.PlainAuth("", s.Username, s.Password, s.Host)
	return smtp.SendMail(addr, auth, from, to, msg)
}

// FakeSender records sent messages for testing.
type FakeSender struct {
	Sent []SentMessage
}

type SentMessage struct {
	From string
	To   []string
	Body []byte
}

func (f *FakeSender) Send(from string, to []string, msg []byte) error {
	f.Sent = append(f.Sent, SentMessage{From: from, To: to, Body: msg})
	return nil
}

// BuildMessage constructs an email message with the exam URL.
func BuildMessage(from, to, subject, url string) string {
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\nYour exam instance is ready.\r\n\r\nAccess it at: %s\r\n",
		from, to, subject, url)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/notifier/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/notifier/
git commit -m "feat: add email notifier with Sender interface"
```

---

### Task 7: Smoke Test Runner

**Files:**
- Create: `internal/smoketest/runner.go`
- Create: `internal/smoketest/runner_test.go`

**Step 1: Write the failing tests**

```go
package smoketest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckHealth_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	err := CheckHealth(context.Background(), srv.URL)
	if err != nil {
		t.Errorf("expected success, got: %v", err)
	}
}

func TestCheckHealth_Failure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	err := CheckHealth(context.Background(), srv.URL)
	if err == nil {
		t.Error("expected error for 503 response")
	}
}

func TestCheckHealth_Unreachable(t *testing.T) {
	err := CheckHealth(context.Background(), "http://127.0.0.1:1")
	if err == nil {
		t.Error("expected error for unreachable host")
	}
}

func TestRunAll(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	targets := []Target{
		{StudentID: "alice", URL: srv.URL},
		{StudentID: "bob", URL: "http://127.0.0.1:1"},
	}

	result := RunAll(context.Background(), targets)
	if result.Passed != 1 {
		t.Errorf("expected 1 passed, got %d", result.Passed)
	}
	if result.Failed != 1 {
		t.Errorf("expected 1 failed, got %d", result.Failed)
	}
	if len(result.Failures) != 1 || result.Failures[0].Student != "bob" {
		t.Errorf("expected bob in failures, got %+v", result.Failures)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/smoketest/ -v`
Expected: FAIL — types not defined.

**Step 3: Write the implementation**

```go
package smoketest

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// Target is a student instance to health-check.
type Target struct {
	StudentID string
	URL       string
}

// Failure records a failed smoke test.
type Failure struct {
	Student string
	Error   string
}

// Result summarizes a smoke test run.
type Result struct {
	Passed   int
	Failed   int
	Failures []Failure
}

// CheckHealth performs an HTTP GET and returns an error if the response is not 2xx.
func CheckHealth(ctx context.Context, url string) error {
	client := &http.Client{Timeout: 5 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// RunAll checks all targets and returns aggregated results.
func RunAll(ctx context.Context, targets []Target) Result {
	var r Result
	for _, t := range targets {
		if err := CheckHealth(ctx, t.URL); err != nil {
			r.Failed++
			r.Failures = append(r.Failures, Failure{Student: t.StudentID, Error: err.Error()})
		} else {
			r.Passed++
		}
	}
	return r
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/smoketest/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/smoketest/
git commit -m "feat: add smoke test health check runner"
```

---

### Task 8: Validating Webhook

**Files:**
- Create: `internal/webhook/exam_webhook.go`
- Create: `internal/webhook/exam_webhook_test.go`

**Step 1: Write the failing tests**

```go
package webhook

import (
	"testing"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	corev1 "k8s.io/api/core/v1"
)

func examWithPhase(phase examv1alpha1.ExamPhase) (*examv1alpha1.Exam, *examv1alpha1.Exam) {
	base := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Template: examv1alpha1.ExamTemplate{Image: "app:v1", Port: 8080},
			Schedule: examv1alpha1.ExamSchedule{
				Unlock: metav1.Now(),
				Lock:   metav1.Now(),
			},
			Students: []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			},
		},
		Status: examv1alpha1.ExamStatus{Phase: phase},
	}
	updated := base.DeepCopy()
	return base, updated
}

func TestAllowImageChangeWhenPending(t *testing.T) {
	old, new := examWithPhase(examv1alpha1.ExamPhasePending)
	new.Spec.Template.Image = "app:v2"
	if err := ValidateUpdate(old, new); err != nil {
		t.Errorf("should allow image change when Pending: %v", err)
	}
}

func TestRejectImageChangeWhenProvisioning(t *testing.T) {
	old, new := examWithPhase(examv1alpha1.ExamPhaseProvisioning)
	new.Spec.Template.Image = "app:v2"
	if err := ValidateUpdate(old, new); err == nil {
		t.Error("should reject image change when Provisioning")
	}
}

func TestRejectStudentIDChangeWhenReady(t *testing.T) {
	old, new := examWithPhase(examv1alpha1.ExamPhaseReady)
	new.Spec.Students[0].ID = "bob"
	if err := ValidateUpdate(old, new); err == nil {
		t.Error("should reject student ID change when Ready")
	}
}

func TestAllowLockTimeChangeWhenUnlocked(t *testing.T) {
	old, new := examWithPhase(examv1alpha1.ExamPhaseUnlocked)
	new.Spec.Schedule.Lock = metav1.Now()
	if err := ValidateUpdate(old, new); err != nil {
		t.Errorf("should allow lock time change when Unlocked: %v", err)
	}
}

func TestAllowEmailChangeWhenReady(t *testing.T) {
	old, new := examWithPhase(examv1alpha1.ExamPhaseReady)
	new.Spec.Students[0].Email = "newemail@test.com"
	if err := ValidateUpdate(old, new); err != nil {
		t.Errorf("should allow email change: %v", err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/webhook/ -v`
Expected: FAIL — `ValidateUpdate` not defined.

**Step 3: Write the implementation**

```go
package webhook

import (
	"fmt"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
)

// ValidateUpdate checks immutability constraints based on the exam's current phase.
func ValidateUpdate(old, new *examv1alpha1.Exam) error {
	if old.Status.Phase == examv1alpha1.ExamPhasePending || old.Status.Phase == "" {
		return nil // all changes allowed when Pending
	}

	// Template is immutable after Pending
	if old.Spec.Template.Image != new.Spec.Template.Image {
		return fmt.Errorf("spec.template.image is immutable after provisioning (current phase: %s)", old.Status.Phase)
	}
	if old.Spec.Template.Port != new.Spec.Template.Port {
		return fmt.Errorf("spec.template.port is immutable after provisioning (current phase: %s)", old.Status.Phase)
	}

	// Student IDs are immutable after Pending
	if len(old.Spec.Students) != len(new.Spec.Students) {
		return fmt.Errorf("spec.students list length is immutable after provisioning")
	}
	for i := range old.Spec.Students {
		if old.Spec.Students[i].ID != new.Spec.Students[i].ID {
			return fmt.Errorf("spec.students[%d].id is immutable after provisioning", i)
		}
	}

	// Unlock time is immutable after Pending
	if !old.Spec.Schedule.Unlock.Equal(&new.Spec.Schedule.Unlock) {
		return fmt.Errorf("spec.schedule.unlock is immutable after provisioning (current phase: %s)", old.Status.Phase)
	}

	return nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/webhook/ -v`
Expected: PASS.

**Step 5: Commit**

```bash
git add internal/webhook/
git commit -m "feat: add validating webhook for spec immutability"
```

---

### Task 9: Controller Reconciler — State Machine Core

**Files:**
- Modify: `internal/controller/exam_controller.go`
- Create: `internal/controller/exam_controller_test.go`

This is the largest task. The reconciler ties everything together with the time-driven state machine.

**Step 1: Write the failing test for phase transitions**

```go
package controller

import (
	"context"
	"testing"
	"time"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDetermineDesiredPhase_Pending(t *testing.T) {
	exam := &examv1alpha1.Exam{
		Status: examv1alpha1.ExamStatus{Phase: ""},
	}
	phase := determineDesiredPhase(exam, time.Now())
	if phase != examv1alpha1.ExamPhaseProvisioning {
		t.Errorf("expected Provisioning, got %s", phase)
	}
}

func TestDetermineDesiredPhase_ReadyWaitingForDryRun(t *testing.T) {
	now := time.Now()
	unlock := metav1.NewTime(now.Add(1 * time.Hour))
	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock: unlock,
				Lock:   metav1.NewTime(now.Add(3 * time.Hour)),
				DryRun: &examv1alpha1.ExamDryRunSpec{
					Before:   metav1.Duration{Duration: 15 * time.Minute},
					Duration: metav1.Duration{Duration: 5 * time.Minute},
				},
			},
		},
		Status: examv1alpha1.ExamStatus{Phase: examv1alpha1.ExamPhaseReady},
	}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseReady {
		t.Errorf("expected Ready (not yet dry run time), got %s", phase)
	}
}

func TestDetermineDesiredPhase_DryRunTime(t *testing.T) {
	now := time.Now()
	unlock := metav1.NewTime(now.Add(10 * time.Minute))
	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock: unlock,
				Lock:   metav1.NewTime(now.Add(2 * time.Hour)),
				DryRun: &examv1alpha1.ExamDryRunSpec{
					Before:   metav1.Duration{Duration: 15 * time.Minute},
					Duration: metav1.Duration{Duration: 5 * time.Minute},
				},
			},
		},
		Status: examv1alpha1.ExamStatus{Phase: examv1alpha1.ExamPhaseReady},
	}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseDryRun {
		t.Errorf("expected DryRun, got %s", phase)
	}
}

func TestDetermineDesiredPhase_Unlocked(t *testing.T) {
	now := time.Now()
	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock: metav1.NewTime(now.Add(-30 * time.Minute)),
				Lock:   metav1.NewTime(now.Add(90 * time.Minute)),
			},
		},
		Status: examv1alpha1.ExamStatus{Phase: examv1alpha1.ExamPhaseVerified},
	}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseUnlocked {
		t.Errorf("expected Unlocked, got %s", phase)
	}
}

func TestDetermineDesiredPhase_Locking(t *testing.T) {
	now := time.Now()
	pastLock := metav1.NewTime(now.Add(-10 * time.Minute))
	futureLock := metav1.NewTime(now.Add(30 * time.Minute))
	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock: metav1.NewTime(now.Add(-2 * time.Hour)),
				Lock:   pastLock,
			},
			Students: []examv1alpha1.ExamStudent{
				{ID: "alice"},
				{ID: "bob", LockOverride: &futureLock},
			},
		},
		Status: examv1alpha1.ExamStatus{Phase: examv1alpha1.ExamPhaseUnlocked},
	}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseLocking {
		t.Errorf("expected Locking, got %s", phase)
	}
}

func TestDetermineDesiredPhase_Locked(t *testing.T) {
	now := time.Now()
	exam := &examv1alpha1.Exam{
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock: metav1.NewTime(now.Add(-3 * time.Hour)),
				Lock:   metav1.NewTime(now.Add(-1 * time.Hour)),
			},
			Students: []examv1alpha1.ExamStudent{
				{ID: "alice"},
			},
		},
		Status: examv1alpha1.ExamStatus{Phase: examv1alpha1.ExamPhaseLocking},
	}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseLocked {
		t.Errorf("expected Locked, got %s", phase)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/controller/ -v -run TestDetermine`
Expected: FAIL — `determineDesiredPhase` not defined.

**Step 3: Write the state machine implementation**

Replace the scaffolded `exam_controller.go` with:

```go
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
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// ExamReconciler reconciles an Exam object.
type ExamReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	Sender notifier.Sender
	Now    func() time.Time // injectable clock for testing
}

//+kubebuilder:rbac:groups=exam.otu.ca,resources=exams,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=exam.otu.ca,resources=exams/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=exam.otu.ca,resources=exams/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=services;namespaces,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses;networkpolicies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch

func (r *ExamReconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *ExamReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := log.FromContext(ctx)

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
		requeueAfter = exam.Spec.Schedule.Unlock.Time.Sub(now)
	case examv1alpha1.ExamPhaseUnlocked:
		err = r.reconcileUnlock(ctx, &exam, now)
		requeueAfter = r.requeueForLock(&exam, now)
	case examv1alpha1.ExamPhaseLocking:
		err = r.reconcileLocking(ctx, &exam, now)
		requeueAfter = r.requeueForNextLock(&exam, now)
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
	latestLock := latestLockTime(exam)

	// All students past their lock time?
	if current == examv1alpha1.ExamPhaseLocking || current == examv1alpha1.ExamPhaseUnlocked {
		if now.After(latestLock) || now.Equal(latestLock) {
			return examv1alpha1.ExamPhaseLocked
		}
	}

	// Some students past lock, some not?
	if current == examv1alpha1.ExamPhaseUnlocked || current == examv1alpha1.ExamPhaseLocking {
		earliest := earliestLockTime(exam)
		if now.After(earliest) || now.Equal(earliest) {
			if now.Before(latestLock) {
				return examv1alpha1.ExamPhaseLocking
			}
		}
	}

	// Past unlock time?
	if now.After(unlockTime) || now.Equal(unlockTime) {
		if current == examv1alpha1.ExamPhaseVerified || current == examv1alpha1.ExamPhaseReady {
			return examv1alpha1.ExamPhaseUnlocked
		}
	}

	// Dry run window?
	if current == examv1alpha1.ExamPhaseReady && schedule.DryRun != nil {
		dryRunStart := unlockTime.Add(-schedule.DryRun.Before.Duration)
		if now.After(dryRunStart) || now.Equal(dryRunStart) {
			return examv1alpha1.ExamPhaseDryRun
		}
	}

	// Dry run completed?
	if current == examv1alpha1.ExamPhaseDryRun {
		if exam.Status.DryRun != nil && exam.Status.DryRun.CompletedAt != nil {
			return examv1alpha1.ExamPhaseVerified
		}
	}

	// Provisioning complete?
	if current == examv1alpha1.ExamPhaseProvisioning {
		allHealthy := true
		for _, s := range exam.Status.Students {
			if s.Phase != examv1alpha1.StudentPhaseHealthy && s.Phase != examv1alpha1.StudentPhaseProvisioned {
				allHealthy = false
				break
			}
		}
		if allHealthy && len(exam.Status.Students) == len(exam.Spec.Students) {
			return examv1alpha1.ExamPhaseReady
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

// Placeholder reconcile methods — implemented in subsequent tasks or filled in during integration.

func (r *ExamReconciler) reconcileProvisioning(ctx context.Context, exam *examv1alpha1.Exam) error {
	ns := examNamespace(exam)

	// Ensure namespace exists
	namespace := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
			Labels: map[string]string{
				"exam.otu.ca/exam":                          exam.Name,
				"pod-security.kubernetes.io/enforce":        "baseline",
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
		controllerutil.SetOwnerReference(exam, dep, r.Scheme)
		if err := r.Client.Create(ctx, dep); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}

		// Create Service
		svc := provisioner.Service(exam, ns, student.ID, studentSlug)
		controllerutil.SetOwnerReference(exam, svc, r.Scheme)
		if err := r.Client.Create(ctx, svc); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}

		// Create Ingress
		ing := provisioner.Ingress(exam, ns, student.ID, studentSlug)
		controllerutil.SetOwnerReference(exam, ing, r.Scheme)
		if err := r.Client.Create(ctx, ing); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}

		// Create deny-all and egress-allow policies
		denyAll := network.DenyAllPolicy(ns, exam.Name, student.ID)
		controllerutil.SetOwnerReference(exam, denyAll, r.Scheme)
		if err := r.Client.Create(ctx, denyAll); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}

		egressAllow := network.EgressAllowlistPolicy(ns, exam.Name, student.ID, "kube-system")
		controllerutil.SetOwnerReference(exam, egressAllow, r.Scheme)
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
		// Test against ClusterIP service internally
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
			controllerutil.SetOwnerReference(exam, ingressAllow, r.Scheme)
			if err := r.Client.Create(ctx, ingressAllow); err != nil && !apierrors.IsAlreadyExists(err) {
				return err
			}

			if i < len(exam.Status.Students) {
				exam.Status.Students[i].Phase = examv1alpha1.StudentPhaseUnlocked
			}
		}
	}

	// Send emails if not yet sent (30 min before unlock window)
	r.sendEmailsIfNeeded(ctx, exam)

	return nil
}

func (r *ExamReconciler) sendEmailsIfNeeded(ctx context.Context, exam *examv1alpha1.Exam) {
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
		if now.After(lt) || now.Equal(lt) {
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
			slug := exam.Status.Students[i].Slug
			ingress := &networkingv1.Ingress{
				ObjectMeta: metav1.ObjectMeta{
					Name:      slug,
					Namespace: ns,
				},
			}
			if err := r.Client.Delete(ctx, ingress); err != nil && !apierrors.IsNotFound(err) {
				return err
			}

			if i < len(exam.Status.Students) {
				exam.Status.Students[i].Phase = examv1alpha1.StudentPhaseLocked
				now := metav1.Now()
				exam.Status.Students[i].LockedAt = &now
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
		return exam.Spec.Schedule.Unlock.Time.Sub(now)
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

func (r *ExamReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&examv1alpha1.Exam{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Complete(r)
}
```

**Step 4: Run the phase determination tests**

Run: `go test ./internal/controller/ -v -run TestDetermine`
Expected: PASS.

**Step 5: Run full build**

Run: `go build ./...`
Expected: Clean build.

**Step 6: Commit**

```bash
git add internal/controller/
git commit -m "feat: implement controller reconciler with time-driven state machine"
```

---

### Task 10: Integration Test with envtest

**Files:**
- Create: `internal/controller/suite_test.go`
- Modify: `internal/controller/exam_controller_test.go` (add integration tests)

**Step 1: Set up the envtest suite**

```go
package controller

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
)

var (
	testEnv   *envtest.Environment
	k8sClient client.Client
	cfg       *rest.Config
	ctx       context.Context
	cancel    context.CancelFunc
)

func TestMain(m *testing.M) {
	ctx, cancel = context.WithCancel(context.Background())
	defer cancel()

	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{filepath.Join("..", "..", "config", "crd", "bases")},
	}

	var err error
	cfg, err = testEnv.Start()
	if err != nil {
		panic(err)
	}
	defer testEnv.Stop()

	err = examv1alpha1.AddToScheme(scheme.Scheme)
	if err != nil {
		panic(err)
	}

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	if err != nil {
		panic(err)
	}

	m.Run()
}
```

**Step 2: Add an integration test that creates an Exam and verifies provisioning**

Add to `exam_controller_test.go`:

```go
func TestReconcile_ProvisioningCreatesResources(t *testing.T) {
	// This test creates an Exam resource and verifies the reconciler
	// transitions it from Pending to Provisioning, creating student resources.
	// Full integration test requires envtest — run with:
	// KUBEBUILDER_ASSETS="$(setup-envtest use -p path)" go test ./internal/controller/ -v -run TestReconcile
	t.Skip("requires envtest setup — enable after Task 1 installs kubebuilder")
}
```

**Step 3: Verify unit tests still pass**

Run: `go test ./... -v -short`
Expected: All unit tests PASS, integration tests skipped.

**Step 4: Commit**

```bash
git add internal/controller/suite_test.go internal/controller/exam_controller_test.go
git commit -m "feat: add envtest suite scaffold and integration test placeholder"
```

---

### Task 11: Wire Webhook into Manager

**Files:**
- Modify: `cmd/main.go` — register webhook with manager
- Create: `config/webhook/` manifests

**Step 1: Add webhook registration to main.go**

Add the validating webhook handler to the manager setup. Kubebuilder's `webhook.CustomValidator` interface wires `ValidateUpdate` to the admission endpoint.

**Step 2: Generate webhook config**

Run: `make manifests`

**Step 3: Verify build**

Run: `go build ./...`
Expected: Clean build.

**Step 4: Commit**

```bash
git add cmd/main.go config/webhook/ internal/webhook/
git commit -m "feat: wire validating webhook into controller manager"
```

---

### Task 12: Makefile, Dockerfile, Sample Manifest

**Files:**
- Modify: `Makefile` (should already be scaffolded, verify targets)
- Modify: `Dockerfile` (should already be scaffolded)
- Create: `config/samples/exam_v1alpha1_exam.yaml`

**Step 1: Create a sample Exam manifest**

```yaml
apiVersion: exam.otu.ca/v1alpha1
kind: Exam
metadata:
  name: sofe4790u-midterm
spec:
  template:
    image: registry.example.com/vuln-app:v2.1
    resources:
      requests:
        cpu: "250m"
        memory: "256Mi"
      limits:
        cpu: "500m"
        memory: "512Mi"
    port: 8080
  schedule:
    unlock: "2026-04-10T14:00:00-04:00"
    lock: "2026-04-10T16:00:00-04:00"
    dryRun:
      before: "15m"
      duration: "5m"
  students:
    - id: john.smith
      email: john.smith@ontariotechu.net
    - id: jane.doe
      email: jane.doe@ontariotechu.net
      lockOverride: "2026-04-10T17:00:00-04:00"
  ingressTLS:
    secretName: exam-wildcard-tls
  domain: exam.otu.ca
  smtp:
    secretRef: exam-smtp-credentials
    from: "noreply@otu.ca"
    subject: "SOFE4790U - Your Exam Instance"
```

**Step 2: Verify full build and manifest generation**

Run: `make generate && make manifests && make build`
Expected: Clean build, CRD YAML in `config/crd/bases/`.

**Step 3: Run all tests**

Run: `go test ./... -v -short`
Expected: All PASS.

**Step 4: Commit**

```bash
git add config/samples/ Makefile Dockerfile
git commit -m "chore: add sample manifest and verify build pipeline"
```

---

## Task Dependency Graph

```
Task 1 (scaffold)
  └── Task 2 (CRD types)
        ├── Task 3 (slug) ─────────────────┐
        ├── Task 4 (network policies) ──────┤
        ├── Task 5 (provisioner) ───────────┤
        ├── Task 6 (email notifier) ────────┤
        ├── Task 7 (smoke tests) ───────────┤
        └── Task 8 (webhook) ──────────────┤
                                            │
                                    Task 9 (controller) ← depends on all above
                                            │
                                    Task 10 (integration tests)
                                            │
                                    Task 11 (wire webhook)
                                            │
                                    Task 12 (build pipeline + sample)
```

Tasks 3–8 are independent of each other and can be implemented in parallel.
