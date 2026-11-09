// Package cloudsdk manages customer Hostlier apps from a vendor's bundled .hostlier.
// It calls the controller API directly with an owner-issued device credential.
package cloudsdk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxDocument = 1024 * 1024

var appName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var volumeSize = regexp.MustCompile(`^([1-9][0-9]*)(Ki|Mi|Gi|Ti)$`)

// Result contains the controller's JSON receipt and optional progress details.
type Result struct{ Stdout, Stderr string }

// Runner is an injectable API adapter for tests and custom transports.
type Runner interface {
	Run(context.Context, []string, string) (Result, error)
}

// InputRunner supplies private secret bytes to the controller API adapter.
type InputRunner interface {
	RunInput(context.Context, []string, string, []byte) (Result, error)
}

// Options binds one immutable vendor bundle to durable local instance state.
type Options struct {
	BundlePath, StateDirectory, CredentialFile, Server string
	Runner                                             Runner
}

// Client owns only local template state; Hostlier owns provisioning and approvals.
type Client struct {
	template              map[string]any
	digest, state, server string
	bundleDirectory       string
	runner                Runner
}

// Open validates the bundle once. It contacts no controller and creates no instance.
func Open(options Options) (*Client, error) {
	if options.BundlePath == "" || options.StateDirectory == "" {
		return nil, errors.New("bundle path and state directory are required")
	}
	bundlePath, err := filepath.Abs(options.BundlePath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(bundlePath)
	if err != nil {
		return nil, err
	}
	project, err := parseProject(data)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	state, err := filepath.Abs(options.StateDirectory)
	if err != nil {
		return nil, err
	}
	runner := options.Runner
	if runner == nil {
		runner, err = newAPIRunner(options.CredentialFile)
		if err != nil {
			return nil, err
		}
	}
	return &Client{template: project, digest: hex.EncodeToString(sum[:]), state: state, server: options.Server, bundleDirectory: filepath.Dir(bundlePath), runner: runner}, nil
}

// BundleDigest identifies the exact .hostlier bytes used for new instances.
func (c *Client) BundleDigest() string { return c.digest }

// SetSecret writes one controller secret through its protected API.
// The value is never placed in arguments, the bundle, or SDK state.
func (c *Client) SetSecret(ctx context.Context, name string, value []byte) error {
	return c.setSecret(ctx, "", name, value)
}

// SetInstanceSecret registers one stored secret as owned by an application.
// The controller restricts its deployment bindings to that app and deletes
// the secret automatically after that app's successful purge.
func (c *Client) SetInstanceSecret(ctx context.Context, app, name string, value []byte) error {
	if !appName.MatchString(app) || strings.HasSuffix(app, "-") {
		return errors.New("invalid owning application name")
	}
	return c.setSecret(ctx, app, name, value)
}

func (c *Client) setSecret(ctx context.Context, app, name string, value []byte) error {
	if !appName.MatchString(name) || strings.HasSuffix(name, "-") || len(value) == 0 || len(value) > 65536 {
		return errors.New("invalid stored secret name or value")
	}
	runner, ok := c.runner.(InputRunner)
	if !ok {
		return errors.New("Hostlier API adapter does not support private secret input")
	}
	args := []string{"secret", "set", name, "--stdin"}
	if app != "" {
		args = append(args, "--app", app)
	}
	if c.server != "" {
		args = append(args, "--server", c.server)
	}
	_, err := runner.RunInput(ctx, args, c.bundleDirectory, value)
	return err
}

// DeleteSecret removes a stored secret after Hostlier confirms it is unused.
// The controller rejects deletion while a deployment, rollback revision, or
// active operation still references the secret. Repeating deletion is safe.
func (c *Client) DeleteSecret(ctx context.Context, name string) error {
	if !appName.MatchString(name) || strings.HasSuffix(name, "-") {
		return errors.New("invalid stored secret name")
	}
	_, err := c.invoke(ctx, c.bundleDirectory, "secret", "delete", name)
	return err
}

// Compute is Hostlier's per-replica allocation. Both fields are required for an SDK change.
type Compute struct{ CPU, Memory string }

// CreateOptions supplies customer-specific values over the complete bundled project.
type CreateOptions struct {
	Name, Route string
	Routes      map[string]string
	Environment map[string]string
	// Secrets replaces declared bundle bindings with per-instance stored secret names.
	// Values must be secret:// references; plaintext credentials are never saved here.
	Secrets        map[string]string
	SidecarSecrets map[string]map[string]string
	Compute        *Compute
	Replicas       *int
}

// Create deploys one customer instance through Hostlier's plan and approval API.
func (c *Client) Create(ctx context.Context, input CreateOptions) (Result, error) {
	directory, err := c.directory(input.Name)
	if err != nil {
		return Result{}, err
	}
	return c.locked(directory, func() (Result, error) {
		if err := noPending(directory); err != nil {
			return Result{}, err
		}
		if _, err := os.Stat(filepath.Join(directory, "retired.json")); err == nil {
			return Result{}, errors.New("this instance name was retired; use a new identity")
		} else if !errors.Is(err, os.ErrNotExist) {
			return Result{}, err
		}
		if _, err := os.Stat(filepath.Join(directory, "instance.json")); err == nil {
			return Result{}, errors.New("instance already exists in SDK state")
		} else if !errors.Is(err, os.ErrNotExist) {
			return Result{}, err
		}
		project, err := clone(c.template)
		if err != nil {
			return Result{}, err
		}
		project["name"] = input.Name
		if err := setRoutes(project, input.Route, input.Routes); err != nil {
			return Result{}, err
		}
		if err := mergeEnvironment(project, input.Environment); err != nil {
			return Result{}, err
		}
		if err := mergeSecrets(project, input.Secrets); err != nil {
			return Result{}, err
		}
		if err := mergeSidecarSecrets(project, input.SidecarSecrets); err != nil {
			return Result{}, err
		}
		if input.Compute != nil {
			if err := setCompute(project, *input.Compute); err != nil {
				return Result{}, err
			}
		}
		if input.Replicas != nil {
			project["replicas"] = *input.Replicas
		}
		result, err := c.apply(ctx, directory, project, "deploy")
		if err != nil {
			return result, err
		}
		return c.finish(ctx, directory, input.Name, result)
	})
}

// SetEnvironment redeploys the saved complete project with changed nonsecret values.
func (c *Client) SetEnvironment(ctx context.Context, name string, changes map[string]string) (Result, error) {
	directory, err := c.directory(name)
	if err != nil {
		return Result{}, err
	}
	return c.locked(directory, func() (Result, error) {
		if err := noPending(directory); err != nil {
			return Result{}, err
		}
		project, err := c.load(directory, name)
		if err != nil {
			return Result{}, err
		}
		if err := c.assertCurrent(ctx, directory, name, project); err != nil {
			return Result{}, err
		}
		if err := mergeEnvironment(project, changes); err != nil {
			return Result{}, err
		}
		result, err := c.apply(ctx, directory, project, "update")
		if err != nil {
			return result, err
		}
		return c.finish(ctx, directory, name, result)
	})
}

// SetCompute uses Hostlier's scaling plan, including its downsize and approval semantics.
func (c *Client) SetCompute(ctx context.Context, name string, compute Compute) (Result, error) {
	directory, err := c.directory(name)
	if err != nil {
		return Result{}, err
	}
	return c.locked(directory, func() (Result, error) {
		if err := noPending(directory); err != nil {
			return Result{}, err
		}
		project, err := c.load(directory, name)
		if err != nil {
			return Result{}, err
		}
		if err := c.assertCurrent(ctx, directory, name, project); err != nil {
			return Result{}, err
		}
		if err := setCompute(project, compute); err != nil {
			return Result{}, err
		}
		candidate, err := c.validate(ctx, directory, project)
		if err != nil {
			return Result{}, err
		}
		_ = os.Remove(candidate)
		if err := c.pending(directory, "compute", project); err != nil {
			return Result{}, err
		}
		result, err := c.invoke(ctx, directory, "scale", name, "--cpu", compute.CPU, "--memory", compute.Memory)
		if err != nil {
			return result, err
		}
		return c.finish(ctx, directory, name, result)
	})
}

// ExpandSidecarVolume updates one declared sidecar volume through Hostlier's
// ordinary update plan. It accepts only expansion; runtime support and quota
// enforcement depend on the selected Hostlier storage backend.
func (c *Client) ExpandSidecarVolume(ctx context.Context, name, sidecar, volume, size string) (Result, error) {
	directory, err := c.directory(name)
	if err != nil {
		return Result{}, err
	}
	return c.locked(directory, func() (Result, error) {
		if err := noPending(directory); err != nil {
			return Result{}, err
		}
		project, err := c.load(directory, name)
		if err != nil {
			return Result{}, err
		}
		if err := c.assertCurrent(ctx, directory, name, project); err != nil {
			return Result{}, err
		}
		sidecars, _ := project["sidecars"].(map[string]any)
		member, _ := sidecars[sidecar].(map[string]any)
		volumes, _ := member["volumes"].(map[string]any)
		declaration, _ := volumes[volume].(map[string]any)
		oldSize, _ := declaration["size"].(string)
		oldBytes, oldValid := volumeBytes(oldSize)
		newBytes, newValid := volumeBytes(size)
		if !oldValid || !newValid || newBytes <= oldBytes {
			return Result{}, errors.New("sidecar volume must be declared and new size must be a valid expansion")
		}
		declaration["size"] = size
		result, err := c.apply(ctx, directory, project, "update")
		if err != nil {
			return result, err
		}
		return c.finish(ctx, directory, name, result)
	})
}

func volumeBytes(value string) (int64, bool) {
	parts := volumeSize.FindStringSubmatch(value)
	if parts == nil {
		return 0, false
	}
	n, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return 0, false
	}
	shift := map[string]uint{"Ki": 10, "Mi": 20, "Gi": 30, "Ti": 40}[parts[2]]
	if n > (int64(^uint64(0)>>1) >> shift) {
		return 0, false
	}
	return n << shift, true
}

// Status reads the named application through the controller API.
func (c *Client) Status(ctx context.Context, name string) (Result, error) {
	if _, err := c.directory(name); err != nil {
		return Result{}, err
	}
	return c.invoke(ctx, c.bundleDirectory, "status", name, "--json")
}

// ApplicationStatus is the controller's named application status, including
// its public route and optional runtime readiness diagnostics.
type ApplicationStatus struct {
	Name          string         `json:"name"`
	URL           string         `json:"url"`
	Status        string         `json:"status"`
	Spec          map[string]any `json:"spec"`
	RuntimeStatus struct {
		Running       *bool `json:"running"`
		ReadyReplicas *int  `json:"readyReplicas"`
	} `json:"runtimeStatus"`
}

// Ready requires a running controller deployment and a public URL. Runtime
// diagnostics, when provided, must also confirm a running ready replica.
func (status ApplicationStatus) Ready() bool {
	if status.Name == "" || status.Status != "running" || status.URL == "" {
		return false
	}
	if status.RuntimeStatus.Running != nil && !*status.RuntimeStatus.Running {
		return false
	}
	return status.RuntimeStatus.ReadyReplicas == nil || *status.RuntimeStatus.ReadyReplicas > 0
}

func (c *Client) StatusSnapshot(ctx context.Context, name string) (ApplicationStatus, error) {
	if _, err := c.directory(name); err != nil {
		return ApplicationStatus{}, err
	}
	return c.readStatus(ctx, c.bundleDirectory, name)
}

func (c *Client) readStatus(ctx context.Context, directory, name string) (ApplicationStatus, error) {
	result, err := c.invoke(ctx, directory, "status", name, "--json")
	if err != nil {
		return ApplicationStatus{}, err
	}
	var status ApplicationStatus
	if json.Unmarshal([]byte(result.Stdout), &status) != nil || status.Name != name || status.Spec == nil || status.Spec["name"] != name {
		return ApplicationStatus{}, errors.New("Hostlier returned invalid application status")
	}
	return status, nil
}

// RecoverPending confirms that an interrupted operation already reached the
// reviewed image digest, desired project, and readiness. The expected digest
// must come from the approved plan or a separate operator review. This method
// never resubmits a mutation.
func (c *Client) RecoverPending(ctx context.Context, name, expectedImageDigest string) error {
	directory, err := c.directory(name)
	if err != nil {
		return err
	}
	_, err = c.locked(directory, func() (Result, error) {
		data, err := os.ReadFile(filepath.Join(directory, "pending.json"))
		if err != nil {
			return Result{}, err
		}
		var pending struct {
			Action       string         `json:"action"`
			Project      map[string]any `json:"project"`
			BundleDigest string         `json:"bundleDigest"`
		}
		if json.Unmarshal(data, &pending) != nil || pending.BundleDigest != c.digest || pending.Project["name"] != name || (pending.Action != "deploy" && pending.Action != "update" && pending.Action != "compute") {
			return Result{}, errors.New("invalid or mismatched pending Hostlier operation")
		}
		status, err := c.readStatus(ctx, directory, name)
		if err != nil {
			return Result{}, err
		}
		actual, err := confirmedSpec(status)
		if err != nil || !status.Ready() || !matchesPending(pending.Project, actual, expectedImageDigest) {
			return Result{}, errors.New("pending operation is not confirmed ready with the exact desired project; reconcile manually")
		}
		return Result{}, c.saveObserved(directory, name, actual)
	})
	return err
}

// Purge permanently deletes an instance and its owned data through Hostlier's
// delete --purge lifecycle action. An owner-authorized service credential can
// execute it unattended. An interrupted result leaves pending.json until the
// exact controller operation is confirmed.
func (c *Client) Purge(ctx context.Context, name string) (Result, error) {
	directory, err := c.directory(name)
	if err != nil {
		return Result{}, err
	}
	return c.locked(directory, func() (Result, error) {
		if err := noPending(directory); err != nil {
			return Result{}, err
		}
		project, err := c.load(directory, name)
		if err != nil {
			return Result{}, err
		}
		if err := c.assertCurrent(ctx, directory, name, project); err != nil {
			return Result{}, err
		}
		inventory, err := c.readInventory(ctx, directory)
		if err != nil {
			return Result{}, err
		}
		known := make([]string, 0, len(inventory.Operations))
		for _, operation := range inventory.Operations {
			known = append(known, operation.ID)
		}
		data, err := json.Marshal(struct {
			Action          string         `json:"action"`
			Project         map[string]any `json:"project"`
			BundleDigest    string         `json:"bundleDigest"`
			KnownOperations []string       `json:"knownOperations"`
		}{"purge", project, c.digest, known})
		if err != nil {
			return Result{}, err
		}
		if err := atomicWrite(filepath.Join(directory, "pending.json"), data); err != nil {
			return Result{}, err
		}
		result, err := c.invoke(ctx, directory, "delete", name, "--purge")
		if err != nil {
			return result, err
		}
		return result, c.markRetired(directory, name, "purged")
	})
}

type operationRecord struct {
	ID, Kind, Name, Action, Status string
	Purge                          bool
}

type controllerInventory struct {
	Applications []struct{ Name string } `json:"applications"`
	Operations   []operationRecord       `json:"operations"`
}

func (c *Client) readInventory(ctx context.Context, directory string) (controllerInventory, error) {
	result, err := c.invoke(ctx, directory, "status", "--json")
	if err != nil {
		return controllerInventory{}, err
	}
	var inventory controllerInventory
	if json.Unmarshal([]byte(result.Stdout), &inventory) != nil || inventory.Operations == nil || inventory.Applications == nil {
		return controllerInventory{}, errors.New("Hostlier returned invalid controller operation inventory")
	}
	return inventory, nil
}

// RecoverPendingPurge reconciles an interrupted delete. It confirms one new
// successful purge and an absent app, or retries only when the controller has
// no new action for this app and its exact previous spec still exists.
func (c *Client) RecoverPendingPurge(ctx context.Context, name string) error {
	directory, err := c.directory(name)
	if err != nil {
		return err
	}
	_, err = c.locked(directory, func() (Result, error) {
		data, err := os.ReadFile(filepath.Join(directory, "pending.json"))
		if err != nil {
			return Result{}, err
		}
		var pending struct {
			Action          string         `json:"action"`
			Project         map[string]any `json:"project"`
			BundleDigest    string         `json:"bundleDigest"`
			KnownOperations []string       `json:"knownOperations"`
		}
		if json.Unmarshal(data, &pending) != nil || pending.Action != "purge" || pending.BundleDigest != c.digest || pending.Project["name"] != name || pending.KnownOperations == nil {
			return Result{}, errors.New("no recoverable pending purge")
		}
		inventory, err := c.readInventory(ctx, directory)
		if err != nil {
			return Result{}, err
		}
		appExists := false
		for _, app := range inventory.Applications {
			if app.Name == name {
				appExists = true
			}
		}
		known := map[string]bool{}
		for _, id := range pending.KnownOperations {
			known[id] = true
		}
		matches, newActions := 0, 0
		for _, operation := range inventory.Operations {
			if !known[operation.ID] && operation.Kind == "action" && operation.Name == name {
				newActions++
				if operation.Action == "delete" && operation.Status == "succeeded" && operation.Purge {
					matches++
				}
			}
		}
		if !appExists && matches == 1 && newActions == 1 {
			return Result{}, c.markRetired(directory, name, "purged")
		}
		if appExists && newActions == 0 {
			if err := c.assertCurrent(ctx, directory, name, pending.Project); err != nil {
				return Result{}, err
			}
			result, err := c.invoke(ctx, directory, "delete", name, "--purge")
			if err != nil {
				return result, err
			}
			return result, c.markRetired(directory, name, "purged")
		}
		return Result{}, errors.New("controller did not confirm a safe permanent deletion or retry")
	})
	return err
}

// ConfirmPurge reconciles an interrupted purge from a controller operation ID.
// It requires a terminal, successful delete with purge=true for this app.
func (c *Client) ConfirmPurge(ctx context.Context, name, operationID string) error {
	if operationID == "" || len(operationID) > 128 || strings.ContainsAny(operationID, "/ \\ \t\r\n") {
		return errors.New("invalid Hostlier operation ID")
	}
	directory, err := c.directory(name)
	if err != nil {
		return err
	}
	_, err = c.locked(directory, func() (Result, error) {
		data, err := os.ReadFile(filepath.Join(directory, "pending.json"))
		if err != nil {
			return Result{}, err
		}
		var pending struct {
			Action          string         `json:"action"`
			Project         map[string]any `json:"project"`
			BundleDigest    string         `json:"bundleDigest"`
			KnownOperations []string       `json:"knownOperations"`
		}
		if json.Unmarshal(data, &pending) != nil || pending.Action != "purge" || pending.BundleDigest != c.digest || pending.Project["name"] != name {
			return Result{}, errors.New("no matching pending purge")
		}
		for _, known := range pending.KnownOperations {
			if known == operationID {
				return Result{}, errors.New("operation predates the pending purge")
			}
		}
		result, err := c.invoke(ctx, directory, "operation", operationID, "--json")
		if err != nil {
			return Result{}, err
		}
		var operation struct {
			ID, Kind, Name, Action, Status string
			Purge                          bool
		}
		if json.Unmarshal([]byte(result.Stdout), &operation) != nil || operation.ID != operationID || operation.Kind != "action" || operation.Name != name || operation.Action != "delete" || operation.Status != "succeeded" || !operation.Purge {
			return Result{}, errors.New("controller did not confirm this permanent deletion")
		}
		inventory, err := c.readInventory(ctx, directory)
		if err != nil {
			return Result{}, err
		}
		for _, app := range inventory.Applications {
			if app.Name == name {
				return Result{}, errors.New("application still exists after alleged purge")
			}
		}
		return Result{}, c.markRetired(directory, name, "purged")
	})
	return err
}

func (c *Client) markRetired(directory, name, disposition string) error {
	data, err := json.Marshal(map[string]string{"name": name, "bundleDigest": c.digest, "disposition": disposition})
	if err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(directory, "retired.json"), data); err != nil {
		return err
	}
	for _, file := range []string{"instance.json", ".hostlier", "pending.json"} {
		if err := os.Remove(filepath.Join(directory, file)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (c *Client) directory(name string) (string, error) {
	if !appName.MatchString(name) || strings.HasSuffix(name, "-") {
		return "", errors.New("invalid Hostlier application name")
	}
	return filepath.Join(c.state, name), nil
}

func (c *Client) invoke(ctx context.Context, directory string, args ...string) (Result, error) {
	if c.server != "" {
		args = append(args, "--server", c.server)
	}
	return c.runner.Run(ctx, args, directory)
}

func (c *Client) locked(directory string, task func() (Result, error)) (Result, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return Result{}, err
	}
	lock := filepath.Join(directory, ".lock")
	file, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		return Result{}, errors.New("instance is already being changed; inspect its operation before retrying")
	}
	if err != nil {
		return Result{}, err
	}
	_ = file.Close()
	defer os.Remove(lock)
	return task()
}

func (c *Client) validate(ctx context.Context, directory string, project map[string]any) (string, error) {
	data, err := render(project)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(directory, "candidate.hostlier")
	if err := atomicWrite(candidate, data); err != nil {
		return "", err
	}
	if _, err := c.invoke(ctx, directory, "validate", "--file", candidate); err != nil {
		_ = os.Remove(candidate)
		return "", err
	}
	return candidate, nil
}

func (c *Client) apply(ctx context.Context, directory string, project map[string]any, action string) (Result, error) {
	candidate, err := c.validate(ctx, directory, project)
	if err != nil {
		return Result{}, err
	}
	defer os.Remove(candidate)
	if err := c.pending(directory, action, project); err != nil {
		return Result{}, err
	}
	result, err := c.invoke(ctx, directory, action, "--file", candidate)
	var rejected *planRejectedError
	if errors.As(err, &rejected) {
		if clearErr := os.Remove(filepath.Join(directory, "pending.json")); clearErr != nil {
			return result, clearErr
		}
	}
	return result, err
}

func (c *Client) pending(directory, action string, project map[string]any) error {
	data, err := json.Marshal(struct {
		Action       string         `json:"action"`
		Project      map[string]any `json:"project"`
		BundleDigest string         `json:"bundleDigest"`
	}{action, project, c.digest})
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(directory, "pending.json"), data)
}

func noPending(directory string) error {
	_, err := os.Stat(filepath.Join(directory, "pending.json"))
	if err == nil {
		return errors.New("previous change has an uncertain result; inspect Hostlier status and pending.json before retrying")
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (c *Client) finish(ctx context.Context, directory, name string, result Result) (Result, error) {
	project, err := c.observed(ctx, directory, name)
	if err != nil {
		return result, err
	}
	return result, c.saveObserved(directory, name, project)
}

func (c *Client) saveObserved(directory, name string, project map[string]any) error {
	data, err := render(project)
	if err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(directory, ".hostlier"), data); err != nil {
		return err
	}
	metadata, err := json.Marshal(struct {
		Name         string `json:"name"`
		BundleDigest string `json:"bundleDigest"`
	}{name, c.digest})
	if err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(directory, "instance.json"), metadata); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(directory, "pending.json")); err != nil {
		return err
	}
	return nil
}

func (c *Client) observed(ctx context.Context, directory, name string) (map[string]any, error) {
	status, err := c.readStatus(ctx, directory, name)
	if err != nil {
		return nil, err
	}
	return confirmedSpec(status)
}

func confirmedSpec(status ApplicationStatus) (map[string]any, error) {
	image, ok := status.Spec["image"].(string)
	if !ok || !strings.Contains(image, "@sha256:") {
		return nil, errors.New("Hostlier did not confirm a pinned deployed image")
	}
	if source, ok := status.Spec["source"].(map[string]any); ok && source["image"] != image {
		return nil, errors.New("Hostlier returned conflicting source and image")
	}
	return status.Spec, nil
}

func matchesPending(want, actual map[string]any, expectedImageDigest string) bool {
	image, ok := actual["image"].(string)
	if !ok || image != expectedImageDigest || !strings.Contains(image, "@sha256:") {
		return false
	}
	copy, err := clone(want)
	if err != nil {
		return false
	}
	copy["image"] = image
	if source, ok := copy["source"].(map[string]any); ok {
		source["image"] = image
	}
	left, leftErr := json.Marshal(copy)
	right, rightErr := json.Marshal(actual)
	return leftErr == nil && rightErr == nil && bytes.Equal(left, right)
}

func (c *Client) assertCurrent(ctx context.Context, directory, name string, project map[string]any) error {
	actual, err := c.observed(ctx, directory, name)
	if err != nil {
		return err
	}
	left, err := json.Marshal(actual)
	if err != nil {
		return err
	}
	right, err := json.Marshal(project)
	if err != nil {
		return err
	}
	if !bytes.Equal(left, right) {
		return errors.New("Hostlier desired configuration changed outside this SDK; inspect before another change")
	}
	return nil
}

func (c *Client) load(directory, name string) (map[string]any, error) {
	data, err := os.ReadFile(filepath.Join(directory, "instance.json"))
	if err != nil {
		return nil, err
	}
	var metadata struct {
		Name         string `json:"name"`
		BundleDigest string `json:"bundleDigest"`
	}
	if json.Unmarshal(data, &metadata) != nil || metadata.Name != name || metadata.BundleDigest != c.digest {
		return nil, errors.New("instance bundle differs from this SDK release; use its pinned bundle")
	}
	data, err = os.ReadFile(filepath.Join(directory, ".hostlier"))
	if err != nil {
		return nil, err
	}
	project, err := parseProject(data)
	if err != nil {
		return nil, err
	}
	if project["name"] != name {
		return nil, errors.New("saved instance name does not match its directory")
	}
	return project, nil
}

func parseProject(data []byte) (map[string]any, error) {
	if len(data) > maxDocument {
		return nil, errors.New("bundle exceeds Hostlier's 1 MiB configuration limit")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, errors.New("invalid bundled .hostlier YAML")
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("exactly one .hostlier document is required")
	}
	if hasAlias(&document) {
		return nil, errors.New("YAML aliases are not allowed in cloud bundles")
	}
	var project map[string]any
	if err := document.Decode(&project); err != nil || project == nil {
		return nil, errors.New("bundle must contain a project mapping")
	}
	if err := rejectLocal(project); err != nil {
		return nil, err
	}
	return project, nil
}

func hasAlias(node *yaml.Node) bool {
	if node.Kind == yaml.AliasNode {
		return true
	}
	for _, child := range node.Content {
		if hasAlias(child) {
			return true
		}
	}
	return false
}

func rejectLocal(project map[string]any) error {
	if project["package"] != nil || project["packages"] != nil {
		return errors.New("local project package hooks are unsupported")
	}
	if source, ok := project["source"].(map[string]any); ok && (source["container"] != nil || source["harnest"] != nil || source["image"] == nil) {
		return errors.New("cloud bundles require a container image")
	}
	if configs, ok := project["configs"].(map[string]any); ok {
		for _, value := range configs {
			if file, ok := value.(map[string]any); ok && file["file"] != nil {
				return errors.New("file-backed managed configs are unsupported")
			}
		}
	}
	if sidecars, ok := project["sidecars"].(map[string]any); ok {
		for _, value := range sidecars {
			member, ok := value.(map[string]any)
			if !ok {
				return errors.New("invalid sidecar")
			}
			if err := rejectLocal(member); err != nil {
				return err
			}
		}
	}
	return nil
}

func render(project map[string]any) ([]byte, error) {
	data, err := yaml.Marshal(project)
	if err != nil {
		return nil, err
	}
	if len(data) > maxDocument {
		return nil, errors.New("rendered project exceeds Hostlier's configuration limit")
	}
	return data, nil
}

func atomicWrite(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".cloud-sdk-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := file.Chmod(0600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func clone(project map[string]any) (map[string]any, error) {
	data, err := render(project)
	if err != nil {
		return nil, err
	}
	return parseProject(data)
}

func mergeEnvironment(project map[string]any, values map[string]string) error {
	env, _ := project["env"].(map[string]any)
	if env == nil {
		env = map[string]any{}
	}
	for key, value := range values {
		if !envName.MatchString(key) || len(value) > 65536 || strings.ContainsRune(value, 0) {
			return errors.New("invalid environment entry")
		}
		env[key] = value
	}
	project["env"] = env
	return nil
}

func mergeSecrets(project map[string]any, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	declared, ok := project["secrets"].(map[string]any)
	if !ok {
		return errors.New("bundle declares no secrets")
	}
	for key, value := range values {
		if !envName.MatchString(key) || !strings.HasPrefix(value, "secret://") || len(value) <= len("secret://") || len(value) > 256 || strings.ContainsAny(value, " \t\r\n") {
			return errors.New("invalid stored secret reference")
		}
		if _, exists := declared[key]; !exists {
			return fmt.Errorf("bundle does not declare secret %s", key)
		}
		declared[key] = value
	}
	return nil
}

func mergeSidecarSecrets(project map[string]any, values map[string]map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	sidecars, ok := project["sidecars"].(map[string]any)
	if !ok {
		return errors.New("bundle declares no sidecars")
	}
	for name, bindings := range values {
		member, ok := sidecars[name].(map[string]any)
		if !ok {
			return fmt.Errorf("bundle does not declare sidecar %s", name)
		}
		if err := mergeSecrets(member, bindings); err != nil {
			return fmt.Errorf("sidecar %s: %w", name, err)
		}
	}
	return nil
}

func setCompute(project map[string]any, value Compute) error {
	if value.CPU == "" || value.Memory == "" {
		return errors.New("CPU and memory are both required")
	}
	project["resources"] = map[string]any{"cpu": value.CPU, "memory": value.Memory}
	return nil
}

func setRoutes(project map[string]any, primary string, routes map[string]string) error {
	if endpoints, ok := project["endpoints"].(map[string]any); ok && len(endpoints) > 0 {
		primaryName, _ := project["primaryEndpoint"].(string)
		if primaryName == "" {
			for _, name := range []string{"http", "main", "default"} {
				if _, ok := endpoints[name]; ok {
					primaryName = name
					break
				}
			}
		}
		if primaryName == "" {
			for name := range endpoints {
				if primaryName == "" || name < primaryName {
					primaryName = name
				}
			}
		}
		for name := range routes {
			if _, ok := endpoints[name]; !ok {
				return errors.New("route names an unknown endpoint")
			}
		}
		for name, value := range endpoints {
			endpoint, ok := value.(map[string]any)
			if !ok {
				return errors.New("invalid endpoint")
			}
			if endpoint["exposure"] != "public" {
				continue
			}
			address := routes[name]
			if name == primaryName && primary != "" {
				address = primary
			}
			if address == "" {
				return fmt.Errorf("customer route required for public endpoint %s", name)
			}
			endpoint["route"] = address
			if name == primaryName {
				project["route"] = address
			}
		}
		return nil
	}
	version, _ := project["version"].(int)
	endpoint, _ := project["endpoint"].(map[string]any)
	if version == 1 || endpoint["exposure"] == "public" {
		if primary == "" {
			return errors.New("customer route required for public application")
		}
		project["route"] = primary
		if endpoint != nil {
			endpoint["route"] = primary
		}
	} else if primary != "" {
		return errors.New("private application cannot receive a public route")
	}
	return nil
}
