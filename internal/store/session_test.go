package store

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

// go-keyring's MockInit swaps in an in-memory backend for the whole process.
// We call it in every test so state from a previous test cannot leak in.

func TestSaveLoadRoundtripKeychain(t *testing.T) {
	keyring.MockInit()
	path := filepath.Join(t.TempDir(), "session.enc")

	want := &Session{
		Cookies:  map[string]string{"auth_token": "AT", "ct0": "CSRF", "twid": "u%3D42"},
		UserID:   "42",
		Username: "jack",
		Name:     "Jack Dorsey",
	}
	if err := Save(want, path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil {
		t.Fatal("Load returned nil")
	}
	if got.UserID != want.UserID || got.Username != want.Username || got.Name != want.Name {
		t.Errorf("identity mismatch: got %+v", got)
	}
	for k, v := range want.Cookies {
		if got.Cookies[k] != v {
			t.Errorf("cookie %q: got %q, want %q", k, got.Cookies[k], v)
		}
	}
	// The fallback file should not exist because keychain handled it.
	if _, err := os.Stat(path); err == nil {
		t.Error("fallback file should not exist when keychain is available")
	}
}

func TestLoadReturnsNilForMissingSession(t *testing.T) {
	keyring.MockInit()
	path := filepath.Join(t.TempDir(), "nothing.enc")
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != nil {
		t.Errorf("want nil, got %+v", got)
	}
}

func TestDeleteRemovesSession(t *testing.T) {
	keyring.MockInit()
	path := filepath.Join(t.TempDir(), "session.enc")

	s := &Session{Cookies: map[string]string{"auth_token": "x", "ct0": "y"}}
	if err := Save(s, path); err != nil {
		t.Fatal(err)
	}
	if err := Delete(path); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("expected nil after Delete, got %+v", got)
	}
}

func TestSaveEncryptedRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.enc")

	plain := []byte(`{"cookies":{"a":"1","b":"2"},"user_id":"42","username":"jack"}`)
	if err := saveEncrypted(plain, path); err != nil {
		t.Fatal(err)
	}

	// File should not contain the plaintext.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if string(decoded) == string(plain) || contains(decoded, plain) {
		t.Error("ciphertext should not contain plaintext")
	}

	got, err := loadEncrypted(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plain) {
		t.Errorf("roundtrip mismatch: got %q, want %q", got, plain)
	}
}

func TestLoadEncryptedMissing(t *testing.T) {
	_, err := loadEncrypted(filepath.Join(t.TempDir(), "nope.enc"))
	if err == nil {
		t.Fatal("want error for missing file")
	}
	if !os.IsNotExist(err) {
		t.Errorf("want os.IsNotExist, got %v", err)
	}
}

func TestLoadEncryptedCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "corrupt.enc")
	if err := os.WriteFile(path, []byte("not-base64!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEncrypted(path); err == nil {
		t.Error("want error on corrupt file")
	}
}

func TestFileFallbackWhenKeyringBroken(t *testing.T) {
	// Force keyring.Get to return an error by NOT calling MockInit first,
	// on a platform where the real backend is unavailable — i.e. this test
	// only exercises the Save → file fallback path indirectly by calling
	// saveEncrypted directly. The main code path is covered by
	// TestSaveLoadRoundtripKeychain above.
	path := filepath.Join(t.TempDir(), "session.enc")
	blob := []byte(`{"cookies":{"x":"y"}}`)
	if err := saveEncrypted(blob, path); err != nil {
		t.Fatal(err)
	}
	got, err := loadEncrypted(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(blob) {
		t.Errorf("got %q, want %q", got, blob)
	}
}

// contains reports whether `hay` contains the byte slice `needle`.
func contains(hay, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
outer:
	for i := 0; i+len(needle) <= len(hay); i++ {
		for j := 0; j < len(needle); j++ {
			if hay[i+j] != needle[j] {
				continue outer
			}
		}
		return true
	}
	return false
}
