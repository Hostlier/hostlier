// Package protected provides host-selected private storage for trusted package adapters.
package protected

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Binding binds an immutable artifact to its full approved operation identity.
type Binding struct{ Owner, Operation, Config, Executable, Bundle, Source, Backend, State string }

// Handle references encrypted bytes rather than an arbitrary filesystem path.
type Handle struct {
	ID      string    `json:"id"`
	SHA256  string    `json:"sha256"`
	Expires time.Time `json:"expires"`
}

// Store keeps artifacts durable and workspaces disposable below a protected root.
type Store struct {
	root string
	aead cipher.AEAD
}

// record is the authenticated private artifact envelope.
type record struct {
	Binding Binding
	Handle  Handle
	Data    []byte
}

// Open opens a host-selected owner-only directory and persistent encryption key.
func Open(root string) (*Store, error) {
	// Enforce the private storage and protocol boundary before proceeding.
	if !filepath.IsAbs(root) {
		return nil, errors.New("state root must be absolute")
	}
	// Stop on storage failure without returning partial private content.
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	// Stop on storage failure without returning partial private content.
	if err := safeDirectory(root); err != nil {
		return nil, err
	}
	key, err := loadKey(filepath.Join(root, "artifact.key"))
	// Stop on storage failure without returning partial private content.
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	// Stop on storage failure without returning partial private content.
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	return &Store{root: root, aead: aead}, err
}

// Save atomically creates a bounded immutable encrypted artifact.
func (s *Store) Save(b Binding, data []byte, ttl time.Duration) (Handle, error) {
	// Require ownership, bounded content and a finite approved artifact lifetime.
	if b.Owner == "" || b.Operation == "" || ttl <= 0 || ttl > 7*24*time.Hour || len(data) > 32*1024*1024 {
		return Handle{}, errors.New("invalid artifact")
	}
	id := make([]byte, 24)
	// Stop on storage failure without returning partial private content.
	if _, err := rand.Read(id); err != nil {
		return Handle{}, err
	}
	sum := sha256.Sum256(data)
	h := Handle{ID: hex.EncodeToString(id), SHA256: hex.EncodeToString(sum[:]), Expires: time.Now().UTC().Add(ttl)}
	plain, err := json.Marshal(record{Binding: b, Handle: h, Data: data})
	// Stop on storage failure without returning partial private content.
	if err != nil {
		return Handle{}, err
	}
	nonce := make([]byte, s.aead.NonceSize())
	// Stop on storage failure without returning partial private content.
	if _, err = rand.Read(nonce); err != nil {
		return Handle{}, err
	}
	sealed := s.aead.Seal(nonce, nonce, plain, []byte(h.ID))
	err = exclusiveWrite(filepath.Join(s.root, h.ID+".plan"), sealed)
	return h, err
}

// Load authenticates ownership, expiry and content before returning private bytes.
func (s *Store) Load(h Handle, b Binding) ([]byte, error) {
	// Validate the opaque handle before constructing a storage filename.
	if len(h.ID) != 48 {
		return nil, errors.New("invalid artifact handle")
	}
	// Validate the opaque handle before constructing a storage filename.
	if _, err := hex.DecodeString(h.ID); err != nil {
		return nil, errors.New("invalid artifact handle")
	}
	sealed, err := s.readArtifact(h)
	// Stop on storage failure without returning partial private content.
	if err != nil {
		return nil, err
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, sealed[:n], sealed[n:], []byte(h.ID))
	// Stop on storage failure without returning partial private content.
	if err != nil {
		return nil, errors.New("invalid artifact")
	}
	var r record
	// Authenticate complete operation identity, original handle and expiry together.
	if json.Unmarshal(plain, &r) != nil || r.Binding != b || r.Handle != h || !time.Now().Before(h.Expires) {
		return nil, errors.New("artifact identity changed or expired")
	}
	sum := sha256.Sum256(r.Data)
	// Refuse content that differs from the authenticated handle digest.
	if hex.EncodeToString(sum[:]) != h.SHA256 {
		return nil, errors.New("artifact content changed")
	}
	return r.Data, nil
}

// Workspace creates an isolated disposable owner-only operation directory.
func (s *Store) Workspace(owner, operation string) (string, func(), error) {
	sum := sha256.Sum256([]byte(owner + "\x00" + operation))
	path, err := os.MkdirTemp(s.root, "work-"+hex.EncodeToString(sum[:8])+"-")
	// Stop on storage failure without returning partial private content.
	return path, func() { os.RemoveAll(path) }, err
}

// Lock serializes native operations for one stable ownership identity.
func (s *Store) Lock(owner string) (func(), error) {
	sum := sha256.Sum256([]byte(owner))
	path := filepath.Join(s.root, "lock-"+hex.EncodeToString(sum[:]))
	err := os.Mkdir(path, 0700)
	// Stop on storage failure without returning partial private content.
	if err != nil {
		return nil, errors.New("owner is locked; investigate abandoned operations")
	}
	// Enforce the private storage and protocol boundary before proceeding.
	return func() { os.Remove(path) }, nil
}

// safeDirectory refuses redirected and publicly accessible storage roots.
func safeDirectory(path string) error {
	resolved, err := filepath.EvalSymlinks(path)
	// Stop on storage failure without returning partial private content.
	if err != nil {
		return err
	}
	absolute, err := filepath.Abs(path)
	// Stop on storage failure without returning partial private content.
	if err != nil || resolved != absolute {
		return errors.New("redirected state directory")
	}
	info, err := os.Lstat(path)
	// Reject redirected, publicly accessible or oversized private storage entries.
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("unsafe state directory")
	}
	return nil
}

// exclusiveWrite prevents existing private artifacts from being overwritten.
func exclusiveWrite(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	// Stop on storage failure without returning partial private content.
	if err != nil {
		return err
	}
	_, writeErr := io.Copy(file, bytes.NewReader(data))
	syncErr := file.Sync()
	closeErr := file.Close()
	// Enforce the private storage and protocol boundary before proceeding.
	if writeErr != nil || syncErr != nil || closeErr != nil {
		os.Remove(path)
		return errors.New("cannot persist artifact")
	}
	return nil
}

// loadKey creates or reopens the protected persistent encryption key.
func loadKey(keyPath string) ([]byte, error) {
	key, err := os.ReadFile(keyPath)
	// Create encryption material only for a previously uninitialized store.
	if os.IsNotExist(err) {
		key = make([]byte, 32)
		// Stop on storage failure without returning partial private content.
		if _, err = rand.Read(key); err != nil {
			return nil, err
		}
		err = exclusiveWrite(keyPath, key)
		// A concurrent initializer must use the same persistent winning key.
		if os.IsExist(err) {
			key, err = os.ReadFile(keyPath)
		}
	}
	// Stop on storage failure without returning partial private content.
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(keyPath)
	// Reject redirected, publicly accessible or oversized private storage entries.
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, errors.New("unsafe artifact key")
	}
	return key, nil
}

// readArtifact bounds ciphertext and refuses unsafe artifact files.
func (s *Store) readArtifact(h Handle) ([]byte, error) {
	path := filepath.Join(s.root, h.ID+".plan")
	info, err := os.Lstat(path)
	// Reject redirected, publicly accessible or oversized private storage entries.
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 48*1024*1024 {
		return nil, errors.New("unsafe artifact")
	}
	sealed, err := os.ReadFile(path)
	// Stop on storage failure without returning partial private content.
	if err != nil || len(sealed) < s.aead.NonceSize() {
		return nil, errors.New("invalid artifact")
	}
	return sealed, nil
}
