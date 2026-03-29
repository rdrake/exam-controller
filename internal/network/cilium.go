package network

import (
	"github.com/rdrake/exam-controller/internal/provisioner"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var ciliumGVK = schema.GroupVersionKind{
	Group:   "cilium.io",
	Version: "v2",
	Kind:    "CiliumNetworkPolicy",
}

var ciliumListGVK = schema.GroupVersionKind{
	Group:   "cilium.io",
	Version: "v2",
	Kind:    "CiliumNetworkPolicyList",
}

// CiliumPolicyProvider creates CiliumNetworkPolicy resources with L7 visibility.
type CiliumPolicyProvider struct{}

func NewCiliumPolicyObject() *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetGroupVersionKind(ciliumGVK)
	return u
}

func NewCiliumPolicyList() *unstructured.UnstructuredList {
	l := &unstructured.UnstructuredList{}
	l.SetGroupVersionKind(ciliumListGVK)
	return l
}

func ciliumPolicy(name, namespace string, labels map[string]string, spec map[string]any) *unstructured.Unstructured {
	u := NewCiliumPolicyObject()
	u.SetName(name)
	u.SetNamespace(namespace)
	u.SetLabels(labels)
	u.Object["spec"] = spec
	return u
}

func (c *CiliumPolicyProvider) DenyAll(namespace string, labels map[string]string) client.Object {
	slug := slugFromLabels(labels)
	return ciliumPolicy(slug+"-deny-all", namespace, labels, map[string]any{
		"endpointSelector": map[string]any{
			"matchLabels": map[string]any{provisioner.LabelSlug: slug},
		},
		"ingressDeny": []any{map[string]any{}},
		"egressDeny":  []any{map[string]any{}},
	})
}

func (c *CiliumPolicyProvider) EgressAllowlist(namespace string, labels map[string]string) client.Object {
	slug := slugFromLabels(labels)
	return ciliumPolicy(slug+"-egress-allow", namespace, labels, map[string]any{
		"endpointSelector": map[string]any{
			"matchLabels": map[string]any{provisioner.LabelSlug: slug},
		},
		"egress": []any{
			map[string]any{
				"toFQDNs": []any{
					map[string]any{"matchPattern": "*.cluster.local"},
				},
				"toPorts": []any{
					map[string]any{
						"ports": []any{
							map[string]any{"port": "53", "protocol": "UDP"},
							map[string]any{"port": "53", "protocol": "TCP"},
						},
					},
				},
			},
		},
	})
}

func (c *CiliumPolicyProvider) IngressAllow(namespace string, labels map[string]string) client.Object {
	slug := slugFromLabels(labels)
	port := portFromLabels(labels)
	return ciliumPolicy(slug+"-ingress-allow", namespace, labels, map[string]any{
		"endpointSelector": map[string]any{
			"matchLabels": map[string]any{provisioner.LabelSlug: slug},
		},
		"ingress": []any{
			map[string]any{
				"fromEndpoints": []any{
					map[string]any{
						"matchLabels": map[string]any{
							"k8s:io.kubernetes.pod.namespace": "ingress-nginx",
							"app.kubernetes.io/name":          "ingress-nginx",
						},
					},
				},
				"toPorts": []any{
					map[string]any{
						"ports": []any{
							map[string]any{"port": port, "protocol": "TCP"},
						},
						"rules": map[string]any{
							"http": []any{
								map[string]any{}, // match all methods
							},
						},
					},
				},
			},
		},
	})
}
