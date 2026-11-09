package usefused

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Main runs a provider-specific optional executable from its administrator-owned installation.
func Main(provider string) error {
	// Executables expose only the private package protocol.
	if len(os.Args) != 2 || os.Args[1] != "serve" {
		return errors.New("this package must be invoked by the Hostlier workflow runner")
	}
	executable, err := os.Executable()
	// Resolve configuration beside the runner's protected executable snapshot.
	if err != nil {
		return errors.New("cannot locate package installation")
	}
	config, err := ReadConfig(filepath.Join(filepath.Dir(executable), provider+".json"))
	// Missing or unsafe connection settings prevent credential transmission.
	if err != nil {
		return err
	}
	p := Package{Provider: provider, Client: Client{Config: config}}
	return p.Serve(context.Background(), os.Stdin, os.Stdout)
}

// ReadConfig reads only a protected, bounded, strict JSON executor configuration.
func ReadConfig(path string) (Config, error) {
	var config Config
	info, err := os.Lstat(path)
	// Symlinks and group/other-writable configuration cannot select a credential destination.
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || info.Size() > 65536 {
		return config, errors.New("missing or unsafe Usefused package configuration")
	}
	file, err := os.Open(path)
	// Do not fall back to ambient environment or CLI credentials.
	if err != nil {
		return config, errors.New("cannot open Usefused package configuration")
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 65537))
	decoder.DisallowUnknownFields()
	// A single explicit executor configuration defines the trusted origin and immutable version.
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF {
		return config, errors.New("invalid Usefused package configuration")
	}
	return config, config.Validate()
}

// Install writes a provider executable and its non-secret executor configuration into a local package directory.
func Install(provider, binaryPath, configPath, directory string) error {
	// Keep this installer scoped to the two supplied package entry points.
	if provider != "aiven" && provider != "railway" {
		return errors.New("expected aiven or railway")
	}
	config, err := ReadConfig(configPath)
	// Validate the destination identity before copying executable code.
	if err != nil {
		return err
	}
	binary, err := readBinary(binaryPath)
	// Bound the executable artifact before creating installation files.
	if err != nil {
		return errors.New("cannot read package binary")
	}
	// Verify administrator ownership before staging either installation artifact.
	if err := preparePackageDirectory(directory); err != nil {
		return err
	}
	encoded, _ := json.MarshalIndent(config, "", "  ")
	// Configuration changes invalidate existing package payload fingerprints.
	if err := installFile(directory, provider+".json", encoded, 0600); err != nil {
		return err
	}
	return installFile(directory, provider, binary, 0700)
}

// readBinary bounds executable artifact reads before allocating an installation buffer.
func readBinary(path string) ([]byte, error) {
	file, err := os.Open(path)
	// Keep source read errors separate from destination installation failures.
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 128*1024*1024+1))
	// Empty or oversized executable artifacts cannot be installed.
	if err != nil || len(data) == 0 || len(data) > 128*1024*1024 {
		return nil, errors.New("invalid package binary")
	}
	return data, nil
}

// preparePackageDirectory creates and verifies the administrator-owned installation destination.
func preparePackageDirectory(directory string) error {
	// Packages are trusted administrator-installed code with private execution inputs.
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(directory)
	// Do not write through a redirected or shared writable package directory.
	if err != nil || !info.IsDir() || info.Mode().Perm()&0022 != 0 {
		return errors.New("unsafe package directory")
	}
	return nil
}

// installFile atomically replaces one administrator-selected package artifact.
func installFile(directory, name string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(directory, ".install-")
	// No destination is changed when staging cannot begin.
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, writeErr := file.Write(data)
	modeErr := file.Chmod(mode)
	syncErr := file.Sync()
	closeErr := file.Close()
	// Never activate a partially written artifact.
	if writeErr != nil || modeErr != nil || syncErr != nil || closeErr != nil {
		return errors.New("cannot stage package artifact")
	}
	return os.Rename(file.Name(), filepath.Join(directory, name))
}
