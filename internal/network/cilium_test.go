package network

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCiliumDenyAll(t *testing.T) {
	p := &CiliumPolicyProvider{}
	obj := p.DenyAll("exam-ns", map[string]string{"exam.otu.ca/slug": "abc123"})
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		t.Fatal("expected *Unstructured")
	}
	if u.GetKind() != "CiliumNetworkPolicy" {
		t.Errorf("kind = %q, want CiliumNetworkPolicy", u.GetKind())
	}
	if u.GetAPIVersion() != "cilium.io/v2" {
		t.Errorf("apiVersion = %q, want cilium.io/v2", u.GetAPIVersion())
	}
	if u.GetName() != "abc123-deny-all" {
		t.Errorf("name = %q, want abc123-deny-all", u.GetName())
	}
}

func TestCiliumEgressAllowlist(t *testing.T) {
	p := &CiliumPolicyProvider{}
	obj := p.EgressAllowlist("exam-ns", map[string]string{"exam.otu.ca/slug": "abc123"})
	u := obj.(*unstructured.Unstructured)
	if u.GetName() != "abc123-egress-allow" {
		t.Errorf("name = %q, want abc123-egress-allow", u.GetName())
	}
	spec, _, _ := unstructured.NestedMap(u.Object, "spec")
	if spec == nil {
		t.Fatal("expected spec")
	}
	egress, _, _ := unstructured.NestedSlice(u.Object, "spec", "egress")
	if len(egress) == 0 {
		t.Error("expected egress rules")
	}
}

func TestCiliumIngressAllow(t *testing.T) {
	p := &CiliumPolicyProvider{}
	obj := p.IngressAllow("exam-ns", map[string]string{"exam.otu.ca/slug": "abc123"})
	u := obj.(*unstructured.Unstructured)
	if u.GetName() != "abc123-ingress-allow" {
		t.Errorf("name = %q, want abc123-ingress-allow", u.GetName())
	}
	ingress, _, _ := unstructured.NestedSlice(u.Object, "spec", "ingress")
	if len(ingress) == 0 {
		t.Error("expected ingress rules with L7 visibility")
	}
}

func TestCiliumImplementsProvider(t *testing.T) {
	var _ PolicyProvider = &CiliumPolicyProvider{}
}
