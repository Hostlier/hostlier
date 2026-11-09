package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"

	"hostlier/internal/config"
	"hostlier/internal/model"
)

// pendingMinionProject is a non-deployable compiler artifact awaiting an Engine-issued app identity.
type pendingMinionProject struct {
	model.Project
	Consent struct {
		EngineURL string `json:"engineUrl"`
		Manifest  string `json:"manifest"`
		Digest    string `json:"digest"`
	} `json:"consent"`
}

// readPendingMinion verifies the exact compiler manifest before sending a bounded request to the controller.
func readPendingMinion(path string) (pendingMinionProject, string, error) {
	var pending pendingMinionProject
	raw, err := readBoundedFusedFile(path)
	// A missing or malformed artifact cannot initiate an upstream consent ceremony.
	if err != nil {
		return pending, "", err
	}
	// Decode only after the local input bound has been enforced.
	if err := json.Unmarshal(raw, &pending); err != nil {
		return pending, "", err
	}
	// The compiler uses one fixed sibling path; arbitrary manifest paths are not trusted file references.
	if !validPendingMinion(pending) {
		return pending, "", errors.New("use the compiler's hostlier-pending.json artifact")
	}
	raw, err = readBoundedFusedFile(filepath.Join(filepath.Dir(path), pending.Consent.Manifest))
	// Fail before registration when the compiler output is missing or has changed.
	if err != nil {
		return pending, "", err
	}
	sum := sha256.Sum256(raw)
	// Digest mismatch means the permission request changed after compilation.
	if !json.Valid(raw) || len(raw) > 65536 || "sha256:"+hex.EncodeToString(sum[:]) != pending.Consent.Digest {
		return pending, "", errors.New("compiled Fused manifest does not match the pending artifact")
	}
	return pending, string(raw), nil
}

// readBoundedFusedFile limits local artifact input independently of controller request limits.
func readBoundedFusedFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	// Failed local reads must not produce an empty consent request.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	// Oversized artifacts are rejected rather than truncated into another document.
	if len(raw) > 1<<20 {
		return nil, errors.New("Fused artifact is too large")
	}
	return raw, err
}

// fusedProvision obtains a per-app grant and exports only public identity plus the stored secret reference.
func (s *session) fusedProvision(authorize bool) error {
	path := s.flags["file"]
	// Explicit artifact selection prevents accidentally authorizing another local project.
	if path == "" {
		return model.Err("ARGUMENTS", "Use --file BUILD/artifact/hostlier-pending.json")
	}
	pending, manifest, err := readPendingMinion(path)
	// Unreadable or changed compiler artifacts cannot start consent.
	if err != nil {
		return err
	}
	return s.provisionPendingMinion(path, pending, manifest, authorize)
}

// provisionPendingMinion shares grant reuse and OAuth handling with the automatic deployment path.
func (s *session) provisionPendingMinion(path string, pending pendingMinionProject, manifest string, authorize bool) error {
	// The bind command recovers local export without creating or replacing upstream credentials.
	if authorize {
		input := map[string]any{"engineUrl": pending.Consent.EngineURL, "manifest": manifest, "grpcUrl": pending.Fused.GRPCURL, "secretName": pending.Fused.TokenSecret, "management": true}
		var status struct {
			Connections map[string]runtimeConnectionStatus `json:"connections"`
		}
		// Reuse an existing app-bound deployment grant instead of creating a new OAuth client per version.
		if err := s.remote.Request(s.ctx, "GET", "/v1/fused", nil, &status); err != nil {
			return err
		}
		// Connected grants can publish within their existing ceiling.
		if connection, exists := status.Connections[s.target]; exists && connection.Status == "connected" {
			// An already-bound artifact needs no upstream mutation or token rotation.
			if !connection.matches(pending) {
				delete(input, "management")
				var result any
				// A failed version publication cannot export an obsolete app identity.
				if err := s.request("POST", "/v1/fused/"+url.PathEscape(s.target)+"/deploy", input, &result); err != nil {
					return err
				}
			}
			return s.bindMinionProject(path, pending)
		}
		// Preserve the existing authenticated relay and owner approval boundary.
		if err := s.fusedConnectDynamicWith(input); err != nil {
			return err
		}
	}
	return s.bindMinionProject(path, pending)
}

// validPendingMinion refuses executable identities and arbitrary manifest paths in pending artifacts.
func validPendingMinion(pending pendingMinionProject) bool {
	return pending.Fused != nil && pending.Fused.AppID == "" && pending.Consent.Manifest == "fused-access.json" && pending.Consent.EngineURL != ""
}

// runtimeConnectionStatus contains only public metadata returned by the controller.
type runtimeConnectionStatus struct {
	DefaultBucketID string                 `json:"defaultBucketId"`
	Buckets         []model.FusedBucket    `json:"buckets"`
	Status          string                 `json:"status"`
	EngineURL       string                 `json:"engineUrl"`
	Runtime         *model.FusedConnection `json:"runtime"`
	ManifestDigest  string                 `json:"manifestDigest"`
}

// matches requires the exact compiler declaration, Engine, gRPC endpoint and secret reference.
func (connection runtimeConnectionStatus) matches(pending pendingMinionProject) bool {
	return connection.Status == "connected" && connection.Runtime != nil && connection.ManifestDigest == pending.Consent.Digest && connection.EngineURL == pending.Consent.EngineURL && connection.Runtime.GRPCURL == pending.Fused.GRPCURL && connection.Runtime.TokenSecret == pending.Fused.TokenSecret
}

// bindMinionProject recovers the authoritative identity without returning the runtime token to the CLI.
func (s *session) bindMinionProject(path string, pending pendingMinionProject) error {
	var status struct {
		Connections map[string]runtimeConnectionStatus `json:"connections"`
	}
	// Only the authenticated controller can supply this project's runtime identity.
	if err := s.remote.Request(s.ctx, "GET", "/v1/fused", nil, &status); err != nil {
		return err
	}
	connection := status.Connections[s.target]
	// Reject another build or Engine even if the connection name happens to match.
	if !connection.matches(pending) {
		return errors.New("controller runtime consent does not match this compiled artifact")
	}
	pending.Project.Fused = connection.Runtime
	project := pending.Project
	// Validate local packaging declarations before export; deployment prepares the actual artifact later.
	if err := config.ValidateInput(project); err != nil {
		return err
	}
	output := filepath.Join(filepath.Dir(path), ".hostlier")
	// Export failure is recoverable with bind, without replaying OAuth.
	if err := writeBoundMinion(output, project); err != nil {
		return err
	}
	return s.print(map[string]any{"file": output, "appId": connection.Runtime.AppID, "manifestDigest": connection.ManifestDigest, "defaultBucketId": connection.DefaultBucketID, "buckets": connection.Buckets})
}

// writeBoundMinion creates a new deployment file without replacing user-authored revisions.
func writeBoundMinion(output string, project model.Project) error {
	// The Fused declaration recreates its token binding on load; exporting the lowered
	// binding would make source readers interpret its stored name as a literal secret.
	project.Secrets = maps.Clone(project.Secrets)
	delete(project.Secrets, "FUSED_APP_TOKEN")
	// Compiler secret names stay references when the exported config is read as user input.
	for key, name := range project.Secrets {
		project.Secrets[key] = "secret://" + name
	}
	raw, err := json.MarshalIndent(project, "", "  ")
	// Serialization must finish before creating the destination.
	if err != nil {
		return err
	}
	file, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	// Existing deployments must remain intact, including after a repeated bind command.
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(raw, '\n'))
	closeErr := file.Close()
	// Remove only this attempt's incomplete output so a bind retry can recover.
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(output)
		return err
	}
	return nil
}
