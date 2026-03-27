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
