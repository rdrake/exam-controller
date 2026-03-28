package network

import (
	"strconv"

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
	portStr := labels["exam.otu.ca/port"]
	if portStr == "" {
		portStr = "8080"
	}
	portNum, _ := strconv.Atoi(portStr)
	portVal := intstr.FromInt32(int32(portNum))
	tcp := corev1.ProtocolTCP
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
					Ports: []networkingv1.NetworkPolicyPort{
						{Port: &portVal, Protocol: &tcp},
					},
				},
			},
		},
	}
}
