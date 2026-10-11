// Package secrets encrypts credentials entered in the Settings UI.
//
// Values are sealed with AES-256-GCM under a 32-byte master key from env
// (RESEARCH_AGENT_SECRETS_KEY, base64). Each value gets a random nonce, and the
// AAD binds the ciphertext to its secret id and workspace, so rows cannot be
// swapped. key_id is a fingerprint of the master key: setting
// RESEARCH_AGENT_SECRETS_KEY_PREVIOUS to the old key keeps old rows readable while
// Reencrypt moves them to the current key.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
)

// Env var names for the master keys.
const (
	EnvKey         = "RESEARCH_AGENT_SECRETS_KEY"
	EnvPreviousKey = "RESEARCH_AGENT_SECRETS_KEY_PREVIOUS"
)

var (
	// ErrNoKey means no master key is configured; UI-entered secrets are
	// unavailable (env var references still work).
	ErrNoKey = errors.New(EnvKey + " is not set; entering keys in Settings is disabled")
	// ErrUnknownKey means a row was sealed with a key that is not loaded.
	ErrUnknownKey = errors.New("secret was encrypted with a master key that is not configured")
)

// Box seals and opens secrets. The zero value (and a nil *Box) has no key.
type Box struct {
	current string
	aeads   map[string]cipher.AEAD
}

// FromEnv builds a Box from lookup (typically os.Getenv). It returns a Box
// with no key (Enabled() false) when EnvKey is unset.
func FromEnv(lookup func(string) string) (*Box, error) {
	b := &Box{aeads: map[string]cipher.AEAD{}}
	cur := strings.TrimSpace(lookup(EnvKey))
	if cur == "" {
		return b, nil
	}
	id, err := b.add(cur)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", EnvKey, err)
	}
	b.current = id
	if prev := strings.TrimSpace(lookup(EnvPreviousKey)); prev != "" {
		if _, err := b.add(prev); err != nil {
			return nil, fmt.Errorf("%s: %w", EnvPreviousKey, err)
		}
	}
	return b, nil
}

func (b *Box) add(encoded string) (string, error) {
	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", fmt.Errorf("decode base64: %w", err)
	}
	if len(key) != 32 {
		return "", fmt.Errorf("key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(key)
	id := hex.EncodeToString(sum[:4])
	b.aeads[id] = aead
	return id, nil
}

// Enabled reports whether a current master key is loaded.
func (b *Box) Enabled() bool { return b != nil && b.current != "" }

// CurrentKeyID is the fingerprint used for new seals.
func (b *Box) CurrentKeyID() string {
	if b == nil {
		return ""
	}
	return b.current
}

// Sealed is the stored form of a secret.
type Sealed struct {
	Ciphertext []byte
	Nonce      []byte
	KeyID      string
}

func aad(id uuid.UUID, workspace *uuid.UUID) []byte {
	out := append([]byte{}, id[:]...)
	if workspace != nil {
		out = append(out, workspace[:]...)
	}
	return out
}

// Seal encrypts plaintext for the secret row (id, workspace).
func (b *Box) Seal(plaintext string, id uuid.UUID, workspace *uuid.UUID) (Sealed, error) {
	if !b.Enabled() {
		return Sealed{}, ErrNoKey
	}
	aead := b.aeads[b.current]
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return Sealed{}, fmt.Errorf("read nonce: %w", err)
	}
	return Sealed{Ciphertext: aead.Seal(nil, nonce, []byte(plaintext), aad(id, workspace)), Nonce: nonce, KeyID: b.current}, nil
}

// Open decrypts a sealed secret. The plaintext must never be logged.
func (b *Box) Open(s Sealed, id uuid.UUID, workspace *uuid.UUID) (string, error) {
	if b == nil || len(b.aeads) == 0 {
		return "", ErrNoKey
	}
	aead, ok := b.aeads[s.KeyID]
	if !ok {
		return "", ErrUnknownKey
	}
	pt, err := aead.Open(nil, s.Nonce, s.Ciphertext, aad(id, workspace))
	if err != nil {
		return "", errors.New("secret could not be decrypted (wrong key or tampered row)")
	}
	return string(pt), nil
}

// Last4 returns the display suffix for a secret value.
func Last4(v string) string {
	r := []rune(strings.TrimSpace(v))
	if len(r) <= 4 {
		return strings.Repeat("•", len(r))
	}
	return string(r[len(r)-4:])
}
