// runx-public-repo-gate: allow-file fleet_host_alias,network_topology
package fleetagent

import (
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// PKCS8PrivateKeyPEMType is the PEM block type for PKCS#8 ed25519 keys.
const PKCS8PrivateKeyPEMType = "PRIVATE KEY"

// ErrHostKeyExists is returned by EnsureHostKey when the key exists and
// Force=false.
var ErrHostKeyExists = errors.New("host key already exists")

// EnsureHostKey generates (or returns existing) ed25519 host key.
func EnsureHostKey(path string, force bool) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	if path == "" {
		return nil, nil, errors.New("host key path required")
	}

	if !force {
		if b, err := os.ReadFile(path); err == nil { //nolint:gosec // G304 operator-configured host key path
			if priv, perr := parseEd25519PrivatePEM(b); perr == nil {
				return priv, priv.Public().(ed25519.PublicKey), ErrHostKeyExists
			}
		} else if !os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("read %s: %w", path, err)
		}
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("ed25519.GenerateKey: %w", err)
	}

	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, nil, fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}

	pemBytes, err := marshalEd25519PrivatePEM(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal key: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, pemBytes, 0o600); err != nil {
		return nil, nil, fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, nil, fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}

	if !force {
		return priv, pub, ErrHostKeyExists
	}
	return priv, pub, nil
}

func parseEd25519PrivatePEM(b []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, errors.New("not a PEM block")
	}
	if block.Type != PKCS8PrivateKeyPEMType {
		return nil, fmt.Errorf("unexpected PEM type %q", block.Type)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("ParsePKCS8PrivateKey: %w", err)
	}
	priv, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("not ed25519 (got %T)", parsed)
	}
	return priv, nil
}

func marshalEd25519PrivatePEM(priv ed25519.PrivateKey) ([]byte, error) {
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("MarshalPKCS8PrivateKey: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: PKCS8PrivateKeyPEMType, Bytes: der}), nil
}

// PublicKeyFingerprint returns a short, hex-encoded fingerprint for log lines.
func PublicKeyFingerprint(pub ed25519.PublicKey) string {
	const hexChars = "0123456789abcdef"
	if len(pub) < 8 {
		return ""
	}
	out := make([]byte, 16)
	for i, b := range pub[:8] {
		out[i*2] = hexChars[b>>4]
		out[i*2+1] = hexChars[b&0x0F]
	}
	return string(out)
}

// SignWithKey wraps crypto.Signer so callers don't need to import it.
func SignWithKey(priv ed25519.PrivateKey, msg []byte) ([]byte, error) {
	signer := crypto.Signer(priv)
	return signer.Sign(rand.Reader, msg, crypto.Hash(0))
}
