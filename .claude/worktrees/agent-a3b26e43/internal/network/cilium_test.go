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

func TestCiliumDenyAll_Fields(t *testing.T) {
	p := &CiliumPolicyProvider{}
	obj := p.DenyAll("exam-ns", map[string]string{"exam.otu.ca/slug": "abc123"})
	u := obj.(*unstructured.Unstructured)
	spec := u.Object["spec"].(map[string]interface{})

	// endpointSelector.matchLabels
	es := spec["endpointSelector"].(map[string]interface{})
	ml := es["matchLabels"].(map[string]interface{})
	if ml["exam.otu.ca/slug"] != "abc123" {
		t.Errorf("endpointSelector.matchLabels[exam.otu.ca/slug] = %v, want %q", ml["exam.otu.ca/slug"], "abc123")
	}

	// ingressDeny array present
	ingressDeny, ok := spec["ingressDeny"].([]interface{})
	if !ok {
		t.Fatal("ingressDeny is not a []interface{}")
	}
	if len(ingressDeny) == 0 {
		t.Error("ingressDeny array is empty, want at least one entry")
	}

	// egressDeny array present
	egressDeny, ok := spec["egressDeny"].([]interface{})
	if !ok {
		t.Fatal("egressDeny is not a []interface{}")
	}
	if len(egressDeny) == 0 {
		t.Error("egressDeny array is empty, want at least one entry")
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

func TestCiliumEgressAllowlist_Fields(t *testing.T) {
	p := &CiliumPolicyProvider{}
	obj := p.EgressAllowlist("exam-ns", map[string]string{"exam.otu.ca/slug": "abc123"})
	u := obj.(*unstructured.Unstructured)
	spec := u.Object["spec"].(map[string]interface{})

	egress := spec["egress"].([]interface{})
	if len(egress) != 1 {
		t.Fatalf("egress rules = %d, want 1", len(egress))
	}
	rule := egress[0].(map[string]interface{})

	// toFQDNs contains matchPattern: *.cluster.local
	toFQDNs := rule["toFQDNs"].([]interface{})
	if len(toFQDNs) == 0 {
		t.Fatal("toFQDNs is empty")
	}
	fqdn := toFQDNs[0].(map[string]interface{})
	if fqdn["matchPattern"] != "*.cluster.local" {
		t.Errorf("toFQDNs[0].matchPattern = %v, want %q", fqdn["matchPattern"], "*.cluster.local")
	}

	// toPorts contains port 53 UDP/TCP
	toPorts := rule["toPorts"].([]interface{})
	if len(toPorts) == 0 {
		t.Fatal("toPorts is empty")
	}
	portRule := toPorts[0].(map[string]interface{})
	ports := portRule["ports"].([]interface{})
	if len(ports) != 2 {
		t.Fatalf("ports len = %d, want 2", len(ports))
	}

	p0 := ports[0].(map[string]interface{})
	if p0["port"] != "53" {
		t.Errorf("port[0] = %v, want %q", p0["port"], "53")
	}
	if p0["protocol"] != "UDP" {
		t.Errorf("port[0] protocol = %v, want %q", p0["protocol"], "UDP")
	}

	p1 := ports[1].(map[string]interface{})
	if p1["port"] != "53" {
		t.Errorf("port[1] = %v, want %q", p1["port"], "53")
	}
	if p1["protocol"] != "TCP" {
		t.Errorf("port[1] protocol = %v, want %q", p1["protocol"], "TCP")
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

func TestCiliumIngressAllow_Fields(t *testing.T) {
	p := &CiliumPolicyProvider{}
	obj := p.IngressAllow("exam-ns", map[string]string{"exam.otu.ca/slug": "abc123", "exam.otu.ca/port": "9090"})
	u := obj.(*unstructured.Unstructured)
	spec := u.Object["spec"].(map[string]interface{})

	ingress := spec["ingress"].([]interface{})
	if len(ingress) != 1 {
		t.Fatalf("ingress rules = %d, want 1", len(ingress))
	}
	rule := ingress[0].(map[string]interface{})

	// fromEndpoints matchLabels include ingress-nginx labels
	fromEndpoints := rule["fromEndpoints"].([]interface{})
	if len(fromEndpoints) == 0 {
		t.Fatal("fromEndpoints is empty")
	}
	ep := fromEndpoints[0].(map[string]interface{})
	ml := ep["matchLabels"].(map[string]interface{})
	if ml["k8s:io.kubernetes.pod.namespace"] != "ingress-nginx" {
		t.Errorf("fromEndpoints namespace = %v, want %q", ml["k8s:io.kubernetes.pod.namespace"], "ingress-nginx")
	}
	if ml["app.kubernetes.io/name"] != "ingress-nginx" {
		t.Errorf("fromEndpoints app label = %v, want %q", ml["app.kubernetes.io/name"], "ingress-nginx")
	}

	// toPorts rules contain HTTP method matching (rules.http)
	toPorts := rule["toPorts"].([]interface{})
	if len(toPorts) == 0 {
		t.Fatal("toPorts is empty")
	}
	portRule := toPorts[0].(map[string]interface{})

	// Port matches label value
	ports := portRule["ports"].([]interface{})
	if len(ports) != 1 {
		t.Fatalf("ports len = %d, want 1", len(ports))
	}
	p0 := ports[0].(map[string]interface{})
	if p0["port"] != "9090" {
		t.Errorf("port = %v, want %q", p0["port"], "9090")
	}
	if p0["protocol"] != "TCP" {
		t.Errorf("protocol = %v, want %q", p0["protocol"], "TCP")
	}

	// rules.http present
	rules := portRule["rules"].(map[string]interface{})
	httpRules := rules["http"].([]interface{})
	if len(httpRules) == 0 {
		t.Error("expected http rules for L7 visibility")
	}
	httpRule := httpRules[0].(map[string]interface{})
	if _, exists := httpRule["method"]; !exists {
		t.Error("expected method key in http rule")
	}
}

func TestCiliumIngressAllow_DefaultPort(t *testing.T) {
	p := &CiliumPolicyProvider{}
	// Labels WITHOUT exam.otu.ca/port key
	obj := p.IngressAllow("exam-ns", map[string]string{"exam.otu.ca/slug": "abc123"})
	u := obj.(*unstructured.Unstructured)
	spec := u.Object["spec"].(map[string]interface{})

	ingress := spec["ingress"].([]interface{})
	if len(ingress) != 1 {
		t.Fatalf("ingress rules = %d, want 1", len(ingress))
	}
	rule := ingress[0].(map[string]interface{})
	toPorts := rule["toPorts"].([]interface{})
	portRule := toPorts[0].(map[string]interface{})
	ports := portRule["ports"].([]interface{})
	p0 := ports[0].(map[string]interface{})
	if p0["port"] != "8080" {
		t.Errorf("default port = %v, want %q", p0["port"], "8080")
	}
}

func TestCiliumImplementsProvider(t *testing.T) {
	var _ PolicyProvider = &CiliumPolicyProvider{}
}
