package sshaccess

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"hostlier/internal/model"
	"hostlier/internal/sshidentity"
)

// Provider keeps account ownership records private and outside the user's writable workspace.
type Provider struct {
	StateDir string
	mu       sync.Mutex
}

// account is a durable ownership record; ambiguous or foreign accounts are never adopted.
type account struct {
	User      string
	UID       int
	PublicKey string
}

const homes = "/var/lib/hostlier-shells"

// Grant creates a non-sudo user or rotates only the key of a previously owned account.
func (p *Provider) Grant(ctx context.Context, actor string, input model.SSHAccessRequest) (model.SSHAccess, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	public, fingerprint, err := sshidentity.PublicKey(input.PublicKey)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return model.SSHAccess{}, err
	}
	// Preserve failure before publishing or using SSH authority.
	if err = p.prepare(); err != nil {
		return model.SSHAccess{}, err
	}
	name := sshidentity.UserName(actor)
	hostKey, port, err := inspectSSHD(ctx, name)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return model.SSHAccess{}, err
	}
	a, err := p.account(ctx, name)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return model.SSHAccess{}, err
	}
	// Preserve failure before publishing or using SSH authority.
	if err = prepareWorkspace(a); err != nil {
		return model.SSHAccess{}, err
	}
	home := filepath.Join(homes, name)
	// An explicit revocation disables the shell; a fresh approved grant may re-enable it.
	if _, err = command(ctx, "/usr/sbin/usermod", "--lock", "--shell", "/bin/sh", name); err != nil {
		return model.SSHAccess{}, err
	}
	// Preserve failure before publishing or using SSH authority.
	if err = atomic(filepath.Join(home, ".ssh", "authorized_keys"), []byte("restrict,pty "+public+"\n"), 0644); err != nil {
		return model.SSHAccess{}, err
	}
	a.PublicKey = public
	// Preserve failure before publishing or using SSH authority.
	if err = p.save(a); err != nil {
		_ = os.Remove(filepath.Join(home, ".ssh", "authorized_keys"))
		return model.SSHAccess{}, err
	}
	return model.SSHAccess{User: name, Port: port, HostKey: hostKey, Fingerprint: fingerprint}, nil
}

// Revoke removes login authority and terminates processes without deleting retained user data.
func (p *Provider) Revoke(ctx context.Context, actor string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Revocation must verify the same protected root installation as enrollment.
	if err := p.prepare(); err != nil {
		return err
	}
	name := sshidentity.UserName(actor)
	a, err := p.read(name)
	// Reject ambiguous state or honor the explicit SSH selection.
	if os.IsNotExist(err) {
		return nil
	}
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return err
	}
	// Preserve failure before publishing or using SSH authority.
	if err = verifyAccountIdentity(ctx, a); err != nil {
		return err
	}
	home := filepath.Join(homes, name)
	// Preserve failure before publishing or using SSH authority.
	if err = atomic(filepath.Join(home, ".ssh", "authorized_keys"), nil, 0644); err != nil {
		return err
	}
	// Preserve failure before publishing or using SSH authority.
	if _, err = command(ctx, "/usr/sbin/usermod", "--shell", "/usr/sbin/nologin", name); err != nil {
		return err
	}
	_, err = command(ctx, "/usr/bin/pkill", "-KILL", "-u", strconv.Itoa(a.UID))
	// pkill returns one when the account has no remaining processes.
	if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
		err = nil
	}
	return err
}

// prepare refuses unsupported or unprotected installations before modifying host accounts.
func (p *Provider) prepare() error {
	// Reject ambiguous state or honor the explicit SSH selection.
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return model.Err("SSH_UNAVAILABLE", "Managed SSH requires a root Linux controller")
	}
	// Preserve failure before publishing or using SSH authority.
	if err := secureDirectory(filepath.Join(p.StateDir, "ssh-access"), 0700); err != nil {
		return err
	}
	return secureDirectory(homes, 0755)
}

// account creates only absent identities; uncertain partial creation requires administrator review.
func (p *Provider) account(ctx context.Context, name string) (account, error) {
	a, err := p.read(name)
	// Preserve failure before publishing or using SSH authority.
	if err == nil {
		return a, verifyAccount(ctx, a)
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if !os.IsNotExist(err) {
		return a, err
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if _, err := command(ctx, "/usr/bin/id", "-u", name); err == nil {
		return a, model.Err("SSH_ACCOUNT", "Existing unowned SSH account requires administrator review")
	}
	home := filepath.Join(homes, name)
	// Reject ambiguous state or honor the explicit SSH selection.
	if _, err := os.Lstat(home); !os.IsNotExist(err) {
		return a, model.Err("SSH_ACCOUNT", "Existing SSH home requires administrator review")
	}
	// Preserve failure before publishing or using SSH authority.
	if _, err := command(ctx, "/usr/sbin/useradd", "--create-home", "--user-group", "--shell", "/usr/sbin/nologin", "--home-dir", home, name); err != nil {
		return a, err
	}
	raw, err := command(ctx, "/usr/bin/id", "-u", name)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return a, err
	}
	uid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	// Preserve failure before publishing or using SSH authority.
	if err != nil || uid < 1000 {
		return a, model.Err("SSH_ACCOUNT", "Invalid dedicated SSH account")
	}
	// No key is installed until its exact Linux UID has a durable controller-owned record.
	a = account{User: name, UID: uid}
	// Preserve failure before publishing or using SSH authority.
	if err = os.Chown(home, 0, 0); err != nil {
		return a, err
	}
	return a, p.save(a)
}

// verifyAccount rejects UID replacement, privilege-bearing supplementary groups and moved homes.
func verifyAccount(ctx context.Context, a account) error {
	// Group changes block new grants, but must not prevent revoking an existing credential.
	if err := verifyAccountIdentity(ctx, a); err != nil {
		return err
	}
	groups, err := command(ctx, "/usr/bin/id", "-Gn", a.User)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return err
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if strings.TrimSpace(string(groups)) != a.User {
		return model.Err("SSH_ACCOUNT", "Dedicated SSH account must have only its own group")
	}
	return nil
}

// command bounds fixed native administrator operations and never invokes a shell.
func command(ctx context.Context, path string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, path, args...).Output()
}

// read rejects account records that could have been replaced by the shell user.
func (p *Provider) read(name string) (account, error) {
	var a account
	path := filepath.Join(p.StateDir, "ssh-access", name+".json")
	info, err := os.Lstat(path)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return a, err
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 4096 {
		return a, model.Err("SSH_ACCOUNT", "Unsafe SSH ownership record")
	}
	raw, err := os.ReadFile(path)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return a, err
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if json.Unmarshal(raw, &a) != nil || a.User != name {
		return a, model.Err("SSH_ACCOUNT", "Invalid SSH ownership record")
	}
	return a, nil
}

// save publishes account ownership before allowing a native SSH login.
func (p *Provider) save(a account) error {
	raw, err := json.Marshal(a)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return err
	}
	return atomic(filepath.Join(p.StateDir, "ssh-access", a.User+".json"), raw, 0600)
}

// atomic replaces only a controller-owned directory entry and syncs complete bytes before publication.
func atomic(path string, data []byte, mode os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".ssh-access-*")
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	// Preserve failure before publishing or using SSH authority.
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	// Preserve failure before publishing or using SSH authority.
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return err
	}
	// Preserve failure before publishing or using SSH authority.
	if closeErr != nil {
		return closeErr
	}
	// Preserve failure before publishing or using SSH authority.
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// secureDirectory rejects links and writable ancestors before trusting root execution paths.
func secureDirectory(path string, mode os.FileMode) error {
	// Reject ambiguous state or honor the explicit SSH selection.
	if !filepath.IsAbs(path) {
		return fmt.Errorf("absolute SSH directory required")
	}
	parent := filepath.Dir(path)
	// Reject ambiguous state or honor the explicit SSH selection.
	if parent != path {
		// Preserve failure before publishing or using SSH authority.
		if err := protectedDirectory(parent); err != nil {
			return err
		}
	}
	// Preserve failure before publishing or using SSH authority.
	if err := os.Mkdir(path, mode); err != nil && !os.IsExist(err) {
		return err
	}
	// Preserve failure before publishing or using SSH authority.
	if err := protectedDirectory(path); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}

// protectedDirectory checks every ancestor without following directory symlinks.
func protectedDirectory(path string) error {
	info, err := os.Lstat(path)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	// Reject ambiguous state or honor the explicit SSH selection.
	if !ok || stat.Uid != 0 || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return model.Err("SSH_PATH", "SSH directories must be protected from other users")
	}
	// An unprivileged controller cannot provision accounts; Linux ownership is checked by the host helper.
	parent := filepath.Dir(path)
	// Reject ambiguous state or honor the explicit SSH selection.
	if parent != path {
		return protectedDirectory(parent)
	}
	return nil
}

// prepareWorkspace separates root-owned login authority from user-owned working files.
func prepareWorkspace(a account) error {
	// A root-owned home prevents the shell user from replacing its authorized_keys directory.
	home := filepath.Join(homes, a.User)
	// Preserve failure before publishing or using SSH authority.
	if err := secureDirectory(home, 0755); err != nil {
		return err
	}
	// Preserve failure before publishing or using SSH authority.
	if err := secureDirectory(filepath.Join(home, ".ssh"), 0755); err != nil {
		return err
	}
	// Preserve failure before publishing or using SSH authority.
	if err := os.MkdirAll(filepath.Join(home, "work"), 0700); err != nil {
		return err
	}
	workInfo, err := os.Lstat(filepath.Join(home, "work"))
	// Preserve failure before publishing or using SSH authority.
	if err != nil || !workInfo.IsDir() {
		return model.Err("SSH_PATH", "Unsafe SSH workspace")
	}
	// Preserve failure before publishing or using SSH authority.
	if err := os.Chown(filepath.Join(home, "work"), a.UID, -1); err != nil {
		return err
	}
	return nil
}

// verifyAccountIdentity prevents revocation from killing a replacement or foreign Linux identity.
func verifyAccountIdentity(ctx context.Context, a account) error {
	raw, err := command(ctx, "/usr/bin/getent", "passwd", a.User)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return err
	}
	parts := strings.Split(strings.TrimSpace(string(raw)), ":")
	// Reject ambiguous state or honor the explicit SSH selection.
	if len(parts) != 7 || parts[2] != strconv.Itoa(a.UID) || a.UID < 1000 || parts[5] != filepath.Join(homes, a.User) {
		return model.Err("SSH_ACCOUNT", "Dedicated SSH account has changed; administrator review required")
	}
	return nil
}
