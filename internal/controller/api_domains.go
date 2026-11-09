package controller

import (
	"net/http"

	"hostlier/internal/config"
	"hostlier/internal/store"
)

// apiDomains routes authenticated requests for authorized domain management.
func (c *Controller) apiDomains(r *http.Request, p []string, actor string) (any, error) {

	// Return the configured domain allowlist.
	if matches(r, p, "GET", 2, "") {
		domains, err := collectionValues[string](r.Context(), c.store, "domains", store.CollectionQuery{})
		return map[string]any{"domains": domains}, err
	}
	// Route additions through normalization and configuration persistence.
	if matches(r, p, "POST", 2, "") {
		return c.addDomain(r, p)
	}
	// Route removals through active-route validation.
	if matches(r, p, "DELETE", 3, "") {
		return c.removeDomain(r, p)
	}
	return endpointNotFound()
}

// addDomain normalizes and durably adds a domain without duplicate entries.
func (c *Controller) addDomain(r *http.Request, p []string) (any, error) {
	var b domainRequest
	// Reject malformed JSON and unrecognized request fields before using caller input.
	if err := decode(r, &b); err != nil {
		return nil, err
	}
	domain, err := config.NormalizeDomain(b.Domain)
	// Reject malformed domain input before comparing or persisting it.
	if err != nil {
		return nil, err
	}
	// Process the full allowlist before committing an authoritative domain update.
	for _, existing := range c.state.Domains {
		// Treat duplicate additions as successful reads without changing state.
		if domain == existing {
			return map[string]any{"domains": c.state.Domains}, nil
		}
	}
	// Require the controller to accept this request before advancing the command workflow.
	if err := c.setDomains(append(append([]string{}, c.state.Domains...), domain)); err != nil {
		return nil, err
	}
	return map[string]any{"domains": c.state.Domains}, nil
}

// removeDomain validates domain removal against active and accepted applications.
func (c *Controller) removeDomain(r *http.Request, p []string) (any, error) {
	// Reject unexpected payload fields on this bodyless action.
	if err := emptyBody(r); err != nil {
		return nil, err
	}
	domains := []string{}
	// Process the full allowlist before committing an authoritative domain update.
	for _, domain := range c.state.Domains {
		// Retain every allowlisted domain except the requested removal.
		if domain != p[2] {
			domains = append(domains, domain)
		}
	}
	// Commit only domain sets that remain valid for active and accepted routes.
	if err := c.setDomains(domains); err != nil {
		return nil, err
	}
	return map[string]any{"domains": domains}, nil
}

// domainRequest bounds the recognized JSON fields for this API action.
type domainRequest struct {
	Domain string `json:"domain"`
}
