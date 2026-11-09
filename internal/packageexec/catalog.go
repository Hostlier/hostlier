package packageexec

// Catalog describes packages advertised by one immutable source selection.
type Catalog struct {
	Repository string             `json:"repository"`
	Version    string             `json:"version"`
	Packages   []AvailablePackage `json:"packages"`
}

// AvailablePackage groups supported executable targets under one installable name.
type AvailablePackage struct {
	Name      string     `json:"name"`
	Platforms []Platform `json:"platforms"`
}

// Download carries verified native bytes and public provenance only.
type Download struct {
	Data    []byte
	Request InstallRequest
}
