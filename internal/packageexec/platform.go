package packageexec

import (
	"bytes"
	"debug/elf"
	"debug/macho"
	"fmt"
)

// CheckPlatform inspects executable headers without running downloaded code.
func CheckPlatform(data []byte, target Platform) error {
	actual, err := ExecutablePlatform(data)
	// Unknown formats and architecture mismatches must fail before privileged installation.
	if err != nil || actual != target {
		return fmt.Errorf("package must be a native %s/%s executable", target.OS, target.Arch)
	}
	return nil
}

// ExecutablePlatform recognizes the native Linux and macOS formats supported by Hostlier.
func ExecutablePlatform(data []byte) (Platform, error) {
	// Linux server packages are ELF executables, including position-independent executables.
	if bytes.HasPrefix(data, []byte{0x7f, 'E', 'L', 'F'}) {
		return elfPlatform(data)
	}
	file, err := macho.NewFile(bytes.NewReader(data))
	// Do not accept shell scripts, object files or malformed Mach-O containers.
	if err != nil {
		return Platform{}, fmt.Errorf("unsupported executable format")
	}
	defer file.Close()
	// Only linked executables can be installed as packages.
	if file.Type != macho.TypeExec {
		return Platform{}, fmt.Errorf("package is not executable")
	}
	arch := map[macho.Cpu]string{macho.CpuAmd64: "amd64", macho.CpuArm64: "arm64"}[file.Cpu]
	return Platform{OS: "darwin", Arch: arch}, nil
}

// elfPlatform rejects relocatable objects and maps supported server architectures.
func elfPlatform(data []byte) (Platform, error) {
	file, err := elf.NewFile(bytes.NewReader(data))
	// Malformed headers cannot establish an execution target.
	if err != nil {
		return Platform{}, err
	}
	defer file.Close()
	// Relocatable objects cannot serve as native package executables.
	if file.Type != elf.ET_EXEC && file.Type != elf.ET_DYN {
		return Platform{}, fmt.Errorf("package is not executable")
	}
	arch := map[elf.Machine]string{elf.EM_X86_64: "amd64", elf.EM_AARCH64: "arm64"}[file.Machine]
	return Platform{OS: "linux", Arch: arch}, nil
}
