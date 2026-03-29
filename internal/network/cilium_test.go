package network

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCiliumDenyAll(t *testing.T) {
	p := &CiliumPolicyProvider{}
	obj := p.DenyAll(testNamespace, map[string]string{"exam.otu.ca/slug": testSlug})
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
	if u.GetName() != testDenyAll {
		t.Errorf("name = %q, want %s", u.GetName(), testDenyAll)
	}
}

func TestCiliumDenyAll_Fields(t *testing.T) {
	p := &CiliumPolicyProvider{}
	obj := p.DenyAll(testNamespace, map[string]string{"exam.otu.ca/slug": testSlug})
	u := obj.(*unstructured.Unstructured)
	spec := u.Object["spec"].(map[string]any)

	// endpointSelector.matchLabels
	es := spec["endpointSelector"].(map[string]any)
	ml := es["matchLabels"].(map[string]any)
	if ml["exam.otu.ca/slug"] != testSlug {
		t.Errorf("endpointSelector.matchLabels[exam.otu.ca/slug] = %v, want %q", ml["exam.otu.ca/slug"], testSlug)
	}

	// ingressDeny array present
	ingressDeny, ok := spec["ingressDeny"].([]any)
	if !ok {
		t.Fatal("ingressDeny is not a []interface{}")
	}
	if len(ingressDeny) == 0 {
		t.Error("ingressDeny array is empty, want at least one entry")
	}

	// egressDeny array present
	egressDeny, ok := spec["egressDeny"].([]any)
	if !ok {
		t.Fatal("egressDeny is not a []interface{}")
	}
	if len(egressDeny) == 0 {
		t.Error("egressDeny array is empty, want at least one entry")
	}
}

func TestCiliumEgressAllowlist(t *testing.T) {
	p := &CiliumPolicyProvider{}
	obj := p.EgressAllowlist(testNamespace, map[string]string{"exam.otu.ca/slug": testSlug})
	u := obj.(*unstructured.Unstructured)
	if u.GetName() != testSlug+"-egress-allow" {
		t.Errorf("name = %q, want %s", u.GetName(), testSlug+"-egress-allow")
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
	obj := p.EgressAllowlist(testNamespace, map[string]string{"exam.otu.ca/slug": testSlug})
	u := obj.(*unstructured.Unstructured)
	spec := u.Object["spec"].(map[string]any)

	egress := spec["egress"].([]any)
	if len(egress) != 1 {
		t.Fatalf("egress rules = %d, want 1", len(egress))
	}
	rule := egress[0].(map[string]any)

	// toFQDNs contains matchPattern: *.cluster.local
	toFQDNs := rule["toFQDNs"].([]any)
	if len(toFQDNs) == 0 {
		t.Fatal("toFQDNs is empty")
	}
	fqdn := toFQDNs[0].(map[string]any)
	if fqdn["matchPattern"] != "*.cluster.local" {
		t.Errorf("toFQDNs[0].matchPattern = %v, want %q", fqdn["matchPattern"], "*.cluster.local")
	}

	// toPorts contains port 53 UDP/TCP
	toPorts := rule["toPorts"].([]any)
	if len(toPorts) == 0 {
		t.Fatal("toPorts is empty")
	}
	portRule := toPorts[0].(map[string]any)
	ports := portRule["ports"].([]any)
	if len(ports) != 2 {
		t.Fatalf("ports len = %d, want 2", len(ports))
	}

	p0 := ports[0].(map[string]any)
	if p0["port"] != "53" {
		t.Errorf("port[0] = %v, want %q", p0["port"], "53")
	}
	if p0["protocol"] != "UDP" {
		t.Errorf("port[0] protocol = %v, want %q", p0["protocol"], "UDP")
	}

	p1 := ports[1].(map[string]any)
	if p1["port"] != "53" {
		t.Errorf("port[1] = %v, want %q", p1["port"], "53")
	}
	if p1["protocol"] != "TCP" {
		t.Errorf("port[1] protocol = %v, want %q", p1["protocol"], "TCP")
	}
}

func TestCiliumIngressAllow(t *testing.T) {
	p := &CiliumPolicyProvider{}
	obj := p.IngressAllow(testNamespace, map[string]string{"exam.otu.ca/slug": testSlug})
	u := obj.(*unstructured.Unstructured)
	if u.GetName() != testSlug+"-ingress-allow" {
		t.Errorf("name = %q, want %s", u.GetName(), testSlug+"-ingress-allow")
	}
	ingress, _, _ := unstructured.NestedSlice(u.Object, "spec", "ingress")
	if len(ingress) == 0 {
		t.Error("expected ingress rules with L7 visibility")
	}
}

func TestCiliumIngressAllow_Fields(t *testing.T) {
	p := &CiliumPolicyProvider{}
	obj := p.IngressAllow(testNamespace, map[string]string{"exam.otu.ca/slug": testSlug, "exam.otu.ca/port": "9090"})
	u := obj.(*unstructured.Unstructured)
	spec := u.Object["spec"].(map[string]any)

	ingress := spec["ingress"].([]any)
	if len(ingress) != 1 {
		t.Fatalf("ingress rules = %d, want 1", len(ingress))
	}
	rule := ingress[0].(map[string]any)

	// fromEndpoints matchLabels include ingress-nginx labels
	fromEndpoints := rule["fromEndpoints"].([]any)
	if len(fromEndpoints) == 0 {
		t.Fatal("fromEndpoints is empty")
	}
	ep := fromEndpoints[0].(map[string]any)
	ml := ep["matchLabels"].(map[string]any)
	if ml["k8s:io.kubernetes.pod.namespace"] != ingressNginx {
		t.Errorf("fromEndpoints namespace = %v, want %q", ml["k8s:io.kubernetes.pod.namespace"], ingressNginx)
	}
	if ml["app.kubernetes.io/name"] != ingressNginx {
		t.Errorf("fromEndpoints app label = %v, want %q", ml["app.kubernetes.io/name"], ingressNginx)
	}

	// toPorts rules contain HTTP method matching (rules.http)
	toPorts := rule["toPorts"].([]any)
	if len(toPorts) == 0 {
		t.Fatal("toPorts is empty")
	}
	portRule := toPorts[0].(map[string]any)

	// Port matches label value
	ports := portRule["ports"].([]any)
	if len(ports) != 1 {
		t.Fatalf("ports len = %d, want 1", len(ports))
	}
	p0 := ports[0].(map[string]any)
	if p0["port"] != "9090" {
		t.Errorf("port = %v, want %q", p0["port"], "9090")
	}
	if p0["protocol"] != "TCP" {
		t.Errorf("protocol = %v, want %q", p0["protocol"], "TCP")
	}

	// rules.http present
	rules := portRule["rules"].(map[string]any)
	httpRules := rules["http"].([]any)
	if len(httpRules) == 0 {
		t.Error("expected http rules for L7 visibility")
	}
	// Rule should be an empty map (match all methods)
	httpRule := httpRules[0].(map[string]any)
	if len(httpRule) != 0 {
		t.Errorf("expected empty http rule (match all), got %v", httpRule)
	}
}

func TestCiliumIngressAllow_DefaultPort(t *testing.T) {
	p := &CiliumPolicyProvider{}
	// Labels WITHOUT exam.otu.ca/port key
	obj := p.IngressAllow(testNamespace, map[string]string{"exam.otu.ca/slug": testSlug})
	u := obj.(*unstructured.Unstructured)
	spec := u.Object["spec"].(map[string]any)

	ingress := spec["ingress"].([]any)
	if len(ingress) != 1 {
		t.Fatalf("ingress rules = %d, want 1", len(ingress))
	}
	rule := ingress[0].(map[string]any)
	toPorts := rule["toPorts"].([]any)
	portRule := toPorts[0].(map[string]any)
	ports := portRule["ports"].([]any)
	p0 := ports[0].(map[string]any)
	if p0["port"] != "8080" {
		t.Errorf("default port = %v, want %q", p0["port"], "8080")
	}
}

func TestCiliumImplementsProvider(t *testing.T) {
	var _ PolicyProvider = &CiliumPolicyProvider{}
}
