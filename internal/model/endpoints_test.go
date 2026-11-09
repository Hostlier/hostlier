package model

import "testing"

// TestManagedFileSecretBindingsPreserveComponentScope covers encrypted file references in primary and sidecars.
func TestManagedFileSecretBindingsPreserveComponentScope(t *testing.T) {
	p := Project{Configs: map[string]ConfigFile{"primary": {Secret: "primary-secret"}}, Sidecars: map[string]Project{"agent": {Configs: map[string]ConfigFile{"token": {Secret: "agent-secret"}}, Secrets: map[string]string{"ENV": "agent-env"}}}}
	refs := SecretBindings(p)
	// Neither file secret can collide with an environment destination or another component.
	if len(refs) != 3 || refs["files/primary"] != "primary-secret" || refs["agent/files/token"] != "agent-secret" || refs["agent/ENV"] != "agent-env" {
		t.Fatalf("wrong scoped references %+v", refs)
	}
}
