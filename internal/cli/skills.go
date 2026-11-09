package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"hostlier/internal/model"
	bundled "hostlier/internal/skills"
)

const skillsHelp = `Skills: hostlier skills list [--json]
        hostlier skills show COMMAND [--json]
        hostlier skills install [COMMAND] [--target codex|claude] [--global] [--force] [--dry-run] [--json]
        hostlier skills install [COMMAND] --output DIRECTORY [--force] [--dry-run] [--json]
Omit COMMAND to install every bundled skill. Default: current project's .agents/skills (Codex).
--target claude uses .claude/skills. --global uses your home instead of the project.
--output selects an exact skills directory and cannot be combined with --target or --global.
Identical files are unchanged; different files require --force. All skills commands work offline.`

// skills handles local instruction discovery and installation without a controller.
func (s *session) skills() error {
	// Validate the complete invocation before reading or writing local state.
	if err := s.validateSkillsArguments(); err != nil {
		return err
	}
	// Each subcommand has a distinct output contract.
	switch s.sub {
	// List the catalog without loading instruction bodies into output.
	case "", "list":
		return s.listSkills()
	// Show one complete instruction file for inspection or piping.
	case "show":
		return s.showSkill()
	// Install all or one command with explicitly selected scope.
	case "install":
		return s.installSkills()
	}
	return model.Err("ARGUMENTS", skillsHelp)
}

// validateSkillsArguments rejects ambiguous selectors and irrelevant flags.
func (s *session) validateSkillsArguments() error {
	maximum := map[string]int{"": 1, "list": 2, "show": 3, "install": 3}
	limit, ok := maximum[s.sub]
	// Showing a skill always requires one explicit command.
	if !ok || len(s.parsed.args) > limit || (s.sub == "show" && s.target == "") {
		return model.Err("ARGUMENTS", skillsHelp)
	}
	allowed := map[string]bool{"json": true, "toon": true}
	// Installation-specific flags cannot silently affect read-only commands.
	if s.sub == "install" {
		allowed = map[string]bool{"json": true, "toon": true, "target": true, "global": true, "output": true, "force": true, "dry-run": true}
	}
	// Reject flags from unrelated commands rather than silently ignoring them.
	for flag, value := range s.flags {
		// Explicit empty values are errors, including an empty output path.
		if !allowed[flag] || value == "" {
			return model.Err("ARGUMENTS", "Invalid skills option --"+flag+"\n"+skillsHelp)
		}
	}
	return nil
}

// listSkills renders stable metadata in human-readable or JSON form.
func (s *session) listSkills() error {
	items, err := bundled.List()
	// Packaging errors must not appear as an empty catalog.
	if err != nil {
		return err
	}
	// Bodies are available separately through skills show.
	for i := range items {
		items[i].Content = ""
	}
	// JSON consumers receive an array in deterministic command order.
	if s.flags["json"] != "" || s.flags["toon"] != "" {
		return s.print(items)
	}
	// Text output makes commands and their selection descriptions discoverable.
	for _, item := range items {
		// Propagate output errors rather than claiming a successful listing.
		if _, err := fmt.Fprintf(s.out, "%s\t%s\n", skillLabel(item), item.Description); err != nil {
			return err
		}
	}
	return nil
}

// showSkill returns exactly the bytes installed for the requested command.
func (s *session) showSkill() error {
	items, err := bundled.Select(s.target)
	// Unknown commands never fall back to the full catalog.
	if err != nil {
		return model.Err("ARGUMENTS", err.Error())
	}
	// Structured output includes metadata alongside the full instruction body.
	if s.flags["json"] != "" || s.flags["toon"] != "" {
		return s.print(items[0])
	}
	_, err = fmt.Fprint(s.out, items[0].Content)
	return err
}

// installSkills resolves the destination and reports every installed or unchanged file.
func (s *session) installSkills() error {
	directory, err := s.skillsDirectory()
	// Invalid destinations cannot trigger partial installation.
	if err != nil {
		return err
	}
	dryRun := s.flags["dry-run"] != ""
	items, err := bundled.Install(s.target, directory, s.flags["force"] != "", dryRun)
	// Preserve actionable conflict and filesystem details.
	if err != nil {
		return model.Err("SKILLS_INSTALL", err.Error())
	}
	// JSON receipts distinguish previews from completed writes.
	if s.flags["json"] != "" || s.flags["toon"] != "" {
		return s.print(map[string]any{"directory": directory, "dryRun": dryRun, "skills": items})
	}
	prefix := ""
	// Never describe a preview as a completed installation.
	if dryRun {
		prefix = "dry-run: "
	}
	// Report exact paths so users can inspect or customize the installed skills.
	for _, item := range items {
		// Output failure is visible even when the files were already installed.
		if _, err := fmt.Fprintf(s.out, "%s%s %s\n", prefix, item.Action, item.Path); err != nil {
			return err
		}
	}
	return nil
}

// skillsDirectory maps agent targets to their documented skill roots.
func (s *session) skillsDirectory() (string, error) {
	// Explicit output names the skills root itself, without appending agent directories.
	if output := s.flags["output"]; output != "" {
		// Conflicting scope flags usually indicate a mistaken destination.
		if s.flags["target"] != "" || s.flags["global"] != "" {
			return "", model.Err("ARGUMENTS", "--output cannot be combined with --target or --global")
		}
		return filepath.Abs(resolveSkillsPath(s.cwd, output))
	}
	roots := map[string]string{"codex": ".agents", "claude": ".claude"}
	root, ok := roots[defaultValue(s.flags["target"], "codex")]
	// Other agents can use their own installation layout through --output.
	if !ok {
		return "", model.Err("ARGUMENTS", "Unknown skills target; use codex, claude, or --output DIRECTORY")
	}
	base := s.cwd
	// Global scope is the OS user's home, never the Hostlier credentials directory.
	if s.flags["global"] != "" {
		var err error
		base, err = os.UserHomeDir()
		// Missing user-home configuration must not fall back to the project.
		if err != nil {
			return "", err
		}
	}
	return filepath.Abs(filepath.Join(base, root, "skills"))
}

// resolveSkillsPath anchors relative output directories to the invocation workspace.
func resolveSkillsPath(cwd, path string) string {
	// Absolute output destinations already identify their intended root.
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(cwd, path)
}

// skillLabel marks topic guides so they are not mistaken for commands.
func skillLabel(item bundled.Skill) string {
	// Commands keep their bare name.
	if !item.Topic {
		return item.Command
	}
	return item.Command + " (guide)"
}
