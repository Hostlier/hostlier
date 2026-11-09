// Package recovery owns authenticated, portable database recovery bundles and S3 transport.
package recovery

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hostlier/internal/model"
)

const MaxPlain = 90 << 20
const MaxObject = MaxPlain + 1024

// Bundle authenticates native bytes together with engine, immutable image and restore evidence.
type Bundle struct {
	Version   int                    `json:"version"`
	ID        string                 `json:"id"`
	Source    string                 `json:"source"`
	CreatedAt int64                  `json:"createdAt"`
	Spec      model.ResourceSpec     `json:"spec"`
	Evidence  model.DatabaseEvidence `json:"evidence"`
	SHA256    string                 `json:"sha256"`
	Data      []byte                 `json:"data"`
}

// Digest binds bytes without disclosing database content in public receipts.
func Digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Seal uses an independently supplied recovery key; controller state alone cannot decrypt an offsite object.
func Seal(key []byte, b Bundle) ([]byte, error) {
	b.Version = 1
	b.SHA256 = Digest(b.Data)
	plain, err := json.Marshal(b)
	// Bound all allocations before encrypting a native snapshot.
	if err != nil || len(plain) > MaxPlain {
		return nil, errors.New("backup bundle too large")
	}
	block, err := aes.NewCipher(key)
	// Only explicit 256-bit recovery keys are accepted.
	if err != nil || len(key) != 32 {
		return nil, errors.New("invalid recovery key")
	}
	aead, err := cipher.NewGCM(block)
	// Encryption setup failure cannot publish plaintext.
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	_, err = rand.Read(nonce)
	// Nonce generation must succeed independently for every immutable object.
	if err != nil {
		return nil, err
	}
	out := append([]byte("HOSTLIER-S3-v1\x00"), nonce...)
	return aead.Seal(out, nonce, plain, []byte("hostlier-database-backup-v1")), nil
}

// Open checks authenticated metadata and content before any imported backup becomes visible.
func Open(key, data []byte) (Bundle, error) {
	var b Bundle
	prefix := []byte("HOSTLIER-S3-v1\x00")
	// Reject malformed objects before creating a decryptor.
	if len(data) < len(prefix)+28 || len(data) > MaxObject || string(data[:len(prefix)]) != string(prefix) {
		return b, errors.New("invalid backup envelope")
	}
	block, err := aes.NewCipher(key)
	// Recovery requires the independently retained 256-bit key.
	if err != nil || len(key) != 32 {
		return b, errors.New("invalid recovery key")
	}
	aead, err := cipher.NewGCM(block)
	// Cryptographic initialization is mandatory.
	if err != nil {
		return b, err
	}
	plain, err := aead.Open(nil, data[len(prefix):len(prefix)+12], data[len(prefix)+12:], []byte("hostlier-database-backup-v1"))
	// Authentication failure never falls back to native import.
	if err != nil {
		return b, errors.New("backup authentication failed")
	}
	// Decode only a bounded, complete, supported bundle.
	if !validBundle(plain, &b) {
		return Bundle{}, errors.New("invalid backup content")
	}
	return b, nil
}

// validBundle checks authenticated metadata and native-byte integrity before any resource import.
func validBundle(plain []byte, b *Bundle) bool {
	// Advance only after the required recovery invariant holds.
	if json.Unmarshal(plain, b) != nil || b.Version != 1 || len(b.Data) == 0 || len(b.Data) > 64<<20 || Digest(b.Data) != b.SHA256 {
		return false
	}
	return validBundleIdentity(*b)
}

// validBundleIdentity rejects unrelated or unsupported native engine evidence.
func validBundleIdentity(b Bundle) bool {
	validEngine := b.Spec.Type == "postgres" || b.Spec.Type == "redis" || b.Spec.Type == "neo4j"
	return validEngine && b.Source != "" && len(b.ID) == 64 && b.Spec.Type == b.Evidence.Engine && b.Evidence.Version != "" && len(b.Evidence.Digest) == 64
}
