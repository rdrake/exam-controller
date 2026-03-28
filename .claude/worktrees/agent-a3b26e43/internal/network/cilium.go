package network

import (
	"fmt"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var ciliumGVK = schema.GroupVersionKind{
	Group:   "cilium.io",
	Version: "v2",
	Kind:    "CiliumNetworkPolicy",
}

// CiliumPolicyProvider creates CiliumNetworkPolicy resources with L7 visibility.
type CiliumPolicyProvider struct{}

func ciliumPolicy(name, namespace string, labels map[string]string, spec map[string]interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ciliumGVK)
	u.SetName(name)
	u.SetNamespace(namespace)
	u.SetLabels(labels)
	u.Object["spec"] = spec
	return u
}

func (c *CiliumPolicyProvider) DenyAll(namespace string, labels map[string]string) client.Object {
	slug := slugFromLabels(labels)
	return ciliumPolicy(slug+"-deny-all", namespace, labels, map[string]interface{}{
		"endpointSelector": map[string]interface{}{
			"matchLabels": map[string]interface{}{"exam.otu.ca/slug": slug},
		},
		"ingressDeny": []interface{}{map[string]interface{}{}},
		"egressDeny":  []interface{}{map[string]interface{}{}},
	})
}

func (c *CiliumPolicyProvider) EgressAllowlist(namespace string, labels map[string]string) client.Object {
	slug := slugFromLabels(labels)
	return ciliumPolicy(slug+"-egress-allow", namespace, labels, map[string]interface{}{
		"endpointSelector": map[string]interface{}{
			"matchLabels": map[string]interface{}{"exam.otu.ca/slug": slug},
		},
		"egress": []interface{}{
			map[string]interface{}{
				"toFQDNs": []interface{}{
					map[string]interface{}{"matchPattern": "*.cluster.local"},
				},
				"toPorts": []interface{}{
					map[string]interface{}{
						"ports": []interface{}{
							map[string]interface{}{"port": "53", "protocol": "UDP"},
							map[string]interface{}{"port": "53", "protocol": "TCP"},
						},
					},
				},
			},
		},
	})
}

func (c *CiliumPolicyProvider) IngressAllow(namespace string, labels map[string]string) client.Object {
	slug := slugFromLabels(labels)
	port := labels["exam.otu.ca/port"] // set by controller when creating labels
	if port == "" {
		port = "8080"
	}
	return ciliumPolicy(slug+"-ingress-allow", namespace, labels, map[string]interface{}{
		"endpointSelector": map[string]interface{}{
			"matchLabels": map[string]interface{}{"exam.otu.ca/slug": slug},
		},
		"ingress": []interface{}{
			map[string]interface{}{
				"fromEndpoints": []interface{}{
					map[string]interface{}{
						"matchLabels": map[string]interface{}{
							"k8s:io.kubernetes.pod.namespace": "ingress-nginx",
							"app.kubernetes.io/name":          "ingress-nginx",
						},
					},
				},
				"toPorts": []interface{}{
					map[string]interface{}{
						"ports": []interface{}{
							map[string]interface{}{"port": fmt.Sprintf("%s", port), "protocol": "TCP"},
						},
						"rules": map[string]interface{}{
							"http": []interface{}{
								map[string]interface{}{"method": ""},
							},
						},
					},
				},
			},
		},
	})
}
