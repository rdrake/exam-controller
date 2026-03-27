package network

import (
	"testing"
)

func TestDenyAllPolicy(t *testing.T) {
	p := DenyAllPolicy("exam-ns", "exam-midterm", "john.smith")
	if p.Name != "john.smith-deny-all" {
		t.Errorf("unexpected name: %s", p.Name)
	}
	if p.Namespace != "exam-ns" {
		t.Errorf("unexpected namespace: %s", p.Namespace)
	}
	if len(p.Spec.PolicyTypes) != 2 {
		t.Errorf("expected 2 policy types (Ingress+Egress), got %d", len(p.Spec.PolicyTypes))
	}
	if len(p.Spec.Ingress) != 0 {
		t.Errorf("expected no ingress rules, got %d", len(p.Spec.Ingress))
	}
	if len(p.Spec.Egress) != 0 {
		t.Errorf("expected no egress rules, got %d", len(p.Spec.Egress))
	}
}

func TestEgressAllowlistPolicy(t *testing.T) {
	p := EgressAllowlistPolicy("exam-ns", "exam-midterm", "john.smith", "kube-system")
	if p.Name != "john.smith-egress-allow" {
		t.Errorf("unexpected name: %s", p.Name)
	}
	if len(p.Spec.Egress) == 0 {
		t.Fatal("expected at least one egress rule")
	}
}

func TestIngressAllowPolicy(t *testing.T) {
	ingressNS := "ingress-nginx"
	ingressLabels := map[string]string{"app.kubernetes.io/name": "ingress-nginx"}
	p := IngressAllowPolicy("exam-ns", "exam-midterm", "john.smith", ingressNS, ingressLabels)
	if p.Name != "john.smith-ingress-allow" {
		t.Errorf("unexpected name: %s", p.Name)
	}
	if len(p.Spec.Ingress) != 1 {
		t.Fatalf("expected 1 ingress rule, got %d", len(p.Spec.Ingress))
	}
	from := p.Spec.Ingress[0].From
	if len(from) != 1 {
		t.Fatalf("expected 1 from peer, got %d", len(from))
	}
	if from[0].NamespaceSelector == nil {
		t.Error("expected namespace selector")
	}
}
