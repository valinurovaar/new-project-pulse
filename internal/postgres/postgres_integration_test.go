package postgres_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valinurovaar/new-project-pulse/internal/domain"
	"github.com/valinurovaar/new-project-pulse/internal/postgres"
	"github.com/valinurovaar/new-project-pulse/internal/store"
	"github.com/valinurovaar/new-project-pulse/migrations"
)

func TestCreditLifecycleAndIdempotency(t *testing.T) {
	uri := os.Getenv("TEST_DATABASE_URI")
	if uri == "" {
		t.Skip("TEST_DATABASE_URI is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, uri); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `TRUNCATE credit_transactions,checks,projects,users CASCADE`); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	pool.Close()
	db, err := postgres.Open(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u, err := db.CreateUser(ctx, "integration-user", "password-hash")
	if err != nil {
		t.Fatal(err)
	}

	completeInitial := func(name, url string) domain.Project {
		p, err := db.CreateProject(ctx, u.ID, name, url)
		if err != nil {
			t.Fatal(err)
		}
		checks, err := db.ListProjectChecks(ctx, p.ID)
		if err != nil || len(checks) != 1 {
			t.Fatalf("initial check: %v %#v", err, checks)
		}
		if err = db.CompleteCheck(ctx, checks[0].ID, 95, []byte(`{"available":true}`)); err != nil {
			t.Fatal(err)
		}
		return p
	}
	first := completeInitial("one", "https://one.example/")
	checks, err := db.ListProjectChecks(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.CompleteCheck(ctx, checks[0].ID, 95, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	repeat, created, err := db.CreateBasicCheck(ctx, first.ID)
	if err != nil || !created {
		t.Fatalf("repeat basic: created=%v err=%v", created, err)
	}
	if err = db.CompleteCheck(ctx, repeat.ID, 95, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	for i, name := range []string{"two", "three", "four", "five"} {
		completeInitial(name, "https://"+name+".example/")
		_ = i
	}
	balance, err := db.Balance(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.Current != 50 || balance.Earned != 50 || balance.Spent != 0 {
		t.Fatalf("reward limits: %+v", balance)
	}

	audit, err := db.CreateAudit(ctx, first, domain.CheckSecurity, 20)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.FailCheck(ctx, audit.ID, "technical failure"); err != nil {
		t.Fatal(err)
	}
	if err = db.FailCheck(ctx, audit.ID, "duplicate delivery"); err != nil {
		t.Fatal(err)
	}
	balance, err = db.Balance(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.Current != 50 || balance.Earned != 50 || balance.Spent != 20 {
		t.Fatalf("refund accounting: %+v", balance)
	}
	txs, err := db.Transactions(ctx, u.ID, 50, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(txs) != 7 {
		t.Fatalf("expected 7 ledger entries, got %d", len(txs))
	}

	poor, err := db.CreateUser(ctx, "low-balance-user", "password-hash")
	if err != nil {
		t.Fatal(err)
	}
	poorProject, err := db.CreateProject(ctx, poor.ID, "poor", "https://poor.example/")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateAudit(ctx, poorProject, domain.CheckSecurity, 20); err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreateAudit(ctx, poorProject, domain.CheckConfiguration, 15); !errors.Is(err, store.ErrInsufficientCredits) {
		t.Fatalf("expected insufficient credits, got %v", err)
	}
}

func TestConcurrentCreditAndArchiveGuards(t *testing.T) {
	uri := os.Getenv("TEST_DATABASE_URI")
	if uri == "" {
		t.Skip("TEST_DATABASE_URI is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if err := migrations.Apply(ctx, uri); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `TRUNCATE credit_transactions,checks,projects,users CASCADE`); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	pool.Close()
	db, err := postgres.Open(ctx, uri)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	u, err := db.CreateUser(ctx, "concurrent-user", "hash")
	if err != nil {
		t.Fatal(err)
	}
	projects := make([]domain.Project, 0, 6)
	checks := make([]domain.Check, 0, 6)
	for i := 0; i < 6; i++ {
		p, err := db.CreateProject(ctx, u.ID, "site-"+string(rune('a'+i)), "https://site-"+string(rune('a'+i))+".example/")
		if err != nil {
			t.Fatal(err)
		}
		history, err := db.ListProjectChecks(ctx, p.ID)
		if err != nil || len(history) != 1 {
			t.Fatalf("history: %v", err)
		}
		projects = append(projects, p)
		checks = append(checks, history[0])
	}
	var wg sync.WaitGroup
	errCh := make(chan error, len(checks))
	for _, check := range checks {
		check := check
		wg.Add(1)
		go func() { defer wg.Done(); errCh <- db.CompleteCheck(ctx, check.ID, 99, []byte(`{}`)) }()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	balance, err := db.Balance(ctx, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	if balance.Current != 50 || balance.Earned != 50 {
		t.Fatalf("concurrent reward cap: %+v", balance)
	}
	audit, err := db.CreateAudit(ctx, projects[0], domain.CheckSecurity, 999)
	if err != nil {
		t.Fatal(err)
	}
	balance, err = db.Balance(ctx, u.ID)
	if err != nil || balance.Current != 30 {
		t.Fatalf("fixed audit price: %+v %v", balance, err)
	}
	if _, err = db.CreateAudit(ctx, projects[0], domain.CheckBasic, 0); err == nil {
		t.Fatal("BASIC accepted as paid audit")
	}
	errCh = make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errCh <- db.FailCheck(ctx, audit.ID, "concurrent failure") }()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	balance, err = db.Balance(ctx, u.ID)
	if err != nil || balance.Current != 50 || balance.Spent != 20 {
		t.Fatalf("single refund: %+v %v", balance, err)
	}
	for i := 0; i < 12; i++ {
		p, err := db.CreateProject(ctx, u.ID, "race-"+string(rune('a'+i)), "https://race-"+string(rune('a'+i))+".example/")
		if err != nil {
			t.Fatal(err)
		}
		history, err := db.ListProjectChecks(ctx, p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.CompleteCheck(ctx, history[0].ID, 60, []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		archiveResult := make(chan error, 1)
		checkResult := make(chan error, 1)
		go func() { <-start; archiveResult <- db.ArchiveProject(ctx, p.ID) }()
		go func() { <-start; _, _, e := db.CreateBasicCheck(ctx, p.ID); checkResult <- e }()
		close(start)
		archiveErr, checkErr := <-archiveResult, <-checkResult
		if archiveErr == nil {
			if !errors.Is(checkErr, store.ErrArchived) {
				t.Fatalf("archive won but check err=%v", checkErr)
			}
		} else if checkErr == nil {
			if !errors.Is(archiveErr, store.ErrConflict) {
				t.Fatalf("check won but archive err=%v", archiveErr)
			}
		} else {
			t.Fatalf("invalid race outcome archive=%v check=%v", archiveErr, checkErr)
		}
	}
}
