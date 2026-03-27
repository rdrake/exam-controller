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
