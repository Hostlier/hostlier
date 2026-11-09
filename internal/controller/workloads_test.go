package controller

import (
	"hostlier/internal/model"
	"testing"
)

// TestPrivateProviderContractPreventsImplicitRevocation covers sidecar consumers and provider lifecycle.
func TestPrivateProviderContractPreventsImplicitRevocation(t *testing.T) {
	provider := model.Project{Name: "agent", Version: 2, Port: 1907, Endpoint: &model.Endpoint{Exposure: "private", Port: 1907, Consumers: []string{"web"}}}
	consumer := model.Project{Name: "web", Version: 2, Sidecars: map[string]model.Project{"client": {Bindings: map[string]string{"URL": "applications.agent.url"}}}}
	c := &Controller{state: State{Applications: map[string]*Application{"agent": {Deployment: model.Deployment{Name: "agent", Status: "running"}, Spec: provider}, "web": {Deployment: model.Deployment{Name: "web", Status: "running"}, Spec: consumer}}}}
	// A healthy provider can update its process while preserving the approved address.
	if err := c.validateProviderChange(provider); err != nil {
		t.Fatal(err)
	}
	changed := provider
	changed.Port = 2000
	// Changing a referenced port would strand already deployed consumer processes.
	if err := c.validateProviderChange(changed); err == nil {
		t.Fatal("live endpoint changed")
	}
	// Stop and delete require consumers to be drained first.
	for _, action := range []string{"stop", "delete"} {
		// The guard sees sidecar bindings as references owned by the parent application.
		if err := c.validateProviderAction(c.state.Applications["agent"], action); err == nil {
			t.Fatal("live provider removed")
		}
	}
	c.state.Applications["web"].Status = "stopped"
	// Once the consumer is stopped, the provider may be intentionally removed.
	if err := c.validateProviderAction(c.state.Applications["agent"], "delete"); err != nil {
		t.Fatal(err)
	}
}
