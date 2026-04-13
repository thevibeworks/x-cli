package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

// Session is what x-cli persists between runs. Contains the cookie map and
// the user identity confirmed by verify_credentials.
type Session struct {
	Cookies  map[string]string `json:"cookies"`
	UserID   string            `json:"user_id"`
	Username string            `json:"username"`
	Name     string            `json:"name"`
}

const (
	keyringService = "x-cli"
	keyringAccount = "session"
)

// Save writes the session first to the OS keychain, falling back to an
// AES-GCM encrypted file at fallbackPath if the keychain is unavailable.
// The encryption key for the file fallback is derived from a machine-scoped
// seed; it is NOT meant to resist offline attackers — it's just so the file
// is not plaintext on disk.
func Save(s *Session, fallbackPath string) error {
	blob, err := json.Marshal(s)
	if err != nil {
		return err
	}

	if err := keyring.Set(keyringService, keyringAccount, string(blob)); err == nil {
		return nil
	}

	return saveEncrypted(blob, fallbackPath)
}

// Load returns the stored session, trying keychain first then the encrypted
// file. Returns (nil, nil) if no session has ever been saved.
func Load(fallbackPath string) (*Session, error) {
	if raw, err := keyring.Get(keyringService, keyringAccount); err == nil {
		var s Session
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, fmt.Errorf("parse keychain session: %w", err)
		}
		return &s, nil
	}

	blob, err := loadEncrypted(fallbackPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var s Session
	if err := json.Unmarshal(blob, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Delete removes the session from both keychain and file fallback.
func Delete(fallbackPath string) error {
	_ = keyring.Delete(keyringService, keyringAccount)
	if err := os.Remove(fallbackPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// -----------------------------------------------------------------------------
// File fallback (AES-256-GCM).
//
// Threat model: this is **obfuscation, not encryption**. The key is derived
// from a machine-stable seed (machine-id or hostname) via SHA-256. An attacker
// with filesystem read access to the user's home directory can trivially
// reproduce the key and decrypt the file. That is the same threat model as
// every other "encrypted config" on disk — its job is to make the cookie not
// casually visible in plaintext and to fail-closed on file swap across
// machines. If you want real at-rest protection, use the OS keychain path
// (the primary path, tried first in Save/Load).
// -----------------------------------------------------------------------------

// aadTag is bound into the GCM seal so that a valid session file from a
// different x-cli version cannot be swapped in and silently accepted.
// Bump the suffix whenever the on-disk format changes.
const aadTag = "x-cli:session:v1"

func fileKey() []byte {
	seed := "x-cli:" + machineID()
	sum := sha256.Sum256([]byte(seed))
	return sum[:]
}

func machineID() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil && len(b) > 0 {
			return string(b)
		}
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "x-cli-default"
}

func saveEncrypted(plain []byte, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll honors umask on create and leaves existing directories
	// untouched. Force 0o700 explicitly so a permissive umask cannot leave
	// the session directory world-readable.
	_ = os.Chmod(dir, 0o700)

	block, err := aes.NewCipher(fileKey())
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	sealed := gcm.Seal(nonce, nonce, plain, []byte(aadTag))
	encoded := base64.StdEncoding.EncodeToString(sealed)

	// Atomic write: create a sibling .tmp, fsync, chmod, rename. If the
	// process dies mid-write, the original session file is unchanged and
	// loadEncrypted still succeeds. `os.Rename` is atomic on POSIX and
	// atomic-enough on Windows for our purposes.
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(encoded)); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

func loadEncrypted(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sealed, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(fileKey())
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, errors.New("session file corrupt")
	}
	nonce, ct := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, []byte(aadTag))
}
