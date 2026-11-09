package cli

import (
	"encoding/json"
	"fmt"
	"hostlier/internal/model"
	"net/url"
	"strconv"
	"time"
)

// backup exposes recurring policy, portable import and durable recovery-job observation.
func (s *session) backup() error {
	// Select the explicit recovery operation or native engine behavior.
	switch s.sub {
	// Handle only this supported recovery operation or retained state.
	case "destinations":
		return s.getPrint("/v1/backup-destinations")
	// Handle only this supported recovery operation or retained state.
	case "list", "":
		return s.getPrint("/v1/backup-policies")
	// Handle only this supported recovery operation or retained state.
	case "objects":
		return s.getPrint("/v1/backup-destinations/" + url.PathEscape(s.target) + "/objects")
	// Handle only this supported recovery operation or retained state.
	case "job":
		return s.recoveryJob(s.target)
	// Handle only this supported recovery operation or retained state.
	case "run", "disable":
		return s.recoveryMutation("/v1/backup-policies/"+url.PathEscape(s.target)+"/"+s.sub, map[string]any{})
	// Handle only this supported recovery operation or retained state.
	case "import":
		body, err := s.destinationIntent()
		// Return command or controller errors before presenting recovery as successful.
		if err != nil {
			return err
		}
		body["objectKey"] = s.target
		return s.recoveryMutation("/v1/backup-imports", body)
	// Handle only this supported recovery operation or retained state.
	case "policy":
		return s.backupPolicy()
	}
	return model.Err("ARGUMENTS", "Use backup destinations|objects|list|policy|run|disable|import|job")
}

// destinationIntent retrieves the nonsecret storage fingerprint before proposing exact human approval.
func (s *session) destinationIntent() (map[string]any, error) {
	var result struct {
		Destinations []struct{ ID, Fingerprint string }
	}
	// Return command or controller errors before presenting recovery as successful.
	if err := s.request("GET", "/v1/backup-destinations", nil, &result); err != nil {
		return nil, err
	}
	// Inspect each retained item before advancing recovery state.
	for _, destination := range result.Destinations {
		// Advance only after the required recovery invariant holds.
		if destination.ID == s.flags["destination"] {
			return map[string]any{"destination": destination.ID, "destinationFingerprint": destination.Fingerprint}, nil
		}
	}
	return nil, model.Err("ARGUMENTS", "Choose an operator-configured --destination; inspect backup destinations")
}

// backupPolicy prepares recurring snapshot and restore-test authority with explicit retention.
func (s *session) backupPolicy() error {
	every, err := time.ParseDuration(s.flags["every"])
	// Return command or controller errors before presenting recovery as successful.
	if err != nil {
		return model.Err("ARGUMENTS", "Use --every 24h with an interval of at least one hour")
	}
	retain, err := strconv.Atoi(s.flags["retain"])
	// Return command or controller errors before presenting recovery as successful.
	if err != nil {
		return model.Err("ARGUMENTS", "Use --retain COUNT")
	}
	body, err := s.destinationIntent()
	// Return command or controller errors before presenting recovery as successful.
	if err != nil {
		return err
	}
	body["name"] = s.target
	body["resource"] = s.flags["resource"]
	body["everySeconds"] = int64(every / time.Second)
	body["retain"] = retain
	return s.recoveryMutation("/v1/backup-policies", body)
}

// resourceUpgrade creates a new named database and never performs an implicit image replacement.
func (s *session) resourceUpgrade() error {
	// Advance only after the required recovery invariant holds.
	if s.sub == "upgrade-status" {
		return s.recoveryJob(s.target)
	}
	// Advance only after the required recovery invariant holds.
	if s.sub == "cutover" || s.sub == "abort-upgrade" {
		action := "cutover"
		// Advance only after the required recovery invariant holds.
		if s.sub == "abort-upgrade" {
			action = "abort"
		}
		return s.recoveryMutation("/v1/recovery-jobs/"+url.PathEscape(s.target)+"/"+action, map[string]any{})
	}
	body, err := s.destinationIntent()
	// Return command or controller errors before presenting recovery as successful.
	if err != nil {
		return err
	}
	body["resource"] = s.target
	body["target"] = s.flags["target"]
	body["to"] = s.flags["to"]
	return s.recoveryMutation("/v1/upgrades", body)
}

// recoveryMutation uses the existing hardware-approval protocol and prints durable job identity immediately.
func (s *session) recoveryMutation(path string, body any) error {
	var result map[string]any
	// Return command or controller errors before presenting recovery as successful.
	if err := s.request("POST", path, body, &result); err != nil {
		return err
	}
	// Advance only after the required recovery invariant holds.
	if id, ok := result["id"].(string); ok && result["kind"] != nil && s.flags["watch"] != "" {
		return s.recoveryJob(id)
	}
	return s.print(result)
}

// recoveryJob watches retained state without treating Ctrl-C as cancellation of database work.
func (s *session) recoveryJob(id string) error {
	// Reject this request: Recovery job ID required.
	if id == "" {
		return model.Err("ARGUMENTS", "Recovery job ID required")
	}
	path := "/v1/recovery-jobs/" + url.PathEscape(id)
	if s.flags["watch"] == "" {
		return s.getPrint(path)
	}
	return s.watchReceipt(s.flags["on"], path, func(data json.RawMessage) error {
		var result map[string]any
		if err := json.Unmarshal(data, &result); err != nil {
			return err
		}
		if result["status"] == "running" || result["status"] == "queued" {
			fmt.Fprintln(s.errOut, "Recovery phase:", result["phase"])
			return nil
		}
		return s.print(result)
	})
}
