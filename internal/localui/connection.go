package localui

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"hostlier/internal/client"
	"hostlier/internal/model"
)

// ConnectionProfile contains public saved metadata; credentials never enter the local API response.
type ConnectionProfile struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

// ConnectionState reports selected identity and an optional administrator-reviewed pairing request.
type ConnectionState struct {
	CLI      *CLIContext         `json:"cli,omitempty"`
	Managed  bool                `json:"managed"`
	Name     string              `json:"name"`
	URL      string              `json:"url"`
	Profiles []ConnectionProfile `json:"profiles"`
	Pending  *PairingView        `json:"pending,omitempty"`
}

// PairingView exposes only the public challenge used by the server administrator.
type PairingView struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Code      string `json:"code"`
	ExpiresAt int64  `json:"expiresAt"`
}

// pendingLink keeps the private polling secret and unauthenticated enrollment transport inside the CLI.
type pendingLink struct {
	PairingView
	id, secret string
	remote     Remote
}

// ManagedRemote lets a local dashboard start unlinked and replace its single upstream subscription.
type ManagedRemote struct {
	mu         sync.Mutex
	control    sync.Mutex
	store      client.Store
	remote     Remote
	name, url  string
	generation uint64
	changed    chan struct{}
	pending    *pendingLink
	factory    func(client.Options) (Remote, error)
}

// NewManagedRemote opens the selected saved profile when available without requiring first-run pairing.
func NewManagedRemote(store client.Store, selected string) (*ManagedRemote, error) {
	m := &ManagedRemote{store: store, changed: make(chan struct{}), factory: newControllerClient}
	profiles, err := store.List()
	// Corrupt saved metadata must not be replaced with an empty connection configuration.
	if err != nil {
		return nil, err
	}
	// Only an explicit name or saved current selection starts a remote connection.
	if selected != "" || profiles.Current != "" {
		// Initial command selection does not rewrite the default saved by another CLI invocation.
		if err = m.selectProfile(selected, false); err != nil {
			return nil, err
		}
	} // First run remains local until the user chooses a controller.
	return m, nil
}

// State reads public connection metadata without opening the separate credentials document.
func (m *ManagedRemote) State() (ConnectionState, error) {
	profiles, err := m.store.List()
	// Metadata failures cannot be hidden as a successful empty profile inventory.
	if err != nil {
		return ConnectionState{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := ConnectionState{CLI: commandContext(m.store.Home), Managed: true, Name: m.name, URL: m.url, Profiles: []ConnectionProfile{}}
	// Public metadata is all the browser needs to choose an existing connection.
	for name, profile := range profiles.Servers {
		out.Profiles = append(out.Profiles, ConnectionProfile{Name: name, URL: profile.URL})
	}
	// The browser never receives the polling identity or enrollment secret.
	if m.pending != nil {
		view := m.pending.PairingView
		out.Pending = &view
	}
	// Keep saved choices stable between browser refreshes.
	sort.Slice(out.Profiles, func(i, j int) bool { return out.Profiles[i].Name < out.Profiles[j].Name })
	return out, nil
}

// Select resolves a real saved credential and publishes a connection transition atomically.
func (m *ManagedRemote) Select(name string) error { return m.selectProfile(name, true) }

// selectProfile separates initial command selection from a user-requested default change.
func (m *ManagedRemote) selectProfile(name string, persist bool) error {
	m.control.Lock()
	defer m.control.Unlock()
	options, err := m.store.Get(name)
	// No existing subscription is canceled until the selected profile is usable.
	if err != nil {
		return err
	}
	remote, err := m.factory(options)
	// Certificate/trust configuration must be valid before the UI adopts a new connection.
	if err != nil {
		return err
	}
	// Match the CLI's saved selection when the default profile was requested.
	if name == "" {
		profiles, err := m.store.List()
		// A missing default cannot be inferred from credentials or controller observations.
		if err != nil {
			return err
		}
		name = profiles.Current
	} // Never invent a profile name from the controller hostname.
	// Explicit UI selection is shared with future ordinary CLI invocations.
	if persist {
		// Do not publish a selected connection unless the shared CLI default was saved.
		if err := m.store.Use(name); err != nil {
			return err
		}
	}
	m.activate(name, options.URL, remote)
	return nil
}

// activate wakes the one watch owner and invalidates all callbacks from the old controller.
func (m *ManagedRemote) activate(name, url string, remote Remote) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.remote, m.name, m.url = remote, name, url
	m.pending = nil
	m.generation++
	close(m.changed)
	m.changed = make(chan struct{})
}

// Request forwards authenticated requests through the currently selected CLI-owned connection.
func (m *ManagedRemote) Request(ctx context.Context, method, path string, body, result any) error {
	m.mu.Lock()
	remote := m.remote
	m.mu.Unlock()
	// An unpaired dashboard cannot acquire controller mutation authority.
	if remote == nil {
		return model.Err("NO_SERVER", "Link a controller first")
	}
	return remote.Request(ctx, method, path, body, result)
}

// WatchStatus owns at most one upstream subscription, including across controller changes.
func (m *ManagedRemote) WatchStatus(ctx context.Context, consume func(client.StatusEvent) error) error {
	// Selection changes cancel and join the previous watch before opening another controller stream.
	for ctx.Err() == nil {
		m.mu.Lock()
		remote, changed, generation := m.remote, m.changed, m.generation
		m.mu.Unlock()
		// Never retain a previous controller's observations during selection or first-run linking.
		if err := consume(client.StatusEvent{Reset: true}); err != nil {
			return err
		}
		// A first-run UI waits locally without polling an unconfigured server.
		if remote == nil {
			// An unlinked UI waits for explicit selection or CLI cancellation.
			select {
			// Shutdown cannot leave a first-run observer waiting indefinitely.
			case <-ctx.Done():
				return ctx.Err()
			// Controller selection is the only signal that can start an upstream watch.
			case <-changed:
				continue
			}
		} // Controller selection is the only wakeup needed here.
		if err := m.watchSelected(ctx, remote, changed, generation, consume); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// watchSelected suppresses old callbacks and waits for explicit selection after permanent remote errors.
func (m *ManagedRemote) watchSelected(ctx context.Context, remote Remote, changed <-chan struct{}, generation uint64, consume func(client.StatusEvent) error) error {
	watch, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	// The selected controller's observer remains independent of browser HTTP handlers.
	go func() {
		// Every callback belongs to the generation that opened this selected stream.
		done <- remote.WatchStatus(watch, func(event client.StatusEvent) error {
			m.mu.Lock()
			defer m.mu.Unlock()
			// An old stream cannot publish after a new controller has been selected.
			if generation != m.generation {
				return context.Canceled
			}
			return consume(event)
		})
	}()
	// Every exit joins the upstream goroutine so reconnects cannot multiply subscriptions.
	select {
	// A new selection replaces this controller and its cached data.
	case <-changed:
		cancel()
		<-done
		return nil
	// CLI shutdown stops observation without stopping remote accepted operations.
	case <-ctx.Done():
		cancel()
		<-done
		return ctx.Err()
	// A permanent auth/protocol failure stays disconnected until the user reconnects or changes selection.
	case <-done:
		// Permanent failure is visible without reusing old authority for another controller.
		if err := consume(client.StatusEvent{}); err != nil {
			return err
		}
		// A permanent remote failure waits for user selection rather than spinning.
		select {
		// Explicit reconnection or selection may restart the single upstream owner.
		case <-changed:
			return nil
		// CLI shutdown interrupts the disconnected state immediately.
		case <-ctx.Done():
			return ctx.Err()
		} // No background resubmission or enrollment occurs.
	}
}

// BeginLink creates a bounded public pairing challenge and retains its private secret in CLI memory.
func (m *ManagedRemote) BeginLink(ctx context.Context, name, address string) (ConnectionState, error) {
	m.control.Lock()
	defer m.control.Unlock()
	address, err := m.validateLink(name, address)
	// Validate local intent before allocating any public controller challenge.
	if err != nil {
		return ConnectionState{}, err
	}
	remote, err := m.factory(client.Options{URL: address})
	// Normal certificate verification and HOSTLIER_CA_FILE also apply to first-run enrollment.
	if err != nil {
		return ConnectionState{}, err
	}
	var receipt pairingReceipt
	// The server's existing administrator-approved pairing protocol remains the sole enrollment authority.
	if err = remote.Request(ctx, "POST", "/v1/pairing/requests", map[string]string{"name": name}, &receipt); err != nil {
		return ConnectionState{}, err
	}
	// A missing secret or already expired challenge must never become a selectable profile.
	if !receipt.valid() {
		return ConnectionState{}, model.Err("ENROLLMENT", "Controller returned an invalid pairing challenge")
	}
	m.mu.Lock()
	m.pending = &pendingLink{PairingView: PairingView{Name: name, URL: address, Code: receipt.Code, ExpiresAt: receipt.ExpiresAt}, id: receipt.RequestID, secret: receipt.RequestSecret, remote: remote}
	m.mu.Unlock()
	return m.State()
}

// PollLink consumes approved enrollment once and persists its credential using the ordinary CLI store.
func (m *ManagedRemote) PollLink(ctx context.Context) (ConnectionState, error) {
	m.control.Lock()
	defer m.control.Unlock()
	m.mu.Lock()
	pending := m.pending
	m.mu.Unlock()
	// Refreshes after completed enrollment simply return the saved selected state.
	if pending == nil {
		return m.State()
	}
	// Expired challenges no longer retain private polling secrets in memory.
	if pending.ExpiresAt <= time.Now().UnixMilli() {
		m.mu.Lock()
		m.pending = nil
		m.mu.Unlock()
		return ConnectionState{}, model.Err("INVALID_ENROLLMENT", "Pairing request expired; create a new request")
	}
	var credential struct {
		ClientID string `json:"clientId"`
		Token    string `json:"token"`
		Status   string `json:"status"`
	}
	// Polling never approves the request and does not expose the private polling secret to the browser.
	if err := pending.remote.Request(ctx, "POST", "/v1/pairing/poll", map[string]string{"requestId": pending.id, "requestSecret": pending.secret}, &credential); err != nil {
		return ConnectionState{}, err
	}
	// A server administrator must approve before the poll can return credentials.
	if credential.Status == "pending" {
		return m.State()
	} // The independent server administrator has not granted enrollment yet.
	// Missing identity or credential cannot publish a usable local connection.
	if credential.Token == "" || credential.ClientID == "" {
		return ConnectionState{}, model.Err("ENROLLMENT", "Controller returned no client credential")
	}
	remote, err := m.factory(client.Options{URL: pending.URL, Token: credential.Token})
	// Construct the credential-bearing transport before advertising a completed link.
	if err != nil {
		return ConnectionState{}, err
	}
	// Publish no completed connection until its local credential is durable.
	if err = m.store.Save(pending.Name, pending.URL, credential.ClientID, credential.Token); err != nil {
		return ConnectionState{}, err
	} // Credentials are written mode 0600, outside the browser.
	m.activate(pending.Name, pending.URL, remote)
	return m.State()
}

// newControllerClient applies the normal verified HTTPS and device-signing transport to the dashboard.
func newControllerClient(options client.Options) (Remote, error) { return client.New(options) }

// validateLink rejects invalid names, insecure origins and conflicting pending enrollment before network I/O.
func (m *ManagedRemote) validateLink(name, address string) (string, error) {
	// Profile identities use the same portable contract in the CLI and browser.
	if err := client.ValidateProfileName(name); err != nil {
		return "", err
	}
	address, err := client.ControllerURL(address)
	// TLS authority is mandatory even for local VM enrollment.
	if err != nil {
		return "", err
	}
	profiles, err := m.store.List()
	// Corrupt public metadata cannot be replaced during a new link attempt.
	if err != nil {
		return "", err
	}
	// Saved identities are selected explicitly rather than silently overwritten.
	if _, exists := profiles.Servers[name]; exists {
		return "", model.Err("PROFILE_EXISTS", "A profile with this name already exists")
	}
	m.mu.Lock()
	pending := m.pending
	m.mu.Unlock()
	// Keep at most one unexpired enrollment secret in this CLI dashboard.
	if pending != nil && pending.ExpiresAt > time.Now().UnixMilli() {
		return "", model.Err("PAIRING_PENDING", "Finish or wait for the current pairing request to expire")
	}
	return address, nil
}

// pairingReceipt is the controller's public challenge together with its CLI-private polling material.
type pairingReceipt struct {
	RequestID     string `json:"requestId"`
	Code          string `json:"code"`
	RequestSecret string `json:"requestSecret"`
	ExpiresAt     int64  `json:"expiresAt"`
}

// valid accepts only the controller's bounded canonical hexadecimal approval locator.
func (r pairingReceipt) valid() bool {
	// Missing or expired secrets cannot turn a request into a selectable connection.
	if r.RequestID == "" || r.RequestSecret == "" || r.ExpiresAt <= time.Now().UnixMilli() {
		return false
	}
	// Pairing codes appear in a human-facing terminal command, so their spelling is canonical.
	if len(r.Code) != 16 {
		return false
	}
	// Reject arbitrary controller-provided command text rather than rendering it as an instruction.
	for _, char := range r.Code {
		// Accept only canonical hexadecimal characters in displayed pairing commands.
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return false
		}
	} // Existing enrollment codes are lower-case hex.
	return true
}

// CLIContext carries public invocation paths only; it never contains a controller or provider credential.
type CLIContext struct {
	Binary string `json:"binary"`
	Home   string `json:"home"`
	CAFile string `json:"caFile,omitempty"`
}

// commandContext preserves this dashboard invocation's credential directory and private-CA trust for terminal handoffs.
func commandContext(home string) *CLIContext {
	binary, err := os.Executable()
	// A missing executable path falls back to normal PATH resolution without exposing runtime errors.
	if err != nil {
		binary = "hostlier"
	}
	home, _ = filepath.Abs(home)
	ca := os.Getenv("HOSTLIER_CA_FILE")
	// Relative trust files must resolve the same way when copied into another terminal directory.
	if ca != "" {
		ca, _ = filepath.Abs(ca)
	}
	return &CLIContext{Binary: binary, Home: home, CAFile: ca}
}
