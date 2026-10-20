// Package auth implements password hashing and signed bearer tokens.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const iterations = 210000

func derive(password string, salt []byte, rounds int) []byte {
	// PBKDF2-HMAC-SHA256, implemented locally to keep authentication portable.
	out := make([]byte, 32)
	var block [4]byte
	block[3] = 1
	h := hmac.New(sha256.New, []byte(password))
	h.Write(salt)
	h.Write(block[:])
	u := h.Sum(nil)
	copy(out, u)
	for i := 1; i < rounds; i++ {
		h.Reset()
		h.Write(u)
		u = h.Sum(nil)
		for j := range out {
			out[j] ^= u[j]
		}
	}
	return out
}

// HashPassword creates a salted, iterated password verifier.
func HashPassword(password string) (string, error) {
	n := utf8.RuneCountInString(password)
	if !utf8.ValidString(password) || n < 8 || n > 1024 || len(password) > 4096 {
		return "", errors.New("password must be 8..1024 characters")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iterations, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(derive(password, salt, iterations))), nil
}

// VerifyPassword compares a password with an encoded verifier in constant time.
func VerifyPassword(encoded, password string) bool {
	p := strings.Split(encoded, "$")
	if len(p) != 4 || p[0] != "pbkdf2-sha256" {
		return false
	}
	n, e := strconv.Atoi(p[1])
	if e != nil || n < 100000 || n > 1000000 {
		return false
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(p[2])
	want, e2 := base64.RawStdEncoding.DecodeString(p[3])
	if e1 != nil || e2 != nil {
		return false
	}
	got := derive(password, salt, n)
	return len(got) == len(want) && subtle.ConstantTimeCompare(got, want) == 1
}

// Manager issues and validates signed access tokens.
type Manager struct {
	secret []byte
	ttl    time.Duration
}

// NewManager returns a token manager using an HMAC secret and token lifetime.
func NewManager(secret string, ttl time.Duration) *Manager { return &Manager{[]byte(secret), ttl} }

// Issue creates a signed token for a user.
func (m *Manager) Issue(userID string, now time.Time) (string, error) {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{"sub": userID, "iat": now.Unix(), "exp": now.Add(m.ttl).Unix()})
	p := base64.RawURLEncoding.EncodeToString(payload)
	unsigned := h + "." + p
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// Parse verifies a token and returns its user ID.
func (m *Manager) Parse(token string, now time.Time) (string, error) {
	p := strings.Split(token, ".")
	if len(p) != 3 {
		return "", errors.New("invalid token")
	}
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(p[0] + "." + p[1]))
	sig, e := base64.RawURLEncoding.DecodeString(p[2])
	if e != nil || !hmac.Equal(sig, mac.Sum(nil)) {
		return "", errors.New("invalid token")
	}
	b, e := base64.RawURLEncoding.DecodeString(p[1])
	if e != nil {
		return "", errors.New("invalid token")
	}
	var c struct {
		Sub string `json:"sub"`
		Exp int64  `json:"exp"`
	}
	if json.Unmarshal(b, &c) != nil || c.Sub == "" || now.Unix() >= c.Exp {
		return "", errors.New("expired or invalid token")
	}
	return c.Sub, nil
}
