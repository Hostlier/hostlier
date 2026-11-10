package model

// ContainerSecurity declares a confined nonroot workload with reviewed syscall policy.
// Kubernetes additionally requires a verified managed node security policy.
type ContainerSecurity struct {
	NoSwap                 bool                  `json:"noSwap" yaml:"noSwap"`
	RunAsUser              uint32                `json:"runAsUser" yaml:"runAsUser"`
	RunAsGroup             uint32                `json:"runAsGroup" yaml:"runAsGroup"`
	ReadOnlyRootFilesystem bool                  `json:"readOnlyRootFilesystem" yaml:"readOnlyRootFilesystem"`
	NoNewPrivileges        bool                  `json:"noNewPrivileges" yaml:"noNewPrivileges"`
	DropCapabilities       []string              `json:"dropCapabilities" yaml:"dropCapabilities"`
	SeccompConfig          string                `json:"seccompConfig,omitempty" yaml:"seccompConfig,omitempty"`
	AppArmorProfile        string                `json:"appArmorProfile,omitempty" yaml:"appArmorProfile,omitempty"`
	PidsLimit              int64                 `json:"pidsLimit" yaml:"pidsLimit"`
	Tmpfs                  map[string]TmpfsMount `json:"tmpfs,omitempty" yaml:"tmpfs,omitempty"`
}

// TmpfsMount supplies bounded writable scratch space beneath a read-only container root.
type TmpfsMount struct {
	Size string `json:"size" yaml:"size"`
	Mode uint32 `json:"mode,omitempty" yaml:"mode,omitempty"`
}
