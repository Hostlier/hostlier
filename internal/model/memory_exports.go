package model

// MemoryExport binds an existing host resource to explicit storage protocols.
// Source paths are interpreted exclusively by the memory controller.
type MemoryExport struct {
	Path      string   `json:"path" yaml:"path"`
	Protocols []string `json:"protocols" yaml:"protocols"`
	ReadOnly  bool     `json:"readOnly" yaml:"readOnly"`
}

// MemoryIdentity pins the resource reviewed by the owner, including its mount.
type MemoryIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mount  uint64 `json:"mount"`
	File   bool   `json:"file"`
}
