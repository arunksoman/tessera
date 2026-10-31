package secrets

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestSecretNeverPrints(t *testing.T) {
	s := New([]byte("hunter2"))
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("x", "password", s)
	js, err := json.Marshal(map[string]Secret{"p": s})
	if err != nil {
		t.Fatal(err)
	}
	for name, out := range map[string]string{
		"%v": fmt.Sprintf("%v", s), "%s": fmt.Sprintf("%s", s), "%+v": fmt.Sprintf("%+v", s), "%#v": fmt.Sprintf("%#v", s),
		"%q": fmt.Sprintf("%q", s), "%x": fmt.Sprintf("%x", s), "%d": fmt.Sprintf("%d", s),
		"struct": fmt.Sprintf("%+v", struct{ P Secret }{s}),
		"slog":   logged.String(), "json": string(js), "String": s.String(),
	} {
		if strings.Contains(out, "hunter2") || strings.Contains(out, "68756e746572") || strings.Contains(out, "104") {
			t.Errorf("%s leaked: %s", name, out)
		}
	}
	if string(s.Reveal()) != "hunter2" {
		t.Fatal("Reveal must return the value")
	}
}

func testKey() []byte { return bytes.Repeat([]byte{7}, KeySize) }

func TestVaultRoundTrip(t *testing.T) {
	dir := t.TempDir()
	v, err := OpenVault(dir, testKey())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := v.Get("dev/password"); err != nil || got != nil {
		t.Fatalf("missing = %v, %v", got, err)
	}
	if err := v.Set("dev/password", []byte("hunter2")); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenVault(dir, testKey())
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get("dev/password")
	if err != nil || string(got) != "hunter2" {
		t.Fatalf("got %q, %v", got, err)
	}
	if err := reopened.Delete("dev/password"); err != nil {
		t.Fatal(err)
	}
	if got, _ := reopened.Get("dev/password"); got != nil {
		t.Fatalf("after delete = %q", got)
	}
}

func TestVaultFileHoldsNoPlaintext(t *testing.T) {
	dir := t.TempDir()
	v, _ := OpenVault(dir, testKey())
	if err := v.Set("dev/password", []byte("hunter2")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, vaultFile))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("hunter2")) {
		t.Fatal("plaintext on disk")
	}
	if runtime.GOOS != "windows" {
		info, _ := os.Stat(filepath.Join(dir, vaultFile))
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("mode = %v", info.Mode())
		}
	}
}

func TestVaultRejectsWrongKeyAndTampering(t *testing.T) {
	dir := t.TempDir()
	v, _ := OpenVault(dir, testKey())
	if err := v.Set("a", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := v.Set("b", []byte("two")); err != nil {
		t.Fatal(err)
	}

	other, _ := OpenVault(dir, bytes.Repeat([]byte{8}, KeySize))
	if _, err := other.Get("a"); err == nil {
		t.Fatal("wrong key must fail")
	}

	// A blob moved to another name must fail, or one profile could read another's secret.
	path := filepath.Join(dir, vaultFile)
	raw, _ := os.ReadFile(path)
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["a"] = m["b"]
	swapped, _ := json.Marshal(m)
	if err := os.WriteFile(path, swapped, 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, _ := OpenVault(dir, testKey())
	if _, err := reopened.Get("a"); err == nil {
		t.Fatal("a swapped blob must fail")
	}
}

func TestOpenVaultRejectsBadKey(t *testing.T) {
	if _, err := OpenVault(t.TempDir(), []byte("short")); err == nil {
		t.Fatal("want an error")
	}
}

func TestMasterKeyFromKeychainIsStable(t *testing.T) {
	keyring.MockInit()
	t.Setenv(EnvKeyFile, "")
	t.Setenv(EnvPassphrase, "")
	dir := t.TempDir()
	a, err := MasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := MasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != KeySize || !bytes.Equal(a, b) {
		t.Fatalf("keys differ or wrong size: %d", len(a))
	}
}

func TestMasterKeyFromFile(t *testing.T) {
	keyring.MockInitWithError(fmt.Errorf("keychain must not be used"))
	dir := t.TempDir()
	file := filepath.Join(dir, "key")
	want := bytes.Repeat([]byte{9}, KeySize)
	if err := os.WriteFile(file, want, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvKeyFile, file)
	got, err := MasterKey(dir)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%x, %v", got, err)
	}
	if err := os.WriteFile(file, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := MasterKey(dir); err == nil {
		t.Fatal("a short key file must fail, not weaken the key")
	}
}

func TestMasterKeyFromPassphrase(t *testing.T) {
	keyring.MockInitWithError(fmt.Errorf("no keychain"))
	t.Setenv(EnvKeyFile, "")
	dir := t.TempDir()
	t.Setenv(EnvPassphrase, "correct horse")
	a, err := MasterKey(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := MasterKey(dir)
	if err != nil || !bytes.Equal(a, b) {
		t.Fatalf("same passphrase and salt must match: %v", err)
	}
	t.Setenv(EnvPassphrase, "wrong horse")
	c, err := MasterKey(dir)
	if err != nil || bytes.Equal(a, c) {
		t.Fatalf("a different passphrase must give a different key: %v", err)
	}
	other := t.TempDir()
	t.Setenv(EnvPassphrase, "correct horse")
	d, _ := MasterKey(other)
	if bytes.Equal(a, d) {
		t.Fatal("each directory needs its own random salt")
	}
}

func TestMasterKeyExplainsNoKeychain(t *testing.T) {
	keyring.MockInitWithError(fmt.Errorf("no dbus"))
	t.Setenv(EnvKeyFile, "")
	t.Setenv(EnvPassphrase, "")
	_, err := MasterKey(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), EnvKeyFile) || !strings.Contains(err.Error(), EnvPassphrase) {
		t.Fatalf("the error must name the fallbacks: %v", err)
	}
}
