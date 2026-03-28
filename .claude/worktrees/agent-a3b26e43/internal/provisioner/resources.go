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
