// Package secrets stores credentials. A master key lives in the OS keychain, or comes
// from a key file or passphrase on headless hosts, and encrypts each secret with AES-256-GCM.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"

	"github.com/zalando/go-keyring"
	"golang.org/x/crypto/argon2"
)

const (
	KeySize = 32

	EnvKeyFile    = "TESSERA_MASTER_KEY_FILE"
	EnvPassphrase = "TESSERA_PASSPHRASE"

	vaultFile       = "secrets.json"
	saltFile        = "secrets.salt"
	keyringService  = "tessera"
	keyringAccount  = "master-key"
	redactedDisplay = "[redacted]"
)

// Secret holds a sensitive value. Every way of printing it gives "[redacted]".
type Secret struct{ value []byte }

func New(v []byte) Secret { return Secret{value: v} }

// Reveal returns the value. Call it only at the point of use.
func (s Secret) Reveal() []byte { return s.value }

func (Secret) String() string               { return redactedDisplay }
func (Secret) GoString() string             { return redactedDisplay }
func (Secret) LogValue() slog.Value         { return slog.StringValue(redactedDisplay) }
func (Secret) MarshalJSON() ([]byte, error) { return json.Marshal(redactedDisplay) }
func (Secret) MarshalText() ([]byte, error) { return []byte(redactedDisplay), nil }
func (Secret) Format(f fmt.State, _ rune)   { f.Write([]byte(redactedDisplay)) }

// MasterKey returns the key from, in order: the key file, the passphrase, the OS keychain.
// The first keychain use creates a random key.
func MasterKey(dir string) ([]byte, error) {
	if path := os.Getenv(EnvKeyFile); path != "" {
		key, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", EnvKeyFile, err)
		}
		if len(key) != KeySize {
			return nil, fmt.Errorf("%s must hold exactly %d bytes, found %d", EnvKeyFile, KeySize, len(key))
		}
		return key, nil
	}
	if pass := os.Getenv(EnvPassphrase); pass != "" {
		salt, err := loadSalt(dir)
		if err != nil {
			return nil, err
		}
		return argon2.IDKey([]byte(pass), salt, 3, 64*1024, 4, KeySize), nil
	}
	return keychainKey()
}

func keychainKey() ([]byte, error) {
	stored, err := keyring.Get(keyringService, keyringAccount)
	if err == nil {
		key, err := hex.DecodeString(stored)
		if err != nil || len(key) != KeySize {
			return nil, errors.New("the master key in the OS keychain is corrupt")
		}
		return key, nil
	}
	if !errors.Is(err, keyring.ErrNotFound) {
		return nil, noKeychain(err)
	}
	key := make([]byte, KeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	if err := keyring.Set(keyringService, keyringAccount, hex.EncodeToString(key)); err != nil {
		return nil, noKeychain(err)
	}
	return key, nil
}

func noKeychain(err error) error {
	return fmt.Errorf("OS keychain unavailable (%w). Set %s to a %d-byte key file, or %s to a passphrase",
		err, EnvKeyFile, KeySize, EnvPassphrase)
}

func loadSalt(dir string) ([]byte, error) {
	path := filepath.Join(dir, saltFile)
	salt, err := os.ReadFile(path)
	if err == nil {
		return salt, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	salt = make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	return salt, writePrivate(path, salt)
}

// Vault is an encrypted name-to-secret file.
type Vault struct {
	path string
	aead cipher.AEAD
	mu   sync.Mutex
}

func OpenVault(dir string, key []byte) (*Vault, error) {
	if len(key) != KeySize {
		return nil, fmt.Errorf("master key must be %d bytes", KeySize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Vault{path: filepath.Join(dir, vaultFile), aead: aead}, nil
}

func (v *Vault) load() (map[string]string, error) {
	raw, err := os.ReadFile(v.path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	return m, json.Unmarshal(raw, &m)
}

func (v *Vault) save(m map[string]string) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(v.path, raw)
}

// Get returns nil if the secret is not set.
func (v *Vault) Get(name string) ([]byte, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	m, err := v.load()
	if err != nil {
		return nil, err
	}
	enc, ok := m[name]
	if !ok {
		return nil, nil
	}
	blob, err := base64.StdEncoding.DecodeString(enc)
	if err != nil || len(blob) < v.aead.NonceSize() {
		return nil, fmt.Errorf("secret %q is corrupt", name)
	}
	nonce, sealed := blob[:v.aead.NonceSize()], blob[v.aead.NonceSize():]
	// The name is authenticated, so a blob moved to another name fails to open.
	plain, err := v.aead.Open(nil, nonce, sealed, []byte(name))
	if err != nil {
		return nil, fmt.Errorf("secret %q cannot be decrypted: wrong master key or a changed file", name)
	}
	return plain, nil
}

func (v *Vault) Set(name string, value []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	m, err := v.load()
	if err != nil {
		return err
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	m[name] = base64.StdEncoding.EncodeToString(v.aead.Seal(nonce, nonce, value, []byte(name)))
	return v.save(m)
}

func (v *Vault) Delete(name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	m, err := v.load()
	if err != nil {
		return err
	}
	delete(m, name)
	return v.save(m)
}

// writePrivate replaces a file atomically, readable only by the owner.
func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	// Chmod is unsupported on Windows, where ACLs inherit from the user profile.
	if runtime.GOOS != "windows" {
		if err := tmp.Chmod(0o600); err != nil {
			tmp.Close()
			return err
		}
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
