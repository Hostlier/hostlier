package providers

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"hostlier/packages/engines"
	"hostlier/packages/usefused"
	"hostlier/pkg/packagekit"
	"hostlier/pkg/workflow"
)

// Package selects the installed provider identity and its bounded HTTP transport.
type Package struct {
	Provider string
	Client   Client
}

// config defines the direct workflow's public engine and lifecycle choices.
type config struct {
	Engine    string    `json:"engine"`
	Mode      string    `json:"mode"`
	Database  string    `json:"database,omitempty"`
	KeyPrefix string    `json:"keyPrefix,omitempty"`
	Instance  *instance `json:"instance,omitempty"`
}

// instance requires explicit paid placement rather than ambient provider defaults.
type instance struct {
	Project string `json:"project"`
	Service string `json:"service"`
	Plan    string `json:"plan"`
	Cloud   string `json:"cloud"`
}

// Handle executes direct workflows and delegates existing connection work to native engine packages.
func (p Package) Handle(ctx context.Context, req workflow.Request) (workflow.Response, error) {
	var c config
	raw, _ := json.Marshal(req.Step.Config)
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	// Reject invalid input: invalid direct provider configuration.
	if d.Decode(&c) != nil {
		return workflow.Response{}, errors.New("invalid direct provider configuration")
	}
	// Reject invalid input: invalid provider protocol.
	if !validProtocol(req, p.Provider) {
		return workflow.Response{}, errors.New("invalid provider protocol")
	}
	// Reject invalid input: unsupported provider.
	if p.Provider != "aiven" && p.Provider != "railway" {
		return workflow.Response{}, errors.New("unsupported provider")
	}
	// Validate provider state before advancing the workflow.
	if c.Mode != "new-instance" {
		return p.existing(ctx, req, c)
	}
	// Reject invalid input: direct Railway new-instance is unavailable: template storage, TLS and scoped-user capability must be verified; use an existing TLS connection or an explicit Usefused executor.
	if p.Provider == "railway" {
		return workflow.Response{}, errors.New("direct Railway new-instance is unavailable: template storage, TLS and scoped-user capability must be verified; use an existing TLS connection or an explicit Usefused executor")
	}
	return p.aiven(ctx, req, c)
}

// existing strips the provider engine selector before invoking the native workflow contract.
func (p Package) existing(ctx context.Context, req workflow.Request, c config) (workflow.Response, error) {
	// Reject invalid input: existing connection cannot include instance settings.
	if c.Instance != nil {
		return workflow.Response{}, errors.New("existing connection cannot include instance settings")
	}
	engine := c.Engine
	// Validate provider state before advancing the workflow.
	if engine == "valkey" {
		engine = "redis"
	}
	// Reject invalid input: direct workflow supports postgres and redis/valkey only.
	if engine != "postgres" && engine != "redis" {
		return workflow.Response{}, errors.New("direct workflow supports postgres and redis/valkey only")
	}
	copyConfig := map[string]any{}
	// Copy public engine options without mutating the caller's configuration.
	for k, v := range req.Step.Config {
		// Validate provider state before advancing the workflow.
		if k != "engine" {
			copyConfig[k] = v
		}
	}
	req.Step.Config = copyConfig
	return (engines.Package{Engine: engine}).Handle(ctx, req)
}

// fingerprint pins provider intent and resolved private inputs to the approved plan.
func fingerprint(req workflow.Request) string {
	req.Action = ""
	req.Prepared = nil
	req.Previous = nil
	data, _ := json.Marshal(req)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Serve exchanges exactly one bounded private protocol document.
func (p Package) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	return packagekit.Serve(ctx, p, in, out)
}

// Main retains explicitly installed Usefused executor configuration as an opt-in route.
func Main(provider string) error {
	// Reject invalid input: this package must be invoked by the Hostlier workflow runner.
	if len(os.Args) != 2 || os.Args[1] != "serve" && os.Args[1] != "describe" {
		return errors.New("expected describe or serve")
	}
	executable, err := os.Executable()
	// Reject invalid input: cannot locate package installation.
	if err != nil {
		return errors.New("cannot locate package installation")
	}
	_, err = os.Lstat(filepath.Join(filepath.Dir(executable), provider+".json"))
	// Any present executor file must pass the existing protected configuration checks.
	if err == nil {
		// The experimental executor does not satisfy the direct package's capability contract.
		if os.Args[1] == "describe" {
			return errors.New("legacy executor has no public package capability")
		}
		return usefused.Main(provider)
	}
	// Reject invalid input: cannot inspect provider installation.
	if !os.IsNotExist(err) {
		return errors.New("cannot inspect provider installation")
	}
	p := Package{Provider: provider}
	// Discovery has no private request and makes no provider call.
	if os.Args[1] == "describe" {
		return packagekit.Describe(p, os.Stdout)
	}
	return p.Serve(context.Background(), os.Stdin, os.Stdout)
}

// validProtocol fixes the installed package identity and limits supported actions.
func validProtocol(req workflow.Request, provider string) bool {
	return req.Version == 1 && req.Key != "" && req.Step.Package == provider && (req.Action == "plan" || req.Action == "apply")
}
