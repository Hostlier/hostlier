package controller

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// managedCredential stores the private token beside its durable verifier identity.
type managedCredential struct {
	ID        string    `json:"id"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// managedCredentials keeps a deployed token alive while a replacement is being installed.
type managedCredentials struct {
	Active  *managedCredential `json:"active,omitempty"`
	Pending *managedCredential `json:"pending,omitempty"`
}

// prepareManagedCredential stages a bounded replacement without revoking working exports.
func (c *Controller) prepareManagedCredential() (managedCredentials, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Stop before publishing authority or readiness when the prerequisite fails.
	if c.closed {
		return managedCredentials{}, errors.New("controller closed")
	}
	credentials, err := c.readManagedCredentials()
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err != nil {
		return credentials, err
	}
	// Handle this lifecycle state before continuing bounded reconciliation.
	if !c.usableManagedCredential(credentials.Pending, 0) {
		credentials.Pending = nil
	}
	// Handle this lifecycle state before continuing bounded reconciliation.
	if credentials.Pending == nil && !c.usableManagedCredential(credentials.Active, 3*24*time.Hour) {
		credentials.Pending = &managedCredential{ID: random()[:24], Token: "hlt_" + random(), ExpiresAt: c.opts.Now().Add(30 * 24 * time.Hour)}
		// Stop before publishing authority or readiness when the prerequisite fails.
		if err = c.writeManagedCredentials(credentials); err != nil {
			return credentials, err
		}
	}
	candidate := credentials.Pending
	// Handle this lifecycle state before continuing bounded reconciliation.
	if candidate == nil {
		candidate = credentials.Active
	}
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err = c.registerManagedCredential(candidate, credentials.Active); err != nil {
		return credentials, err
	}
	return credentials, nil
}

// usableManagedCredential respects explicit revocation and scheduled early rotation.
func (c *Controller) usableManagedCredential(token *managedCredential, advance time.Duration) bool {
	// Keep requests or credentials outside this scoped contract excluded.
	if token == nil || !c.opts.Now().Add(advance).Before(token.ExpiresAt) {
		return false
	}
	record, exists := c.state.TelemetryCollectors[token.ID]
	return !exists || (!record.Revoked && record.Verifier == verifier(token.Token) && record.Runtime == c.state.Runtime)
}

// registerManagedCredential persists authority while keeping at most active and pending managed slots.
func (c *Controller) registerManagedCredential(token, active *managedCredential) error {
	original := c.state.TelemetryCollectors
	live := c.liveTelemetryCollectors()
	// Process the bounded credential inventory or wait between supervision attempts.
	for id, record := range live {
		// Handle this lifecycle state before continuing bounded reconciliation.
		if record.Managed && id != token.ID && (active == nil || id != active.ID) {
			delete(live, id)
		}
	}
	// Stop before publishing authority or readiness when the prerequisite fails.
	if _, exists := live[token.ID]; !exists && len(live) >= 64 {
		return errors.New("collector credential capacity reached")
	}
	live[token.ID] = TelemetryCollector{ID: token.ID, Name: "Hostlier managed collector", Runtime: c.state.Runtime, Verifier: verifier(token.Token), ExpiresAt: token.ExpiresAt, Managed: true}
	c.state.TelemetryCollectors = live
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err := c.store.Save(c.state); err != nil {
		c.state.TelemetryCollectors = original
		return err
	}
	return nil
}

// completeManagedCredential retires old authority only after the replacement is healthy.
func (c *Controller) completeManagedCredential(credentials managedCredentials, token *managedCredential) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// Stop before publishing authority or readiness when the prerequisite fails.
	if c.closed {
		return errors.New("controller closed")
	}
	record, exists := c.state.TelemetryCollectors[token.ID]
	// Never resurrect a credential revoked while runtime provisioning was in flight.
	if !exists || record.Revoked || record.Verifier != verifier(token.Token) || !c.opts.Now().Before(record.ExpiresAt) {
		return errors.New("collector credential revoked during provisioning")
	}
	credentials.Active = token
	credentials.Pending = nil
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err := c.writeManagedCredentials(credentials); err != nil {
		return err
	}
	return c.registerManagedCredential(token, nil)
}

// readManagedCredentials never follows links and bounds private JSON before parsing.
func (c *Controller) readManagedCredentials() (managedCredentials, error) {
	path := filepath.Join(c.opts.StateDir, "managed-collector.json")
	var result managedCredentials
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err := privateManagedFile(path); err != nil {
		return result, err
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	// Stop before publishing authority or readiness when the prerequisite fails.
	if os.IsNotExist(err) {
		return result, nil
	}
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err != nil {
		return result, err
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, 4097))
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err != nil || len(body) > 4096 {
		return result, errors.New("invalid managed collector credential file")
	}
	// Stop before publishing authority or readiness when the prerequisite fails.
	if json.Unmarshal(body, &result) != nil {
		return result, errors.New("invalid managed collector credential file")
	}
	// Process the bounded credential inventory or wait between supervision attempts.
	for _, token := range []*managedCredential{result.Active, result.Pending} {
		// Stop before publishing authority or readiness when the prerequisite fails.
		if !validManagedCredential(token) {
			return result, errors.New("invalid managed collector credential")
		}
	}
	return result, nil
}

// writeManagedCredentials atomically persists a private checkpoint before granting its authority.
func (c *Controller) writeManagedCredentials(value managedCredentials) error {
	path := filepath.Join(c.opts.StateDir, "managed-collector.json")
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err := privateManagedFile(path); err != nil {
		return err
	}
	body, err := json.Marshal(value)
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(c.opts.StateDir, ".managed-collector-*")
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	// Handle this lifecycle state before continuing bounded reconciliation.
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	closed := file.Close()
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err != nil {
		return err
	}
	// Stop before publishing authority or readiness when the prerequisite fails.
	if closed != nil {
		return closed
	}
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err = os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(c.opts.StateDir)
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// privateManagedFile refuses redirection and shared files before private credentials are accessed.
func privateManagedFile(path string) error {
	info, err := os.Lstat(path)
	// Stop before publishing authority or readiness when the prerequisite fails.
	if os.IsNotExist(err) {
		return nil
	}
	// Stop before publishing authority or readiness when the prerequisite fails.
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	// Stop before publishing authority or readiness when the prerequisite fails.
	if !info.Mode().IsRegular() || !ok || stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm()&0077 != 0 {
		return errors.New("managed collector credentials must be a private regular file")
	}
	return nil
}

// validManagedCredential accepts only bounded persisted token identities generated by this controller.
func validManagedCredential(token *managedCredential) bool {
	return token == nil || (len(token.ID) == 24 && len(token.Token) == 68 && strings.HasPrefix(token.Token, "hlt_") && !token.ExpiresAt.IsZero())
}
