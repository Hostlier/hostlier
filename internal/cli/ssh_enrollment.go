package cli

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/pem"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"hostlier/internal/client"
	"hostlier/internal/model"
	"hostlier/internal/sshidentity"
)

// enrollShell requests an approved unprivileged account through the existing controller login.
func (s *session) enrollShell(profile, box string) (client.SSHConnection, error) {
	// Reject ambiguous state or honor the explicit SSH selection.
	if box != "" {
		return client.SSHConnection{}, model.Err("SSH_NOT_CONFIGURED", "For fleet boxes reuse setup SSH with server ssh configure --ssh USER@HOST; managed enrollment requires a direct controller profile")
	}
	s.flags["server"] = profile
	// Preserve failure before publishing or using SSH authority.
	if err := s.connectRemote(); err != nil {
		return client.SSHConnection{}, err
	}
	// Preserve failure before publishing or using SSH authority.
	if err := s.requireControllerCapability("sshAccess"); err != nil {
		return client.SSHConnection{}, err
	}
	p, err := s.store.List()
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return client.SSHConnection{}, err
	}
	identity := p.Servers[profile]
	keyPath, err := s.shellIdentityPath(identity)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return client.SSHConnection{}, err
	}
	public, err := shellPublicKey(keyPath)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return client.SSHConnection{}, err
	}
	var access model.SSHAccess
	// Preserve failure before publishing or using SSH authority.
	if err = s.request("POST", "/v1/controller/ssh", model.SSHAccessRequest{PublicKey: public}, &access); err != nil {
		return client.SSHConnection{}, err
	}
	connection, err := shellConnection(identity, keyPath, public, access)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return connection, err
	}
	// Preserve failure before publishing or using SSH authority.
	if err = s.store.SaveSSH(profile, "", connection); err != nil {
		return connection, err
	}
	return connection, nil
}

// shellPublicKey creates one protected local identity or reuses its existing private key.
func shellPublicKey(path string) (string, error) {
	info, err := os.Lstat(path)
	// Preserve failure before publishing or using SSH authority.
	if err == nil {
		// Reject ambiguous state or honor the explicit SSH selection.
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return "", model.Err("SSH_KEY", "SSH private key must be a private regular file")
		}
		raw, err := os.ReadFile(path)
		// Preserve failure before publishing or using SSH authority.
		if err != nil {
			return "", err
		}
		signer, err := ssh.ParsePrivateKey(raw)
		// Preserve failure before publishing or using SSH authority.
		if err != nil {
			return "", model.Err("SSH_KEY", "Cannot read managed SSH key")
		}
		return string(ssh.MarshalAuthorizedKey(signer.PublicKey())), nil
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if !os.IsNotExist(err) {
		return "", err
	}
	return createShellKey(path)
}

// createShellKey writes only to a new owner-only file; existing identities are never overwritten.
func createShellKey(path string) (string, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return "", err
	}
	block, err := ssh.MarshalPrivateKey(private, "")
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return "", err
	}
	_, err = f.Write(pem.EncodeToMemory(block))
	// Preserve failure before publishing or using SSH authority.
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return "", err
	}
	// Preserve failure before publishing or using SSH authority.
	if closeErr != nil {
		return "", closeErr
	}
	key, err := ssh.NewPublicKey(public)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return "", err
	}
	return string(ssh.MarshalAuthorizedKey(key)), nil
}

// revokeShell closes managed access through the approved API without needing a working SSH login.
func (s *session) revokeShell(profile, box string, connection client.SSHConnection) error {
	// Reject ambiguous state or honor the explicit SSH selection.
	if box != "" || !connection.Managed {
		return model.Err("SSH_NOT_MANAGED", "Only controller-provisioned SSH access can be revoked here")
	}
	s.flags["server"] = profile
	// Preserve failure before publishing or using SSH authority.
	if err := s.connectRemote(); err != nil {
		return err
	}
	// Preserve failure before publishing or using SSH authority.
	if err := s.request("DELETE", "/v1/controller/ssh", nil, nil); err != nil {
		return err
	}
	// Preserve failure before publishing or using SSH authority.
	if err := s.store.SaveSSH(profile, "", client.SSHConnection{}); err != nil {
		return err
	}
	return s.print(map[string]bool{"revoked": true})
}

// shellIdentityPath keeps per-device private keys inside a protected, non-symlink directory.
func (s *session) shellIdentityPath(identity client.Profile) (string, error) {
	dir := filepath.Join(s.store.Home, "ssh")
	// Preserve failure before publishing or using SSH authority.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return "", err
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if !info.IsDir() {
		return "", model.Err("SSH_KEY", "SSH key directory cannot be a symlink")
	}
	// Preserve failure before publishing or using SSH authority.
	if err := os.Chmod(dir, 0700); err != nil {
		return "", err
	}
	return filepath.Join(dir, fmt.Sprintf("%x", sha256.Sum256([]byte(identity.URL+"|"+identity.ClientID)))), nil
}

// shellConnection binds the granted user and host-key pin to the enrolled controller identity.
func shellConnection(identity client.Profile, keyPath, public string, access model.SSHAccess) (client.SSHConnection, error) {
	// Preserve failure before publishing or using SSH authority.
	if err := validateShellGrant(identity, public, access); err != nil {
		return client.SSHConnection{}, err
	}
	hostKey, _, err := sshidentity.PublicKey(access.HostKey)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return client.SSHConnection{}, err
	}
	origin, err := url.Parse(identity.URL)
	// Preserve failure before publishing or using SSH authority.
	if err != nil || origin.Hostname() == "" {
		return client.SSHConnection{}, model.Err("SSH_PROTOCOL", "Invalid controller origin")
	}
	host := origin.Hostname()
	known := keyPath + ".known_hosts"
	// Preserve failure before publishing or using SSH authority.
	if err = writeShellHostPin(known, []byte(knownhosts.Normalize(net.JoinHostPort(host, strconv.Itoa(access.Port)))+" "+hostKey+"\n")); err != nil {
		return client.SSHConnection{}, err
	}
	destination := host
	// Reject ambiguous state or honor the explicit SSH selection.
	if net.ParseIP(host) != nil && net.ParseIP(host).To4() == nil {
		destination = "[" + host + "]"
	}
	return client.SSHConnection{Destination: access.User + "@" + destination, IdentityFile: keyPath, KnownHostsFile: known, Port: access.Port, Managed: true}, nil
}

// writeShellHostPin atomically replaces a public host pin without following an existing file link.
func writeShellHostPin(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".hostkey-*")
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
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
	return os.Rename(f.Name(), path)
}

// validateShellGrant prevents a response for another key or Linux account from becoming local SSH authority.
func validateShellGrant(identity client.Profile, public string, access model.SSHAccess) error {
	_, fingerprint, err := sshidentity.PublicKey(public)
	// Preserve failure before publishing or using SSH authority.
	if err != nil {
		return err
	}
	// Reject ambiguous state or honor the explicit SSH selection.
	if access.Fingerprint != fingerprint || access.User != sshidentity.UserName(identity.ClientID) || access.Port < 1 || access.Port > 65535 {
		return model.Err("SSH_PROTOCOL", "Controller returned an invalid SSH account")
	}
	return nil
}
