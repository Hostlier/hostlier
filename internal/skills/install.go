package skills

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Installation reports each selected skill and the action taken on its file.
type Installation struct {
	Command string `json:"command"`
	Path    string `json:"path"`
	Action  string `json:"action"`
}

// Install preflights the entire selection, then writes each changed skill atomically.
func Install(command, directory string, force, dryRun bool) ([]Installation, error) {
	items, err := Select(command)
	// Unknown selections must not create destination directories.
	if err != nil {
		return nil, err
	}
	plan, err := preflight(items, directory, force)
	// Dry runs and conflicts both leave the destination untouched.
	if err != nil || dryRun {
		return plan, err
	}
	// Each committed file is complete even if a later filesystem operation fails.
	for i, item := range items {
		// Identical installations preserve file timestamps and permissions.
		if plan[i].Action == "unchanged" {
			continue
		}
		// Report which skill failed; already committed files are safe to retry.
		if err := writeSkill(plan[i].Path, []byte(item.Content), force); err != nil {
			return plan[:i], fmt.Errorf("install %s: %w", item.Name, err)
		}
	}
	return plan, nil
}

// preflight rejects conflicting files before installing any selected skill.
func preflight(items []Skill, directory string, force bool) ([]Installation, error) {
	plan := make([]Installation, 0, len(items))
	// Check every destination so a late conflict cannot partially install the set.
	for _, item := range items {
		folder := filepath.Join(directory, item.Name)
		// A skill directory must not redirect writes into another skill or external tree.
		if err := checkFolder(folder); err != nil {
			return nil, err
		}
		path := filepath.Join(folder, "SKILL.md")
		action, err := fileAction(path, []byte(item.Content), force)
		// Preserve edits unless replacement was explicitly requested.
		if err != nil {
			return nil, err
		}
		plan = append(plan, Installation{Command: item.Command, Path: path, Action: action})
	}
	return plan, nil
}

// checkFolder accepts a missing or ordinary directory but never a symlink.
func checkFolder(path string) error {
	info, err := os.Lstat(path)
	// New skills do not yet have their containing folder.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	// Surface permissions and other filesystem failures without attempting writes.
	if err != nil {
		return err
	}
	// Symlinks and files are not owned skill directories.
	if !info.IsDir() {
		return fmt.Errorf("skill directory is not a regular directory: %s", path)
	}
	return nil
}

// fileAction distinguishes new, identical and explicitly replaceable skill files.
func fileAction(path string, data []byte, force bool) (string, error) {
	info, err := os.Lstat(path)
	// A missing skill can be created without replacement permission.
	if errors.Is(err, os.ErrNotExist) {
		return "created", nil
	}
	// Do not turn unreadable destinations into replacement candidates.
	if err != nil {
		return "", err
	}
	// Never follow a SKILL.md symlink or read a special file.
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("skill destination is not a regular file: %s", path)
	}
	previous, err := os.ReadFile(path)
	// Existing content must be readable before comparing or replacing it.
	if err != nil {
		return "", err
	}
	// Reinstalling the same binary is an idempotent no-op.
	if bytes.Equal(previous, data) {
		return "unchanged", nil
	}
	// Different content may contain user edits and requires an explicit overwrite.
	if !force {
		return "", fmt.Errorf("skill already exists with different content: %s; use --force to replace it", path)
	}
	return "updated", nil
}

// writeSkill stages complete bytes in the destination directory before publication.
func writeSkill(path string, data []byte, force bool) error {
	directory := filepath.Dir(path)
	// Create only the selected skills' directories.
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	// Recheck a skill directory that might have changed since preflight.
	if err := checkFolder(directory); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".hostlier-skill-*")
	// A staging failure leaves the prior skill intact.
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	// Close and flush before making the skill visible to an agent.
	if err := stageSkill(file, data); err != nil {
		return err
	}
	// Replacement switches a complete file in one filesystem operation.
	if force {
		return os.Rename(file.Name(), path)
	}
	// Linking publishes exclusively so a concurrent creator is never overwritten.
	return os.Link(file.Name(), path)
}

// stageSkill finishes the temporary file and propagates write or close failures.
func stageSkill(file *os.File, data []byte) error {
	_, err := file.Write(data)
	// Make installed documentation readable wherever the skill directory is shared.
	if err == nil {
		err = file.Chmod(0644)
	}
	// Flush complete content before publication.
	if err == nil {
		err = file.Sync()
	}
	return errors.Join(err, file.Close())
}
