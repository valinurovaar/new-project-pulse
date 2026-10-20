package config

import (
	"strings"
	"testing"
	"time"
)

func TestEnvironmentOverridesFlags(t *testing.T) {
	t.Setenv("RUN_ADDRESS", ":9999")
	t.Setenv("WORKER_COUNT", "4")
	t.Setenv("CHECK_INTERVAL", "2h")
	t.Setenv("AUTH_SECRET", strings.Repeat("x", 32))
	c, e := Load([]string{"-a", ":1234", "-w", "1"})
	if e != nil {
		t.Fatal(e)
	}
	if c.RunAddress != ":9999" || c.WorkerCount != 4 || c.CheckInterval != 2*time.Hour {
		t.Fatalf("unexpected config: %+v", c)
	}
}
func TestRejectsWeakSecret(t *testing.T) {
	t.Setenv("AUTH_SECRET", "")
	if _, e := Load(nil); e == nil {
		t.Fatal("weak secret accepted")
	}
}
func TestRejectsSubsecondInterval(t *testing.T) {
	t.Setenv("AUTH_SECRET", strings.Repeat("x", 32))
	t.Setenv("CHECK_INTERVAL", "500ms")
	if _, e := Load(nil); e == nil {
		t.Fatal("subsecond interval accepted")
	}
}
func TestEmptyEnvironmentStillOverridesFlag(t *testing.T) {
	t.Setenv("AUTH_SECRET", strings.Repeat("x", 32))
	t.Setenv("RUN_ADDRESS", "")
	if _, e := Load([]string{"-a", ":8080"}); e == nil {
		t.Fatal("empty environment value did not override flag")
	}
}
