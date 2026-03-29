package provisioner

import (
	"testing"

	examv1alpha1 "github.com/rdrake/exam-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	testSlug = "a1b2c3d4"
	testHost = testSlug + ".exam.test.com"
)

func testExam() *examv1alpha1.Exam {
	return &examv1alpha1.Exam{
		ObjectMeta: metav1.ObjectMeta{Name: "test-exam", Namespace: "exam-system"},
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
	dep := Deployment(testExam(), "exam-ns", "alice", testSlug)
	if dep.Name != testSlug {
		t.Errorf("name = %q, want slug-only name", dep.Name)
	}
	if dep.Labels["exam.otu.ca/student"] != "alice" {
		t.Error("expected student label")
	}
	if dep.Labels["exam.otu.ca/slug"] != testSlug {
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

func TestDeployment_SecurityContext(t *testing.T) {
	dep := Deployment(testExam(), "exam-ns", "alice", testSlug)
	ps := dep.Spec.Template.Spec
	sc := ps.Containers[0].SecurityContext

	// RunAsNonRoot=true
	if sc.RunAsNonRoot == nil || !*sc.RunAsNonRoot {
		t.Error("expected RunAsNonRoot=true")
	}

	// AllowPrivilegeEscalation=false
	if sc.AllowPrivilegeEscalation == nil || *sc.AllowPrivilegeEscalation {
		t.Error("expected AllowPrivilegeEscalation=false")
	}

	// Capabilities.Drop=["ALL"]
	if sc.Capabilities == nil {
		t.Fatal("expected Capabilities to be set")
	}
	if len(sc.Capabilities.Drop) != 1 || sc.Capabilities.Drop[0] != "ALL" {
		t.Errorf("Capabilities.Drop = %v, want [ALL]", sc.Capabilities.Drop)
	}

	// SeccompProfile.Type=RuntimeDefault
	if sc.SeccompProfile == nil {
		t.Fatal("expected SeccompProfile to be set")
	}
	if sc.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Errorf("SeccompProfile.Type = %q, want RuntimeDefault", sc.SeccompProfile.Type)
	}

	// AutomountServiceAccountToken=false
	if ps.AutomountServiceAccountToken == nil || *ps.AutomountServiceAccountToken {
		t.Error("expected AutomountServiceAccountToken=false")
	}
}

func TestDeployment_Resources(t *testing.T) {
	exam := testExam()
	dep := Deployment(exam, "exam-ns", "alice", testSlug)
	container := dep.Spec.Template.Spec.Containers[0]

	cpuReq := container.Resources.Requests[corev1.ResourceCPU]
	if cpuReq.Cmp(resource.MustParse("250m")) != 0 {
		t.Errorf("CPU request = %s, want 250m", cpuReq.String())
	}

	memReq := container.Resources.Requests[corev1.ResourceMemory]
	if memReq.Cmp(resource.MustParse("256Mi")) != 0 {
		t.Errorf("Memory request = %s, want 256Mi", memReq.String())
	}
}

func TestDeployment_ImageAndPort(t *testing.T) {
	exam := testExam()
	dep := Deployment(exam, "exam-ns", "alice", testSlug)
	container := dep.Spec.Template.Spec.Containers[0]

	if container.Image != "vuln-app:v1" {
		t.Errorf("Image = %q, want %q", container.Image, "vuln-app:v1")
	}

	if len(container.Ports) != 1 {
		t.Fatalf("expected 1 container port, got %d", len(container.Ports))
	}
	if container.Ports[0].ContainerPort != 8080 {
		t.Errorf("ContainerPort = %d, want 8080", container.Ports[0].ContainerPort)
	}
}

func TestDeployment_Replicas(t *testing.T) {
	dep := Deployment(testExam(), "exam-ns", "alice", testSlug)

	if dep.Spec.Replicas == nil {
		t.Fatal("expected Replicas to be set")
	}
	if *dep.Spec.Replicas != 1 {
		t.Errorf("Replicas = %d, want 1", *dep.Spec.Replicas)
	}
}

func TestDeployment_Selector(t *testing.T) {
	dep := Deployment(testExam(), "exam-ns", "alice", testSlug)

	sel := dep.Spec.Selector
	if sel == nil {
		t.Fatal("expected Selector to be set")
	}
	slug, ok := sel.MatchLabels["exam.otu.ca/slug"]
	if !ok {
		t.Fatal("expected exam.otu.ca/slug in selector matchLabels")
	}
	if slug != testSlug {
		t.Errorf("selector exam.otu.ca/slug = %q, want %q", slug, testSlug)
	}
}

func TestServiceNaming(t *testing.T) {
	svc := Service(testExam(), "exam-ns", "alice", testSlug)
	if svc.Name != testSlug {
		t.Errorf("name = %q, want slug", svc.Name)
	}
}

func TestService_Ports(t *testing.T) {
	svc := Service(testExam(), "exam-ns", "alice", testSlug)

	if len(svc.Spec.Ports) != 1 {
		t.Fatalf("expected 1 service port, got %d", len(svc.Spec.Ports))
	}
	if svc.Spec.Ports[0].Port != 8080 {
		t.Errorf("Port = %d, want 8080", svc.Spec.Ports[0].Port)
	}
}

func TestService_Selector(t *testing.T) {
	svc := Service(testExam(), "exam-ns", "alice", testSlug)

	sel := svc.Spec.Selector
	if len(sel) != 1 {
		t.Fatalf("expected 1 selector entry, got %d", len(sel))
	}
	slug, ok := sel["exam.otu.ca/slug"]
	if !ok {
		t.Fatal("expected exam.otu.ca/slug in service selector")
	}
	if slug != testSlug {
		t.Errorf("selector exam.otu.ca/slug = %q, want %q", slug, testSlug)
	}
}

func TestIngressHost(t *testing.T) {
	ing := Ingress(testExam(), "exam-ns", "alice", testSlug)
	if ing.Name != testSlug {
		t.Errorf("name = %q, want slug", ing.Name)
	}
	host := ing.Spec.Rules[0].Host
	if host != testHost {
		t.Errorf("host = %q, want a1b2c3d4.exam.test.com", host)
	}
	if ing.Spec.TLS[0].SecretName != "tls-secret" {
		t.Error("expected TLS secret reference")
	}
}

func TestIngress_TLS(t *testing.T) {
	ing := Ingress(testExam(), "exam-ns", "alice", testSlug)

	if len(ing.Spec.TLS) != 1 {
		t.Fatalf("expected 1 TLS entry, got %d", len(ing.Spec.TLS))
	}
	tls := ing.Spec.TLS[0]

	// Verify TLS hosts
	if len(tls.Hosts) != 1 {
		t.Fatalf("expected 1 TLS host, got %d", len(tls.Hosts))
	}
	wantHost := testHost
	if tls.Hosts[0] != wantHost {
		t.Errorf("TLS host = %q, want %q", tls.Hosts[0], wantHost)
	}

	// Verify secretName
	if tls.SecretName != "tls-secret" {
		t.Errorf("TLS secretName = %q, want %q", tls.SecretName, "tls-secret")
	}
}

func TestIngress_Rules(t *testing.T) {
	ing := Ingress(testExam(), "exam-ns", "alice", testSlug)

	if len(ing.Spec.Rules) != 1 {
		t.Fatalf("expected 1 ingress rule, got %d", len(ing.Spec.Rules))
	}
	rule := ing.Spec.Rules[0]

	// Verify host
	wantHost := testHost
	if rule.Host != wantHost {
		t.Errorf("rule host = %q, want %q", rule.Host, wantHost)
	}

	// Verify HTTP paths
	if rule.HTTP == nil {
		t.Fatal("expected HTTP rule value to be set")
	}
	if len(rule.HTTP.Paths) != 1 {
		t.Fatalf("expected 1 HTTP path, got %d", len(rule.HTTP.Paths))
	}
	path := rule.HTTP.Paths[0]

	// path = "/"
	if path.Path != "/" {
		t.Errorf("path = %q, want %q", path.Path, "/")
	}

	// pathType = Prefix
	if path.PathType == nil {
		t.Fatal("expected PathType to be set")
	}
	if *path.PathType != networkingv1.PathTypePrefix {
		t.Errorf("pathType = %q, want Prefix", *path.PathType)
	}

	// backend service name = slug
	if path.Backend.Service == nil {
		t.Fatal("expected backend service to be set")
	}
	if path.Backend.Service.Name != testSlug {
		t.Errorf("backend service name = %q, want %q", path.Backend.Service.Name, testSlug)
	}

	// backend port = exam port
	if path.Backend.Service.Port.Number != 8080 {
		t.Errorf("backend service port = %d, want 8080", path.Backend.Service.Port.Number)
	}
}

func TestLabels_WithStudentID(t *testing.T) {
	l := Labels(testExam(), "alice", testSlug)

	if l[LabelExam] != "test-exam" {
		t.Errorf("exam label = %q, want %q", l[LabelExam], "test-exam")
	}
	if l[LabelExamNamespace] != "exam-system" {
		t.Errorf("exam namespace label = %q, want %q", l[LabelExamNamespace], "exam-system")
	}
	if l[LabelSlug] != testSlug {
		t.Errorf("slug label = %q, want %q", l[LabelSlug], testSlug)
	}
	if l[LabelStudent] != "alice" {
		t.Errorf("student label = %q, want %q", l[LabelStudent], "alice")
	}
}

func TestLabels_WithoutStudentID(t *testing.T) {
	l := Labels(testExam(), "", testSlug)

	if l[LabelExam] != "test-exam" {
		t.Errorf("exam label = %q, want %q", l[LabelExam], "test-exam")
	}
	if l[LabelExamNamespace] != "exam-system" {
		t.Errorf("exam namespace label = %q, want %q", l[LabelExamNamespace], "exam-system")
	}
	if l[LabelSlug] != testSlug {
		t.Errorf("slug label = %q, want %q", l[LabelSlug], testSlug)
	}

	// Verify student key is ABSENT (not just empty)
	if _, ok := l[LabelStudent]; ok {
		t.Error("expected student label to be absent when studentID is empty")
	}

	if len(l) != 3 {
		t.Errorf("expected 3 labels, got %d: %v", len(l), l)
	}
}
