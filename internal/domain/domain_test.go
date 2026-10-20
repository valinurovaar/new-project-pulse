package domain

import "testing"

func TestStatusFromScore(t *testing.T) {
	cases := []struct {
		n  int
		s  ProjectStatus
		ok bool
	}{{100, ProjectHealthy, true}, {80, ProjectHealthy, true}, {79, ProjectDegraded, true}, {50, ProjectDegraded, true}, {49, ProjectCritical, true}, {0, ProjectCritical, true}, {-1, "", false}, {101, "", false}}
	for _, c := range cases {
		got, ok := StatusFromScore(c.n)
		if got != c.s || ok != c.ok {
			t.Fatalf("score %d: got %q,%v", c.n, got, ok)
		}
	}
}
func TestAuditCostsAndFinal(t *testing.T) {
	for typ, want := range map[CheckType]int{CheckPerformance: 10, CheckSecurity: 20, CheckConfiguration: 15} {
		got, ok := AuditCost(typ)
		if !ok || got != want {
			t.Fatalf("%s cost=%d", typ, got)
		}
	}
	if _, ok := AuditCost(CheckBasic); ok {
		t.Fatal("BASIC must be free/non-audit")
	}
	for _, s := range []CheckStatus{CheckCompleted, CheckFailed, CheckCancelled} {
		if !IsFinal(s) {
			t.Fatalf("%s must be final", s)
		}
	}
	if IsFinal(CheckRunning) {
		t.Fatal("RUNNING must not be final")
	}
}
