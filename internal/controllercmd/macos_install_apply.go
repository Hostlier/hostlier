package controllercmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"hostlier/internal/model"
)

func (sys macInitSystem) apply(ctx context.Context, approved macInitPlan, inspect func() (macInitPlan, error)) error {
	if err := os.MkdirAll(approved.StateDir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(approved.StateDir, "init.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return model.Err("INSTALL_STATE", "Another macOS installation is running")
	}
	p, err := inspect()
	if err != nil {
		return err
	}
	if p.Fingerprint != approved.Fingerprint {
		return model.Err("STALE_PLAN", "Review the updated macOS installation plan")
	}
	if err := sys.stageMacBinary(p); err != nil {
		return err
	}
	if p.config != nil {
		if err := writeMacInstallFile(p.ConfigPath, p.config, 0600, false); err != nil {
			return err
		}
	}
	return sys.activateMacAgent(ctx, p)
}

func (sys macInitSystem) stageMacBinary(p macInitPlan) error {
	digest, err := maintenanceHash(p.Executable)
	if err == nil {
		if digest == p.SourceSHA256 {
			return nil
		}
		return model.Err("INSTALL_OWNERSHIP", "The installed controller binary differs from its verified digest")
	}
	if !os.IsNotExist(err) {
		return err
	}
	data, err := os.ReadFile(sys.executable)
	if err != nil {
		return err
	}
	if macDigest(data) != p.SourceSHA256 {
		return model.Err("STALE_PLAN", "The controller executable changed after inspection")
	}
	return writeMacInstallFile(p.Executable, data, 0700, false)
}

func (sys macInitSystem) activateMacAgent(ctx context.Context, p macInitPlan) error {
	changed := !bytes.Equal(p.plist, p.previous)
	if p.AlreadyLoaded && !changed {
		return sys.ready(ctx, p)
	}
	target := sys.domain() + "/" + p.Label
	if p.AlreadyLoaded {
		if err := sys.bootoutMacAgent(ctx, target); err != nil {
			return err
		}
	}
	if changed {
		if err := writeMacInstallFile(p.LaunchAgent, p.plist, 0644, p.previous != nil); err != nil {
			return errors.Join(err, sys.restoreMacAgent(p, false, false))
		}
	}
	if err := sys.launchCommand(ctx, "bootstrap", sys.domain(), p.LaunchAgent); err != nil {
		return errors.Join(err, sys.restoreMacAgent(p, changed, true))
	}
	if err := sys.ready(ctx, p); err != nil {
		return errors.Join(err, sys.restoreMacAgent(p, changed, true))
	}
	if p.previousBinary != "" && p.previousBinary != p.Executable {
		// Delete a superseded executable only if it still matches the agent that owned it.
		if digest, err := maintenanceHash(p.previousBinary); err == nil && p.previousBinary == p.binaryPath(digest) {
			_ = os.Remove(p.previousBinary)
		}
	}
	return nil
}

func (sys macInitSystem) restoreMacAgent(p macInitPlan, restoreFile, stopCandidate bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if stopCandidate {
		target := sys.domain() + "/" + p.Label
		out, err := sys.launch(ctx, "print", target)
		if err == nil {
			if !strings.Contains(out, "path = "+p.LaunchAgent+"\n") {
				return model.Err("INSTALL_OWNERSHIP", "The launchd service changed during installation")
			}
			if err := sys.bootoutMacAgent(ctx, target); err != nil {
				return err
			}
		} else if !strings.Contains(out, "Could not find service") {
			return fmt.Errorf("inspect launchd service during recovery: %w: %s", err, strings.TrimSpace(out))
		}
	}
	if restoreFile {
		if p.previous == nil {
			return os.Remove(p.LaunchAgent)
		}
		if err := writeMacInstallFile(p.LaunchAgent, p.previous, 0644, true); err != nil {
			return err
		}
	}
	if p.AlreadyLoaded {
		return sys.launchCommand(ctx, "bootstrap", sys.domain(), p.LaunchAgent)
	}
	return nil
}

// bootoutMacAgent waits for launchd to release the label before loading its replacement.
func (sys macInitSystem) bootoutMacAgent(ctx context.Context, target string) error {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	if err := sys.launchCommand(ctx, "bootout", target); err != nil {
		return err
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		out, err := sys.launch(ctx, "print", target)
		if err != nil {
			if strings.Contains(out, "Could not find service") {
				return nil
			}
			return fmt.Errorf("wait for launchd to unload %s: %w: %s", target, err, strings.TrimSpace(out))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for launchd to unload %s: %w", target, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (sys macInitSystem) launchCommand(ctx context.Context, args ...string) error {
	out, err := sys.launch(ctx, args...)
	if err != nil {
		return fmt.Errorf("launchctl %s: %w: %s", args[0], err, strings.TrimSpace(out))
	}
	return nil
}

// writeMacInstallFile syncs a complete staging file before publishing it.
func writeMacInstallFile(path string, data []byte, mode os.FileMode, replace bool) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	stage, err := os.CreateTemp(parent, ".hostlier-init-")
	if err != nil {
		return err
	}
	defer os.Remove(stage.Name())
	err = stage.Chmod(mode)
	if err == nil {
		_, err = stage.Write(data)
	}
	if err == nil {
		err = stage.Sync()
	}
	if err = errors.Join(err, stage.Close()); err != nil {
		return err
	}
	if replace {
		return os.Rename(stage.Name(), path)
	}
	return os.Link(stage.Name(), path)
}
