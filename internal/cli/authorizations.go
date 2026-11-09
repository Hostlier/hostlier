package cli

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"hostlier/internal/model"
)

// authorizeUsage documents both exact-name and sandbox windows.
const authorizeUsage = "Usage: authorize --device ID --on local|HOST (--app APP[,APP] | --app-prefix PREFIX- --max-apps N [--route-domain DOMAIN] [--keep-apps]) --scope deploy --for 2h [--max-uses 20 --secrets NAME[,NAME] --key FILE]"

// authorize requests an explicitly bounded window and optionally signs it in the owner's local terminal.
func (s *session) authorize() error {
	duration, err := time.ParseDuration(s.flags["for"])
	// Reject the request before side effects: exactly one of --app or --app-prefix is required.
	if err != nil || s.flags["device"] == "" || s.flags["on"] == "" || (s.flags["app"] == "") == (s.flags["app-prefix"] == "") || s.flags["scope"] != "deploy" {
		return model.Err("ARGUMENTS", authorizeUsage)
	}
	uses, err := optionalCount(s.flags["max-uses"], 20, "Invalid maximum use count")
	// Reject the request before side effects: Invalid maximum use count.
	if err != nil {
		return err
	}
	body := map[string]any{"device": s.flags["device"], "host": s.flags["on"], "scope": "deploy", "durationSeconds": int64(duration / time.Second), "maxUses": uses}
	// Exact-name windows and sandbox windows carry different app selectors.
	if err = s.addAuthorizeApps(body); err != nil {
		return err
	}
	// Apply this check before granting authority or advancing protocol state.
	if s.flags["secrets"] != "" {
		body["secrets"] = strings.Split(s.flags["secrets"], ",")
	}
	var result any
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err = s.request("POST", "/v1/authorizations", body, &result); err != nil {
		return err
	}
	return s.print(result)
}

// addAuthorizeApps adds exact app names or the sandbox prefix, capacity and route bounds.
func (s *session) addAuthorizeApps(body map[string]any) error {
	// Exact names keep the original window contract.
	if s.flags["app"] != "" {
		// Sandbox-only options cannot silently widen an exact window.
		if s.flags["max-apps"] != "" || s.flags["route-domain"] != "" || s.flags["keep-apps"] != "" {
			return model.Err("ARGUMENTS", "--max-apps, --route-domain and --keep-apps require --app-prefix")
		}
		body["apps"] = strings.Split(s.flags["app"], ",")
		return nil
	}
	apps, err := optionalCount(s.flags["max-apps"], 5, "Invalid maximum app count")
	// Reject the request before side effects: Invalid maximum app count.
	if err != nil {
		return err
	}
	body["appPrefix"], body["maxApps"] = s.flags["app-prefix"], apps
	// The route boundary is optional.
	if s.flags["route-domain"] != "" {
		body["routeDomain"] = s.flags["route-domain"]
	}
	// Teardown at expiry is the default; keeping apps is explicit.
	if s.flags["keep-apps"] != "" {
		body["keepApps"] = true
	}
	return nil
}

// optionalCount parses a positive integer flag or returns its default.
func optionalCount(value string, fallback int, message string) (int, error) {
	// An omitted flag selects the documented default.
	if value == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(value)
	// Non-numeric values are rejected before any request.
	if err != nil {
		return 0, model.Err("ARGUMENTS", message)
	}
	return n, nil
}

// authorizations inspects or explicitly revokes retained delegation windows.
func (s *session) authorizations() error {
	// Apply this check before granting authority or advancing protocol state.
	if s.sub == "" || s.sub == "list" {
		return s.getPrint("/v1/authorizations")
	}
	// Apply this check before granting authority or advancing protocol state.
	if s.sub == "revoke" && s.target != "" {
		var result any
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err := s.request("POST", "/v1/authorizations/"+url.PathEscape(s.target)+"/revoke", map[string]any{}, &result); err != nil {
			return err
		}
		return s.print(result)
	}
	return model.Err("ARGUMENTS", "Usage: authorizations list | authorizations revoke ID")
}
