package auth

import (
	"errors"
	"strings"
	"testing"
)

var fast = Argon2Params{Memory: 1024, Time: 1, Threads: 1, SaltLen: 16, KeyLen: 32}

func TestPasswordRoundTrip(t *testing.T) {
	h, err := HashPassword("correct horse battery", fast)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=1024,t=1,p=1$") {
		t.Fatalf("unexpected hash format %q", h)
	}
	if err := VerifyPassword("correct horse battery", h); err != nil {
		t.Fatalf("verify correct: %v", err)
	}
	if err := VerifyPassword("wrong", h); !errors.Is(err, ErrMismatch) {
		t.Fatalf("verify wrong: %v", err)
	}
	h2, _ := HashPassword("correct horse battery", fast)
	if h == h2 {
		t.Fatal("hashes must be salted")
	}
}

func TestVerifyMalformed(t *testing.T) {
	for _, h := range []string{"", "plain", "$bcrypt$x$y$z$w", "$argon2id$v=18$m=1,t=1,p=1$AA$AA", "$argon2id$v=19$m=x$AA$AA"} {
		if err := VerifyPassword("x", h); err == nil || errors.Is(err, ErrMismatch) {
			t.Errorf("hash %q: want format error, got %v", h, err)
		}
	}
}

func TestTokens(t *testing.T) {
	a, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewToken()
	if a == b || len(a) != 43 {
		t.Fatalf("tokens %q %q", a, b)
	}
	again := a
	if SessionID(a) == a || len(SessionID(a)) != 64 || SessionID(a) != SessionID(again) || SessionID(a) == SessionID(b) {
		t.Fatal("session id must be a stable sha256 hex digest, not the token")
	}
	if !TokensEqual(a, a) || TokensEqual(a, b) || TokensEqual("", "") {
		t.Fatal("TokensEqual")
	}
}

func TestRoleAllows(t *testing.T) {
	tests := []struct {
		r, min Role
		want   bool
	}{
		{RoleAdmin, RoleEditor, true},
		{RoleEditor, RoleEditor, true},
		{RoleViewer, RoleEditor, false},
		{RoleEditor, RoleAdmin, false},
		{RoleViewer, RoleViewer, true},
		{Role("bogus"), RoleViewer, false},
	}
	for _, tt := range tests {
		if got := tt.r.Allows(tt.min); got != tt.want {
			t.Errorf("%s.Allows(%s) = %v", tt.r, tt.min, got)
		}
	}
}
