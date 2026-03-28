package provider

import (
	"k8s.io/client-go/discovery"

	"github.com/rdrake/exam-controller/internal/network"
)

// SelectPolicyProvider inspects the cluster API to choose Cilium or vanilla NetworkPolicy.
func SelectPolicyProvider(disc discovery.DiscoveryInterface) network.PolicyProvider {
	if disc == nil {
		return &network.VanillaPolicyProvider{}
	}
	resources, err := disc.ServerResourcesForGroupVersion("cilium.io/v2")
	if err == nil && resources != nil {
		for _, r := range resources.APIResources {
			if r.Kind == "CiliumNetworkPolicy" {
				return &network.CiliumPolicyProvider{}
			}
		}
	}
	return &network.VanillaPolicyProvider{}
}
