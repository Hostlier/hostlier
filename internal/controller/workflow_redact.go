package controller

import (
	"net/url"
	"strings"
)

// redactWorkflow protects retained package outputs even when an app logs only a URL password.
func (c *Controller) redactWorkflow(value string) string {
	// Workflow outputs are stored outside the ordinary named secret inventory.
	for _, record := range c.state.WorkflowRecords {
		result, err := c.workflowResult(record)
		// Unavailable ciphertext contains no recoverable value to redact.
		if err != nil {
			continue
		}
		// Inspect all explicitly returned private outputs.
		for _, private := range result.StepOutputs() {
			value = redactConnection(value, private)
		}
	}
	return value
}

// redactConnection removes full values and common encoded database password representations.
func redactConnection(text, private string) string {
	// Empty values must never trigger a replace-at-every-position operation.
	if private == "" {
		return text
	}
	text = strings.ReplaceAll(text, private, "[REDACTED]")
	parsed, err := url.Parse(private)
	// Some outputs are plain passwords rather than connection URLs.
	if err != nil || parsed.User == nil {
		return text
	}
	password, _ := parsed.User.Password()
	// A URL without credentials has no additional private component to redact.
	if password == "" {
		return text
	}
	text = strings.ReplaceAll(text, password, "[REDACTED]")
	return strings.ReplaceAll(text, url.QueryEscape(password), "[REDACTED]")
}
