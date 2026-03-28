package network

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
)

const (
	testSlug      = "abc123"
	testNamespace = "exam-ns"
	testDenyAll   = testSlug + "-deny-all"
	ingressNginx  = "ingress-nginx"
)

func TestVanillaDenyAll(t *testing.T) {
	p := &VanillaPolicyProvider{}
	obj := p.DenyAll(testNamespace, map[string]string{"exam.otu.ca/slug": testSlug})
	np, ok := obj.(*networkingv1.NetworkPolicy)
	if !ok {
		t.Fatal("expected *NetworkPolicy")
	}
	if np.Namespace != testNamespace {
		t.Errorf("namespace = %q, want %q", np.Namespace, testNamespace)
	}
	if np.Name != testDenyAll {
		t.Errorf("name = %q, want %q", np.Name, testDenyAll)
	}
	if len(np.Spec.PolicyTypes) != 2 {
		t.Fatalf("policyTypes len = %d, want 2", len(np.Spec.PolicyTypes))
	}
}

func TestVanillaDenyAll_Fields(t *testing.T) {
	p := &VanillaPolicyProvider{}
	obj := p.DenyAll(testNamespace, map[string]string{"exam.otu.ca/slug": testSlug})
	np := obj.(*networkingv1.NetworkPolicy)

	// Name
	if np.Name != testDenyAll {
		t.Errorf("name = %q, want %q", np.Name, testDenyAll)
	}

	// PodSelector.MatchLabels
	ml := np.Spec.PodSelector.MatchLabels
	if ml == nil {
		t.Fatal("PodSelector.MatchLabels is nil")
	}
	if ml["exam.otu.ca/slug"] != testSlug {
		t.Errorf("PodSelector.MatchLabels[exam.otu.ca/slug] = %q, want %q", ml["exam.otu.ca/slug"], testSlug)
	}
	if len(ml) != 1 {
		t.Errorf("PodSelector.MatchLabels has %d entries, want 1", len(ml))
	}

	// PolicyTypes = [Ingress, Egress]
	if len(np.Spec.PolicyTypes) != 2 {
		t.Fatalf("PolicyTypes len = %d, want 2", len(np.Spec.PolicyTypes))
	}
	if np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress {
		t.Errorf("PolicyTypes[0] = %q, want %q", np.Spec.PolicyTypes[0], networkingv1.PolicyTypeIngress)
	}
	if np.Spec.PolicyTypes[1] != networkingv1.PolicyTypeEgress {
		t.Errorf("PolicyTypes[1] = %q, want %q", np.Spec.PolicyTypes[1], networkingv1.PolicyTypeEgress)
	}

	// Ingress is empty
	if len(np.Spec.Ingress) != 0 {
		t.Errorf("Ingress len = %d, want 0", len(np.Spec.Ingress))
	}

	// Egress is empty
	if len(np.Spec.Egress) != 0 {
		t.Errorf("Egress len = %d, want 0", len(np.Spec.Egress))
	}
}

func TestVanillaEgressAllowlist(t *testing.T) {
	p := &VanillaPolicyProvider{}
	obj := p.EgressAllowlist(testNamespace, map[string]string{"exam.otu.ca/slug": testSlug})
	np := obj.(*networkingv1.NetworkPolicy)
	if np.Name != testSlug+"-egress-allow" {
		t.Errorf("name = %q, want %q", np.Name, testSlug+"-egress-allow")
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

func TestVanillaEgressAllowlist_Fields(t *testing.T) {
	p := &VanillaPolicyProvider{}
	obj := p.EgressAllowlist(testNamespace, map[string]string{"exam.otu.ca/slug": testSlug})
	np := obj.(*networkingv1.NetworkPolicy)

	// PolicyTypes = [Egress] only
	if len(np.Spec.PolicyTypes) != 1 {
		t.Fatalf("PolicyTypes len = %d, want 1", len(np.Spec.PolicyTypes))
	}
	if np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeEgress {
		t.Errorf("PolicyTypes[0] = %q, want %q", np.Spec.PolicyTypes[0], networkingv1.PolicyTypeEgress)
	}

	if len(np.Spec.Egress) != 1 {
		t.Fatalf("egress rules = %d, want 1", len(np.Spec.Egress))
	}
	rule := np.Spec.Egress[0]

	// Two ports: 53/UDP and 53/TCP
	if len(rule.Ports) != 2 {
		t.Fatalf("egress ports = %d, want 2", len(rule.Ports))
	}
	port0 := rule.Ports[0]
	if port0.Port.IntValue() != 53 {
		t.Errorf("port[0] = %d, want 53", port0.Port.IntValue())
	}
	if *port0.Protocol != corev1.ProtocolUDP {
		t.Errorf("port[0] protocol = %q, want %q", *port0.Protocol, corev1.ProtocolUDP)
	}
	port1 := rule.Ports[1]
	if port1.Port.IntValue() != 53 {
		t.Errorf("port[1] = %d, want 53", port1.Port.IntValue())
	}
	if *port1.Protocol != corev1.ProtocolTCP {
		t.Errorf("port[1] protocol = %q, want %q", *port1.Protocol, corev1.ProtocolTCP)
	}

	// Namespace selector
	if len(rule.To) != 1 {
		t.Fatalf("egress to = %d, want 1", len(rule.To))
	}
	peer := rule.To[0]
	if peer.NamespaceSelector == nil {
		t.Fatal("NamespaceSelector is nil")
	}
	if peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "kube-system" {
		t.Errorf("NamespaceSelector = %v, want kubernetes.io/metadata.name=kube-system",
			peer.NamespaceSelector.MatchLabels)
	}

	// Pod selector
	if peer.PodSelector == nil {
		t.Fatal("PodSelector is nil")
	}
	if peer.PodSelector.MatchLabels["k8s-app"] != "kube-dns" {
		t.Errorf("PodSelector = %v, want k8s-app=kube-dns", peer.PodSelector.MatchLabels)
	}
}

func TestVanillaIngressAllow(t *testing.T) {
	p := &VanillaPolicyProvider{}
	obj := p.IngressAllow(testNamespace, map[string]string{"exam.otu.ca/slug": testSlug, "exam.otu.ca/port": "9090"})
	np := obj.(*networkingv1.NetworkPolicy)
	if np.Name != testSlug+"-ingress-allow" {
		t.Errorf("name = %q, want %q", np.Name, testSlug+"-ingress-allow")
	}
	if len(np.Spec.Ingress) != 1 {
		t.Fatalf("ingress rules = %d, want 1", len(np.Spec.Ingress))
	}
	rule := np.Spec.Ingress[0]
	from := rule.From
	if len(from) != 1 {
		t.Fatalf("from = %d, want 1", len(from))
	}
	if from[0].NamespaceSelector == nil || from[0].PodSelector == nil {
		t.Error("expected both namespaceSelector and podSelector")
	}
	if len(rule.Ports) != 1 {
		t.Fatalf("ports = %d, want 1", len(rule.Ports))
	}
	if rule.Ports[0].Port.String() != "9090" {
		t.Errorf("port = %q, want 9090", rule.Ports[0].Port.String())
	}
}

func TestVanillaIngressAllow_Fields(t *testing.T) {
	p := &VanillaPolicyProvider{}
	obj := p.IngressAllow(testNamespace, map[string]string{"exam.otu.ca/slug": testSlug, "exam.otu.ca/port": "9090"})
	np := obj.(*networkingv1.NetworkPolicy)

	// PolicyTypes = [Ingress] only
	if len(np.Spec.PolicyTypes) != 1 {
		t.Fatalf("PolicyTypes len = %d, want 1", len(np.Spec.PolicyTypes))
	}
	if np.Spec.PolicyTypes[0] != networkingv1.PolicyTypeIngress {
		t.Errorf("PolicyTypes[0] = %q, want %q", np.Spec.PolicyTypes[0], networkingv1.PolicyTypeIngress)
	}

	if len(np.Spec.Ingress) != 1 {
		t.Fatalf("ingress rules = %d, want 1", len(np.Spec.Ingress))
	}
	rule := np.Spec.Ingress[0]

	// Port matches label value, protocol is TCP
	if len(rule.Ports) != 1 {
		t.Fatalf("ports = %d, want 1", len(rule.Ports))
	}
	if rule.Ports[0].Port.String() != "9090" {
		t.Errorf("port = %q, want %q", rule.Ports[0].Port.String(), "9090")
	}
	if *rule.Ports[0].Protocol != corev1.ProtocolTCP {
		t.Errorf("protocol = %q, want %q", *rule.Ports[0].Protocol, corev1.ProtocolTCP)
	}

	// Namespace selector: ingress-nginx
	if len(rule.From) != 1 {
		t.Fatalf("from = %d, want 1", len(rule.From))
	}
	peer := rule.From[0]
	if peer.NamespaceSelector == nil {
		t.Fatal("NamespaceSelector is nil")
	}
	if peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != ingressNginx {
		t.Errorf("NamespaceSelector = %v, want kubernetes.io/metadata.name=ingress-nginx",
			peer.NamespaceSelector.MatchLabels)
	}

	// Pod selector: ingress-nginx
	if peer.PodSelector == nil {
		t.Fatal("PodSelector is nil")
	}
	if peer.PodSelector.MatchLabels["app.kubernetes.io/name"] != ingressNginx {
		t.Errorf("PodSelector = %v, want app.kubernetes.io/name=ingress-nginx",
			peer.PodSelector.MatchLabels)
	}
}

func TestVanillaIngressAllow_DefaultPort(t *testing.T) {
	p := &VanillaPolicyProvider{}
	// Labels WITHOUT exam.otu.ca/port key
	obj := p.IngressAllow(testNamespace, map[string]string{"exam.otu.ca/slug": testSlug})
	np := obj.(*networkingv1.NetworkPolicy)

	if len(np.Spec.Ingress) != 1 {
		t.Fatalf("ingress rules = %d, want 1", len(np.Spec.Ingress))
	}
	rule := np.Spec.Ingress[0]
	if len(rule.Ports) != 1 {
		t.Fatalf("ports = %d, want 1", len(rule.Ports))
	}
	if rule.Ports[0].Port.String() != "8080" {
		t.Errorf("default port = %q, want %q", rule.Ports[0].Port.String(), "8080")
	}
}

func TestVanillaIngressAllow_CustomPort(t *testing.T) {
	p := &VanillaPolicyProvider{}
	obj := p.IngressAllow(testNamespace, map[string]string{"exam.otu.ca/slug": testSlug, "exam.otu.ca/port": "9090"})
	np := obj.(*networkingv1.NetworkPolicy)

	if len(np.Spec.Ingress) != 1 {
		t.Fatalf("ingress rules = %d, want 1", len(np.Spec.Ingress))
	}
	rule := np.Spec.Ingress[0]
	if len(rule.Ports) != 1 {
		t.Fatalf("ports = %d, want 1", len(rule.Ports))
	}
	if rule.Ports[0].Port.String() != "9090" {
		t.Errorf("custom port = %q, want %q", rule.Ports[0].Port.String(), "9090")
	}
}

func TestPolicyProviderInterface(t *testing.T) {
	// Compile-time check that VanillaPolicyProvider implements PolicyProvider
	var _ PolicyProvider = &VanillaPolicyProvider{}
}
