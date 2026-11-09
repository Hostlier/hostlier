package runtime

import (
	"encoding/json"
	"fmt"
	"hostlier/internal/config"
	"hostlier/internal/model"
)

// applyContainerSecurity converts reviewed typed policy into literal Docker API settings.
func applyContainerSecurity(p model.Project, host, body map[string]any) error {
	// Legacy callers keep their original image-defined identity and filesystem behavior.
	if p.Security == nil {
		return nil
	}
	// Direct runtime callers cannot bypass schema or default-deny seccomp validation.
	if err := config.ValidateContainerSecurity(p); err != nil {
		return err
	}
	memory, _ := config.ParseMemory(p.Resources.Memory)
	host["Memory"], host["MemorySwap"] = memory, memory
	s := p.Security
	body["User"] = fmt.Sprintf("%d:%d", s.RunAsUser, s.RunAsGroup)
	host["ReadonlyRootfs"] = true
	host["CapDrop"] = []string{"ALL"}
	host["PidsLimit"] = s.PidsLimit
	options := []string{"no-new-privileges:true"}
	// Inline approved JSON avoids privileged daemon reads from arbitrary host paths.
	if s.SeccompConfig != "" {
		var profile any
		json.Unmarshal([]byte(p.Configs[s.SeccompConfig].Content), &profile)
		data, _ := json.Marshal(profile)
		options = append(options, "seccomp="+string(data))
	}
	// An omitted named profile retains Docker's normal confined AppArmor profile.
	if s.AppArmorProfile != "" {
		options = append(options, "apparmor="+s.AppArmorProfile)
	}
	host["SecurityOpt"] = options
	scratch := map[string]string{}
	// Every scratch mount keeps the kernel's device/suid restrictions and a bounded byte budget.
	for path, mount := range s.Tmpfs {
		size, _ := config.ParseMemory(mount.Size)
		mode := mount.Mode
		// Private temporary directories default to owner-only access.
		if mode == 0 {
			mode = 0700
		}
		scratch[path] = fmt.Sprintf("rw,nosuid,nodev,size=%d,mode=%o", size, mode)
	}
	host["Tmpfs"] = scratch
	return nil
}

// hasContainerSecurity prevents unsupported backends from silently ignoring confinement requirements.
func hasContainerSecurity(p model.Project) bool {
	// The primary's requirements are independent of sidecar configuration.
	if p.Security != nil {
		return true
	}
	// Every member's declared boundary must be implemented by the selected backend.
	for _, member := range p.Sidecars {
		// A member cannot silently lose a stricter policy than the primary.
		if member.Security != nil {
			return true
		}
	}
	return false
}
