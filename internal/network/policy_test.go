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
