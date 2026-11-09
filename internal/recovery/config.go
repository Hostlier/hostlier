package recovery

import (
	"bytes"
	"encoding/hex"
	"errors"
	"gopkg.in/yaml.v3"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Config references owner-only credential files; no API accepts destination credentials.
type Config struct {
	Destinations []destinationConfig `yaml:"destinations"`
}

// destinationConfig separates public storage identity from private credential references.
type destinationConfig struct {
	ID, Endpoint, Region, Bucket, Prefix string
	AccessKeyFile                        string `yaml:"accessKeyFile"`
	SecretKeyFile                        string `yaml:"secretKeyFile"`
	EncryptionKeyFile                    string `yaml:"encryptionKeyFile"`
}

// PrivateFile refuses shared, symlinked or unbounded operator secrets.
func PrivateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	// Only private regular files can supply recovery authority.
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 65536 {
		return nil, errors.New("recovery configuration requires owner-only regular files")
	}
	return os.ReadFile(path)
}

// Load resolves configured destinations once at startup without persisting their keys in controller state.
func Load(path string) (map[string]*Destination, error) {
	data, err := PrivateFile(path)
	// Unavailable configuration prevents startup instead of disabling encryption.
	if err != nil {
		return nil, err
	}
	var config Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	// Reject trailing documents and misspelled secret references.
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid recovery configuration")
	}
	destinations := map[string]*Destination{}
	// Each alias binds an explicit operator-controlled storage boundary.
	for _, entry := range config.Destinations {
		// Advance only after the required recovery invariant holds.
		if destinations[entry.ID] != nil {
			return nil, errors.New("duplicate recovery destination")
		}
		destination, err := loadDestination(entry, filepath.Dir(path))
		// Propagate failure before performing the next recovery side effect.
		if err != nil {
			return nil, err
		}
		destinations[entry.ID] = destination
	}
	return destinations, nil
}

// validDestination requires a canonical HTTPS origin and a bounded, unambiguous storage namespace.
func validDestination(entry destinationConfig) bool {
	endpoint, err := url.Parse(entry.Endpoint)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return false
	}
	validOrigin := validStorageOrigin(endpoint)
	validNames := regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`).MatchString(entry.ID) && entry.Region != "" && entry.Bucket != "" && strings.Trim(entry.Prefix, "/") != ""
	return validOrigin && validNames
}

// loadDestination resolves credentials relative to the protected configuration without serializing secrets.
func loadDestination(entry destinationConfig, dir string) (*Destination, error) {
	// Advance only after the required recovery invariant holds.
	if !validDestination(entry) {
		return nil, errors.New("recovery destination requires a unique alias, HTTPS origin, region, bucket and prefix")
	}
	access, err := credentialFile(dir, entry.AccessKeyFile)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return nil, err
	}
	secret, err := credentialFile(dir, entry.SecretKeyFile)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return nil, err
	}
	encoded, err := credentialFile(dir, entry.EncryptionKeyFile)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return nil, err
	}
	key, err := hex.DecodeString(encoded)
	// Propagate failure before performing the next recovery side effect.
	if err != nil || len(key) != 32 {
		return nil, errors.New("recovery encryption key requires 64 hexadecimal characters")
	}
	return &Destination{ID: entry.ID, Endpoint: entry.Endpoint, Region: entry.Region, Bucket: entry.Bucket, Prefix: entry.Prefix, Key: key, Objects: NewS3(entry.Endpoint, entry.Region, entry.Bucket, entry.Prefix, access, secret)}, nil
}

// credentialFile rejects absent or multiline credentials and supports relocatable operator configuration.
func credentialFile(dir, path string) (string, error) {
	// Advance only after the required recovery invariant holds.
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	data, err := PrivateFile(path)
	// Propagate failure before performing the next recovery side effect.
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	// Advance only after the required recovery invariant holds.
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("invalid recovery credential file")
	}
	return value, nil
}

// validStorageOrigin prevents credentials, paths or URL parameters from changing signed S3 routing.
func validStorageOrigin(endpoint *url.URL) bool {
	return endpoint.Scheme == "https" && endpoint.Hostname() != "" && endpoint.User == nil && endpoint.RawQuery == "" && endpoint.Fragment == "" && endpoint.Path == ""
}
