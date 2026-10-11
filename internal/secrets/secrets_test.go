package secrets

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func newKey() string {
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	return base64.StdEncoding.EncodeToString(k)
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestSealOpen(t *testing.T) {
	box, err := FromEnv(env(map[string]string{EnvKey: newKey()}))
	if err != nil || !box.Enabled() {
		t.Fatalf("box: %v", err)
	}
	id, ws := uuid.New(), uuid.New()
	s, err := box.Seal("sk-ant-secret-value", id, &ws)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(s.Ciphertext), "secret") {
		t.Fatal("ciphertext contains plaintext")
	}
	got, err := box.Open(s, id, &ws)
	if err != nil || got != "sk-ant-secret-value" {
		t.Fatalf("open = %q, %v", got, err)
	}
	// AAD binds the row: a different id or workspace must fail.
	other := uuid.New()
	if _, err := box.Open(s, other, &ws); err == nil {
		t.Fatal("opened with wrong id")
	}
	if _, err := box.Open(s, id, nil); err == nil {
		t.Fatal("opened with wrong workspace scope")
	}
	s2, _ := box.Seal("sk-ant-secret-value", id, &ws)
	if string(s2.Nonce) == string(s.Nonce) {
		t.Fatal("nonce reused")
	}
}

func TestRotation(t *testing.T) {
	oldKey, newKeyV := newKey(), newKey()
	oldBox, _ := FromEnv(env(map[string]string{EnvKey: oldKey}))
	id := uuid.New()
	sealed, _ := oldBox.Seal("v1", id, nil)

	rotated, err := FromEnv(env(map[string]string{EnvKey: newKeyV, EnvPreviousKey: oldKey}))
	if err != nil {
		t.Fatal(err)
	}
	if rotated.CurrentKeyID() == sealed.KeyID {
		t.Fatal("current key should differ after rotation")
	}
	got, err := rotated.Open(sealed, id, nil)
	if err != nil || got != "v1" {
		t.Fatalf("previous key should still open: %q %v", got, err)
	}
	newOnly, _ := FromEnv(env(map[string]string{EnvKey: newKeyV}))
	if _, err := newOnly.Open(sealed, id, nil); !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("want ErrUnknownKey, got %v", err)
	}
}

func TestNoKeyAndBadKey(t *testing.T) {
	box, err := FromEnv(env(nil))
	if err != nil || box.Enabled() {
		t.Fatal("empty env should yield a disabled box")
	}
	if _, err := box.Seal("x", uuid.New(), nil); !errors.Is(err, ErrNoKey) {
		t.Fatalf("want ErrNoKey, got %v", err)
	}
	if _, err := FromEnv(env(map[string]string{EnvKey: base64.StdEncoding.EncodeToString([]byte("short"))})); err == nil {
		t.Fatal("short key must be rejected")
	}
	if Last4("sk-ant-abcd1234") != "1234" || Last4("ab") != "••" {
		t.Fatal("Last4")
	}
}
