package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	bundled "hostlier/internal/skills"
)

// skillReceipt decodes installation output without relying on human-facing wording.
type skillReceipt struct {
	Directory string                 `json:"directory"`
	DryRun    bool                   `json:"dryRun"`
	Skills    []bundled.Installation `json:"skills"`
}

// TestSkillsCatalogCoversCLICommands catches undocumented commands in future releases.
func TestSkillsCatalogCoversCLICommands(t *testing.T) {
	output := invokeSkills(t, t.TempDir(), "list", "--json")
	var catalog []bundled.Skill
	// Public catalog output must remain usable by coding agents.
	if err := json.Unmarshal([]byte(output), &catalog); err != nil {
		t.Fatal(err)
	}
	var commands []string
	// Each canonical command retains its independently installable guide.
	for command := range commandHelp {
		commands = append(commands, command)
	}
	byCommand := map[string]bundled.Skill{}
	commandSkills := countCommandSkills(t, catalog)
	// List metadata excludes bodies to keep discovery compact.
	for _, item := range catalog {
		// Duplicate identities and invalid discovery metadata would hide skills.
		if item.Name != "hostlier-"+item.Command || item.Description == "" || item.Content != "" {
			t.Fatalf("invalid catalog entry: %+v", item)
		}
		byCommand[item.Command] = item
	}
	// Every advertised top-level command should have an independently usable skill.
	if commandSkills != len(commands) {
		t.Fatalf("catalog has %d command skills for %d commands", commandSkills, len(commands))
	}
	// Coverage follows the CLI's public command surface rather than a copied fixture.
	for _, command := range commands {
		// Missing commands require authoring a real command guide.
		if _, ok := byCommand[command]; !ok {
			t.Errorf("no skill for %s", command)
		}
	}
}

// TestSkillsInstallOneAndReinstall verifies selection, exact content and idempotence offline.
func TestSkillsInstallOneAndReinstall(t *testing.T) {
	cwd := t.TempDir()
	receipt := installSkillsReceipt(t, cwd, "deploy")
	root := filepath.Join(cwd, ".agents", "skills")
	// The default destination is scoped to the current project.
	if receipt.Directory != root || len(receipt.Skills) != 1 || receipt.Skills[0].Action != "created" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	path := filepath.Join(root, "hostlier-deploy", "SKILL.md")
	want := invokeSkills(t, cwd, "show", "deploy")
	assertSkillFile(t, path, want)
	before := skillInfo(t, path)
	second := installSkillsReceipt(t, cwd, "deploy")
	// No-op installs preserve timestamps instead of rewriting identical files.
	if second.Skills[0].Action != "unchanged" || !before.ModTime().Equal(skillInfo(t, path).ModTime()) {
		t.Fatal("reinstall rewrote identical instructions")
	}
	entries, err := os.ReadDir(root)
	// A one-command install must not introduce any other command's skill.
	if err != nil || len(entries) != 1 {
		t.Fatalf("unexpected installed entries: %v %v", entries, err)
	}
}

// TestSkillsInstallAllUsesOmittedCommand verifies the user's install-all syntax.
func TestSkillsInstallAllUsesOmittedCommand(t *testing.T) {
	cwd := t.TempDir()
	receipt := installSkillsReceipt(t, cwd)
	catalog, err := bundled.List()
	// Every bundled skill is included when the command selector is absent.
	if err != nil || len(receipt.Skills) != len(catalog) {
		t.Fatalf("incomplete installation: %+v %v", receipt, err)
	}
	// Compare installed bytes with the files inspected through the public catalog.
	for i, item := range catalog {
		assertSkillFile(t, receipt.Skills[i].Path, item.Content)
	}
}

// TestSkillsDestinations verifies target, global and custom-directory resolution.
func TestSkillsDestinations(t *testing.T) {
	cwd, home := t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOSTLIER_HOME", filepath.Join(home, "credentials"))
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"claude-project", []string{"--target", "claude"}, filepath.Join(cwd, ".claude", "skills")},
		{"codex-global", []string{"--target", "codex", "--global"}, filepath.Join(home, ".agents", "skills")},
		{"claude-global", []string{"--target", "claude", "--global"}, filepath.Join(home, ".claude", "skills")},
		{"relative-output", []string{"--output", "custom skills"}, filepath.Join(cwd, "custom skills")},
		{"absolute-output", []string{"--output", filepath.Join(home, "custom")}, filepath.Join(home, "custom")},
	}
	// Each supported scope has an independent destination and no controller dependency.
	for _, test := range cases {
		// Isolate each destination assertion while sharing the simulated user home.
		t.Run(test.name, func(t *testing.T) {
			args := append([]string{"status"}, test.args...)
			receipt := installSkillsReceipt(t, cwd, args...)
			// Hostlier credential storage must never determine the skill destination.
			if receipt.Directory != test.want {
				t.Fatalf("got %q, want %q", receipt.Directory, test.want)
			}
			assertSkillFile(t, filepath.Join(test.want, "hostlier-status", "SKILL.md"), invokeSkills(t, cwd, "show", "status"))
		})
	}
}

// TestSkillsConflictsPreflightAndForce protects edits and unrelated user resources.
func TestSkillsConflictsPreflightAndForce(t *testing.T) {
	cwd := t.TempDir()
	root := filepath.Join(cwd, ".agents", "skills")
	path := filepath.Join(root, "hostlier-workflow", "SKILL.md")
	writeSkillFixture(t, path, "user edits\n")
	extra := filepath.Join(root, "hostlier-workflow", "notes.txt")
	writeSkillFixture(t, extra, "keep this\n")
	_, err := invoke([]string{"skills", "install"}, "", Options{CWD: cwd, Home: t.TempDir()})
	// A conflict late in the catalog must stop all earlier writes.
	if err == nil {
		t.Fatal("conflicting install unexpectedly succeeded")
	}
	assertSkillFile(t, path, "user edits\n")
	assertSkillMissing(t, filepath.Join(root, "hostlier-clients"))
	receipt := installSkillsReceipt(t, cwd, "workflow", "--force")
	// Force replaces only the selected instruction file.
	if receipt.Skills[0].Action != "updated" {
		t.Fatalf("unexpected replacement action: %+v", receipt)
	}
	assertSkillFile(t, path, invokeSkills(t, cwd, "show", "workflow"))
	assertSkillFile(t, extra, "keep this\n")
}

// TestSkillsDryRunDoesNotCreateOrReplace verifies previews are entirely read-only.
func TestSkillsDryRunDoesNotCreateOrReplace(t *testing.T) {
	cwd := t.TempDir()
	receipt := installSkillsReceipt(t, cwd, "--dry-run")
	// Preview receipts must be distinguishable from completed writes.
	if !receipt.DryRun || len(receipt.Skills) == 0 {
		t.Fatalf("invalid dry-run receipt: %+v", receipt)
	}
	assertSkillMissing(t, filepath.Join(cwd, ".agents"))
	path := filepath.Join(cwd, ".agents", "skills", "hostlier-deploy", "SKILL.md")
	writeSkillFixture(t, path, "custom deployment instructions")
	installSkillsReceipt(t, cwd, "deploy", "--force", "--dry-run")
	assertSkillFile(t, path, "custom deployment instructions")
}

// TestSkillsRejectsMalformedCommands ensures invalid selectors cannot write files.
func TestSkillsRejectsMalformedCommands(t *testing.T) {
	cases := [][]string{
		{"install", "missing"}, {"install", "../deploy"}, {"install", "deploy", "status"},
		{"install", "--all"}, {"install", "all"}, {"show"}, {"show", "deploy", "extra"},
		{"list", "deploy"}, {"list", "--force"}, {"install", "--target", "unknown"},
		{"install", "--output", "custom", "--global"}, {"install", "--output", "custom", "--target", "codex"},
		{"install", "--output", ""}, {"install", "--target"}, {"install", "--server", "production"},
		{"install", "--yes"}, {"install", "--force", "--force"}, {"remove", "deploy"},
	}
	// Exercise the real parser as well as skills-specific validation.
	for _, args := range cases {
		// Give every malformed invocation a fresh workspace.
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cwd := t.TempDir()
			_, err := invoke(append([]string{"skills"}, args...), "", Options{CWD: cwd, Home: t.TempDir()})
			// Invalid inputs must fail without a destination being created.
			if err == nil {
				t.Fatal("invalid invocation succeeded")
			}
			entries, readErr := os.ReadDir(cwd)
			// No rejected invocation may leave even an empty skill directory.
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("invalid invocation changed workspace: %v %v", entries, readErr)
			}
		})
	}
}

// TestSkillsRejectsSymlinkDestinations prevents overwriting linked user content.
func TestSkillsRejectsSymlinkDestinations(t *testing.T) {
	// Test both redirectable skill directories and individual instruction files.
	for _, directoryLink := range []bool{false, true} {
		// Exercise each redirect independently with an external sentinel file.
		t.Run(map[bool]string{false: "file", true: "directory"}[directoryLink], func(t *testing.T) {
			cwd, outside := t.TempDir(), t.TempDir()
			original := filepath.Join(outside, "SKILL.md")
			writeSkillFixture(t, original, "unrelated instructions")
			link := filepath.Join(cwd, ".agents", "skills", "hostlier-deploy")
			target := outside
			// File symlinks sit inside an ordinary owned skill folder.
			if !directoryLink {
				link = filepath.Join(link, "SKILL.md")
				target = original
			}
			makeSkillSymlink(t, target, link)
			_, err := invoke([]string{"skills", "install", "deploy", "--force"}, "", Options{CWD: cwd, Home: t.TempDir()})
			// Force never authorizes following a linked skill destination.
			if err == nil {
				t.Fatal("symlink installation succeeded")
			}
			assertSkillFile(t, original, "unrelated instructions")
		})
	}
}

// TestSkillsAliasesAndJSONShow verifies command aliases and inspectable instruction metadata.
func TestSkillsAliasesAndJSONShow(t *testing.T) {
	cwd := t.TempDir()
	canonical := invokeSkills(t, cwd, "show", "domains")
	// CLI aliases and full skill names resolve to the same standalone guide.
	for _, name := range []string{"domain", "hostlier-domains"} {
		// Exact content equality also checks the show/install identity contract.
		if actual := invokeSkills(t, cwd, "show", name); actual != canonical {
			t.Fatalf("alias %s differs from canonical skill", name)
		}
	}
	var shown bundled.Skill
	output := invokeSkills(t, cwd, "show", "domains", "--json")
	// JSON inspection exposes the same body as the plain-text command.
	if json.Unmarshal([]byte(output), &shown) != nil || shown.Content != canonical || shown.Command != "domains" {
		t.Fatalf("invalid JSON skill: %s", output)
	}
}

// invokeSkills asserts that a local skill command neither prompts nor contacts a controller.
func invokeSkills(t *testing.T, cwd string, args ...string) string {
	t.Helper()
	client := &fakeClient{}
	output, err := invoke(append([]string{"skills"}, args...), "", Options{CWD: cwd, Home: filepath.Join(cwd, "no-profile"), Client: client})
	// No enrollment, authentication or interactive dependency is permitted.
	if err != nil || len(client.calls) != 0 {
		t.Fatalf("skills command: %v; controller calls: %v; output: %s", err, client.calls, output)
	}
	return output
}

// installSkillsReceipt reads structured receipts from the public installer command.
func installSkillsReceipt(t *testing.T, cwd string, args ...string) skillReceipt {
	t.Helper()
	arguments := append([]string{"install"}, args...)
	arguments = append(arguments, "--json")
	output := invokeSkills(t, cwd, arguments...)
	var receipt skillReceipt
	// Invalid output is a CLI compatibility failure, even when files were created.
	if err := json.Unmarshal([]byte(output), &receipt); err != nil {
		t.Fatal(err, output)
	}
	return receipt
}

// writeSkillFixture prepares user-owned files for conflict and preservation tests.
func writeSkillFixture(t *testing.T, path, data string) {
	t.Helper()
	// Create fixture parents with ordinary project permissions.
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	// Fixture setup failures must not masquerade as installation failures.
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
}

// assertSkillFile verifies complete instruction bytes or preserved user content.
func assertSkillFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	// A successful receipt must correspond to the actual intended file bytes.
	if err != nil || string(data) != want {
		t.Fatalf("unexpected content at %s: %v", path, err)
	}
}

// assertSkillMissing verifies that rejected or previewed work made no filesystem entry.
func assertSkillMissing(t *testing.T, path string) {
	t.Helper()
	// Lstat also detects unintended dangling symlinks.
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("expected no entry at %s: %v", path, err)
	}
}

// skillInfo reads file metadata for no-op installation assertions.
func skillInfo(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	// Metadata must exist after a successful installation.
	if err != nil {
		t.Fatal(err)
	}
	return info
}

// makeSkillSymlink prepares an intentional redirect for installer boundary tests.
func makeSkillSymlink(t *testing.T, target, link string) {
	t.Helper()
	// Only parents are created so the symlink occupies the actual destination.
	if err := os.MkdirAll(filepath.Dir(link), 0755); err != nil {
		t.Fatal(err)
	}
	// Supported Hostlier platforms provide ordinary filesystem symlinks.
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

// TestMinionCommandGuide installs the product guide and answers local help without a controller call.
func TestMinionCommandGuide(t *testing.T) {
	cwd := t.TempDir()
	receipt := installSkillsReceipt(t, cwd, "minion")
	// The topic installs exactly one skill under its own name.
	if len(receipt.Skills) != 1 || !strings.HasSuffix(receipt.Skills[0].Path, filepath.Join("hostlier-minion", "SKILL.md")) {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	guide := invokeSkills(t, cwd, "show", "minion")
	// The guide centres on the Fused connection and defers deployment to the deploy skill.
	for _, want := range []string{"name: hostlier-minion", "fused:", "tokenSecret", "hostlier skills show deploy"} {
		// Each anchor must be present in the shipped guide.
		if !strings.Contains(guide, want) {
			t.Errorf("guide missing %q", want)
		}
	}
	listing := invokeSkills(t, cwd, "list")
	// Minion now owns executable product commands rather than being a documentation-only topic.
	if !strings.Contains(listing, "minion\t") {
		t.Fatal("Minion command not listed")
	}
	// Both the bare topic and its help flag explain the guide offline.
	for _, args := range [][]string{{"minion"}, {"minion", "--help"}, {"help", "minion"}} {
		client := &fakeClient{}
		output, err := invoke(args, "", Options{CWD: cwd, Home: filepath.Join(cwd, "no-profile"), Client: client})
		// Topic help never contacts a controller or fails.
		if err != nil || len(client.calls) != 0 || !strings.Contains(output, "hostlier skills show minion") {
			t.Fatalf("%v: %v %q", args, err, output)
		}
	}
}

// countCommandSkills checks topic flags against the command list and counts command skills.
func countCommandSkills(t *testing.T, catalog []bundled.Skill) int {
	t.Helper()
	count := 0
	// Each skill is either a command guide or a topic, never both.
	for _, item := range catalog {
		// Topic guides must never shadow a real command.
		if _, isCommand := commandHelp[item.Command]; item.Topic == isCommand {
			t.Errorf("skill %s topic=%v disagrees with the command list", item.Command, item.Topic)
		}
		// Only command skills count towards command coverage.
		if !item.Topic {
			count++
		}
	}
	return count
}
