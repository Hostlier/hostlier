package emailauth

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config keeps credentials out of YAML by referencing process environment or owner-only files.
type Config struct {
	AllowedEmails []string `yaml:"allowedEmails"`
	Provider      string   `yaml:"provider"`
	From          string   `yaml:"from"`
	Fused         struct {
		EngineURL   string `yaml:"engineURL,omitempty"`
		AppFamilyID string `yaml:"appFamilyID,omitempty"`
		TokenEnv    string `yaml:"tokenEnv,omitempty"`
		TokenFile   string `yaml:"tokenFile,omitempty"`
	} `yaml:"fused,omitempty"`
	SMTP struct {
		Host         string `yaml:"host"`
		Port         string `yaml:"port"`
		Mode         string `yaml:"mode"`
		Username     string `yaml:"username"`
		PasswordEnv  string `yaml:"passwordEnv"`
		PasswordFile string `yaml:"passwordFile"`
	} `yaml:"smtp,omitempty"`
}

// Load validates operator configuration and constructs exactly one explicitly selected sender.
func Load(path, controller string) (*Service, error) {
	data, err := privateFile(path, 65536)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return nil, err
	}
	var config Config
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	// Stop before this invalid or unavailable state can reach the next protocol step.
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("invalid email enrollment configuration")
	}
	sender, err := config.sender(filepath.Dir(path))
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return nil, err
	}
	return New(config.AllowedEmails, sender, controller)
}

// sender never changes provider or falls back after invalid configuration.
func (c Config) sender(directory string) (Sender, error) {
	// Apply this check before granting authority or advancing protocol state.
	if c.Provider == "smtp" {
		password, err := readCredential(directory, c.SMTP.PasswordEnv, c.SMTP.PasswordFile)
		// Propagate failure rather than continuing with incomplete or unverified state.
		if err != nil {
			return nil, err
		}
		sender := SMTP{Host: c.SMTP.Host, Port: c.SMTP.Port, Mode: c.SMTP.Mode, From: c.From, Username: c.SMTP.Username, Password: password}
		return sender, sender.Validate()
	}
	key, err := readCredential(directory, c.Fused.TokenEnv, c.Fused.TokenFile)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return nil, err
	}
	return NewFused(c.Provider, c.Fused.EngineURL, c.Fused.AppFamilyID, key, c.From)
}

// readCredential resolves exactly one secret source without printing its value or provider diagnostics.
func readCredential(directory, environment, file string) (string, error) {
	// Apply this check before granting authority or advancing protocol state.
	if environment != "" && file != "" {
		return "", errors.New("choose either a credential environment variable or a credential file")
	}
	// Apply this check before granting authority or advancing protocol state.
	if environment != "" {
		value := os.Getenv(environment)
		// Apply this check before granting authority or advancing protocol state.
		if value == "" {
			return "", errors.New("email credential environment variable is empty")
		}
		return value, nil
	}
	// Apply this check before granting authority or advancing protocol state.
	if file == "" {
		return "", nil
	}
	// Apply this check before granting authority or advancing protocol state.
	if !filepath.IsAbs(file) {
		file = filepath.Join(directory, file)
	}
	value, err := privateFile(file, 8192)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(value)), nil
}

// privateFile rejects symlinks, shared permissions and oversized security configuration before reading it.
func privateFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > limit {
		return nil, errors.New("email configuration and credential files must be bounded regular owner-only files")
	}
	data, err := os.ReadFile(path)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return nil, errors.New("cannot read email configuration or credentials")
	}
	return data, nil
}
