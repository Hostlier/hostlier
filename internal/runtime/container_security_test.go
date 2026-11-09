package runtime

import (
	"hostlier/internal/model"
	"strings"
	"testing"
)

// TestWorkerDockerSecurityPreservesReviewedConfinement checks the actual daemon payload boundaries.
func TestWorkerDockerSecurityPreservesReviewedConfinement(t *testing.T) {
	p := model.Project{Version: 2, Resources: &model.Limits{Memory: "512Mi"}, Security: &model.ContainerSecurity{NoSwap: true, RunAsUser: 999, RunAsGroup: 999, ReadOnlyRootFilesystem: true, NoNewPrivileges: true, DropCapabilities: []string{"ALL"}, PidsLimit: 1024, SeccompConfig: "policy", Tmpfs: map[string]model.TmpfsMount{"/tmp": {Size: "256Mi", Mode: 01777}}}, Configs: map[string]model.ConfigFile{"policy": {Mount: "/app/policy", Content: `{"defaultAction":"SCMP_ACT_ERRNO","syscalls":[{"names":["read"],"action":"SCMP_ACT_ALLOW"}]}`}}}
	host, body := map[string]any{}, map[string]any{}
	// Direct runtime entry points enforce schema before producing container settings.
	if err := applyContainerSecurity(p, host, body); err != nil {
		t.Fatal(err)
	}
	// A user-selected policy must not restore root, capabilities or a writable container filesystem.
	if body["User"] != "999:999" || host["ReadonlyRootfs"] != true || host["PidsLimit"] != int64(1024) {
		t.Fatalf("identity/budget %v %v", body, host)
	}
	options := strings.Join(host["SecurityOpt"].([]string), " ")
	scratch := host["Tmpfs"].(map[string]string)["/tmp"]
	// Daemon input contains the reviewed JSON, no arbitrary host profile path, and explicit privilege/device restrictions.
	if !strings.Contains(options, "no-new-privileges:true") || !strings.Contains(options, "seccomp={") || !strings.Contains(scratch, "nosuid,nodev") || !strings.Contains(scratch, "size=268435456") {
		t.Fatal("confinement missing")
	}
}
