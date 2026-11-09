package config

import (
	"hostlier/internal/model"
	"maps"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var fusedAppID = regexp.MustCompile(`^[a-fA-F0-9]{8}-(?:[a-fA-F0-9]{4}-){3}[a-fA-F0-9]{12}$`)

// ValidateFusedConnection shares runtime identity validation with consent provisioning.
func ValidateFusedConnection(f model.FusedConnection) error { return validateFusedConnection(f) }

// normalizeFusedConnection lowers one immutable Engine identity into ordinary runtime bindings.
func normalizeFusedConnection(p model.Project) (model.Project, error) {
	// Ordinary applications retain their current environment and secret ownership.
	if p.Fused == nil {
		return p, nil
	}
	// No runtime binding is emitted until the complete connection is validated.
	if err := validateFusedConnection(*p.Fused); err != nil {
		return p, err
	}
	p.Env, p.Secrets = maps.Clone(p.Env), maps.Clone(p.Secrets)
	// Allocate copied maps so normalization never mutates a captured source revision.
	if p.Env == nil {
		p.Env = map[string]string{}
	}
	// Secret references are copied independently from public values.
	if p.Secrets == nil {
		p.Secrets = map[string]string{}
	}
	expected := map[string]string{"FUSED_ENGINE_GRPC_URL": strings.TrimRight(p.Fused.GRPCURL, "/"), "FUSED_APP_ID": p.Fused.AppID}
	// Each generated variable must have one unambiguous producer.
	for name, value := range expected {
		// Already-normalized revisions are valid, but authored conflicts cannot be silently overwritten.
		if err := bindFusedEnvironment(p, name, value); err != nil {
			return p, err
		}
	}
	return bindFusedToken(p)
}

// validateFusedConnection checks public connection metadata without loading credentials.
func validateFusedConnection(f model.FusedConnection) error {
	// Version IDs and secret reference names are distinct from management credentials or family names.
	if !fusedAppID.MatchString(f.AppID) || !secretRE.MatchString(f.TokenSecret) {
		return invalid("Fused requires an immutable app UUID and stored execution-token secret name")
	}
	u, err := url.Parse(f.GRPCURL)
	// Invalid parsing cannot reach any transport credential resolution.
	if err != nil {
		return invalid("Invalid Fused gRPC origin")
	}
	// Origins cannot carry provider routes, queries, or embedded secrets.
	if !fusedOrigin(u) {
		return invalid("Fused requires a gRPC origin without credentials or paths")
	}
	// A valid authority still requires the same encryption policy as Bridge.
	if !fusedTransport(u) {
		return invalid("Fused gRPC requires TLS except for loopback development")
	}
	return validateFusedPort(u.Port())
}

// fusedOrigin admits only a single endpoint authority with an optional root slash.
func fusedOrigin(u *url.URL) bool {
	return u.Hostname() != "" && u.User == nil && u.RawQuery == "" && !u.ForceQuery && u.Fragment == "" && (u.Path == "" || u.Path == "/")
}

// fusedTransport preserves Bridge's TLS requirement and narrow loopback development exception.
func fusedTransport(u *url.URL) bool {
	// TLS is independent of where the Engine is deployed.
	if u.Scheme == "https" || u.Scheme == "grpcs" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return (u.Scheme == "http" || u.Scheme == "grpc") && ip != nil && ip.IsLoopback()
}

// validateFusedPort rejects values the runtime cannot use as a TCP endpoint.
func validateFusedPort(value string) error {
	// Omitted ports follow the client transport's documented defaults.
	if value == "" {
		return nil
	}
	port, err := strconv.ParseUint(value, 10, 16)
	// Zero and overflowing ports cannot select a deployed Engine.
	if err != nil || port == 0 {
		return invalid("Invalid Fused gRPC port")
	}
	return nil
}

// bindFusedEnvironment preserves idempotent validation while rejecting alternate value producers.
func bindFusedEnvironment(p model.Project, name, value string) error {
	// A captured normalized revision may already contain this exact value.
	if prior, ok := p.Env[name]; ok && prior != value {
		return invalid("Fused connection conflicts with environment")
	}
	// Public connection metadata must not resolve through a secret instead.
	if _, ok := p.Secrets[name]; ok {
		return invalid("Fused connection conflicts with secret binding")
	}
	// Dependencies cannot silently replace the declared Engine identity.
	if _, ok := p.Bindings[name]; ok {
		return invalid("Fused connection conflicts with dependency binding")
	}
	p.Env[name] = value
	return nil
}

// bindFusedToken delegates credentials to Hostlier's protected secret store and redaction paths.
func bindFusedToken(p model.Project) (model.Project, error) {
	// An execution token must never become ordinary captured environment data.
	if _, ok := p.Env["FUSED_APP_TOKEN"]; ok {
		return p, invalid("Fused execution tokens require a stored secret")
	}
	// Dependency outputs cannot substitute a different credential producer.
	if _, ok := p.Bindings["FUSED_APP_TOKEN"]; ok {
		return p, invalid("Fused token conflicts with dependency binding")
	}
	// Repeated validation must retain the same explicit secret identity.
	if prior, ok := p.Secrets["FUSED_APP_TOKEN"]; ok && prior != p.Fused.TokenSecret {
		return p, invalid("Fused token conflicts with secret binding")
	}
	p.Secrets["FUSED_APP_TOKEN"] = p.Fused.TokenSecret
	return p, nil
}
