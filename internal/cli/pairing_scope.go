package cli

import (
	"hostlier/internal/model"
	"strconv"
	"strings"
	"time"
)

// pairingScope sends no restrictions only when every permission flag was omitted.
func (s *session) pairingScope() (map[string]any, error) {
	scope := map[string]any{}
	// An explicit flag, including a zero or empty value, selects bounded validation.
	if !s.hasPairingRestrictions() {
		return scope, nil
	}
	// A supplied scope must not be ignored when deciding whether authority is unrestricted.
	if mode, present := s.flags["scope"]; present && mode != "deploy" {
		return nil, model.Err("ARGUMENTS", "Restricted pairing supports --scope deploy; omit all permission flags for all access")
	}
	duration, err := time.ParseDuration(s.flags["for"])
	// Partial restrictions cannot silently escalate to full administrative authority.
	if err != nil || duration < time.Minute || s.flags["app"] == "" {
		return nil, model.Err("ARGUMENTS", "Restricted access requires --app APP and --for DURATION (1 minute–8 hours); omit all permission flags for all access")
	}
	uses, err := strconv.Atoi(defaultValue(s.flags["max-uses"], "20"))
	// Numeric errors and explicit zero values must remain errors.
	if err != nil || uses < 1 {
		return nil, model.Err("ARGUMENTS", "Invalid maximum use count")
	}
	scope["apps"], scope["durationSeconds"], scope["maxUses"] = strings.Split(s.flags["app"], ","), int64(duration/time.Second), uses
	s.addPairingSecrets(scope)
	return scope, nil
}

// addPairingSecrets includes only explicitly named secret permissions.
func (s *session) addPairingSecrets(scope map[string]any) {
	// Copy the two distinct secret capabilities without adding defaults.
	for flag, field := range map[string]string{"secrets": "secrets", "create-secrets": "createSecrets"} {
		// Missing names confer no secret authority on a restricted grant.
		if s.flags[flag] != "" {
			scope[field] = strings.Split(s.flags[flag], ",")
		}
	}
}

// hasPairingRestrictions distinguishes omitted permission flags from explicit empty values.
func (s *session) hasPairingRestrictions() bool {
	// Each supported permission flag narrows the requested connection.
	for _, name := range []string{"scope", "app", "secrets", "create-secrets", "for", "max-uses"} {
		// Flag presence is intentional even when its value is invalid.
		if _, present := s.flags[name]; present {
			return true
		}
	}
	return false
}
