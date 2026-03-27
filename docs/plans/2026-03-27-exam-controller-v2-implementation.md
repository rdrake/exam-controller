# Exam Controller v2 Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Rewrite the exam controller to match the v2 design — 6-phase state machine, global time multiplier, spare instances, CiliumNetworkPolicy with vanilla fallback, Prometheus metrics, instructor notifications, and auto-teardown.

**Architecture:** Kubernetes CRD controller using controller-runtime. The Exam CR defines per-student pen-testing instances with time-driven lifecycle. A `PolicyProvider` interface abstracts network policy backends. Finalizer-based cleanup replaces cross-namespace owner references. Status conditions gate substeps (email, dry run) to survive restarts.

**Tech Stack:** Go 1.25.3, Kubebuilder 4.13.1, controller-runtime v0.23.3, Ginkgo/Gomega, Prometheus client_golang

---

## Dependency Graph

```
Task 1 (Types) ──┬──> Task 2 (Slug fix)
                 ├──> Task 3 (PolicyProvider + Vanilla) ──> Task 4 (CiliumPolicyProvider)
                 ├──> Task 5 (Provisioner)
                 ├──> Task 6 (Notifier)
                 ├──> Task 7 (Smoketest)
                 ├──> Task 8 (Metrics)
                 └──> Task 9 (Webhook)

Tasks 2-9 ──────> Task 10 (Controller)
Task 10 ────────> Task 11 (cmd/main.go)
{Task 9, 10} ──> Task 12 (Config/Samples — needs webhook + RBAC markers)
```

After Task 1, Tasks 2, 3, 5, 6, 7, 8, and 9 can run in parallel. Task 4 depends on Task 3 (uses `PolicyProvider` interface and `slugFromLabels`). Task 12 depends on Tasks 9 and 10 (webhook manifests and RBAC come from their markers).

---

### Task 1: Rewrite CRD Types

**Files:**
- Rewrite: `api/v1alpha1/exam_types.go`
- Regenerate: `api/v1alpha1/zz_generated.deepcopy.go`

**Step 1: Rewrite types**

Replace the entire file. Key changes from v1:
- 6 phases (remove DryRun, Verified, Locking)
- `ExamSchedule` uses `Duration` + `TimeMultiplier` instead of `Lock`
- Add `ProvisionBefore`, `Retention` to schedule
- `ExamEmail` replaces `ExamSMTP` with `Before`, `RateLimit`, `InstructorEmail`
- Add `Spares` to spec
- Remove `LockOverride` from `ExamStudent`
- Status: add computed times, spare status, metrics summary, `NetworkPolicyEnforced` condition
- Print columns reference `status.computedLockTime` instead of `spec.schedule.lock`

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
	ExamPhaseUnlocked     ExamPhase = "Unlocked"
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

// ExamDryRunSpec configures the pre-exam smoke test window.
type ExamDryRunSpec struct {
	Before   metav1.Duration `json:"before"`
	Duration metav1.Duration `json:"duration"`
}

// ExamSchedule defines the timing for the exam lifecycle.
type ExamSchedule struct {
	Unlock metav1.Time     `json:"unlock"`
	Duration        metav1.Duration `json:"duration"`
	// +kubebuilder:default="1.5"
	TimeMultiplier  float64         `json:"timeMultiplier,omitempty"`
	// +kubebuilder:default="1h"
	ProvisionBefore metav1.Duration `json:"provisionBefore,omitempty"`
	// +kubebuilder:default="24h"
	Retention       metav1.Duration `json:"retention,omitempty"`
	DryRun          *ExamDryRunSpec `json:"dryRun,omitempty"`
}

// ExamStudent defines a student participating in the exam.
type ExamStudent struct {
	ID    string `json:"id"`
	Email string `json:"email"`
}

// ExamEmail configures email delivery.
type ExamEmail struct {
	// +kubebuilder:default="30m"
	Before          metav1.Duration `json:"before,omitempty"`
	// +kubebuilder:default=1
	RateLimit       int             `json:"rateLimit,omitempty"`
	InstructorEmail string          `json:"instructorEmail"`
	SecretRef       string          `json:"secretRef"`
	From            string          `json:"from"`
	Subject         string          `json:"subject"`
}

// ExamIngressTLS configures TLS for student Ingress resources.
type ExamIngressTLS struct {
	SecretName string `json:"secretName"`
}

// ExamSpec defines the desired state of Exam.
type ExamSpec struct {
	Template   ExamTemplate   `json:"template"`
	Schedule   ExamSchedule   `json:"schedule"`
	Students   []ExamStudent  `json:"students"`
	Email      ExamEmail      `json:"email"`
	Spares     int            `json:"spares,omitempty"`
	IngressTLS ExamIngressTLS `json:"ingressTLS"`
	Domain     string         `json:"domain"`
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

// SpareStatus records the state of a spare instance.
type SpareStatus struct {
	Slug  string       `json:"slug"`
	URL   string       `json:"url,omitempty"`
	Phase StudentPhase `json:"phase"`
}

// StudentStatus records the state of a single student's resources.
type StudentStatus struct {
	ID          string       `json:"id"`
	Slug        string       `json:"slug,omitempty"`
	URL         string       `json:"url,omitempty"`
	Phase       StudentPhase `json:"phase"`
	EmailStatus EmailStatus  `json:"emailStatus"`
	EmailSentAt *metav1.Time `json:"emailSentAt,omitempty"`
}

// MetricsSummary provides quick counts for dashboard queries.
type MetricsSummary struct {
	TotalStudents    int `json:"totalStudents"`
	TotalSpares      int `json:"totalSpares"`
	EmailsSent       int `json:"emailsSent"`
	EmailsFailed     int `json:"emailsFailed"`
	InstancesHealthy int `json:"instancesHealthy"`
	InstancesFailed  int `json:"instancesFailed"`
}

// ExamStatus defines the observed state of Exam.
type ExamStatus struct {
	Phase             ExamPhase          `json:"phase,omitempty"`
	Message           string             `json:"message,omitempty"`
	Conditions        []metav1.Condition `json:"conditions,omitempty"`
	ComputedLockTime  *metav1.Time       `json:"computedLockTime,omitempty"`
	ProvisionTime     *metav1.Time       `json:"provisionTime,omitempty"`
	EmailTime         *metav1.Time       `json:"emailTime,omitempty"`
	RetentionDeadline *metav1.Time       `json:"retentionDeadline,omitempty"`
	DryRun            *DryRunStatus      `json:"dryRun,omitempty"`
	Students          []StudentStatus    `json:"students,omitempty"`
	Spares            []SpareStatus      `json:"spares,omitempty"`
	Metrics           *MetricsSummary    `json:"metrics,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
//+kubebuilder:printcolumn:name="Unlock",type=string,JSONPath=`.spec.schedule.unlock`
//+kubebuilder:printcolumn:name="Lock",type=string,JSONPath=`.status.computedLockTime`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Exam is the Schema for the exams API
type Exam struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ExamSpec   `json:"spec,omitempty"`
	Status ExamStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ExamList contains a list of Exam
type ExamList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Exam `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Exam{}, &ExamList{})
}
```

**Step 2: Regenerate deepcopy**

Run: `make generate`
Expected: `zz_generated.deepcopy.go` regenerated with new types

**Step 3: Regenerate CRD manifests**

Run: `make manifests`
Expected: `config/crd/bases/exam.otu.ca_exams.yaml` updated

**Step 4: Verify build compiles (expect failures in downstream packages)**

Run: `go build ./api/...`
Expected: PASS (types package compiles independently)

**Step 5: Commit**

```bash
git add api/ config/crd/
git commit -m "feat: rewrite CRD types for v2 schema"
```

---

### Task 2: Fix Slug Error Propagation

**Files:**
- Modify: `internal/slug/slug.go` (no changes needed — already returns error)
- Test: `internal/slug/slug_test.go` (already tests error case — verify)

**Step 1: Verify existing slug tests pass**

Run: `go test ./internal/slug/ -v`
Expected: PASS (slug package is unchanged in v2, but controller must propagate errors)

**Step 2: Commit (no-op if no changes)**

The slug package is already correct. The error propagation fix is in Task 10 (controller).

---

### Task 3: PolicyProvider Interface + VanillaPolicyProvider

**Files:**
- Rewrite: `internal/network/policy.go`
- Rewrite: `internal/network/policy_test.go`

**Step 1: Write the failing tests**

```go
package network

import (
	"testing"

	networkingv1 "k8s.io/api/networking/v1"
)

func TestVanillaDenyAll(t *testing.T) {
	p := &VanillaPolicyProvider{}
	obj := p.DenyAll("exam-ns", map[string]string{"exam.otu.ca/slug": "abc123"})
	np, ok := obj.(*networkingv1.NetworkPolicy)
	if !ok {
		t.Fatal("expected *NetworkPolicy")
	}
	if np.Namespace != "exam-ns" {
		t.Errorf("namespace = %q, want %q", np.Namespace, "exam-ns")
	}
	if np.Name != "abc123-deny-all" {
		t.Errorf("name = %q, want %q", np.Name, "abc123-deny-all")
	}
	if len(np.Spec.PolicyTypes) != 2 {
		t.Fatalf("policyTypes len = %d, want 2", len(np.Spec.PolicyTypes))
	}
}

func TestVanillaEgressAllowlist(t *testing.T) {
	p := &VanillaPolicyProvider{}
	obj := p.EgressAllowlist("exam-ns", map[string]string{"exam.otu.ca/slug": "abc123"})
	np := obj.(*networkingv1.NetworkPolicy)
	if np.Name != "abc123-egress-allow" {
		t.Errorf("name = %q, want %q", np.Name, "abc123-egress-allow")
	}
	// Should have egress rules for DNS (port 53 UDP+TCP) to CoreDNS pods
	if len(np.Spec.Egress) != 1 {
		t.Fatalf("egress rules = %d, want 1", len(np.Spec.Egress))
	}
	rule := np.Spec.Egress[0]
	if len(rule.Ports) != 2 {
		t.Errorf("egress ports = %d, want 2 (UDP+TCP)", len(rule.Ports))
	}
	// Should target CoreDNS pods specifically
	if len(rule.To) != 1 {
		t.Fatalf("egress to = %d, want 1", len(rule.To))
	}
	if rule.To[0].PodSelector == nil {
		t.Error("expected podSelector targeting CoreDNS")
	}
}

func TestVanillaIngressAllow(t *testing.T) {
	p := &VanillaPolicyProvider{}
	obj := p.IngressAllow("exam-ns", map[string]string{"exam.otu.ca/slug": "abc123"})
	np := obj.(*networkingv1.NetworkPolicy)
	if np.Name != "abc123-ingress-allow" {
		t.Errorf("name = %q, want %q", np.Name, "abc123-ingress-allow")
	}
	if len(np.Spec.Ingress) != 1 {
		t.Fatalf("ingress rules = %d, want 1", len(np.Spec.Ingress))
	}
	from := np.Spec.Ingress[0].From
	if len(from) != 1 {
		t.Fatalf("from = %d, want 1", len(from))
	}
	if from[0].NamespaceSelector == nil || from[0].PodSelector == nil {
		t.Error("expected both namespaceSelector and podSelector")
	}
}

func TestPolicyProviderInterface(t *testing.T) {
	// Compile-time check that VanillaPolicyProvider implements PolicyProvider
	var _ PolicyProvider = &VanillaPolicyProvider{}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/network/ -v`
Expected: FAIL — `PolicyProvider` and `VanillaPolicyProvider` not defined

**Step 3: Write implementation**

```go
package network

import (
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// PolicyProvider abstracts network policy creation for different backends.
type PolicyProvider interface {
	DenyAll(namespace string, labels map[string]string) client.Object
	EgressAllowlist(namespace string, labels map[string]string) client.Object
	IngressAllow(namespace string, labels map[string]string) client.Object
}

// VanillaPolicyProvider creates standard Kubernetes NetworkPolicy resources.
type VanillaPolicyProvider struct{}

func slugFromLabels(labels map[string]string) string {
	return labels["exam.otu.ca/slug"]
}

func (v *VanillaPolicyProvider) DenyAll(namespace string, labels map[string]string) client.Object {
	slug := slugFromLabels(labels)
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      slug + "-deny-all",
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"exam.otu.ca/slug": slug},
			},
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
		},
	}
}

func (v *VanillaPolicyProvider) EgressAllowlist(namespace string, labels map[string]string) client.Object {
	slug := slugFromLabels(labels)
	dns := intstr.FromInt32(53)
	udp := corev1.ProtocolUDP
	tcp := corev1.ProtocolTCP
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      slug + "-egress-allow",
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"exam.otu.ca/slug": slug},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
							},
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"k8s-app": "kube-dns"},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Port: &dns, Protocol: &udp},
						{Port: &dns, Protocol: &tcp},
					},
				},
			},
		},
	}
}

func (v *VanillaPolicyProvider) IngressAllow(namespace string, labels map[string]string) client.Object {
	slug := slugFromLabels(labels)
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      slug + "-ingress-allow",
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"exam.otu.ca/slug": slug},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					From: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"kubernetes.io/metadata.name": "ingress-nginx"},
							},
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"app.kubernetes.io/name": "ingress-nginx"},
							},
						},
					},
				},
			},
		},
	}
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/network/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/network/
git commit -m "feat: add PolicyProvider interface with vanilla NetworkPolicy backend"
```

---

### Task 4: CiliumPolicyProvider

**Files:**
- Create: `internal/network/cilium.go`
- Create: `internal/network/cilium_test.go`

**Step 1: Write the failing tests**

```go
package network

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCiliumDenyAll(t *testing.T) {
	p := &CiliumPolicyProvider{}
	obj := p.DenyAll("exam-ns", map[string]string{"exam.otu.ca/slug": "abc123"})
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		t.Fatal("expected *Unstructured")
	}
	if u.GetKind() != "CiliumNetworkPolicy" {
		t.Errorf("kind = %q, want CiliumNetworkPolicy", u.GetKind())
	}
	if u.GetAPIVersion() != "cilium.io/v2" {
		t.Errorf("apiVersion = %q, want cilium.io/v2", u.GetAPIVersion())
	}
	if u.GetName() != "abc123-deny-all" {
		t.Errorf("name = %q, want abc123-deny-all", u.GetName())
	}
}

func TestCiliumEgressAllowlist(t *testing.T) {
	p := &CiliumPolicyProvider{}
	obj := p.EgressAllowlist("exam-ns", map[string]string{"exam.otu.ca/slug": "abc123"})
	u := obj.(*unstructured.Unstructured)
	if u.GetName() != "abc123-egress-allow" {
		t.Errorf("name = %q, want abc123-egress-allow", u.GetName())
	}
	spec, _, _ := unstructured.NestedMap(u.Object, "spec")
	if spec == nil {
		t.Fatal("expected spec")
	}
	egress, _, _ := unstructured.NestedSlice(u.Object, "spec", "egress")
	if len(egress) == 0 {
		t.Error("expected egress rules")
	}
}

func TestCiliumIngressAllow(t *testing.T) {
	p := &CiliumPolicyProvider{}
	obj := p.IngressAllow("exam-ns", map[string]string{"exam.otu.ca/slug": "abc123"})
	u := obj.(*unstructured.Unstructured)
	if u.GetName() != "abc123-ingress-allow" {
		t.Errorf("name = %q, want abc123-ingress-allow", u.GetName())
	}
	ingress, _, _ := unstructured.NestedSlice(u.Object, "spec", "ingress")
	if len(ingress) == 0 {
		t.Error("expected ingress rules with L7 visibility")
	}
}

func TestCiliumImplementsProvider(t *testing.T) {
	var _ PolicyProvider = &CiliumPolicyProvider{}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/network/ -v -run Cilium`
Expected: FAIL — `CiliumPolicyProvider` not defined

**Step 3: Write implementation**

Use `unstructured.Unstructured` to avoid pulling in the Cilium Go module dependency. The objects have the correct GVK and spec structure for controller-runtime's client to manage.

```go
package network

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var ciliumGVK = schema.GroupVersionKind{
	Group:   "cilium.io",
	Version: "v2",
	Kind:    "CiliumNetworkPolicy",
}

// CiliumPolicyProvider creates CiliumNetworkPolicy resources with L7 visibility.
type CiliumPolicyProvider struct{}

func ciliumPolicy(name, namespace string, labels map[string]string, spec map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ciliumGVK)
	u.SetName(name)
	u.SetNamespace(namespace)
	u.SetLabels(labels)
	u.Object["spec"] = spec
	return u
}

func (c *CiliumPolicyProvider) DenyAll(namespace string, labels map[string]string) client.Object {
	slug := slugFromLabels(labels)
	return ciliumPolicy(slug+"-deny-all", namespace, labels, map[string]interface{}{
		"endpointSelector": map[string]interface{}{
			"matchLabels": map[string]interface{}{"exam.otu.ca/slug": slug},
		},
		"ingressDeny": []interface{}{map[string]interface{}{}},
		"egressDeny":  []interface{}{map[string]interface{}{}},
	})
}

func (c *CiliumPolicyProvider) EgressAllowlist(namespace string, labels map[string]string) client.Object {
	slug := slugFromLabels(labels)
	return ciliumPolicy(slug+"-egress-allow", namespace, labels, map[string]interface{}{
		"endpointSelector": map[string]interface{}{
			"matchLabels": map[string]interface{}{"exam.otu.ca/slug": slug},
		},
		"egress": []interface{}{
			map[string]interface{}{
				"toFQDNs": []interface{}{
					map[string]interface{}{"matchPattern": "*.cluster.local"},
				},
				"toPorts": []interface{}{
					map[string]interface{}{
						"ports": []interface{}{
							map[string]interface{}{"port": "53", "protocol": "UDP"},
							map[string]interface{}{"port": "53", "protocol": "TCP"},
						},
					},
				},
			},
		},
	})
}

func (c *CiliumPolicyProvider) IngressAllow(namespace string, labels map[string]string) client.Object {
	slug := slugFromLabels(labels)
	port := labels["exam.otu.ca/port"] // set by controller when creating labels
	if port == "" {
		port = "8080"
	}
	return ciliumPolicy(slug+"-ingress-allow", namespace, labels, map[string]interface{}{
		"endpointSelector": map[string]interface{}{
			"matchLabels": map[string]interface{}{"exam.otu.ca/slug": slug},
		},
		"ingress": []interface{}{
			map[string]interface{}{
				"fromEndpoints": []interface{}{
					map[string]interface{}{
						"matchLabels": map[string]interface{}{
							"k8s:io.kubernetes.pod.namespace": "ingress-nginx",
							"app.kubernetes.io/name":          "ingress-nginx",
						},
					},
				},
				"toPorts": []interface{}{
					map[string]interface{}{
						"ports": []interface{}{
							map[string]interface{}{"port": fmt.Sprintf("%s", port), "protocol": "TCP"},
						},
						"rules": map[string]interface{}{
							"http": []interface{}{
								map[string]interface{}{"method": ""},
							},
						},
					},
				},
			},
		},
	})
}
```

Note: The controller must include `"exam.otu.ca/port": fmt.Sprintf("%d", exam.Spec.Template.Port)` in the labels map when calling `PolicyProvider.IngressAllow()`. The `VanillaPolicyProvider.IngressAllow` should also read port from labels for consistency — update it to match.

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/network/ -v`
Expected: PASS (all vanilla + cilium tests)

**Step 5: Commit**

```bash
git add internal/network/
git commit -m "feat: add CiliumNetworkPolicy provider with L7 visibility"
```

---

### Task 5: Rewrite Provisioner

**Files:**
- Rewrite: `internal/provisioner/resources.go`
- Rewrite: `internal/provisioner/resources_test.go`

**Step 1: Write the failing tests**

Key changes: resources named by slug only (no studentID in names), labels use slug as primary key, support spare instances (no studentID label).

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
		ObjectMeta: metav1.ObjectMeta{Name: "test-exam"},
		Spec: examv1alpha1.ExamSpec{
			Template: examv1alpha1.ExamTemplate{
				Image: "vuln-app:v1",
				Port:  8080,
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("250m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
				},
			},
			IngressTLS: examv1alpha1.ExamIngressTLS{SecretName: "tls-secret"},
			Domain:     "exam.test.com",
		},
	}
}

func TestDeploymentStudent(t *testing.T) {
	dep := Deployment(testExam(), "exam-ns", "alice", "a1b2c3d4")
	if dep.Name != "a1b2c3d4" {
		t.Errorf("name = %q, want slug-only name", dep.Name)
	}
	if dep.Labels["exam.otu.ca/student"] != "alice" {
		t.Error("expected student label")
	}
	if dep.Labels["exam.otu.ca/slug"] != "a1b2c3d4" {
		t.Error("expected slug label")
	}
	// Security checks
	ps := dep.Spec.Template.Spec
	if ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken {
		t.Error("expected automountServiceAccountToken=false")
	}
	sc := ps.Containers[0].SecurityContext
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Error("expected runAsNonRoot")
	}
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("expected no privilege escalation")
	}
}

func TestDeploymentSpare(t *testing.T) {
	dep := Deployment(testExam(), "exam-ns", "", "x9y8z7w6")
	if dep.Name != "x9y8z7w6" {
		t.Errorf("name = %q, want slug", dep.Name)
	}
	if _, ok := dep.Labels["exam.otu.ca/student"]; ok {
		t.Error("spare should not have student label")
	}
	if dep.Labels["exam.otu.ca/slug"] != "x9y8z7w6" {
		t.Error("expected slug label")
	}
}

func TestServiceNaming(t *testing.T) {
	svc := Service(testExam(), "exam-ns", "alice", "a1b2c3d4")
	if svc.Name != "a1b2c3d4" {
		t.Errorf("name = %q, want slug", svc.Name)
	}
}

func TestIngressHost(t *testing.T) {
	ing := Ingress(testExam(), "exam-ns", "alice", "a1b2c3d4")
	if ing.Name != "a1b2c3d4" {
		t.Errorf("name = %q, want slug", ing.Name)
	}
	host := ing.Spec.Rules[0].Host
	if host != "a1b2c3d4.exam.test.com" {
		t.Errorf("host = %q, want a1b2c3d4.exam.test.com", host)
	}
	if ing.Spec.TLS[0].SecretName != "tls-secret" {
		t.Error("expected TLS secret reference")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/provisioner/ -v`
Expected: FAIL — function signatures changed

**Step 3: Write implementation**

```go
package provisioner

import (
	"fmt"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

// Labels returns the standard labels for a resource. If studentID is empty, the
// resource is a spare and the student label is omitted.
func Labels(examName, studentID, slug string) map[string]string {
	l := map[string]string{
		"exam.otu.ca/exam": examName,
		"exam.otu.ca/slug": slug,
	}
	if studentID != "" {
		l["exam.otu.ca/student"] = studentID
	}
	return l
}

func Deployment(exam *examv1alpha1.Exam, namespace, studentID, slug string) *appsv1.Deployment {
	labels := Labels(exam.Name, studentID, slug)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      slug,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: ptr.To[int32](1),
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"exam.otu.ca/slug": slug}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
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
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
								SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
							},
						},
					},
				},
			},
		},
	}
}

func Service(exam *examv1alpha1.Exam, namespace, studentID, slug string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      slug,
			Namespace: namespace,
			Labels:    Labels(exam.Name, studentID, slug),
		},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"exam.otu.ca/slug": slug},
			Ports:    []corev1.ServicePort{{Port: exam.Spec.Template.Port}},
		},
	}
}

func Ingress(exam *examv1alpha1.Exam, namespace, studentID, slug string) *networkingv1.Ingress {
	pathType := networkingv1.PathTypePrefix
	host := fmt.Sprintf("%s.%s", slug, exam.Spec.Domain)
	return &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{
			Name:      slug,
			Namespace: namespace,
			Labels:    Labels(exam.Name, studentID, slug),
		},
		Spec: networkingv1.IngressSpec{
			TLS: []networkingv1.IngressTLS{
				{Hosts: []string{host}, SecretName: exam.Spec.IngressTLS.SecretName},
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
											Port: networkingv1.ServiceBackendPort{Number: exam.Spec.Template.Port},
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

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/provisioner/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/provisioner/
git commit -m "feat: rewrite provisioner with slug-based naming and spare support"
```

---

### Task 6: Rewrite Notifier

**Files:**
- Rewrite: `internal/notifier/email.go`
- Rewrite: `internal/notifier/email_test.go`

**Step 1: Write the failing tests**

Key additions: rate limiting, instructor email builder, failed student list in unlock notification.

```go
package notifier

import (
	"strings"
	"testing"
	"time"
)

func TestBuildStudentMessage(t *testing.T) {
	msg := BuildStudentMessage("noreply@test.com", "alice@test.com", "Test Exam", "https://abc123.exam.test.com")
	if !strings.Contains(msg, "From: noreply@test.com") {
		t.Error("missing From header")
	}
	if !strings.Contains(msg, "To: alice@test.com") {
		t.Error("missing To header")
	}
	if !strings.Contains(msg, "https://abc123.exam.test.com") {
		t.Error("missing URL in body")
	}
}

func TestBuildSparesMessage(t *testing.T) {
	urls := []string{"https://x1.exam.test.com", "https://x2.exam.test.com"}
	msg := BuildSparesMessage("noreply@test.com", "prof@test.com", "Test Exam", urls)
	if !strings.Contains(msg, "x1.exam.test.com") {
		t.Error("missing first spare URL")
	}
	if !strings.Contains(msg, "x2.exam.test.com") {
		t.Error("missing second spare URL")
	}
}

func TestBuildUnlockNotification(t *testing.T) {
	failed := []string{"alice"}
	msg := BuildUnlockNotification("noreply@test.com", "prof@test.com", "Test Exam", 48, 2, failed)
	if !strings.Contains(msg, "48 students") {
		t.Error("missing student count")
	}
	if !strings.Contains(msg, "alice") {
		t.Error("missing failed student in notification")
	}
}

func TestBuildLockNotification(t *testing.T) {
	msg := BuildLockNotification("noreply@test.com", "prof@test.com", "Test Exam", 48, 50, 0)
	if !strings.Contains(msg, "ended") {
		t.Error("missing ended message")
	}
}

func TestFakeSenderRecords(t *testing.T) {
	s := &FakeSender{}
	err := s.Send("from@test.com", []string{"to@test.com"}, []byte("msg"))
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(s.Sent))
	}
}

func TestRetrySender_SucceedsFirstTry(t *testing.T) {
	inner := &FakeSender{}
	rs := NewRetrySender(inner, 3)
	err := rs.Send("from@test.com", []string{"to@test.com"}, []byte("msg"))
	if err != nil {
		t.Fatal(err)
	}
	if len(inner.Sent) != 1 {
		t.Fatalf("sent = %d, want 1", len(inner.Sent))
	}
}

func TestRetrySender_RetriesOnFailure(t *testing.T) {
	inner := &FailNSender{FailCount: 2}
	rs := NewRetrySender(inner, 3)
	err := rs.Send("from@test.com", []string{"to@test.com"}, []byte("msg"))
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if inner.Attempts != 3 {
		t.Errorf("attempts = %d, want 3", inner.Attempts)
	}
}

func TestRetrySender_ExhaustsRetries(t *testing.T) {
	inner := &FailNSender{FailCount: 5}
	rs := NewRetrySender(inner, 3)
	err := rs.Send("from@test.com", []string{"to@test.com"}, []byte("msg"))
	if err == nil {
		t.Fatal("expected error after exhausting retries")
	}
}

func TestSenderInterfaceCompliance(t *testing.T) {
	var _ Sender = &RetrySender{}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/notifier/ -v`
Expected: FAIL

**Step 3: Write implementation**

```go
package notifier

import (
	"fmt"
	"net/smtp"
	"strings"
	"time"
)

// Sender sends email messages.
type Sender interface {
	Send(from string, to []string, msg []byte) error
}

// SMTPSender sends emails via SMTP.
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

// SentMessage records a sent message for testing.
type SentMessage struct {
	From string
	To   []string
	Body []byte
}

// FakeSender records messages for testing.
type FakeSender struct {
	Sent []SentMessage
}

func (f *FakeSender) Send(from string, to []string, msg []byte) error {
	f.Sent = append(f.Sent, SentMessage{From: from, To: to, Body: msg})
	return nil
}

// RetrySender wraps a Sender with exponential backoff retries.
type RetrySender struct {
	inner      Sender
	maxRetries int
}

func NewRetrySender(inner Sender, maxRetries int) *RetrySender {
	return &RetrySender{inner: inner, maxRetries: maxRetries}
}

func (r *RetrySender) Send(from string, to []string, msg []byte) error {
	var err error
	for attempt := 0; attempt <= r.maxRetries; attempt++ {
		err = r.inner.Send(from, to, msg)
		if err == nil {
			return nil
		}
		if attempt < r.maxRetries {
			time.Sleep(time.Duration(1<<uint(attempt)) * 100 * time.Millisecond)
		}
	}
	return fmt.Errorf("after %d retries: %w", r.maxRetries, err)
}

// FailNSender fails the first N attempts, then succeeds. For testing.
type FailNSender struct {
	FailCount int
	Attempts  int
}

func (f *FailNSender) Send(from string, to []string, msg []byte) error {
	f.Attempts++
	if f.Attempts <= f.FailCount {
		return fmt.Errorf("simulated failure %d", f.Attempts)
	}
	return nil
}

func buildMessage(from, to, subject, body string) string {
	return fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s", from, to, subject, body)
}

func BuildStudentMessage(from, to, subject, url string) string {
	body := fmt.Sprintf("Your exam instance is ready.\n\nAccess your instance at: %s\n\nThis link is unique to you. Do not share it.\n", url)
	return buildMessage(from, to, subject, body)
}

func BuildSparesMessage(from, to, subject string, urls []string) string {
	body := fmt.Sprintf("Spare instances are ready.\n\n%s\n", strings.Join(urls, "\n"))
	return buildMessage(from, to, subject+" - Spare Instances", body)
}

func BuildUnlockNotification(from, to, subject string, students, spares int, failedEmails []string) string {
	body := fmt.Sprintf("Exam is live.\n\n%d students, %d spares.\n", students, spares)
	if len(failedEmails) > 0 {
		body += fmt.Sprintf("\nFailed email delivery for: %s\nPlease share URLs manually.\n", strings.Join(failedEmails, ", "))
	}
	return buildMessage(from, to, subject+" - Exam Unlocked", body)
}

func BuildLockNotification(from, to, subject string, students, healthy, failed int) string {
	body := fmt.Sprintf("Exam has ended.\n\n%d students, %d instances healthy, %d failed.\n", students, healthy, failed)
	return buildMessage(from, to, subject+" - Exam Locked", body)
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/notifier/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/notifier/
git commit -m "feat: rewrite notifier with rate limiting and instructor notifications"
```

---

### Task 7: Rewrite Smoke Test Runner

**Files:**
- Rewrite: `internal/smoketest/runner.go`
- Rewrite: `internal/smoketest/runner_test.go`

**Step 1: Write the failing tests**

Add `CheckBlocked` for negative connectivity test.

```go
package smoketest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCheckHealth_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	if err := CheckHealth(context.Background(), srv.URL); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestCheckHealth_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	}))
	defer srv.Close()
	if err := CheckHealth(context.Background(), srv.URL); err == nil {
		t.Error("expected error for 503")
	}
}

func TestCheckBlocked_Reachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	// If we CAN reach it, CheckBlocked should return an error (policy not enforced)
	if err := CheckBlocked(context.Background(), srv.URL); err == nil {
		t.Error("expected error when service is reachable (policy not enforced)")
	}
}

func TestCheckBlocked_Unreachable(t *testing.T) {
	// Unreachable URL — connection refused = policy working
	if err := CheckBlocked(context.Background(), "http://127.0.0.1:1"); err != nil {
		t.Errorf("expected nil (blocked is good), got: %v", err)
	}
}

func TestRunAll(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(503)
	}))
	defer bad.Close()

	targets := []Target{
		{StudentID: "alice", URL: good.URL},
		{StudentID: "bob", URL: bad.URL},
	}
	result := RunAll(context.Background(), targets)
	if result.Passed != 1 {
		t.Errorf("passed = %d, want 1", result.Passed)
	}
	if result.Failed != 1 {
		t.Errorf("failed = %d, want 1", result.Failed)
	}
	if len(result.Failures) != 1 || result.Failures[0].Student != "bob" {
		t.Errorf("expected bob in failures")
	}
}

func TestRunDryRun_PolicyEnforced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	targets := []Target{{StudentID: "alice", URL: srv.URL}}
	// negativeURL is unreachable = policy enforced
	dr := RunDryRun(context.Background(), targets, "http://127.0.0.1:1")
	if !dr.PolicyEnforced {
		t.Error("expected PolicyEnforced=true when negative URL is unreachable")
	}
	if dr.Result.Passed != 1 {
		t.Errorf("passed = %d, want 1", dr.Result.Passed)
	}
}

func TestRunDryRun_PolicyNotEnforced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	targets := []Target{{StudentID: "alice", URL: srv.URL}}
	// negativeURL IS reachable = policy NOT enforced
	dr := RunDryRun(context.Background(), targets, srv.URL)
	if dr.PolicyEnforced {
		t.Error("expected PolicyEnforced=false when negative URL is reachable")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/smoketest/ -v`
Expected: FAIL — `CheckBlocked` not defined

**Step 3: Write implementation**

```go
package smoketest

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type Target struct {
	StudentID string
	URL       string
}

type Failure struct {
	Student string
	Error   string
}

type Result struct {
	Passed   int
	Failed   int
	Failures []Failure
}

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

// CheckBlocked verifies that a URL is NOT reachable. Returns an error if the
// service IS reachable (meaning NetworkPolicy enforcement is broken).
func CheckBlocked(ctx context.Context, url string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil // Can't even form the request, treat as blocked
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil // Connection refused/timeout = blocked = good
	}
	defer resp.Body.Close()
	return fmt.Errorf("service reachable (HTTP %d) — NetworkPolicy not enforced", resp.StatusCode)
}

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

// DryRunResult combines health check results with policy enforcement check.
type DryRunResult struct {
	Result         Result
	PolicyEnforced bool
}

// RunDryRun runs all health checks plus a negative connectivity test.
// negativeURL should be a student Service URL reachable only if NetworkPolicy
// enforcement is broken. If CheckBlocked returns an error, PolicyEnforced is false.
func RunDryRun(ctx context.Context, targets []Target, negativeURL string) DryRunResult {
	result := RunAll(ctx, targets)
	policyEnforced := true
	if err := CheckBlocked(ctx, negativeURL); err != nil {
		policyEnforced = false
	}
	return DryRunResult{Result: result, PolicyEnforced: policyEnforced}
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/smoketest/ -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/smoketest/
git commit -m "feat: add negative connectivity test to smoke test runner"
```

---

### Task 8: Prometheus Metrics Package

**Files:**
- Create: `internal/metrics/metrics.go`
- Create: `internal/metrics/metrics_test.go`

**Step 1: Write the failing tests**

```go
package metrics

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricsRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)

	if m.ReconcileDuration == nil {
		t.Error("ReconcileDuration not initialized")
	}
	if m.ReconcileErrors == nil {
		t.Error("ReconcileErrors not initialized")
	}
	if m.PhaseTransitions == nil {
		t.Error("PhaseTransitions not initialized")
	}
	if m.InstancesTotal == nil {
		t.Error("InstancesTotal not initialized")
	}
	if m.InstancesHealthy == nil {
		t.Error("InstancesHealthy not initialized")
	}
	if m.InstancesFailed == nil {
		t.Error("InstancesFailed not initialized")
	}
	if m.EmailsSent == nil {
		t.Error("EmailsSent not initialized")
	}
	if m.EmailsFailed == nil {
		t.Error("EmailsFailed not initialized")
	}
	if m.SecondsUntilUnlock == nil {
		t.Error("SecondsUntilUnlock not initialized")
	}
	if m.SecondsUntilLock == nil {
		t.Error("SecondsUntilLock not initialized")
	}
}

func TestRecordPhaseTransition(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewExamMetrics(reg)
	m.PhaseTransitions.WithLabelValues("test-exam", "Pending", "Provisioning").Inc()
	// No panic = success
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/metrics/ -v`
Expected: FAIL — package doesn't exist

**Step 3: Write implementation**

```go
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

type ExamMetrics struct {
	ReconcileDuration prometheus.Histogram
	ReconcileErrors   prometheus.Counter
	PhaseTransitions  *prometheus.CounterVec
	InstancesTotal    *prometheus.GaugeVec
	InstancesHealthy  *prometheus.GaugeVec
	InstancesFailed   *prometheus.GaugeVec
	EmailsSent        *prometheus.CounterVec
	EmailsFailed      *prometheus.CounterVec
	DryRunPassed      *prometheus.GaugeVec
	DryRunFailed      *prometheus.GaugeVec
	SecondsUntilUnlock *prometheus.GaugeVec
	SecondsUntilLock   *prometheus.GaugeVec
}

func NewExamMetrics(reg prometheus.Registerer) *ExamMetrics {
	m := &ExamMetrics{
		ReconcileDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "exam_reconcile_duration_seconds",
			Help:    "Time spent per reconcile loop.",
			Buckets: prometheus.DefBuckets,
		}),
		ReconcileErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "exam_reconcile_errors_total",
			Help: "Total reconcile failures.",
		}),
		PhaseTransitions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "exam_phase_transitions_total",
			Help: "Phase changes labeled by exam, from, and to.",
		}, []string{"exam", "from", "to"}),
		InstancesTotal: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "exam_instances_total",
			Help: "Total instances (students + spares).",
		}, []string{"exam"}),
		InstancesHealthy: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "exam_instances_healthy",
			Help: "Instances passing health checks.",
		}, []string{"exam"}),
		InstancesFailed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "exam_instances_failed",
			Help: "Instances in failed state.",
		}, []string{"exam"}),
		EmailsSent: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "exam_emails_sent_total",
			Help: "Emails successfully sent.",
		}, []string{"exam"}),
		EmailsFailed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "exam_emails_failed_total",
			Help: "Email delivery failures.",
		}, []string{"exam"}),
		DryRunPassed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "exam_dryrun_passed",
			Help: "Dry run pass count.",
		}, []string{"exam"}),
		DryRunFailed: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "exam_dryrun_failed",
			Help: "Dry run fail count.",
		}, []string{"exam"}),
		SecondsUntilUnlock: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "exam_seconds_until_unlock",
			Help: "Countdown to unlock (0 after unlock).",
		}, []string{"exam"}),
		SecondsUntilLock: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "exam_seconds_until_lock",
			Help: "Countdown to lock (0 after lock).",
		}, []string{"exam"}),
	}
	reg.MustRegister(
		m.ReconcileDuration, m.ReconcileErrors, m.PhaseTransitions,
		m.InstancesTotal, m.InstancesHealthy, m.InstancesFailed,
		m.EmailsSent, m.EmailsFailed,
		m.DryRunPassed, m.DryRunFailed,
		m.SecondsUntilUnlock, m.SecondsUntilLock,
	)
	return m
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./internal/metrics/ -v`
Expected: PASS

Note: You may need to run `go get github.com/prometheus/client_golang/prometheus` if not already a dependency. Check `go.mod` first — it's likely already pulled in transitively via controller-runtime.

**Step 5: Commit**

```bash
git add internal/metrics/
git commit -m "feat: add Prometheus metrics package"
```

---

### Task 9: Rewrite Webhook

**Files:**
- Rewrite: `api/v1alpha1/exam_webhook.go`
- Delete: `internal/webhook/` (logic moves into api webhook, no duplication)

**Step 1: Write the failing tests**

Create `api/v1alpha1/exam_webhook_test.go`:

```go
package v1alpha1

import (
	"context"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func baseExam() *Exam {
	return &Exam{
		Spec: ExamSpec{
			Template: ExamTemplate{Image: "app:v1", Port: 8080},
			Schedule: ExamSchedule{
				Unlock:          metav1.NewTime(time.Now().Add(2 * time.Hour)),
				Duration:        metav1.Duration{Duration: 2 * time.Hour},
				TimeMultiplier:  1.5,
				ProvisionBefore: metav1.Duration{Duration: 1 * time.Hour},
			},
			Students: []ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			},
			Email: ExamEmail{
				Before:          metav1.Duration{Duration: 30 * time.Minute},
				RateLimit:       1,
				InstructorEmail: "prof@test.com",
				SecretRef:       "smtp",
				From:            "noreply@test.com",
				Subject:         "Test",
			},
			IngressTLS: ExamIngressTLS{SecretName: "tls"},
			Domain:     "exam.test.com",
		},
	}
}

func TestValidateCreate_Valid(t *testing.T) {
	v := &examValidator{}
	_, err := v.ValidateCreate(context.Background(), baseExam())
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestValidateCreate_MissingInstructorEmail(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	e.Spec.Email.InstructorEmail = ""
	_, err := v.ValidateCreate(context.Background(), e)
	if err == nil {
		t.Error("expected error for missing instructorEmail")
	}
}

func TestValidateCreate_DurationZero(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	e.Spec.Schedule.Duration = metav1.Duration{}
	_, err := v.ValidateCreate(context.Background(), e)
	if err == nil {
		t.Error("expected error for zero duration")
	}
}

func TestValidateCreate_MultiplierTooLow(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	e.Spec.Schedule.TimeMultiplier = 0.5
	_, err := v.ValidateCreate(context.Background(), e)
	if err == nil {
		t.Error("expected error for multiplier < 1.0")
	}
}

func TestValidateCreate_NoStudents(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	e.Spec.Students = nil
	_, err := v.ValidateCreate(context.Background(), e)
	if err == nil {
		t.Error("expected error for empty students")
	}
}

func TestValidateCreate_EmailTimingInsufficient(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	// 100 students at 1/s = 100s minimum × 1.5 = 150s, but emailBefore = 2m = 120s
	e.Spec.Students = make([]ExamStudent, 100)
	for i := range e.Spec.Students {
		e.Spec.Students[i] = ExamStudent{ID: "s", Email: "s@test.com"}
	}
	e.Spec.Email.Before = metav1.Duration{Duration: 2 * time.Minute}
	_, err := v.ValidateCreate(context.Background(), e)
	if err == nil {
		t.Error("expected error for insufficient email timing")
	}
}

func TestValidateCreate_ProvisionBeforeEmailBefore(t *testing.T) {
	v := &examValidator{}
	e := baseExam()
	e.Spec.Schedule.ProvisionBefore = metav1.Duration{Duration: 10 * time.Minute}
	e.Spec.Email.Before = metav1.Duration{Duration: 30 * time.Minute}
	_, err := v.ValidateCreate(context.Background(), e)
	if err == nil {
		t.Error("expected error: provisionBefore must be > emailBefore")
	}
}

func TestValidateUpdate_ImmutableImageAfterPending(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseProvisioning
	updated := old.DeepCopy()
	updated.Spec.Template.Image = "app:v2"
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Error("expected error for image change after Pending")
	}
}

func TestValidateUpdate_AllowImageChangeWhenPending(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhasePending
	updated := old.DeepCopy()
	updated.Spec.Template.Image = "app:v2"
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err != nil {
		t.Errorf("should allow image change when Pending: %v", err)
	}
}

func TestValidateUpdate_DurationMutableWhenUnlocked(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseUnlocked
	updated := old.DeepCopy()
	updated.Spec.Schedule.Duration = metav1.Duration{Duration: 3 * time.Hour}
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err != nil {
		t.Errorf("should allow duration change when Unlocked: %v", err)
	}
}

func TestValidateUpdate_DurationImmutableWhenLocked(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseLocked
	updated := old.DeepCopy()
	updated.Spec.Schedule.Duration = metav1.Duration{Duration: 3 * time.Hour}
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Error("expected error for duration change when Locked")
	}
}

func TestValidateUpdate_SparesImmutableAfterPending(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseReady
	old.Spec.Spares = 2
	updated := old.DeepCopy()
	updated.Spec.Spares = 5
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Error("expected error for spares change after Pending")
	}
}

func TestValidateUpdate_ResourcesImmutableAfterPending(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseProvisioning
	updated := old.DeepCopy()
	updated.Spec.Template.Resources.Requests = nil
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Error("expected error for resources change after Pending")
	}
}

func TestValidateUpdate_DomainImmutableAfterPending(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseReady
	updated := old.DeepCopy()
	updated.Spec.Domain = "other.com"
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Error("expected error for domain change after Pending")
	}
}

func TestValidateUpdate_LockTimeGuardWhenUnlocked(t *testing.T) {
	v := &examValidator{}
	old := baseExam()
	old.Status.Phase = ExamPhaseUnlocked
	// Unlock was 1h ago
	old.Spec.Schedule.Unlock = metav1.NewTime(time.Now().Add(-1 * time.Hour))
	updated := old.DeepCopy()
	// Set duration to 10 minutes — lockTime would be in the past
	updated.Spec.Schedule.Duration = metav1.Duration{Duration: 10 * time.Minute}
	updated.Spec.Schedule.TimeMultiplier = 1.0
	_, err := v.ValidateUpdate(context.Background(), old, updated)
	if err == nil {
		t.Error("expected error: computed lockTime would be in the past")
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./api/v1alpha1/ -v -run TestValidate`
Expected: FAIL

**Step 3: Write implementation**

Rewrite `api/v1alpha1/exam_webhook.go`:

```go
package v1alpha1

import (
	"context"
	"fmt"
	"math"
	"reflect"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func SetupExamWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr, &Exam{}).
		WithValidator(&examValidator{}).
		Complete()
}

// +kubebuilder:webhook:path=/validate-exam-otu-ca-v1alpha1-exam,mutating=false,failurePolicy=fail,sideEffects=None,groups=exam.otu.ca,resources=exams,verbs=create;update,versions=v1alpha1,name=vexam-v1alpha1.kb.io,admissionReviewVersions=v1

type examValidator struct{}

func (v *examValidator) ValidateCreate(_ context.Context, exam *Exam) (admission.Warnings, error) {
	if len(exam.Spec.Students) == 0 {
		return nil, fmt.Errorf("spec.students must have at least one entry")
	}
	if exam.Spec.Schedule.Duration.Duration <= 0 {
		return nil, fmt.Errorf("spec.schedule.duration must be > 0")
	}
	mult := exam.Spec.Schedule.TimeMultiplier
	if mult == 0 {
		mult = 1.5
	}
	if mult < 1.0 {
		return nil, fmt.Errorf("spec.schedule.timeMultiplier must be >= 1.0")
	}
	if exam.Spec.Email.InstructorEmail == "" {
		return nil, fmt.Errorf("spec.email.instructorEmail is required")
	}
	if exam.Spec.Spares < 0 {
		return nil, fmt.Errorf("spec.spares must be >= 0")
	}

	// Email timing validation: emailBefore >= ceil(students/rate) * 1.5
	rateLimit := exam.Spec.Email.RateLimit
	if rateLimit <= 0 {
		rateLimit = 1
	}
	emailBefore := exam.Spec.Email.Before.Duration
	if emailBefore == 0 {
		emailBefore = 30 * time.Minute
	}
	minEmailTime := math.Ceil(float64(len(exam.Spec.Students))/float64(rateLimit)) * 1.5
	if emailBefore.Seconds() < minEmailTime {
		return nil, fmt.Errorf("spec.email.before (%v) is too short to send %d emails at %d/s (need %.0fs with retry buffer)",
			emailBefore, len(exam.Spec.Students), rateLimit, minEmailTime)
	}

	provisionBefore := exam.Spec.Schedule.ProvisionBefore.Duration
	if provisionBefore == 0 {
		provisionBefore = 1 * time.Hour
	}
	if provisionBefore <= emailBefore {
		return nil, fmt.Errorf("spec.schedule.provisionBefore (%v) must be greater than spec.email.before (%v)",
			provisionBefore, emailBefore)
	}

	// Format validations
	if errs := validation.IsDNS1123Subdomain(exam.Spec.Domain); len(errs) > 0 {
		return nil, fmt.Errorf("spec.domain %q is not a valid DNS domain: %s", exam.Spec.Domain, errs[0])
	}
	for i, s := range exam.Spec.Students {
		if errs := validation.IsValidLabelValue(s.ID); len(errs) > 0 {
			return nil, fmt.Errorf("spec.students[%d].id %q is not a valid label value: %s", i, s.ID, errs[0])
		}
	}

	return nil, nil
}

func (v *examValidator) ValidateUpdate(_ context.Context, oldExam, newExam *Exam) (admission.Warnings, error) {
	phase := oldExam.Status.Phase
	if phase == ExamPhasePending || phase == "" {
		return nil, nil
	}

	// Immutable after Pending
	if oldExam.Spec.Template.Image != newExam.Spec.Template.Image {
		return nil, fmt.Errorf("spec.template.image is immutable after provisioning (current phase: %s)", phase)
	}
	if oldExam.Spec.Template.Port != newExam.Spec.Template.Port {
		return nil, fmt.Errorf("spec.template.port is immutable after provisioning (current phase: %s)", phase)
	}
	if !reflect.DeepEqual(oldExam.Spec.Template.Resources, newExam.Spec.Template.Resources) {
		return nil, fmt.Errorf("spec.template.resources is immutable after provisioning (current phase: %s)", phase)
	}
	if !oldExam.Spec.Schedule.Unlock.Equal(&newExam.Spec.Schedule.Unlock) {
		return nil, fmt.Errorf("spec.schedule.unlock is immutable after provisioning (current phase: %s)", phase)
	}
	if oldExam.Spec.Spares != newExam.Spec.Spares {
		return nil, fmt.Errorf("spec.spares is immutable after provisioning (current phase: %s)", phase)
	}
	if oldExam.Spec.Domain != newExam.Spec.Domain {
		return nil, fmt.Errorf("spec.domain is immutable after provisioning (current phase: %s)", phase)
	}
	if len(oldExam.Spec.Students) != len(newExam.Spec.Students) {
		return nil, fmt.Errorf("spec.students list length is immutable after provisioning")
	}
	for i := range oldExam.Spec.Students {
		if oldExam.Spec.Students[i].ID != newExam.Spec.Students[i].ID {
			return nil, fmt.Errorf("spec.students[%d].id is immutable after provisioning", i)
		}
	}

	// Immutable after Locked (duration and multiplier)
	if phase == ExamPhaseLocked || phase == ExamPhaseTearingDown {
		if oldExam.Spec.Schedule.Duration != newExam.Spec.Schedule.Duration {
			return nil, fmt.Errorf("spec.schedule.duration is immutable after locking (current phase: %s)", phase)
		}
		if oldExam.Spec.Schedule.TimeMultiplier != newExam.Spec.Schedule.TimeMultiplier {
			return nil, fmt.Errorf("spec.schedule.timeMultiplier is immutable after locking (current phase: %s)", phase)
		}
	}

	// Guard: if duration or multiplier changed, computed lockTime must be >= now
	if phase == ExamPhaseUnlocked {
		if oldExam.Spec.Schedule.Duration != newExam.Spec.Schedule.Duration ||
			oldExam.Spec.Schedule.TimeMultiplier != newExam.Spec.Schedule.TimeMultiplier {
			mult := newExam.Spec.Schedule.TimeMultiplier
			if mult == 0 {
				mult = 1.5
			}
			newLockTime := newExam.Spec.Schedule.Unlock.Add(
				time.Duration(float64(newExam.Spec.Schedule.Duration.Duration) * mult))
			if newLockTime.Before(time.Now()) {
				return nil, fmt.Errorf("computed lockTime (%v) would be in the past", newLockTime)
			}
		}
	}

	return nil, nil
}

func (v *examValidator) ValidateDelete(_ context.Context, _ *Exam) (admission.Warnings, error) {
	return nil, nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./api/v1alpha1/ -v -run TestValidate`
Expected: PASS

**Step 5: Remove duplicate internal/webhook package**

```bash
rm -rf internal/webhook/
```

**Step 6: Commit**

```bash
git add api/v1alpha1/ && git rm -r internal/webhook/
git commit -m "feat: rewrite webhook with creation validation and v2 immutability rules"
```

---

### Task 10: Rewrite Controller

This is the largest task. The controller implements the 6-phase state machine, finalizer-based cleanup, substep conditions, computed status times, drift correction, degraded conditions, and Prometheus metric updates.

**Files:**
- Rewrite: `internal/controller/exam_controller.go`
- Rewrite: `internal/controller/phase_test.go`
- Rewrite: `internal/controller/exam_controller_test.go`
- Keep: `internal/controller/suite_test.go` (test infrastructure, minor updates)

**Step 1: Write phase transition unit tests**

Rewrite `internal/controller/phase_test.go`:

```go
package controller

import (
	"testing"
	"time"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func examWithSchedule(unlock time.Time, duration time.Duration, multiplier float64) *examv1alpha1.Exam {
	return &examv1alpha1.Exam{
		ObjectMeta: metav1.ObjectMeta{Name: "test-exam"},
		Spec: examv1alpha1.ExamSpec{
			Schedule: examv1alpha1.ExamSchedule{
				Unlock:          metav1.NewTime(unlock),
				Duration:        metav1.Duration{Duration: duration},
				TimeMultiplier:  multiplier,
				ProvisionBefore: metav1.Duration{Duration: 1 * time.Hour},
				Retention:       metav1.Duration{Duration: 24 * time.Hour},
			},
			Email: examv1alpha1.ExamEmail{
				Before:          metav1.Duration{Duration: 30 * time.Minute},
				RateLimit:       1,
				InstructorEmail: "prof@test.com",
				SecretRef:       "smtp",
				From:            "noreply@test.com",
				Subject:         "Test",
			},
			Students: []examv1alpha1.ExamStudent{
				{ID: "alice", Email: "alice@test.com"},
			},
		},
	}
}

func TestComputeLockTime(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	lt := computeLockTime(unlock, 2*time.Hour, 1.5)
	want := unlock.Add(3 * time.Hour)
	if !lt.Equal(want) {
		t.Errorf("lockTime = %v, want %v", lt, want)
	}
}

func TestComputeLockTime_DefaultMultiplier(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	lt := computeLockTime(unlock, 2*time.Hour, 0) // 0 means use default 1.5
	want := unlock.Add(3 * time.Hour)
	if !lt.Equal(want) {
		t.Errorf("lockTime = %v, want %v", lt, want)
	}
}

func TestExamNamespace(t *testing.T) {
	ns := examNamespace("sofe4790u-midterm")
	if ns != "exam-sofe4790u-midterm" {
		t.Errorf("namespace = %q, want exam-sofe4790u-midterm", ns)
	}
}

func TestEffectiveMultiplier(t *testing.T) {
	if effectiveMultiplier(0) != 1.5 {
		t.Error("zero should default to 1.5")
	}
	if effectiveMultiplier(2.0) != 2.0 {
		t.Error("explicit value should pass through")
	}
}

func TestDetermineDesiredPhase_PendingBeforeProvisionTime(t *testing.T) {
	now := time.Date(2026, 4, 10, 12, 0, 0, 0, time.UTC)
	exam := examWithSchedule(now.Add(2*time.Hour), 2*time.Hour, 1.5) // provision at unlock-1h = now+1h
	exam.Status.Phase = examv1alpha1.ExamPhasePending
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhasePending {
		t.Errorf("phase = %q, want Pending (not yet provision time)", phase)
	}
}

func TestDetermineDesiredPhase_PendingToProvisioning(t *testing.T) {
	now := time.Date(2026, 4, 10, 13, 0, 0, 0, time.UTC)
	exam := examWithSchedule(now.Add(1*time.Hour), 2*time.Hour, 1.5) // provision at now
	exam.Status.Phase = examv1alpha1.ExamPhasePending
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseProvisioning {
		t.Errorf("phase = %q, want Provisioning", phase)
	}
}

func TestDetermineDesiredPhase_ReadyToUnlocked(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	now := unlock.Add(1 * time.Minute)
	exam := examWithSchedule(unlock, 2*time.Hour, 1.5)
	exam.Status.Phase = examv1alpha1.ExamPhaseReady
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseUnlocked {
		t.Errorf("phase = %q, want Unlocked", phase)
	}
}

func TestDetermineDesiredPhase_ReadyWaiting(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	now := unlock.Add(-10 * time.Minute)
	exam := examWithSchedule(unlock, 2*time.Hour, 1.5)
	exam.Status.Phase = examv1alpha1.ExamPhaseReady
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseReady {
		t.Errorf("phase = %q, want Ready (still waiting)", phase)
	}
}

func TestDetermineDesiredPhase_UnlockedToLocked(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	lockTime := unlock.Add(3 * time.Hour)
	now := lockTime.Add(1 * time.Minute)
	exam := examWithSchedule(unlock, 2*time.Hour, 1.5)
	exam.Status.Phase = examv1alpha1.ExamPhaseUnlocked
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseLocked {
		t.Errorf("phase = %q, want Locked", phase)
	}
}

func TestDetermineDesiredPhase_LockedToTearingDown(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	lockTime := unlock.Add(3 * time.Hour)
	retentionDeadline := lockTime.Add(24 * time.Hour)
	now := retentionDeadline.Add(1 * time.Minute)
	exam := examWithSchedule(unlock, 2*time.Hour, 1.5)
	exam.Status.Phase = examv1alpha1.ExamPhaseLocked
	exam.Status.RetentionDeadline = &metav1.Time{Time: retentionDeadline}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseTearingDown {
		t.Errorf("phase = %q, want TearingDown", phase)
	}
}

func TestDetermineDesiredPhase_LockedWaiting(t *testing.T) {
	unlock := time.Date(2026, 4, 10, 14, 0, 0, 0, time.UTC)
	lockTime := unlock.Add(3 * time.Hour)
	retentionDeadline := lockTime.Add(24 * time.Hour)
	now := lockTime.Add(1 * time.Hour)
	exam := examWithSchedule(unlock, 2*time.Hour, 1.5)
	exam.Status.Phase = examv1alpha1.ExamPhaseLocked
	exam.Status.RetentionDeadline = &metav1.Time{Time: retentionDeadline}
	phase := determineDesiredPhase(exam, now)
	if phase != examv1alpha1.ExamPhaseLocked {
		t.Errorf("phase = %q, want Locked (retention not expired)", phase)
	}
}
```

**Step 2: Run tests to verify they fail**

Run: `go test ./internal/controller/ -v -run "TestCompute|TestExamNamespace|TestEffective|TestDetermineDesiredPhase"`
Expected: FAIL — functions not defined

**Step 3: Write the controller implementation**

Rewrite `internal/controller/exam_controller.go`:

```go
package controller

import (
	"context"
	"fmt"
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
		return examv1alpha1.ExamPhaseProvisioning // stays until all resources healthy

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

	// Create namespace if needed
	namespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}
	if err := r.Create(ctx, namespace); err != nil && !errors.IsAlreadyExists(err) {
		return ctrl.Result{}, err
	}

	// Provision student instances
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

	// Provision spare instances
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

	// Set degraded condition if any failed
	if !allHealthy {
		meta.SetStatusCondition(&exam.Status.Conditions, metav1.Condition{
			Type:    "ProvisioningDegraded",
			Status:  metav1.ConditionTrue,
			Reason:  "SomeInstancesFailed",
			Message: "One or more instances failed to provision",
		})
	}

	// Check if all provisioned instances are healthy (pods running)
	if r.allInstancesHealthy(ctx, exam, ns) {
		meta.SetStatusCondition(&exam.Status.Conditions, metav1.Condition{
			Type:   "Provisioned",
			Status: metav1.ConditionTrue,
			Reason: "AllHealthy",
		})
		exam.Status.Phase = examv1alpha1.ExamPhaseReady

		// Send spare URLs to instructor
		if exam.Spec.Spares > 0 && r.Sender != nil {
			var urls []string
			for _, sp := range exam.Status.Spares {
				urls = append(urls, sp.URL)
			}
			msg := notifier.BuildSparesMessage(exam.Spec.Email.From, exam.Spec.Email.InstructorEmail, exam.Spec.Email.Subject, urls)
			if err := r.Sender.Send(exam.Spec.Email.From, []string{exam.Spec.Email.InstructorEmail}, []byte(msg)); err != nil {
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

	// Substep 1: Send emails (gated by AllEmailsSent condition, triggered at emailTime)
	if !meta.IsStatusConditionTrue(exam.Status.Conditions, "AllEmailsSent") && !now.Before(emailTime) {
		// Send ONE email per reconcile, requeue after rate interval. This avoids
		// blocking the reconcile loop for long periods.
		rateLimit := exam.Spec.Email.RateLimit
		if rateLimit <= 0 {
			rateLimit = 1
		}
		sent := r.sendNextPendingEmail(ctx, exam)
		if sent {
			return ctrl.Result{RequeueAfter: time.Second / time.Duration(rateLimit)}, nil
		}
		// All emails sent (or failed after retries)
		meta.SetStatusCondition(&exam.Status.Conditions, metav1.Condition{
			Type:   "AllEmailsSent",
			Status: metav1.ConditionTrue,
			Reason: "Complete",
		})
	}

	// Substep 2: Dry run (gated by DryRunComplete condition, triggered at dryRunTime)
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

	// Drift correction: ensure deny-all + egress policies exist, no ingress-allow
	r.enforcePolicies(ctx, exam, false) // false = locked (no ingress-allow)

	// Requeue for next substep or unlock
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
	ns := examNamespace(exam.Name)

	// Create ingress-allow policies and ensure ingress resources exist
	r.enforcePolicies(ctx, exam, true) // true = unlocked (ingress-allow present)

	// Update student phases
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

	// Send instructor unlock notification (once)
	if !meta.IsStatusConditionTrue(exam.Status.Conditions, "InstructorNotifiedUnlock") && r.Sender != nil {
		var failedEmails []string
		for _, s := range exam.Status.Students {
			if s.EmailStatus == examv1alpha1.EmailStatusFailed {
				failedEmails = append(failedEmails, s.ID)
			}
		}
		msg := notifier.BuildUnlockNotification(exam.Spec.Email.From, exam.Spec.Email.InstructorEmail,
			exam.Spec.Email.Subject, len(exam.Spec.Students), exam.Spec.Spares, failedEmails)
		if err := r.Sender.Send(exam.Spec.Email.From, []string{exam.Spec.Email.InstructorEmail}, []byte(msg)); err != nil {
			logger.Error(err, "Failed to send unlock notification")
		} else {
			meta.SetStatusCondition(&exam.Status.Conditions, metav1.Condition{
				Type: "InstructorNotifiedUnlock", Status: metav1.ConditionTrue, Reason: "Sent",
			})
		}
	}

	exam.Status.Message = fmt.Sprintf("Exam in progress — %d students, %d spares", len(exam.Spec.Students), exam.Spec.Spares)

	_ = ns // used by enforcePolicies
	lockTime := exam.Status.ComputedLockTime.Time
	return ctrl.Result{RequeueAfter: time.Until(lockTime)}, nil
}

func (r *ExamReconciler) reconcileLocked(ctx context.Context, exam *examv1alpha1.Exam, now time.Time) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	ns := examNamespace(exam.Name)

	// Remove ingress-allow policies and delete Ingress resources
	r.enforcePolicies(ctx, exam, false) // false = locked
	r.deleteIngresses(ctx, ns, exam.Name)

	// Update student phases
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

	// Send instructor lock notification (once)
	if !meta.IsStatusConditionTrue(exam.Status.Conditions, "InstructorNotifiedLock") && r.Sender != nil {
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
		if err := r.Sender.Send(exam.Spec.Email.From, []string{exam.Spec.Email.InstructorEmail}, []byte(msg)); err != nil {
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

func (r *ExamReconciler) provisionInstance(ctx context.Context, exam *examv1alpha1.Exam, ns, studentID, slug string) error {
	dep := provisioner.Deployment(exam, ns, studentID, slug)
	if err := r.Create(ctx, dep); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	svc := provisioner.Service(exam, ns, studentID, slug)
	if err := r.Create(ctx, svc); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	ing := provisioner.Ingress(exam, ns, studentID, slug)
	if err := r.Create(ctx, ing); err != nil && !errors.IsAlreadyExists(err) {
		return err
	}
	labels := provisioner.Labels(exam.Name, studentID, slug)
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
	if r.Sender == nil {
		return false
	}
	for i := range exam.Status.Students {
		if exam.Status.Students[i].EmailStatus == examv1alpha1.EmailStatusPending {
			student := exam.Spec.Students[i]
			msg := notifier.BuildStudentMessage(exam.Spec.Email.From, student.Email, exam.Spec.Email.Subject, exam.Status.Students[i].URL)
			now := metav1.Now()
			if err := r.Sender.Send(exam.Spec.Email.From, []string{student.Email}, []byte(msg)); err != nil {
				exam.Status.Students[i].EmailStatus = examv1alpha1.EmailStatusFailed
			} else {
				exam.Status.Students[i].EmailStatus = examv1alpha1.EmailStatusSent
				exam.Status.Students[i].EmailSentAt = &now
			}
			return true // sent one, requeue for next
		}
	}
	return false // none pending
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

	// Negative connectivity test URL: pick first student's service from controller pod
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

	// Set NetworkPolicyEnforced condition
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

	// Set degraded condition if dry run had failures
	if dr.Result.Failed > 0 {
		meta.SetStatusCondition(&exam.Status.Conditions, metav1.Condition{
			Type:    "DryRunFailed",
			Status:  metav1.ConditionTrue,
			Reason:  "SomeFailed",
			Message: fmt.Sprintf("%d of %d checks failed", dr.Result.Failed, dr.Result.Passed+dr.Result.Failed),
		})
	}
}

// enforcePolicies ensures the correct network policies and ingress resources
// exist for the current phase. This handles drift correction.
func (r *ExamReconciler) enforcePolicies(ctx context.Context, exam *examv1alpha1.Exam, unlocked bool) {
	ns := examNamespace(exam.Name)
	allSlugs := r.collectSlugs(exam)

	for _, entry := range allSlugs {
		labels := provisioner.Labels(exam.Name, entry.studentID, entry.slug)
		labels["exam.otu.ca/port"] = fmt.Sprintf("%d", exam.Spec.Template.Port)

		// Deny-all and egress-allow should always exist
		denyAll := r.PolicyProvider.DenyAll(ns, labels)
		if err := r.Create(ctx, denyAll); errors.IsAlreadyExists(err) {
			// exists, good
		}
		egressAllow := r.PolicyProvider.EgressAllowlist(ns, labels)
		if err := r.Create(ctx, egressAllow); errors.IsAlreadyExists(err) {
			// exists, good
		}

		ingressAllow := r.PolicyProvider.IngressAllow(ns, labels)
		if unlocked {
			// Create ingress-allow if missing
			if err := r.Create(ctx, ingressAllow); err != nil && !errors.IsAlreadyExists(err) {
				log.FromContext(ctx).Error(err, "drift: failed to create ingress-allow", "slug", entry.slug)
			}
		} else {
			// Delete ingress-allow if present
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

func (r *ExamReconciler) deleteIngresses(ctx context.Context, ns, examName string) {
	var ingresses networkingv1.IngressList
	if err := r.List(ctx, &ingresses, client.InNamespace(ns), client.MatchingLabels{"exam.otu.ca/exam": examName}); err != nil {
		return
	}
	for i := range ingresses.Items {
		_ = r.Delete(ctx, &ingresses.Items[i])
	}
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

	// Update Prometheus gauges
	if r.Metrics != nil {
		name := exam.Name
		r.Metrics.InstancesTotal.WithLabelValues(name).Set(float64(len(exam.Spec.Students) + exam.Spec.Spares))
		r.Metrics.InstancesHealthy.WithLabelValues(name).Set(float64(healthy))
		r.Metrics.InstancesFailed.WithLabelValues(name).Set(float64(failed))
	}
}

// SetupWithManager registers the controller. Uses label-based watches for
// cross-namespace resources (Deployments, Services, Ingresses, NetworkPolicies
// live in per-exam namespaces, not the controller namespace).
func (r *ExamReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Map function: given a child resource with exam.otu.ca/exam label,
	// enqueue the Exam CR in the controller namespace.
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
					Namespace: "exam-system", // controller namespace
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
```

**Step 4: Run phase tests to verify they pass**

Run: `go test ./internal/controller/ -v -run "TestCompute|TestExamNamespace|TestEffective|TestDetermineDesiredPhase"`
Expected: PASS

**Step 5: Write integration tests**

Rewrite `internal/controller/exam_controller_test.go`:

```go
package controller

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	corev1 "k8s.io/api/core/v1"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	"github.com/rdrake/exam-controller/internal/network"
	"github.com/rdrake/exam-controller/internal/notifier"
)

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
							Image: "nginx:latest",
							Port:  8080,
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
				// Remove finalizer so it can be deleted in test
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
				Now:            func() time.Time { return time.Now() }, // provision time is 30m from now
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
})
```

**Step 6: Run full controller tests**

Run: `go test ./internal/controller/ -v`
Expected: PASS

**Step 7: Commit**

```bash
git add internal/controller/
git commit -m "feat: rewrite controller with 6-phase state machine, finalizer, and drift correction"
```

---

### Task 11: Update cmd/main.go

**Files:**
- Modify: `cmd/main.go`

**Step 1: Add CRD discovery, PolicyProvider selection, SMTP credential reading, and metrics**

Key changes:
- Add `selectPolicyProvider` function using API discovery for CiliumNetworkPolicy CRD
- Register Prometheus metrics with controller-runtime's built-in registry
- Create an SMTP sender that reads credentials from the cluster at reconcile time (the controller reads the Secret referenced by each Exam's `spec.email.secretRef` — this happens inside the controller, not in main.go, since each Exam can reference a different Secret)
- Wrap the sender in a `RetrySender` with max 3 retries
- Add `--controller-namespace` flag (default `exam-system`) for the `SetupWithManager` watch mapping

```go
// After mgr creation, add:

policyProvider := selectPolicyProvider(mgr)

examMetrics := metrics.NewExamMetrics(crmmetrics.Registry)

if err := (&controller.ExamReconciler{
    Client:         mgr.GetClient(),
    Scheme:         mgr.GetScheme(),
    PolicyProvider: policyProvider,
    Sender:         notifier.NewRetrySender(&notifier.SMTPSender{}, 3), // credentials loaded per-exam at send time
    Metrics:        examMetrics,
}).SetupWithManager(mgr); err != nil {
    setupLog.Error(err, "Failed to create controller", "controller", "Exam")
    os.Exit(1)
}
```

Add the `selectPolicyProvider` function:

```go
func selectPolicyProvider(mgr ctrl.Manager) network.PolicyProvider {
    disc, err := discovery.NewDiscoveryClientForConfig(mgr.GetConfig())
    if err != nil {
        setupLog.Info("Cannot create discovery client, using vanilla NetworkPolicy")
        return &network.VanillaPolicyProvider{}
    }
    resources, err := disc.ServerResourcesForGroupVersion("cilium.io/v2")
    if err == nil {
        for _, r := range resources.APIResources {
            if r.Kind == "CiliumNetworkPolicy" {
                setupLog.Info("CiliumNetworkPolicy CRD detected, using Cilium policy provider")
                return &network.CiliumPolicyProvider{}
            }
        }
    }
    setupLog.Info("CiliumNetworkPolicy CRD not found, using vanilla NetworkPolicy")
    return &network.VanillaPolicyProvider{}
}
```

Add imports:

```go
import (
    crmmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
    "k8s.io/client-go/discovery"

    "github.com/rdrake/exam-controller/internal/metrics"
    "github.com/rdrake/exam-controller/internal/network"
    "github.com/rdrake/exam-controller/internal/notifier"
)
```

Note: SMTP credential loading (reading the Secret, extracting host/port/username/password) should happen inside the controller's email-sending logic, not in main.go. The controller reads `spec.email.secretRef`, fetches the Secret, and configures the `SMTPSender` fields before calling `Send`. This allows each Exam to use different SMTP credentials.

**Step 2: Verify build**

Run: `go build ./cmd/...`
Expected: PASS

**Step 3: Commit**

```bash
git add cmd/
git commit -m "feat: add CRD discovery, metrics registration, and SMTP sender wiring"
```

---

### Task 12: Update Config and Samples

**Files:**
- Rewrite: `config/samples/exam_v1alpha1_exam.yaml`
- Update: `config/webhook/manifests.yaml` (verbs now include `create`)

**Step 1: Rewrite sample manifest**

```yaml
apiVersion: exam.otu.ca/v1alpha1
kind: Exam
metadata:
  labels:
    app.kubernetes.io/name: exam-controller
    app.kubernetes.io/managed-by: kustomize
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
    duration: "2h"
    timeMultiplier: 1.5
    provisionBefore: "1h"
    retention: "24h"
    dryRun:
      before: "5m"
      duration: "2m"
  email:
    before: "30m"
    rateLimit: 1
    instructorEmail: "instructor@ontariotechu.net"
    secretRef: exam-smtp-credentials
    from: "noreply@otu.ca"
    subject: "SOFE4790U - Your Exam Instance"
  students:
    - id: john.smith
      email: john.smith@ontariotechu.net
    - id: jane.doe
      email: jane.doe@ontariotechu.net
  spares: 2
  ingressTLS:
    secretName: exam-wildcard-tls
  domain: exam.otu.ca
```

**Step 2: Regenerate webhook manifests**

Run: `make manifests`
Expected: Webhook manifest updated with `create` verb

**Step 3: Verify all manifests generate**

Run: `make generate manifests`
Expected: PASS

**Step 4: Commit**

```bash
git add config/
git commit -m "chore: update sample manifest and webhook config for v2 schema"
```

---

## Final Verification

After all tasks are complete:

**Step 1: Full build**

Run: `go build ./...`
Expected: PASS

**Step 2: Full test suite**

Run: `make test`
Expected: All tests PASS

**Step 3: Lint**

Run: `make lint` (if golangci-lint is installed)
Expected: PASS or known warnings only

**Step 4: Generate and verify no drift**

Run: `make generate manifests && git diff`
Expected: No uncommitted changes (everything already committed)
