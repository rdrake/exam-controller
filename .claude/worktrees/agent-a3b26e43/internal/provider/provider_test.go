package provider

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"

	"github.com/rdrake/exam-controller/internal/network"
)

func TestSelectPolicyProvider_CiliumAvailable(t *testing.T) {
	client := fakeclientset.NewSimpleClientset()
	fakeDisc := client.Discovery().(*fakediscovery.FakeDiscovery)
	fakeDisc.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "cilium.io/v2",
			APIResources: []metav1.APIResource{
				{Kind: "CiliumNetworkPolicy"},
			},
		},
	}
	p := SelectPolicyProvider(fakeDisc)
	if _, ok := p.(*network.CiliumPolicyProvider); !ok {
		t.Errorf("expected CiliumPolicyProvider, got %T", p)
	}
}

func TestSelectPolicyProvider_CiliumNotAvailable(t *testing.T) {
	client := fakeclientset.NewSimpleClientset()
	fakeDisc := client.Discovery().(*fakediscovery.FakeDiscovery)
	fakeDisc.Resources = []*metav1.APIResourceList{}
	p := SelectPolicyProvider(fakeDisc)
	if _, ok := p.(*network.VanillaPolicyProvider); !ok {
		t.Errorf("expected VanillaPolicyProvider, got %T", p)
	}
}

func TestSelectPolicyProvider_NilDiscovery(t *testing.T) {
	p := SelectPolicyProvider(nil)
	if _, ok := p.(*network.VanillaPolicyProvider); !ok {
		t.Errorf("expected VanillaPolicyProvider on nil disc, got %T", p)
	}
}
