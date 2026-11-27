package fleet

import (
	"net"
	"net/url"
	"strings"
)

// Port is the master’s dedicated inbound mTLS port for controller connections.
const Port = "7464"

// HostRecord binds an immutable host name to an explicitly enrolled TLS identity.
type HostRecord struct {
	Name        string `json:"name"`
	URL         string `json:"url,omitempty"` // Optional display address; never dialed by the master.
	Fingerprint string `json:"fingerprint"`
}

// ValidHostName keeps host identifiers canonical across paths, reviews and saved policies.
func ValidHostName(name string) bool {
	// Fail closed when the required identity, scope or format does not match.
	if name == "" || len(name) > 63 || name == "identity" {
		return false
	}
	// Validate each entry before accepting the complete collection.
	for _, ch := range name {
		// Fail closed when the required identity, scope or format does not match.
		if !(ch >= 'a' && ch <= 'z') && !(ch >= '0' && ch <= '9') && ch != '-' {
			return false
		}
	}
	return true
}

// Member is the public identity returned to a box administrator over the local socket.
type Member struct {
	Hostname    string `json:"hostname"`
	Fingerprint string `json:"fingerprint"`
	Master      string `json:"master"`
	MasterURL   string `json:"masterURL,omitempty"`
}

// MasterChange compares the pin observed before enrollment to avoid replacing a concurrent switch.
type MasterChange struct {
	PreviousURL *string `json:"previousURL,omitempty"`
	Previous    string  `json:"previous"`
	URL         string  `json:"url,omitempty"`
	Fingerprint string  `json:"fingerprint"`
}

// ValidURL accepts a literal origin safe for transport and generated service arguments.
func ValidURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == "" && !strings.ContainsAny(value, " \t\r\n\"'\\%$`")
}

// MasterEndpoint maps a public controller origin to its dedicated fleet listener.
func MasterEndpoint(origin string) string {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Hostname() == "" {
		return ""
	}
	return "https://" + net.JoinHostPort(parsed.Hostname(), Port)
}
