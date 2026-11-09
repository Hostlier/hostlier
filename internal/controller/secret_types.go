package controller

// Secret retains encrypted secret revisions for deployment and rollback.
type Secret struct {
	Current   string            `json:"current"`
	Revisions map[string]string `json:"revisions"`
	Owner     string            `json:"owner,omitempty"`
}

// SecretRef pins a named secret revision without exposing its value.
type SecretRef struct {
	Name     string `json:"name"`
	Revision string `json:"revision"`
}

// Write retains a keyed secret-upload replay signature.
type Write struct {
	Signature string `json:"signature"`
	Name      string `json:"name"`
	Revision  string `json:"revision"`
}
