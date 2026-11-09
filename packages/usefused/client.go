// Package usefused connects optional Hostlier packages to a version-pinned Fused consumer of a Unified App.
package usefused

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"hostlier/pkg/workflow"
)

var uuid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
var operation = regexp.MustCompile(`^unified_app:[a-z][a-z0-9_]{0,63}$`)

// Config is installed beside the package by the server administrator, never chosen by a deployment file.
type Config struct {
	EngineURL         string `json:"engineUrl"`
	ConsumerVersionID string `json:"consumerVersionId"`
	Operation         string `json:"operation"`
	UserRef           string `json:"userRef,omitempty"`
}

// Validate requires verified HTTPS and the exact immutable consumer-version invocation contract.
func (c Config) Validate() error {
	_, err := validateEngineOrigin(c.EngineURL)
	// Reject unsafe destinations before checking the invocation contract.
	if err != nil {
		return err
	}
	// Family URLs and arbitrary provider operations are intentionally unsupported.
	if !uuid.MatchString(c.ConsumerVersionID) || !operation.MatchString(c.Operation) || len(c.UserRef) > 256 {
		return errors.New("Usefused requires a consumer version ID and unified_app operation alias")
	}
	return nil
}

// validateEngineOrigin confines credential transmission to an HTTPS origin.
func validateEngineOrigin(origin string) (*url.URL, error) {
	u, err := url.Parse(origin)
	// Origins cannot embed credentials or alternative execution paths.
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return nil, errors.New("Usefused requires an HTTPS Engine origin")
	}
	// Keep the execution path fully determined by the approved consumer version.
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("Usefused Engine URL must be an origin")
	}
	return u, nil
}

// Client has no automatic retries; uncertain mutations stay uncertain in the controller journal.
type Client struct {
	Config Config
	HTTP   *http.Client
}

// Input is the shared contract implemented by the deployment Unified App.
type Input struct {
	Version  int               `json:"version"`
	Action   string            `json:"action"`
	Provider string            `json:"provider"`
	Key      string            `json:"key"`
	UserRef  string            `json:"userRef,omitempty"`
	Spec     map[string]any    `json:"spec"`
	Inputs   map[string]string `json:"inputs,omitempty"`
	Previous json.RawMessage   `json:"previous,omitempty"`
	Plan     json.RawMessage   `json:"plan,omitempty"`
}

// Output binds responses to the requested action, provider and ownership identity.
type Output struct {
	Version  int              `json:"version"`
	Action   string           `json:"action"`
	Provider string           `json:"provider"`
	Key      string           `json:"key"`
	Summary  []string         `json:"summary,omitempty"`
	Plan     json.RawMessage  `json:"plan,omitempty"`
	Ready    bool             `json:"ready"`
	Result   *workflow.Result `json:"result,omitempty"`
}

// Call invokes an explicitly selected Unified App attachment through its pinned consumer version.
func (c Client) Call(ctx context.Context, token string, input Input) (Output, error) {
	// Validate administrator configuration before sending any credential.
	if err := c.Config.Validate(); err != nil {
		return Output{}, err
	}
	// An execution token is supplied privately by the controller, never a provider token or CLI control key.
	if token == "" || len(token) > 65536 {
		return Output{}, errors.New("missing Usefused execution token")
	}
	input.UserRef = c.Config.UserRef
	body, err := json.Marshal(map[string]any{"operation": c.Config.Operation, "input": input})
	// Bound outbound inputs and fail before network access for invalid payloads.
	if err != nil || len(body) > 4*1024*1024 {
		return Output{}, errors.New("invalid Usefused input")
	}
	endpoint := strings.TrimSuffix(c.Config.EngineURL, "/") + "/v1/apps/" + c.Config.ConsumerVersionID + "/executions"
	request, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(body))
	// URL construction must succeed without leaking the token into diagnostics.
	if err != nil {
		return Output{}, errors.New("invalid Usefused endpoint")
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 10 * time.Minute}
	// Tests inject an in-memory transport; production uses standard verified TLS.
	if c.HTTP != nil {
		client = *c.HTTP
	}
	// Redirects must never move execution credentials or mutations to another endpoint.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(request)
	// Transport errors may include private URLs or provider diagnostics; report a fixed error.
	if err != nil {
		return Output{}, errors.New("Usefused execution unavailable; do not blindly retry")
	}
	defer response.Body.Close()
	return decodeResponse(response, input)
}

// decodeResponse verifies both the Engine execution envelope and the authored Unified App contract.
func decodeResponse(response *http.Response, input Input) (Output, error) {
	var envelope struct {
		Status string `json:"status"`
		Output Output `json:"output"`
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4*1024*1024+1))
	// HTTP success alone does not mean the hosted operation succeeded.
	if err != nil || response.StatusCode != http.StatusOK || len(raw) > 4*1024*1024 {
		return Output{}, errors.New("Usefused execution failed")
	}
	// Private exceptions and diagnostic fields never leave this adapter.
	if json.Unmarshal(raw, &envelope) != nil || envelope.Status != "succeeded" {
		return Output{}, errors.New("Usefused execution did not succeed")
	}
	output := envelope.Output
	// A response for another target or phase cannot authorize continuation.
	if output.Version != 1 || output.Action != input.Action || output.Provider != input.Provider || output.Key != input.Key {
		return Output{}, errors.New("Usefused returned a mismatched execution")
	}
	return output, nil
}
