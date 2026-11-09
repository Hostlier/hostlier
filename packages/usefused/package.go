package usefused

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"hostlier/packages/aiven"
	"hostlier/packages/database"
	"hostlier/packages/railway"
	"hostlier/pkg/workflow"
)

// Package implements the same protocol for Aiven and Railway using distinct installed identities.
type Package struct {
	Provider string
	Client   Client
}

// payload pins the remote plan and local invocation configuration across approval.
type payload struct {
	Fingerprint string          `json:"fingerprint"`
	Remote      json.RawMessage `json:"remote"`
}

// Handle validates package intent before contacting the hosted deployment workflow.
func (p Package) Handle(ctx context.Context, req workflow.Request) (workflow.Response, error) {
	// Requests cannot change the installed provider identity or protocol version.
	if req.Version != 1 || req.Step.Package != p.Provider {
		return workflow.Response{}, errors.New("invalid package request")
	}
	// Validate engine and provisioning mode locally before contacting Fused.
	if err := p.validateDeployment(req); err != nil {
		return workflow.Response{}, err
	}
	input := Input{Version: 1, Action: req.Action, Provider: p.Provider, Key: req.Key, Spec: req.Step.Config, Previous: req.Previous, Inputs: privateInputs(req)}
	// Only apply requests carry a reviewed remote mutation plan.
	if req.Action == "apply" {
		var approved payload
		// A changed installed endpoint or specification requires a new approval.
		if json.Unmarshal(req.Prepared, &approved) != nil || approved.Fingerprint != p.fingerprint(req) {
			return workflow.Response{}, errors.New("Usefused package configuration changed after approval")
		}
		input.Plan = approved.Remote
	} else if req.Action != "plan" { // Reject any implicit destructive or lifecycle action.
		return workflow.Response{}, errors.New("unsupported package action")
	}
	output, err := p.Client.Call(ctx, req.Secrets["executionToken"], input)
	// No package response is emitted when remote execution is uncertain or failed.
	if err != nil {
		return workflow.Response{}, err
	}
	return p.response(req, output)
}

// response enforces readiness and keeps remote plans opaque until approved execution.
func (p Package) response(req workflow.Request, output Output) (workflow.Response, error) {
	response := workflow.Response{Version: 1}
	// Plan executions must not claim or return newly provisioned resources.
	if req.Action == "plan" {
		// Planning requires a concrete review and remote plan, not provider side effects.
		if output.Result != nil || output.Ready || len(output.Summary) == 0 || !json.Valid(output.Plan) {
			return response, errors.New("invalid Usefused planning result")
		}
		data, _ := json.Marshal(payload{Fingerprint: p.fingerprint(req), Remote: output.Plan})
		response.Prepared = &workflow.Prepared{Summary: output.Summary, Payload: data}
		return response, nil
	}
	// Provisioning success requires explicit readiness plus usable private connection fields.
	if !output.Ready || output.Result == nil || !workflow.ValidResult(*output.Result) {
		return response, errors.New("Usefused resource is not ready")
	}
	response.Result = output.Result
	return response, nil
}

// fingerprint binds the executor installation and full step configuration, excluding live secret values.
func (p Package) fingerprint(req workflow.Request) string {
	raw, _ := json.Marshal([]any{p.Provider, p.Client.Config, req.Key, req.Step})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

// privateInputs forwards only the database connection; provider tokens remain in Fused buckets.
func privateInputs(req workflow.Request) map[string]string {
	values := map[string]string{}
	// Existing database creation may use a connection from an earlier workflow step.
	if connection := req.Inputs["connection"]; connection != "" {
		values["connection"] = connection
	}
	// A direct protected administrative connection is the alternative source.
	if connection := req.Secrets["connection"]; connection != "" {
		values["connection"] = connection
	}
	return values
}

// validateDeployment reuses the package capability schemas while Fused owns provider credentials.
func (p Package) validateDeployment(req workflow.Request) error {
	// Fused owns provider tokens; only its execution token and an optional database connection are accepted.
	if err := validateInputNames(req.Step); err != nil {
		return err
	}
	parts := strings.Split(req.Key, "/")
	// The controller includes a namespace before app and step identity.
	if len(parts) < 2 {
		return errors.New("invalid package resource key")
	}
	config, err := deploymentConfig(req.Step.Config)
	// Reject fields outside provider deployment choices before schema validation.
	if err != nil {
		return err
	}
	config["version"], config["app"], config["name"] = 1, parts[len(parts)-2], req.Step.ID
	config["outputSecret"] = "workflow-managed-output"
	// Existing connections must have an explicit protected source, even during planning.
	if config["mode"] == database.NewDatabase {
		// Later producer values may be unknown during plan, but their references are explicit.
		if req.Step.Secrets["connection"] == "" && req.Step.Inputs["connection"] == "" {
			return errors.New("new-database requires a connection input")
		}
		config["connectionSecret"] = "workflow-managed-connection"
	} else if req.Step.Secrets["connection"] != "" || req.Step.Inputs["connection"] != "" { // New services must not receive unrelated administrative credentials.
		return errors.New("new-instance cannot use an existing connection input")
	}
	return p.validateProvider(config)
}

// deploymentConfig copies only the deployment choices owned by the provider schema.
func deploymentConfig(source map[string]any) (map[string]any, error) {
	config := map[string]any{}
	allowed := map[string]bool{"engine": true, "mode": true, "database": true, "collection": true, "instance": true}
	// Lifecycle identities and secret publication are owned by the workflow, not repeated in package config.
	for key, value := range source {
		// Keep the project configuration limited to provider deployment choices.
		if !allowed[key] {
			return nil, errors.New("unsupported package deployment field")
		}
		config[key] = value
	}
	instance, _ := source["instance"].(map[string]any)
	// Authentication strategy is configured in the Unified App's provider bucket.
	if instance["tokenSecret"] != nil || instance["tokenType"] != nil {
		return nil, errors.New("provider credentials belong in Usefused, not package configuration")
	}
	return config, nil
}

// validateInputNames prevents ignored secrets and accidental provider credential forwarding.
func validateInputNames(step workflow.Step) error {
	// Every invocation must explicitly select its protected execution credential.
	if step.Secrets["executionToken"] == "" {
		return errors.New("package requires executionToken secret reference")
	}
	// Source secrets have a deliberately small public alias contract.
	for alias := range step.Secrets {
		// No provider API keys or unused secret values are accepted.
		if alias != "executionToken" && alias != "connection" {
			return errors.New("unsupported package secret alias")
		}
	}
	// Predecessor outputs can supply only the existing database connection.
	for alias := range step.Inputs {
		// The execution credential can never come from untrusted predecessor output.
		if alias != "connection" {
			return errors.New("unsupported package input alias")
		}
	}
	return nil
}

// validateProvider checks the provider schema without requiring provider credentials in Hostlier.
func (p Package) validateProvider(config map[string]any) error {
	raw, err := json.Marshal(config)
	// YAML decoding accepts JSON and applies the same strict unknown-field validation.
	if err != nil {
		return errors.New("invalid deployment configuration")
	}
	// Provider-specific placement retains the original optional package schema.
	switch p.Provider {
	// Aiven credentials are supplied by the Unified App's configured bucket.
	case "aiven":
		var c aiven.Config
		// Invalid provider fields must fail before network execution.
		if err := database.Decode(bytes.NewReader(raw), &c); err != nil {
			return err
		}
		// A placeholder satisfies reference validation without forwarding an actual provider token.
		if c.Instance != nil {
			c.Instance.TokenSecret = "usefused-managed"
		}
		return c.Validate()
	// Railway authentication and token types belong to its connected Fused service.
	case "railway":
		var c railway.Config
		// Strict provider decoding prevents ignored or contradictory options.
		if err := database.Decode(bytes.NewReader(raw), &c); err != nil {
			return err
		}
		// Bucket credentials choose the actual provider authentication strategy.
		if c.Instance != nil {
			c.Instance.TokenSecret = "usefused-managed"
			c.Instance.TokenType = "project"
		}
		return c.Validate()
	// Installed identities cannot dynamically select arbitrary providers.
	default:
		return errors.New("unsupported provider package")
	}
}

// Serve handles exactly one bounded request over private stdin/stdout pipes.
func (p Package) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	raw, err := io.ReadAll(io.LimitReader(in, 4*1024*1024+1))
	// Refuse oversized requests before decoding configuration or credentials.
	if err != nil || len(raw) > 4*1024*1024 {
		return errors.New("invalid package input")
	}
	var request workflow.Request
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	// Reject multiple documents and unsupported protocol fields.
	if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("invalid package input")
	}
	response, err := p.Handle(ctx, request)
	// Never emit partial private output on failure.
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(response)
}
