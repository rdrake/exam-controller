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
