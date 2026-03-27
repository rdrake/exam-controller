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
