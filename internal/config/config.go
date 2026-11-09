// Package config validates declarative Hostlier configuration without side effects.
package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"hostlier/internal/model"
)

var nameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var secretRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`)
var envRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var labelRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var imageRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)
var sizeRE = regexp.MustCompile(`^[1-9][0-9]*(?:Ki|Mi|Gi|Ti)$`)
var pathRE = regexp.MustCompile(`^/[A-Za-z0-9/._~-]*$`)

// ValidSize accepts quantities whose byte count fits both runtime backends.
func ValidSize(value string) bool {
	// Reject malformed quantities before slicing the unit suffix.
	if !sizeRE.MatchString(value) {
		return false
	}
	n, err := strconv.ParseUint(value[:len(value)-2], 10, 64)
	shift := map[string]uint{"Ki": 10, "Mi": 20, "Gi": 30, "Ti": 40}[value[len(value)-2:]]
	return err == nil && n <= (^uint64(0)>>1)>>shift
}

// invalid gives every schema rejection the stable INVALID_CONFIG API code.
func invalid(message string) error { return model.Err("INVALID_CONFIG", message) }

// NormalizeDomain canonicalizes a concrete DNS hostname for comparisons and routing.
func NormalizeDomain(value string) (string, error) {
	host := strings.ToLower(value)
	// Exclude addresses and URL syntax that could escape hostname-only routing.
	if len(host) == 0 || len(host) > 253 || !strings.Contains(host, ".") || net.ParseIP(host) != nil || strings.ContainsAny(host, " /:@?#%*\\\t\r\n") {
		return "", invalid("Domain must be a DNS hostname without a scheme, wildcard, port, or path")
	}
	// Validate each DNS label rather than accepting a syntactically plausible whole string.
	for _, part := range strings.Split(host, ".") {
		// Reject empty, oversized or improperly hyphenated labels.
		if !labelRE.MatchString(part) {
			return "", invalid("Invalid DNS hostname")
		}
	}
	return host, nil
}

// ValidateServer normalizes the bounded supported-domain allowlist.
func ValidateServer(value model.ServerConfig) (model.ServerConfig, error) {
	// Keep the server schema version explicit for future migrations.
	if value.Version != 1 {
		return value, invalid("Unsupported server configuration version")
	}
	// Bound both empty allowlists and excessive configuration sizes.
	if len(value.Domains) == 0 || len(value.Domains) > 256 {
		return value, invalid("domains must contain 1 to 256 hostnames")
	}
	seen := map[string]bool{}
	domains := make([]string, 0, len(value.Domains))
	// Normalize domains before deduplication so case aliases cannot bypass uniqueness.
	for _, domain := range value.Domains {
		host, err := NormalizeDomain(domain)
		// Stop before persisting an invalid supported hostname.
		if err != nil {
			return value, err
		}
		// Reject repeated canonical hostnames.
		if seen[host] {
			return value, invalid("Duplicate supported domain")
		}
		seen[host] = true
		domains = append(domains, host)
	}
	value.Domains = domains
	return value, nil
}

// ParseRoute produces a canonical HTTPS route and enforces ownership restrictions.
func ParseRoute(value string, domains []string, reservedHost string) (model.Route, error) {
	var result model.Route
	address, err := routeAddress(value)
	// Reject ambiguous input before splitting host and path.
	if err != nil {
		return result, err
	}
	host, routePath, found := strings.Cut(address, "/")
	// An absent path denotes the root route.
	if !found {
		routePath = "/"
	} else {
		routePath = "/" + routePath
	}
	host, err = NormalizeDomain(host)
	// Require the route authority to be a concrete DNS hostname.
	if err != nil {
		return result, err
	}
	// Reject traversal and repeated separators before path normalization.
	if err := validateRoutePath(routePath); err != nil {
		return result, err
	}
	routePath = strings.TrimRight(routePath, "/")
	// Preserve a single root slash after trimming trailing separators.
	if routePath == "" {
		routePath = "/"
	}
	// Apply the configured domain boundary before reserving a route.
	if err := validateRouteDomain(host, domains); err != nil {
		return result, err
	}
	// Protect the controller hostname from application claims.
	if reservedHost != "" && strings.EqualFold(host, reservedHost) {
		return result, model.Err("RESERVED_ROUTE", "Controller hostname is reserved")
	}
	url := "https://" + host
	// Avoid appending an extra slash for the canonical root URL.
	if routePath != "/" {
		url += routePath
	}
	return model.Route{Hostname: host, Path: routePath, URL: url, Key: host + routePath}, nil
}

// ValidateProject validates and normalizes a project in declaration order.
func ValidateProject(p model.Project) (model.Project, error) {
	var err error
	p, err = normalizeWorkload(p)
	// Reject invalid v2 topology before running shared value validators.
	if err != nil {
		return p, err
	}
	// Run the focused validators in their original order to preserve error precedence.
	for _, validate := range []func(model.Project) (model.Project, error){normalizeFusedConnection, validateManagedConfigs, validateContainerSecurity, validateProjectIdentity, normalizeProjectRoute, validateProjectHealth, validateProjectResources, validateIdlePolicy, validateProjectAutoscaling, validateProjectLimits, validateProjectEnvironment, validateProjectSecrets, validateProjectVolumes, validateProjectDependencies, validateProjectBindings} {
		p, err = validate(p)
		// Return the partially normalized project with its first validation failure.
		if err != nil {
			return p, err
		}
	}
	return validateProjectWorkflow(p)
}

// Fingerprint hashes canonical JSON for immutable approval comparisons.
func Fingerprint(value any) string {
	data, err := json.Marshal(value)
	// An unencodable value cannot produce a usable approval fingerprint.
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// readYAML reads one bounded, alias-free document with strict schema fields.
func readYAML(path string, target any) error {
	file, err := os.Open(path)
	// Preserve filesystem errors so callers can distinguish missing configuration.
	if err != nil {
		return err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	// Stop if the bounded configuration read is interrupted.
	if err != nil {
		return err
	}
	return DecodeYAML(data, target)
}

// DecodeYAML checks one bounded, alias-free configuration snapshot without reopening its file.
func DecodeYAML(data []byte, target any) error {
	// Reject documents that exceed the allocation limit rather than parsing a truncated prefix.
	if len(data) > 1024*1024 {
		return invalid("Configuration exceeds 1 MiB")
	}
	var tree yaml.Node
	// Translate malformed YAML into a stable configuration error.
	if err := yaml.Unmarshal(data, &tree); err != nil {
		return invalid("Invalid YAML configuration")
	}
	// Reject unsafe tree structure before typed decoding.
	if err := validateYAMLTree(&tree, 0); err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	// Keep scalar values out of schema-error messages.
	if err := decoder.Decode(target); err != nil {
		return invalid(fmt.Sprintf("Invalid configuration: %s", safeYAMLError(err)))
	}
	var extra any
	// Require end of stream after the single expected document.
	if err := decoder.Decode(&extra); err != io.EOF {
		return invalid("Exactly one YAML document is required")
	}
	return nil
}
func safeYAMLError(err error) string { // Parser messages can embed scalar values; do not echo them.
	// Distinguish unknown fields without returning parser text containing scalar values.
	if strings.Contains(err.Error(), "not found in type") {
		return "unknown field"
	}
	return "schema or value error"
}

// ReadProjectInput reads source declarations before local artifact preparation.
func ReadProjectInput(path string) (model.Project, error) {
	input, err := ReadProjectSources(path, "")
	return input.Project, err
}

// ReadProject loads and validates a declarative application document.
func ReadProject(path string) (model.Project, error) {
	p, err := ReadProjectInput(path)
	// Do not validate a partially decoded project.
	if err != nil {
		return p, err
	}
	return ValidateProject(p)
}

// ReadServer loads and validates the supported-domain document.
func ReadServer(path string) (model.ServerConfig, error) {
	var s model.ServerConfig
	// Do not normalize a partially decoded server configuration.
	if err := readYAML(path, &s); err != nil {
		return s, err
	}
	return ValidateServer(s)
}

// validateRoutePath rejects ambiguous separators and parent traversal segments.
func validateRoutePath(routePath string) error {
	// Disallow repeated separators and unsupported path characters.
	if !pathRE.MatchString(routePath) || strings.Contains(routePath, "//") {
		return invalid("Invalid route path")
	}
	// Inspect path components before trimming harmless trailing slashes.
	for _, segment := range strings.Split(routePath, "/") {
		// Reject traversal and current-directory aliases.
		if segment == "." || segment == ".." {
			return invalid("Ambiguous route path")
		}
	}
	return nil
}

// validateRouteDomain enforces the configured domain allowlist when present.
func validateRouteDomain(host string, domains []string) error {
	// A nil list permits validation without an installation-specific domain boundary.
	if domains != nil {
		matched := false
		// Accept exact supported domains and their subdomains.
		for _, domain := range domains {
			d, e := NormalizeDomain(domain)
			// Only normalized allowlist entries may authorize a matching hostname.
			if e == nil && (host == d || strings.HasSuffix(host, "."+d)) {
				matched = true
				break
			}
		}
		// Reject suffix lookalikes and hosts outside the configured domain boundary.
		if !matched {
			return model.Err("UNSUPPORTED_DOMAIN", "Route hostname is outside supported domains")
		}
	}
	return nil
}

// validateYAMLTree rejects aliases and bounds recursive YAML nesting.
func validateYAMLTree(n *yaml.Node, depth int) error {
	// Bound recursion and forbid aliases before following child nodes.
	if depth > 32 || n.Kind == yaml.AliasNode {
		return invalid("YAML aliases and excessive nesting are unsupported")
	}
	// Validate every nested YAML node against the same structural limits.
	for _, child := range n.Content {
		// Stop at the first unsafe descendant.
		if err := validateYAMLTree(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// routeAddress bounds route input and accepts only the HTTPS scheme.
func routeAddress(value string) (string, error) {
	// Bound input size and exclude URL delimiters that change route interpretation.
	if len(value) == 0 || len(value) > 2048 || strings.ContainsAny(value, " \\?#@%\t\r\n") {
		return "", invalid("Route must contain a hostname and an unambiguous optional path")
	}
	address := strings.TrimPrefix(value, "https://")
	// Reject schemes other than the optional HTTPS prefix.
	if strings.Contains(address, "://") {
		return "", invalid("Only HTTPS routes are supported")
	}
	return address, nil
}
