package nginx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// SharedOptions identifies the existing master and the only include Hostlier may replace.
type SharedOptions struct {
	MainConfig, Fragment, StateDirectory, CertificatesFile string
	Options
	Dump func(context.Context, string) ([]byte, error)
}

var sharedMu sync.Mutex

// ActivationError carries only fixed stage names into protected diagnostics.
// The underlying error remains available to local callers and rollback.
type ActivationError struct {
	Stage string
	Err   error
}

func (e *ActivationError) Error() string { return e.Stage + ": " + e.Err.Error() }
func (e *ActivationError) Unwrap() error { return e.Err }
func (e *ActivationError) DiagnosticStage() string {
	var nested *ActivationError
	if errors.As(e.Err, &nested) {
		return e.Stage + "/" + nested.DiagnosticStage()
	}
	return e.Stage
}

func atActivationStage(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &ActivationError{Stage: stage, Err: err}
}

// SharedDefaults keeps coexistence assets separate from standalone Nginx and external Certbot.
func SharedDefaults(o SharedOptions) SharedOptions {
	if o.MainConfig == "" { // Keep the shared edge on its dedicated defaults.
		o.MainConfig = "/etc/nginx/nginx.conf"
	}
	if o.Fragment == "" { // Keep the shared edge on its dedicated defaults.
		o.Fragment = "/etc/nginx/conf.d/zz-hostlier.conf"
	}
	if o.StateDirectory == "" { // Keep the shared edge on its dedicated defaults.
		o.StateDirectory = "/etc/hostlier/nginx-existing"
	}
	if o.CertificatesFile == "" { // Keep the shared edge on its dedicated defaults.
		o.CertificatesFile = "/etc/hostlier/nginx-existing-tls.json"
	}
	if o.CertificateDirectory == "" { // Keep the shared edge on its dedicated defaults.
		o.CertificateDirectory = "/etc/hostlier/nginx-existing-certificates"
	}
	o.IsolatedACME = true
	o.CertificateUnit = "hostlier-nginx-existing-cert@"
	if o.Dump == nil { // Keep the shared edge on its dedicated defaults.
		o.Dump = dumpConfiguration
	}
	o.Options = defaults(o.Options)
	return o
}

// sharedState permits either side of an interrupted atomic publication, but never manual edits.
type sharedState struct {
	Hashes []string `json:"hashes"`
}

// contentHash binds generated configuration bytes to the private ownership record.
func contentHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// sharedPaths rejects redirection of managed files and unsafe Nginx interpolation.
func sharedPaths(o SharedOptions) error {
	for _, path := range []string{o.MainConfig, o.Fragment, o.StateDirectory, o.CertificatesFile, o.CertificateDirectory, o.ChallengeDirectory} { // Reject redirected or unsafe coexistence paths.
		if !safePath(path) || filepath.Clean(path) != path { // Reject redirected or unsafe coexistence paths.
			return errors.New("unsafe Nginx coexistence path")
		}
	}
	for _, path := range []string{o.Fragment, o.StateDirectory, filepath.Join(o.StateDirectory, "state.json")} { // Reject redirected or unsafe coexistence paths.
		info, err := os.Lstat(path)
		if err != nil && !errors.Is(err, os.ErrNotExist) { // Reject redirected or unsafe coexistence paths.
			return err
		}
		if err == nil && info.Mode()&os.ModeSymlink != 0 { // Reject redirected or unsafe coexistence paths.
			return errors.New("Nginx coexistence assets cannot be symlinks")
		}
	}
	return nil
}

// sharedPrevious refuses an existing include unless its exact content was previously published here.
func sharedPrevious(o SharedOptions) ([]byte, error) {
	if err := sharedPaths(o); err != nil { // Require exact recorded ownership before replacing an include.
		return nil, err
	}
	data, err := os.ReadFile(o.Fragment)
	if errors.Is(err, os.ErrNotExist) { // Require exact recorded ownership before replacing an include.
		return nil, nil
	}
	if err != nil { // Require exact recorded ownership before replacing an include.
		return nil, err
	}
	record, err := os.ReadFile(filepath.Join(o.StateDirectory, "state.json"))
	if err != nil { // Require exact recorded ownership before replacing an include.
		return nil, errors.New("existing Hostlier Nginx include has no ownership record")
	}
	var state sharedState
	if json.Unmarshal(record, &state) != nil { // Require exact recorded ownership before replacing an include.
		return nil, errors.New("invalid Nginx include ownership record")
	}
	for _, hash := range state.Hashes { // Require exact recorded ownership before replacing an include.
		if hash == contentHash(data) { // Require exact recorded ownership before replacing an include.
			return data, nil
		}
	}
	return nil, errors.New("Hostlier Nginx include was edited externally; restore it before retrying")
}

// InspectShared verifies the include boundary, external routes and certificate references without writes.
func InspectShared(ctx context.Context, options SharedOptions, hosts []string) (string, error) {
	o := SharedDefaults(options)
	if _, err := sharedPrevious(o); err != nil { // Complete read-only validation before authorizing publication.
		return "", err
	}
	dump, err := o.Dump(ctx, o.MainConfig)
	if err != nil { // Complete read-only validation before authorizing publication.
		return "", err
	}
	if err = inspectSharedDump(o, dump, hosts); err != nil { // Complete read-only validation before authorizing publication.
		return "", err
	}
	certificates, err := externalCertificates(o)
	if err != nil { // Complete read-only validation before authorizing publication.
		return "", err
	}
	for _, host := range hosts { // Complete read-only validation before authorizing publication.
		if cert, ok := certificates[host]; ok { // Complete read-only validation before authorizing publication.
			if err = validateCertificate(host, cert); err != nil { // Complete read-only validation before authorizing publication.
				return "", err
			}
		}
	}
	data, _ := json.Marshal(certificates)
	return contentHash(append(dump, data...)), nil
}

// CheckSharedController checks an initialized include without replacing application routes on init retries.
func CheckSharedController(ctx context.Context, options SharedOptions, controller string) error {
	o := SharedDefaults(options)
	if _, err := InspectShared(ctx, o, []string{controller}); err != nil { // Preserve existing application routes while verifying controller readiness.
		return err
	}
	data, err := sharedPrevious(o)
	if err != nil { // Preserve existing application routes while verifying controller readiness.
		return err
	}
	marker := fmt.Sprintf("server { listen %s ssl; server_name %s;", o.HTTPSAddress, controller)
	if !strings.Contains(strings.ReplaceAll(string(data), " ssl http2;", " ssl;"), marker) { // Preserve existing application routes while verifying controller readiness.
		return errors.New("Hostlier controller TLS route is incomplete; inspect the owned include before retrying initialization")
	}
	certificates, err := sharedCertificates(o, []string{controller})
	if err != nil { // Preserve existing application routes while verifying controller readiness.
		return err
	}
	return validateCertificate(controller, certificates[controller])
}

// ApplyShared stages challenges for new names, then activates TLS through the existing master.
func ApplyShared(ctx context.Context, options SharedOptions, controller, upstream string, routes []Route) error {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	o := SharedDefaults(options)
	unlock, err := lockShared(ctx, o)
	if err != nil { // Stop activation when its required preparation fails.
		return atActivationStage("lock", err)
	}
	defer unlock()
	hosts, err := sharedHosts(controller, routes)
	if err != nil { // Stop activation when its required preparation fails.
		return atActivationStage("route", err)
	}
	if _, err = InspectShared(ctx, o, hosts); err != nil { // Stop activation when its required preparation fails.
		return atActivationStage("ownership-inspection", err)
	}
	certificates, err := sharedCertificates(o, hosts)
	if err != nil { // Stop activation when its required preparation fails.
		return atActivationStage("certificate-reference", err)
	}
	previous, err := sharedPrevious(o)
	if err != nil { // Stop activation when its required preparation fails.
		return atActivationStage("owned-include", err)
	}
	bootstrap, err := renderShared(o, controller, upstream, routes, certificates, true)
	if err != nil { // Stop activation when its required preparation fails.
		return atActivationStage("bootstrap-render", err)
	}
	if err = publishShared(ctx, o, bootstrap, hosts); err != nil { // Stop activation when its required preparation fails.
		return atActivationStage("bootstrap-publication", err)
	}
	err = finishShared(ctx, o, controller, upstream, routes, hosts, certificates)
	if err != nil { // Stop activation when its required preparation fails.
		return restoreShared(ctx, o, previous, atActivationStage("tls-cutover", err))
	}
	return nil
}

// lockShared coordinates installer and controller publications across processes without following a substituted lock.
func lockShared(ctx context.Context, o SharedOptions) (func(), error) {
	if err := sharedPaths(o); err != nil { // Serialize changes across controller and installer processes.
		return nil, err
	}
	if err := os.MkdirAll(o.StateDirectory, 0700); err != nil { // Serialize changes across controller and installer processes.
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(o.StateDirectory, "lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil { // Serialize changes across controller and installer processes.
		return nil, err
	}
	if err = acquireSharedLock(ctx, file); err != nil { // Serialize changes across controller and installer processes.
		file.Close()
		return nil, err
	}
	return func() { syscall.Flock(int(file.Fd()), syscall.LOCK_UN); file.Close() }, nil // Serialize changes across controller and installer processes.
}

// acquireSharedLock waits only within the caller's bounded activation context.
func acquireSharedLock(ctx context.Context, file *os.File) error {
	for { // Bound lock acquisition while another publisher is active.
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil { // Bound lock acquisition while another publisher is active.
			return nil
		}
		if err != syscall.EWOULDBLOCK { // Bound lock acquisition while another publisher is active.
			return err
		}
		select { // Bound lock acquisition while another publisher is active.
		case <-ctx.Done(): // Bound lock acquisition while another publisher is active.
			return ctx.Err()
		case <-time.After(50 * time.Millisecond): // Bound lock acquisition while another publisher is active.
		}
	}
}

// finishShared obtains missing certificates and revalidates reservations before the TLS cutover.
func finishShared(ctx context.Context, o SharedOptions, controller, upstream string, routes []Route, hosts []string, certificates map[string]CertificateReference) error {
	var err error
	if err = issueSharedCertificates(ctx, o, hosts, certificates); err != nil { // Keep TLS activation dependent on successful issuance and fresh inspection.
		return atActivationStage("certificate-issuance", err)
	}
	// Certificate issuance can take minutes; recheck external hostname ownership before TLS cutover.
	if _, err = InspectShared(ctx, o, hosts); err != nil {
		return atActivationStage("cutover-inspection", err)
	}
	data, err := renderShared(o, controller, upstream, routes, certificates, false)
	if err == nil { // Keep TLS activation dependent on successful issuance and fresh inspection.
		err = publishShared(ctx, o, data, hosts)
		return atActivationStage("cutover-publication", err)
	}
	return atActivationStage("cutover-render", err)
}

// recordShared journals hashes before publication so interrupted writes remain recognizable.
func recordShared(o SharedOptions, hashes ...string) error {
	if err := os.MkdirAll(o.StateDirectory, 0700); err != nil { // Persist ownership before changing the live include.
		return err
	}
	data, _ := json.Marshal(sharedState{Hashes: hashes})
	return writeConfig(filepath.Join(o.StateDirectory, "state.json"), data)
}

// publishShared validates the entire existing configuration with the candidate include in place.
func publishShared(ctx context.Context, o SharedOptions, data []byte, hosts []string) error {
	previous, err := sharedPrevious(o)
	if err != nil { // Keep the last working include recoverable until full configuration validation succeeds.
		return atActivationStage("owned-include", err)
	}
	if err = recordShared(o, contentHash(previous), contentHash(data)); err != nil { // Keep the last working include recoverable until full configuration validation succeeds.
		return atActivationStage("ownership-record", err)
	}
	if err = writeConfig(o.Fragment, data); err != nil { // Keep the last working include recoverable until full configuration validation succeeds.
		return atActivationStage("include-write", err)
	}
	// Nginx treats duplicate names as warnings; recheck ownership with the candidate installed before reload.
	if _, err = InspectShared(ctx, o, hosts); err != nil {
		return restoreShared(ctx, o, previous, atActivationStage("published-inspection", err))
	}
	if err = o.Run(ctx, "nginx", "-t", "-c", o.MainConfig); err != nil { // Keep the last working include recoverable until full configuration validation succeeds.
		return restoreShared(ctx, o, previous, atActivationStage("nginx-validation", err))
	}
	if err = o.Run(ctx, "nginx", "-s", "reload", "-c", o.MainConfig); err != nil { // Keep the last working include recoverable until full configuration validation succeeds.
		return restoreShared(ctx, o, previous, atActivationStage("nginx-reload", err))
	}
	return atActivationStage("ownership-record", recordShared(o, contentHash(data)))
}

// restoreShared restores only our include and always reports the original failure to its caller.
func restoreShared(ctx context.Context, o SharedOptions, previous []byte, cause error) error {
	// Recovery gets a bounded independent context even if ACME or the caller timed out.
	recovery, cancel := context.WithTimeout(context.WithoutCancel(ctx), 20*time.Second)
	defer cancel()
	if _, err := sharedPrevious(o); err != nil { // Retain the original error while restoring only Hostlier-owned configuration.
		return fmt.Errorf("%w; rollback refused: %v", cause, err)
	}
	var err error
	if previous == nil { // Retain the original error while restoring only Hostlier-owned configuration.
		err = os.Remove(o.Fragment)
		if errors.Is(err, os.ErrNotExist) { // Retain the original error while restoring only Hostlier-owned configuration.
			err = nil
		}
	} else {
		err = recordShared(o, contentHash(previous))
		if err == nil { // Retain the original error while restoring only Hostlier-owned configuration.
			err = writeConfig(o.Fragment, previous)
		}
	}
	if err == nil { // Retain the original error while restoring only Hostlier-owned configuration.
		err = o.Run(recovery, "nginx", "-t", "-c", o.MainConfig)
	}
	if err == nil { // Retain the original error while restoring only Hostlier-owned configuration.
		err = o.Run(recovery, "nginx", "-s", "reload", "-c", o.MainConfig)
	}
	if err != nil { // Retain the original error while restoring only Hostlier-owned configuration.
		return fmt.Errorf("%w; Nginx include rollback failed: %v", cause, err)
	}
	return cause
}
