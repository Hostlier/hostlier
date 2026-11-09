// Package store provides single-writer transactional SQLite state and authenticated encryption.
package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Store owns the exclusive writer lock and authenticated encryption state.
type Store struct {
	db     *sql.DB
	dir    string
	lock   *os.File
	aead   cipher.AEAD
	macKey []byte
}

// Open locks state, loads its original encryption key and decodes the durable snapshot.
func Open(dir string, state any) (*Store, error) {
	lock, err := lockDirectory(dir)
	// Stop if another controller owns the state directory or it cannot be protected.
	if err != nil {
		return nil, err
	}
	s := &Store{dir: dir, lock: lock}
	// Release the writer lock on every initialization failure.
	fail := func(err error) (*Store, error) { s.Close(); return nil, err }
	key, err := readEncryptionKey(dir)
	// Keep encrypted state inaccessible when the original key cannot be loaded.
	if err != nil {
		return fail(err)
	}
	s.macKey = append([]byte{}, key...)
	block, err := aes.NewCipher(key)
	// Reject unsupported cipher keys before opening any records.
	if err != nil {
		return fail(err)
	}
	s.aead, err = cipher.NewGCM(block)
	// Require authenticated GCM encryption before accepting state.
	if err != nil {
		return fail(err)
	}
	// Propagate failures before publishing recovery state.
	if err := s.openDatabase(state); err != nil {
		return fail(err)
	}

	return s, nil
}

// Save serializes state and durably replaces its protected on-disk snapshot.
func (s *Store) Save(state any) error {
	// A closed controller cannot publish additional durable transitions.
	if s.db == nil {
		return errors.New("controller store is closed")
	}
	data, err := json.Marshal(state)
	// Reject unserializable state before touching its durable snapshot.
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE controller_snapshot SET payload = ? WHERE id = 1", data); err != nil {
		return err
	}
	if err = indexCollections(tx, data); err != nil {
		return err
	}
	if err = archiveActivity(tx, data); err != nil {
		return err
	}
	return tx.Commit()
}

// Encrypt seals a secret with a fresh nonce and authenticated encryption.
func (s *Store) Encrypt(value string) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	// Never reuse a nonce when secure random generation fails.
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(s.aead.Seal(nonce, nonce, []byte(value), nil)), nil
}

// Decrypt validates and authenticates an encrypted record before returning plaintext.
func (s *Store) Decrypt(value string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(value)
	// Reject invalid base64 before attempting authenticated decryption.
	if err != nil {
		return "", err
	}
	n := s.aead.NonceSize()
	// Require a complete nonce prefix before slicing encrypted data.
	if len(raw) < n {
		return "", errors.New("invalid encrypted record")
	}
	plain, err := s.aead.Open(nil, raw[:n], raw[n:], nil)
	return string(plain), err
}

// Close releases the advisory writer lock and closes its descriptor idempotently.
func (s *Store) Close() error {
	// Repeated closes are safe once the writer descriptor has been cleared.
	if s.db != nil {
		s.db.Close()
		s.db = nil
	}
	// Validate the precondition before accepting protected state.
	if s.lock == nil {
		return nil
	}
	err := syscall.Flock(int(s.lock.Fd()), syscall.LOCK_UN)
	e := s.lock.Close()
	s.lock = nil
	// Report lock-release failure even when descriptor close also fails.
	if err != nil {
		return err
	}
	return e
}

// WriteBlob durably commits an opaque encrypted record alongside protected state.
func (s *Store) WriteBlob(name string, data []byte) error {
	// Reject path traversal before writing a protected backup blob.
	if err := validBlobName(name); err != nil {
		return err
	}
	return s.writeAtomic(name, name+".tmp", data)
}

// ReadBlob reads a confined encrypted blob without interpreting its contents.
func (s *Store) ReadBlob(name string) ([]byte, error) {
	// Reject path traversal before reading a protected backup blob.
	if err := validBlobName(name); err != nil {
		return nil, err
	}
	// Refuse redirected private records before reading their bytes.
	if err := regularOrMissing(filepath.Join(s.dir, name)); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(s.dir, name))
}

// Authenticate protects secret-write deduplication without publishing an offline guessing oracle.
func (s *Store) Authenticate(value string) string {
	mac := hmac.New(sha256.New, s.macKey)
	mac.Write([]byte("hostlier-secret-write-v1\x00"))
	mac.Write([]byte(value))
	return "hmac:" + hex.EncodeToString(mac.Sum(nil))
}

// readEncryptionKey loads the original state key or creates one for a fresh store.
func readEncryptionKey(dir string) ([]byte, error) {
	keyPath := filepath.Join(dir, "secret.key")
	// Refuse redirected key files before reading protected material.
	if err := regularOrMissing(keyPath); err != nil {
		return nil, err
	}
	key, err := os.ReadFile(keyPath)
	// Create a key only when the existing key file is absent.
	if errors.Is(err, os.ErrNotExist) {
		key, err = createEncryptionKey(dir, keyPath)
	}
	// Preserve unreadable or failed key creation errors.
	if err != nil {
		return nil, err
	}
	// Require the expected 256-bit key size.
	if len(key) != 32 {
		return nil, errors.New("invalid secret encryption key")
	}
	// Restore owner-only permissions before retaining the loaded key.
	if err = os.Chmod(keyPath, 0600); err != nil {
		return nil, err
	}
	return key, nil
}

// createEncryptionKey refuses to replace a key when encrypted state already exists.
func createEncryptionKey(dir, keyPath string) ([]byte, error) {
	// Refuse a replacement key when a durable state snapshot already exists.
	if existingState(dir) {
		return nil, errors.New("secret encryption key missing; restore original key")
	}
	key := make([]byte, 32)
	// Require cryptographic entropy for a fresh controller key.
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	// Exclusively create the key to prevent concurrent replacement.
	if err != nil {
		return nil, err
	}
	_, err = f.Write(key)
	// Flush the key bytes before reporting successful creation.
	if err == nil {
		err = f.Sync()
	}
	f.Close()
	return key, err
}

// syncDirectory persists a completed rename before reporting success.
func syncDirectory(path string) error {
	dir, err := os.Open(path)
	// Require a directory descriptor before flushing rename metadata.
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// lockDirectory protects owner-only state against concurrent controller writers.
func lockDirectory(dir string) (*os.File, error) {
	// Reject redirected directory ancestry before creating protected state.
	if err := safeStateDirectory(dir); err != nil {
		return nil, err
	}
	// Create the state directory with owner-only access.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	// Protect an existing directory before opening its lock file.
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	// Refuse a redirected advisory lock file.
	if err := regularOrMissing(filepath.Join(dir, "controller.lock")); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "controller.lock"), os.O_CREATE|os.O_RDWR, 0600)
	// Stop if the lock descriptor cannot be opened securely.
	if err != nil {
		return nil, err
	}
	// Acquire the nonblocking exclusive writer lock or close the unused descriptor.
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("controller state is already locked: %w", err)
	}
	return lock, nil
}

// validBlobName confines backup records to the protected state directory.
func validBlobName(name string) error {
	// Allow only a filename within the protected state directory.
	if filepath.Base(name) != name || name == "." || name == ".." {
		return errors.New("invalid blob name")
	}
	return nil
}

// writeAtomic is the common commit path for encrypted blobs and controller state.
func (s *Store) writeAtomic(name, temp string, data []byte) error {
	// Refuse redirected final and temporary files before replacing protected data.
	if err := regularOrMissing(filepath.Join(s.dir, name)); err != nil {
		return err
	}
	// Refuse a hostile temporary path left by another actor.
	if err := regularOrMissing(filepath.Join(s.dir, temp)); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, temp), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	// Require a writable temporary file before beginning the commit.
	if err != nil {
		return err
	}
	// Write sensitive bytes only after owner-only permissions are enforced.
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	// Flush completed contents before publishing the temporary file.
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	// Preserve any permission, write or fsync failure before considering close errors.
	if err != nil {
		return err
	}
	// Do not rename a file whose close failed.
	if closeErr != nil {
		return closeErr
	}
	// Publish the durable file atomically before syncing its parent directory.
	if err = os.Rename(filepath.Join(s.dir, temp), filepath.Join(s.dir, name)); err != nil {
		return err
	}
	return syncDirectory(s.dir)
}

// safeStateDirectory rejects symlinks in existing ancestry while permitting a fresh leaf.
func safeStateDirectory(path string) error {
	// Standard macOS temporary roots are system-owned aliases.
	if path == "/var" || path == "/tmp" {
		resolved, err := filepath.EvalSymlinks(path)
		// Propagate failures before publishing recovery state.
		if err != nil {
			return err
		}
		// Linux temporary roots are real directories rather than aliases.
		if resolved != path {
			return safeStateDirectory(resolved)
		}
	}
	info, err := os.Lstat(path)
	// Missing children are safe only when their existing ancestors are safe.
	if errors.Is(err, os.ErrNotExist) {
		return safeStateDirectory(filepath.Dir(path))
	}
	// Require a genuine directory before protecting its contents.
	if err != nil {
		return err
	}
	// Symbolic links cannot provide controller ownership boundaries.
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe state directory")
	}
	parent := filepath.Dir(path)
	// Stop after validating the filesystem root.
	if parent != path {
		return safeStateDirectory(parent)
	}
	return nil
}
