package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordRoundTrip(t *testing.T) {
	h, e := HashPassword("correct horse battery staple")
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(h, "correct") {
		t.Fatal("hash contains password")
	}
	if !VerifyPassword(h, "correct horse battery staple") {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword(h, "wrong password") {
		t.Fatal("wrong password accepted")
	}
}
func TestPasswordValidation(t *testing.T) {
	if _, e := HashPassword("short"); e == nil {
		t.Fatal("short password accepted")
	}
	if VerifyPassword("broken", "anything") {
		t.Fatal("broken hash accepted")
	}
	if _, e := HashPassword("пар"); e == nil {
		t.Fatal("short Unicode password accepted by byte length")
	}
}
func TestTokenRoundTripAndTamper(t *testing.T) {
	now := time.Unix(1000, 0)
	m := NewManager(strings.Repeat("s", 32), time.Hour)
	token, e := m.Issue("user-1", now)
	if e != nil {
		t.Fatal(e)
	}
	id, e := m.Parse(token, now.Add(time.Minute))
	if e != nil || id != "user-1" {
		t.Fatalf("parse=%q,%v", id, e)
	}
	if _, e = m.Parse(token+"x", now); e == nil {
		t.Fatal("tampered token accepted")
	}
	if _, e = m.Parse(token, now.Add(2*time.Hour)); e == nil {
		t.Fatal("expired token accepted")
	}
}
