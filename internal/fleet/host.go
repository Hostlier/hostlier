package fleet

// Port is the dedicated mTLS port on an installed fleet member.
const Port = "7464"

// HostRecord binds an immutable host name to an explicitly enrolled TLS identity.
type HostRecord struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
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
}

// MasterChange compares the pin observed before enrollment to avoid replacing a concurrent switch.
type MasterChange struct {
	Previous    string `json:"previous"`
	Fingerprint string `json:"fingerprint"`
}
