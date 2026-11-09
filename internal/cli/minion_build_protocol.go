package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// minionBuildProtocol reads the installed compiler's declared CLI contract, including hoisted installs.
func minionBuildProtocol(script string) (int, error) {
	data, err := os.ReadFile(filepath.Join(filepath.Dir(script), "..", "..", "package.json"))
	if err != nil {
		return 0, err
	}
	var manifest struct {
		Hostlier struct{ MinionBuildProtocol int }
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return 0, err
	}
	version := manifest.Hostlier.MinionBuildProtocol
	if version != 0 && version != 1 {
		return 0, fmt.Errorf("Unsupported Minion build protocol %d; update the Hostlier CLI", version)
	}
	return version, nil
}

// Legacy compilers require concrete image fields even when exporting a local container.
func legacyMinionDefaults(setup *minionInitSetup) {
	if setup.value("image") == "" {
		setup.set("image", setup.value("name")+":0.1.0")
	}
	if setup.value("baseImage") == "" {
		setup.set("baseImage", "node:24.21.0-bookworm-slim")
	}
}

// buildLegacyMinion adapts older single-config compilers without rewriting the pinned app.
func (s *session) buildLegacyMinion(script string, setup *minionInitSetup, outdir, arch string) error {
	legacyMinionDefaults(setup)
	setup.set("architecture", arch)
	setup.set("outdir", outdir)
	if setup.value("entry") == "" {
		setup.set("entry", minionInitEntry(s.cwd))
	}
	data, err := json.Marshal(setup.Fields)
	// Never pass credential-like fields through command-line arguments or diagnostics.
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(s.cwd, ".hostlier-minion-*.json")
	// A sibling config preserves entry, UI and generated declaration paths.
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	closeErr := file.Close()
	// Do not compile partial settings.
	if err != nil {
		return err
	}
	// Flush the complete input before passing its name to Node.
	if closeErr != nil {
		return closeErr
	}
	return s.runMinionProcess(s.cwd, "node", []string{script, "build", file.Name()}, s.in, s.errOut, s.errOut)
}
