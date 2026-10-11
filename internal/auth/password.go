// Package auth implements password hashing, session tokens, CSRF tokens, and
// role checks. HTTP wiring lives in internal/httpapi.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2Params are the argon2id cost parameters. Defaults follow the OWASP
// recommendation (m=64 MiB, t=3, p=2).
type Argon2Params struct {
	Memory  uint32 // KiB
	Time    uint32
	Threads uint8
	SaltLen uint32
	KeyLen  uint32
}

var DefaultArgon2 = Argon2Params{Memory: 64 * 1024, Time: 3, Threads: 2, SaltLen: 16, KeyLen: 32}

// MinPasswordLen is enforced when setting passwords.
const MinPasswordLen = 12

var ErrMismatch = errors.New("password does not match")

// HashPassword returns a PHC-format argon2id hash.
func HashPassword(password string, p Argon2Params) (string, error) {
	salt := make([]byte, p.SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("read salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLen)
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.Memory, p.Time, p.Threads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword checks password against a hash from HashPassword in
// constant time. It returns ErrMismatch for a wrong password and another
// error for a malformed hash.
func VerifyPassword(password, encoded string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return errors.New("unsupported password hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return errors.New("unsupported argon2 version")
	}
	var p Argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Time, &p.Threads); err != nil {
		return fmt.Errorf("parse argon2 params: %w", err)
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return fmt.Errorf("decode salt: %w", err)
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return fmt.Errorf("decode key: %w", err)
	}
	got := argon2.IDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, uint32(len(want))) // #nosec G115 -- key length is 32
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

// dummyHash is verified against when a login names an unknown user, so the
// response time does not reveal whether the account exists.
var dummyHash = func() string {
	h, err := HashPassword("not-a-real-password-placeholder", DefaultArgon2)
	if err != nil {
		panic(err)
	}
	return h
}()

// BurnVerify performs a verification against a dummy hash and discards the
// result. Call it on the unknown-user path of login.
func BurnVerify(password string) {
	_ = VerifyPassword(password, dummyHash)
}
