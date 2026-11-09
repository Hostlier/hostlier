package config

import (
	"hostlier/internal/model"
	"testing"
)

// secureProject models the finite nonroot contract accepted for a reviewed worker image.
func secureProject() model.Project {
	return model.Project{Version: 2, Resources: &model.Limits{Memory: "512Mi"}, Security: &model.ContainerSecurity{NoSwap: true, RunAsUser: 999, RunAsGroup: 999, ReadOnlyRootFilesystem: true, NoNewPrivileges: true, DropCapabilities: []string{"ALL"}, PidsLimit: 1024, SeccompConfig: "policy", Tmpfs: map[string]model.TmpfsMount{"/tmp": {Size: "256Mi", Mode: 01777}}}, Configs: map[string]model.ConfigFile{"policy": {Mount: "/app/policy.json", Content: `{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[{"names":["read","write"],"action":"SCMP_ACT_ALLOW"}]}`}}}
}

// TestWorkerSecurityRejectsPrivilegeAndPolicyEscapes checks security failures before daemon mutation.
func TestWorkerSecurityRejectsPrivilegeAndPolicyEscapes(t *testing.T) {
	cases := []func(*model.Project){
		// Root cannot select the confined worker contract.
		func(p *model.Project) { p.Security.RunAsUser = 0 },
		// Writable roots cannot be hidden behind a custom syscall profile.
		func(p *model.Project) { p.Security.ReadOnlyRootFilesystem = false },
		// Privilege transitions remain disabled even when the policy is default-deny.
		func(p *model.Project) { p.Security.NoNewPrivileges = false },
		// Extra capability grants are deliberately absent from this schema.
		func(p *model.Project) { p.Security.DropCapabilities = []string{"NET_RAW"} },
		// Host-global unconfined profiles are never a fallback.
		func(p *model.Project) { p.Security.AppArmorProfile = "unconfined" },
		// Default-allow is not accepted as an equivalent to the published worker profile.
		func(p *model.Project) {
			p.Configs["policy"] = model.ConfigFile{Mount: "/app/policy", Content: `{"defaultAction":"SCMP_ACT_ALLOW","syscalls":[{"names":["read"],"action":"SCMP_ACT_ALLOW"}]}`}
		},
		// Kernel filesystems cannot be replaced by scratch storage.
		func(p *model.Project) { p.Security.Tmpfs = map[string]model.TmpfsMount{"/proc": {Size: "1Mi"}} },
		// Temporary storage cannot shadow the profile or other reviewed files.
		func(p *model.Project) { p.Security.Tmpfs = map[string]model.TmpfsMount{"/app": {Size: "1Mi"}} },
	}
	// Each test starts from a fresh valid policy so mutations cannot mask one another.
	for index, change := range cases {
		p := secureProject()
		change(&p)
		// Every escape must fail the public validation boundary.
		if err := ValidateContainerSecurity(p); err == nil {
			t.Fatalf("unsafe case %d accepted", index)
		}
	}
	// The actual restricted default remains usable after the rejection cases.
	if err := ValidateContainerSecurity(secureProject()); err != nil {
		t.Fatal(err)
	}
}
