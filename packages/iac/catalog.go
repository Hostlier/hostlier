package iac

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// LoadCatalog reads an administrator-installed non-secret catalog outside request input.
func LoadCatalog(path string) (map[string]Installed, error) {
	resolved, err := filepath.EvalSymlinks(path)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil || resolved != path {
		return nil, errors.New("unsafe installed catalog path")
	}
	info, err := os.Lstat(path)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 1024*1024 {
		return nil, errors.New("unsafe installed catalog")
	}
	raw, err := os.ReadFile(path)
	// Stop this phase when private storage or native execution cannot be verified.
	if err != nil {
		return nil, errors.New("catalog unavailable")
	}
	var catalog map[string]Installed
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	// Reject an identity or visibility mismatch before publishing the operation result.
	if decoder.Decode(&catalog) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid installed catalog")
	}
	return catalog, nil
}
