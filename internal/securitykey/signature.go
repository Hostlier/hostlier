// Package securitykey verifies domain-separated OpenSSH FIDO signatures with mandatory presence and verification.
package securitykey

import (
	"bytes"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/pem"
	"errors"
	"strings"

	"golang.org/x/crypto/ssh"
)

const Namespace = "hostlier-approval-v1"

// Envelope is the version-one SSHSIG wire envelope following the six-byte magic preamble.
type Envelope struct {
	Version       uint32
	PublicKey     []byte
	Namespace     string
	Reserved      string
	HashAlgorithm string
	Signature     []byte
}

// Credential contains only trusted public key material and its last accepted authenticator counter.
type Credential struct {
	PublicKey   string `json:"publicKey"`
	Fingerprint string `json:"fingerprint"`
	Counter     uint32 `json:"counter"`
	Revoked     bool   `json:"revoked,omitempty"`
}

// Parse accepts exactly one bare FIDO key, excluding ordinary software-key and SSH certificate algorithms.
func Parse(value string) (ssh.PublicKey, error) {
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(value))
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil || len(options) != 0 || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("one bare OpenSSH security-key public key is required")
	}
	// Stop before this invalid or unavailable state can reach the next protocol step.
	if key.Type() != ssh.KeyAlgoSKECDSA256 && key.Type() != ssh.KeyAlgoSKED25519 {
		return nil, errors.New("a FIDO security key is required")
	}
	return key, nil
}

// SignedData constructs the standardized SSHSIG namespace and digest binding without implementing cryptography.
func SignedData(envelope Envelope, message []byte) ([]byte, error) {
	var digest []byte
	// Keep distinct protocol outcomes on explicit execution paths.
	switch envelope.HashAlgorithm {
	// Handle only this explicit protocol outcome.
	case "sha256":
		sum := sha256.Sum256(message)
		digest = sum[:]
	// Handle only this explicit protocol outcome.
	case "sha512":
		sum := sha512.Sum512(message)
		digest = sum[:]
	// Handle only this explicit protocol outcome.
	default:
		return nil, errors.New("unsupported signature hash")
	}
	payload := struct {
		Namespace, Reserved, HashAlgorithm string
		Digest                             []byte
	}{envelope.Namespace, envelope.Reserved, envelope.HashAlgorithm, digest}
	return append([]byte("SSHSIG"), ssh.Marshal(payload)...), nil
}

// Verify validates the exact enrolled key, signature, signed touch/PIN flags and monotonic counter.
func Verify(credential Credential, message []byte, armored string) (uint32, error) {
	key, err := Parse(credential.PublicKey)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil || credential.Revoked {
		return 0, errors.New("approver key unavailable")
	}
	block, err := decodeArmor(armored)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return 0, err
	}
	var envelope Envelope
	// Apply this check before granting authority or advancing protocol state.
	if ssh.Unmarshal(block.Bytes[6:], &envelope) != nil || envelope.Version != 1 || envelope.Namespace != Namespace || envelope.Reserved != "" || !bytes.Equal(envelope.PublicKey, key.Marshal()) {
		return 0, errors.New("signature identity or namespace mismatch")
	}
	return verifyEnvelope(key, credential.Counter, envelope, message)
}

// verifyEnvelope delegates cryptographic verification to Go's maintained SSH implementation.
func verifyEnvelope(key ssh.PublicKey, last uint32, envelope Envelope, message []byte) (uint32, error) {
	var signature ssh.Signature
	// Apply this check before granting authority or advancing protocol state.
	if ssh.Unmarshal(envelope.Signature, &signature) != nil {
		return 0, errors.New("invalid security-key signature")
	}
	var flags struct {
		Flags   byte
		Counter uint32
	}
	// Apply this check before granting authority or advancing protocol state.
	if ssh.Unmarshal(signature.Rest, &flags) != nil || flags.Flags&0x05 != 0x05 {
		return 0, errors.New("security key must verify the user and require touch")
	}
	// Apply this check before granting authority or advancing protocol state.
	if (last != 0 || flags.Counter != 0) && flags.Counter <= last {
		return 0, errors.New("security-key counter regression")
	}
	data, err := SignedData(envelope, message)
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err != nil {
		return 0, err
	}
	// Propagate failure rather than continuing with incomplete or unverified state.
	if err = key.Verify(data, &signature); err != nil {
		return 0, err
	}
	return flags.Counter, nil
}

// decodeArmor accepts one exact SSHSIG envelope with no appended data.
func decodeArmor(armored string) (*pem.Block, error) {
	block, rest := pem.Decode([]byte(armored))
	// Stop before this invalid or unavailable state can reach the next protocol step.
	if !strings.HasPrefix(armored, "-----BEGIN SSH SIGNATURE-----") || block == nil || block.Type != "SSH SIGNATURE" || len(bytes.TrimSpace(rest)) != 0 || !bytes.HasPrefix(block.Bytes, []byte("SSHSIG")) {
		return nil, errors.New("invalid SSH signature")
	}
	return block, nil
}
