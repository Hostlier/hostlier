package cli

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"hostlier/internal/collector"
	"hostlier/internal/model"
)

const telemetryHelp = `Telemetry: telemetry render --runtime docker|k3s --url https://CONTROLLER --output NEW_DIRECTORY [--image IMAGE]
           telemetry token --name COLLECTOR --socket /run/hostlier/controller.sock
           telemetry collectors --socket /run/hostlier/controller.sock
           telemetry revoke ID --socket /run/hostlier/controller.sock
           telemetry metrics|logs [--app APP] [--component NAME] [--revision ID] [--limit N] [--before ID] [--server PROFILE]
Render prepares shared-collector configuration without deploying it. Tokens expire after 30 days;
token creation prints the secret once. Store it in the collector's protected environment/Secret.
History is bounded and may contain sensitive application logs. No per-workload monitoring sidecar.`

// telemetryCommand separates offline rendering from authenticated ingestion administration and history.
func (s *session) telemetryCommand() error {
	// Rendering is offline and must not load a controller credential.
	if s.sub == "render" {
		return s.renderTelemetry()
	}
	// Propagate failure without publishing an incomplete or misleading result.
	if err := s.connectRemote(); err != nil {
		return err
	}
	// Keep each telemetry operation on its own authorization and I/O path.
	switch s.sub {
	// Dispatch only this supported telemetry operation.
	case "token":
		var result any
		// Propagate failure without publishing an incomplete or misleading result.
		if err := s.request("POST", "/v1/telemetry/collectors", map[string]any{"name": s.flags["name"]}, &result); err != nil {
			return err
		}
		return s.print(result)
	// Dispatch only this supported telemetry operation.
	case "collectors":
		return s.getPrint("/v1/telemetry/collectors")
	// Dispatch only this supported telemetry operation.
	case "revoke":
		// Revocation requires one explicit collector identity.
		if s.target == "" {
			return model.Err("ARGUMENTS", telemetryHelp)
		}
		var result any
		// Propagate failure without publishing an incomplete or misleading result.
		if err := s.request("POST", "/v1/telemetry/collectors/"+url.PathEscape(s.target)+"/revoke", map[string]any{}, &result); err != nil {
			return err
		}
		return s.print(result)
	case "metrics", "logs": // History is an authenticated bounded read.
		return s.telemetryHistory()

	}
	return model.Err("ARGUMENTS", telemetryHelp)
}

// renderTelemetry writes a new directory so existing credentials and reviewed manifests are never overwritten.
func (s *session) renderTelemetry() error {
	// Require an explicit destination for the reviewable deployment bundle.
	if s.flags["output"] == "" {
		return model.Err("ARGUMENTS", telemetryHelp)
	}
	bundle, err := collector.Render(collector.Options{Runtime: s.flags["runtime"], Endpoint: s.flags["url"], Image: s.flags["image"]})
	// Propagate failure without publishing an incomplete or misleading result.
	if err != nil {
		return err
	}
	directory := s.flags["output"]
	// Resolve relative destinations against this invocation workspace.
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(s.cwd, directory)
	}
	// Propagate failure without publishing an incomplete or misleading result.
	if err = os.Mkdir(directory, 0700); err != nil {
		return err
	}
	files := map[string]string{"collector.yaml": bundle.Config, "deployment.yaml": bundle.Deployment}
	// Write each reviewed artifact with owner-only file permissions.
	for name, body := range files {
		// Propagate failure without publishing an incomplete or misleading result.
		if err = os.WriteFile(filepath.Join(directory, name), []byte(body), 0600); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(s.out, "Collector configuration written to %s. Review deployment.yaml and provide the referenced collector credential before installing.\n", directory)
	return err
}

// telemetryHistory carries only documented query selectors to the history endpoint.
func (s *session) telemetryHistory() error {
	query := url.Values{}
	query.Set("signal", s.sub)
	// Filter paths never become arbitrary upstream URLs.
	for _, name := range []string{"app", "component", "revision", "limit", "before"} {
		// Omitted selectors retain the server defaults.
		if s.flags[name] != "" {
			query.Set(name, s.flags[name])
		}
	}
	return s.getPrint("/v1/telemetry/history?" + query.Encode())
}
