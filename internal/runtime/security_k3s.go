package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// K3sSecurityPolicy records the node-wide hard process ceiling. Kubernetes has a
// Pod PID budget, so a shared budget no larger than any component's original
// ceiling is used. It is deliberately stricter than independent Docker budgets.
type K3sSecurityPolicy struct {
	Version      int   `json:"version"`
	PodPidsLimit int64 `json:"podPidsLimit"`
}

// validateK3sSecurity checks every container's declared boundary independently.
func (k *K3s) validateK3sSecurity(p model.Project) error {
	// Complete every selected item before advancing the migration boundary.
	for _, component := range append([]model.Project{p}, migrationMembers(p)...) {
		// Preserve failure before advancing to the next external-effect boundary.
		if err := k.validateK3sComponent(component); err != nil {
			return err
		}
	}
	return nil
}

// validateK3sComponent requires a shared node ceiling that cannot widen its PID budget.
func (k *K3s) validateK3sComponent(p model.Project) error {
	// Require the declared confinement contract before accepting this workload.
	if p.Security == nil {
		return nil
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := config.ValidateContainerSecurity(p); err != nil {
		return err
	}
	policy := k.options.K3sSecurity
	// Require the declared confinement contract before accepting this workload.
	if policy == nil || policy.Version != 1 || policy.PodPidsLimit < 16 || policy.PodPidsLimit > p.Security.PidsLimit {
		return model.Err("UNSUPPORTED_SECURITY", "Kubernetes security requires a verified managed no-swap node with a Pod PID ceiling no larger than each component's pidsLimit; use the runtime migration workflow")
	}
	return validateK3sSeccomp(p)
}

// validateK3sSeccomp refuses Docker extensions that OCI runtimes could silently ignore.
func validateK3sSeccomp(p model.Project) error {
	// Require the declared confinement contract before accepting this workload.
	if p.Security.SeccompConfig == "" {
		return nil
	}
	var profile map[string]any
	json.Unmarshal([]byte(p.Configs[p.Security.SeccompConfig].Content), &profile)
	// Treat absent optional state explicitly rather than inferring authority.
	if profile["archMap"] != nil {
		return model.Err("UNSUPPORTED_SECURITY", "Convert Docker seccomp archMap to an explicit OCI architectures list before migration")
	}
	rules, _ := profile["syscalls"].([]any)
	// Complete every selected item before advancing the migration boundary.
	for _, rule := range rules {
		r, _ := rule.(map[string]any)
		// Treat absent optional state explicitly rather than inferring authority.
		if r["includes"] != nil || r["excludes"] != nil {
			return model.Err("UNSUPPORTED_SECURITY", "Resolve Docker-specific seccomp includes/excludes into explicit OCI syscall rules before migration")
		}
	}
	return nil
}

// migrationMembers keeps this migration step within its validated recovery boundary.
func migrationMembers(p model.Project) []model.Project {
	out := []model.Project{}
	// Complete every selected item before advancing the migration boundary.
	for _, name := range model.MemberNames(p) {
		out = append(out, p.Sidecars[name])
	}
	return out
}

// verifyK3sSecurity checks live kubelet configuration, rather than treating a
// persisted promise as evidence that PID and swap restrictions are enforced.
func (k *K3s) verifyK3sSecurity(ctx context.Context) error {
	node, err := k.securityNode(ctx)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	raw, err := k.run(ctx, []string{"get", "--raw", "/api/v1/nodes/" + node + "/proxy/configz"}, nil)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	var response struct{ KubeletConfig kubeletSecurity }
	// Validate the decoded native response before using its contents.
	if json.Unmarshal(raw, &response) != nil {
		return model.Err("K3S_SECURITY", "Cannot inspect kubelet security configuration")
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err := k.verifyKubeletPolicy(response.KubeletConfig); err != nil {
		return err
	}
	return verifyLocalSwap()
}

// securityNode confines node-local policy and profile distribution to one local node.
func (k *K3s) securityNode(ctx context.Context) (string, error) {
	raw, err := k.run(ctx, []string{"get", "nodes", "-o", "json"}, nil)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return "", err
	}
	var nodes struct {
		Items []struct{ Metadata struct{ Name string } }
	}
	// Validate the decoded native response before using its contents.
	if json.Unmarshal(raw, &nodes) != nil || len(nodes.Items) != 1 {
		return "", model.Err("K3S_SECURITY", "Security policy requires exactly one local Kubernetes node")
	}
	return nodes.Items[0].Metadata.Name, nil
}

// kubeletSecurity is the live configuration evidence required for no-swap workloads.
type kubeletSecurity struct {
	PodPidsLimit int64
	FailSwapOn   bool
	MemorySwap   struct{ SwapBehavior string }
}

// verifyKubeletPolicy checks actual enforcement settings against the reviewed node policy.
func (k *K3s) verifyKubeletPolicy(cfg kubeletSecurity) error {
	// Require the declared confinement contract before accepting this workload.
	if cfg.PodPidsLimit != k.options.K3sSecurity.PodPidsLimit || !cfg.FailSwapOn || cfg.MemorySwap.SwapBehavior != "" && cfg.MemorySwap.SwapBehavior != "NoSwap" {
		return model.Err("K3S_SECURITY", "Live kubelet PID/swap configuration differs from the approved node policy")
	}
	return nil
}

// verifyLocalSwap refuses a host on which no-swap enforcement cannot be established.
func verifyLocalSwap() error {
	swaps, err := os.ReadFile("/proc/swaps")
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return err
	}
	// Reject incompatible state before continuing the protected migration step.
	if len(strings.Fields(string(swaps))) > 5 {
		return model.Err("K3S_SECURITY", "Disable host swap before starting no-swap workloads")
	}
	return nil
}

// loadK3sSecurity reads only a protected, regular node policy file.
func loadK3sSecurity(stateDir string) (*K3sSecurityPolicy, error) {
	path := filepath.Join(stateDir, "k3s-security.json")
	info, err := os.Lstat(path)
	// Preserve failure before advancing to the next external-effect boundary.
	if os.IsNotExist(err) {
		return nil, nil
	}
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return nil, err
	}
	// Reject incompatible state before continuing the protected migration step.
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, model.Err("K3S_SECURITY", "Unsafe Kubernetes node security policy")
	}
	raw, err := os.ReadFile(path)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return nil, err
	}
	var policy K3sSecurityPolicy
	// Preserve failure before advancing to the next external-effect boundary.
	if err := json.Unmarshal(raw, &policy); err != nil {
		return nil, err
	}
	return &policy, nil
}

// finishSecureManifests applies confinement after group files and sidecars exist.
func (k *K3s) finishSecureManifests(p model.Project, dc model.DeployContext, manifests []map[string]any) ([]map[string]any, error) {
	manifests, err := k.extendGroupManifests(p, dc, manifests)
	// Preserve failure before advancing to the next external-effect boundary.
	if err != nil {
		return nil, err
	}
	return k.secureK3sManifests(p, manifests), nil
}

// seccompFilename keeps this migration step within its validated recovery boundary.
func seccompFilename(p model.Project) string {
	return "hostlier-" + shortHash(p.Configs[p.Security.SeccompConfig].Content) + ".json"
}

// prepareK3sSeccomp distributes immutable reviewed profiles onto the sole node.
func (k *K3s) prepareK3sSeccomp(p model.Project) error {
	// Complete every selected item before advancing the migration boundary.
	for _, component := range append([]model.Project{p}, migrationMembers(p)...) {
		// Require the declared confinement contract before accepting this workload.
		if component.Security == nil || component.Security.SeccompConfig == "" {
			continue
		}
		dir := "/var/lib/kubelet/seccomp"
		// Preserve failure before advancing to the next external-effect boundary.
		if err := atomicWrite(filepath.Join(dir, seccompFilename(component)), []byte(component.Configs[component.Security.SeccompConfig].Content)); err != nil {
			return err
		}
	}
	return nil
}

// secureK3sManifests keeps this migration step within its validated recovery boundary.
func (k *K3s) secureK3sManifests(p model.Project, manifests []map[string]any) []map[string]any {
	// Require the declared confinement contract before accepting this workload.
	if !hasContainerSecurity(p) {
		return manifests
	}
	// Complete every selected item before advancing the migration boundary.
	for _, manifest := range manifests {
		// Reject incompatible state before continuing the protected migration step.
		if manifest["kind"] != "Deployment" {
			continue
		}
		pod := manifest["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
		init, _ := pod["initContainers"].([]any)
		setup := []any{}
		// Verify each selected container under the current ownership scope.
		for _, container := range pod["containers"].([]any) {
			k.secureK3sContainer(p, "primary", container.(map[string]any), pod, &setup)
		}
		// Complete every selected item before advancing the migration boundary.
		for _, container := range init {
			c := container.(map[string]any)
			name := c["name"].(string)
			k.secureK3sContainer(p.Sidecars[name], name, c, pod, &setup)
		}
		pod["initContainers"] = append(setup, init...)
	}
	return manifests
}

// secureK3sContainer keeps this migration step within its validated recovery boundary.
func (k *K3s) secureK3sContainer(p model.Project, member string, c, pod map[string]any, setup *[]any) {
	s := p.Security
	// Treat absent optional state explicitly rather than inferring authority.
	if s == nil {
		return
	}
	seccomp := map[string]any{"type": "RuntimeDefault"}
	// Reject incompatible state before continuing the protected migration step.
	if s.SeccompConfig != "" {
		seccomp = map[string]any{"type": "Localhost", "localhostProfile": seccompFilename(p)}
	}
	armor := map[string]any{"type": "RuntimeDefault"}
	// Reject incompatible state before continuing the protected migration step.
	if s.AppArmorProfile != "" {
		armor = map[string]any{"type": "Localhost", "localhostProfile": s.AppArmorProfile}
	}
	c["securityContext"] = map[string]any{"runAsNonRoot": true, "runAsUser": s.RunAsUser, "runAsGroup": s.RunAsGroup, "readOnlyRootFilesystem": true, "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []string{"ALL"}}, "seccompProfile": seccomp, "appArmorProfile": armor}
	paths := make([]string, 0, len(s.Tmpfs))
	// Complete every selected item before advancing the migration boundary.
	for path := range s.Tmpfs {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	// Complete every selected item before advancing the migration boundary.
	for _, path := range paths {
		mount := s.Tmpfs[path]
		name := "scratch-" + shortHash(member+path)
		volumes, _ := pod["volumes"].([]any)
		pod["volumes"] = append(volumes, map[string]any{"name": name, "emptyDir": map[string]any{"medium": "Memory", "sizeLimit": mount.Size}})
		mounts, _ := c["volumeMounts"].([]any)
		c["volumeMounts"] = append(mounts, map[string]any{"name": name, "mountPath": path})
		mode := mount.Mode
		// Reject incompatible state before continuing the protected migration step.
		if mode == 0 {
			mode = 0700
		}
		// Literal numeric arguments avoid embedding user paths in shell programs.
		command := fmt.Sprintf("chown %d:%d /scratch && chmod %s /scratch", s.RunAsUser, s.RunAsGroup, strconv.FormatUint(uint64(mode), 8))
		*setup = append(*setup, map[string]any{"name": name, "image": collectorTrustHelper, "command": []string{"sh", "-ec", command}, "volumeMounts": []any{map[string]any{"name": name, "mountPath": "/scratch"}}, "resources": map[string]any{"limits": map[string]string{"cpu": "100m", "memory": "16Mi"}}, "securityContext": map[string]any{"runAsUser": 0, "readOnlyRootFilesystem": true, "allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []string{"ALL"}, "add": []string{"CHOWN", "FOWNER"}}, "seccompProfile": map[string]string{"type": "RuntimeDefault"}, "appArmorProfile": map[string]string{"type": "RuntimeDefault"}}})
	}
}
