package plugins

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
)

// signContext separates descriptor signatures from anything else the same key signs.
const signContext = "tessera-plugin-descriptor-v1\n"

const keyHeader = "tessera-plugin-signing-key-v1 "

// Keygen returns a new public key, base64, and the private key file contents.
func Keygen() (public string, private []byte, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, err
	}
	return base64.StdEncoding.EncodeToString(pub), []byte(keyHeader + base64.StdEncoding.EncodeToString(priv.Seed()) + "\n"), nil
}

// Sign signs a descriptor with a private key file's contents and returns the .sig contents.
func Sign(private, descriptor []byte) ([]byte, error) {
	s, ok := strings.CutPrefix(strings.TrimSpace(string(private)), keyHeader)
	if !ok {
		return nil, errors.New("not a tess plugin signing key")
	}
	seed, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(seed) != ed25519.SeedSize {
		return nil, errors.New("signing key is corrupt")
	}
	sig := ed25519.Sign(ed25519.NewKeyFromSeed(seed), signedMessage(descriptor))
	return []byte(base64.StdEncoding.EncodeToString(sig) + "\n"), nil
}

// SignFile writes path.sig for the descriptor at path.
func SignFile(keyFile, path string) error {
	key, err := os.ReadFile(keyFile)
	if err != nil {
		return err
	}
	desc, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := parseDescriptor(desc); err != nil {
		return err
	}
	sig, err := Sign(key, desc)
	if err != nil {
		return err
	}
	return os.WriteFile(path+".sig", sig, 0o644)
}

func signedMessage(descriptor []byte) []byte {
	return append([]byte(signContext), descriptor...)
}

// ParseKey checks a base64 Ed25519 public key.
func ParseKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("not a base64 Ed25519 public key")
	}
	return ed25519.PublicKey(b), nil
}

// verify returns the name of the trusted key that signed descriptor. Keys are tried in name
// order so the answer is stable when two names share a key.
func verify(trusted map[string]string, descriptor, sig []byte) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(raw) != ed25519.SignatureSize {
		return "", errors.New("signature file is malformed")
	}
	names := make([]string, 0, len(trusted))
	for n := range trusted {
		names = append(names, n)
	}
	slices.Sort(names)
	msg := signedMessage(descriptor)
	for _, n := range names {
		pub, err := ParseKey(trusted[n])
		if err != nil {
			return "", fmt.Errorf("trusted key %q: %w", n, err)
		}
		if ed25519.Verify(pub, msg, raw) {
			return n, nil
		}
	}
	return "", errors.New("signature does not match any trusted key")
}
