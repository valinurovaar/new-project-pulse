package worker

import (
	"context"
	"github.com/valinurovaar/new-project-pulse/internal/domain"
	"github.com/valinurovaar/new-project-pulse/internal/testutil"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

type publicResolver struct{}

func (publicResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	return []net.IP{net.ParseIP("8.8.8.8")}, nil
}

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func testInspector(fn roundTripFunc) *Inspector {
	u, _ := url.Parse("http://inspector.test")
	return &Inspector{base: u, client: &http.Client{Transport: fn, Timeout: time.Second}}
}
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestSubmitAndComplete(t *testing.T) {
	var completed bool
	externalID := "task-1"
	ins := testInspector(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			if r.Header.Get("Idempotency-Key") != "check-1" {
				t.Errorf("missing idempotency key")
			}
			return response(202, `{"task_id":"task-1","status":"QUEUED"}`), nil
		}
		return response(200, `{"task_id":"task-1","status":"COMPLETED","score":92,"result":{"available":true}}`), nil
	})
	db := &testutil.Store{}
	db.ProjectByIDFn = func(context.Context, string) (domain.Project, error) {
		return domain.Project{URL: "https://example.com"}, nil
	}
	db.MarkSubmittedFn = func(_ context.Context, id, ext string, _ time.Time) error {
		if id != "check-1" || ext != "task-1" {
			t.Fatal("wrong submission")
		}
		return nil
	}
	db.CompleteCheckFn = func(_ context.Context, id string, score int, result []byte) error {
		completed = id == "check-1" && score == 92 && len(result) > 0
		return nil
	}
	p := New(db, ins, 1, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.resolver = publicResolver{}
	if e := p.process(context.Background(), domain.Check{ID: "check-1", ProjectID: "project-1", Type: domain.CheckBasic, Status: domain.CheckNew}); e != nil {
		t.Fatal(e)
	}
	if e := p.process(context.Background(), domain.Check{ID: "check-1", Status: domain.CheckRunning, ExternalTaskID: &externalID}); e != nil {
		t.Fatal(e)
	}
	if !completed {
		t.Fatal("result was not completed")
	}
}
func TestTechnicalFailureRefundPath(t *testing.T) {
	ins := testInspector(func(*http.Request) (*http.Response, error) { return response(422, ""), nil })
	failed := false
	db := &testutil.Store{}
	db.ProjectByIDFn = func(context.Context, string) (domain.Project, error) {
		return domain.Project{URL: "https://example.com"}, nil
	}
	db.FailCheckFn = func(context.Context, string, string) error { failed = true; return nil }
	p := New(db, ins, 1, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.resolver = publicResolver{}
	if e := p.process(context.Background(), domain.Check{ID: "paid", ProjectID: "p", Type: domain.CheckSecurity, Status: domain.CheckNew, ChargedCredits: 20}); e != nil {
		t.Fatal(e)
	}
	if !failed {
		t.Fatal("permanent rejection must fail the audit")
	}
}

func TestPollingStateTransitions(t *testing.T) {
	status := "RUNNING"
	ins := testInspector(func(r *http.Request) (*http.Response, error) {
		if status == "RATE" {
			resp := response(429, "")
			resp.Header.Set("Retry-After", "7")
			return resp, nil
		}
		return response(200, `{"task_id":"x","status":"`+status+`"}`), nil
	})
	running, queued, failed, retried := false, false, false, false
	db := &testutil.Store{}
	db.MarkRunningFn = func(context.Context, string, time.Time) error { running = true; return nil }
	db.MarkQueuedFn = func(context.Context, string, time.Time) error { queued = true; return nil }
	db.FailCheckFn = func(context.Context, string, string) error { failed = true; return nil }
	db.RetryLaterFn = func(context.Context, string, time.Time, string) error { retried = true; return nil }
	p := New(db, ins, 1, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	id := "x"
	base := domain.Check{ID: "c", Status: domain.CheckRunning, ExternalTaskID: &id}
	if e := p.process(context.Background(), base); e != nil || !running {
		t.Fatal("RUNNING not persisted")
	}
	status = "QUEUED"
	if e := p.process(context.Background(), base); e != nil || !queued {
		t.Fatal("QUEUED not persisted")
	}
	status = "FAILED"
	if e := p.process(context.Background(), base); e != nil || !failed {
		t.Fatal("FAILED not persisted")
	}
	failed = false
	status = "COMPLETED"
	if e := p.process(context.Background(), base); e != nil || !failed {
		t.Fatal("missing score must fail")
	}
	failed = false
	status = "RATE"
	if e := p.process(context.Background(), base); e != nil || !retried {
		t.Fatal("429 must retry")
	}
	retried = false
	base.Attempts = 6
	if e := p.process(context.Background(), base); e != nil || !failed {
		t.Fatal("exhausted retries must fail")
	}
}
func TestInspectorValidation(t *testing.T) {
	if _, e := NewInspector("::bad", time.Second); e == nil {
		t.Fatal("bad URL accepted")
	}
	if _, e := NewInspector("file:///tmp/inspector", time.Second); e == nil {
		t.Fatal("non-HTTP inspector accepted")
	}
	if retryAfter("12") != 12*time.Second || retryAfter("bad") != 5*time.Second {
		t.Fatal("retry-after parsing")
	}
	ins := testInspector(func(*http.Request) (*http.Response, error) { return response(202, `{"status":"QUEUED"}`), nil })
	if _, e := ins.submit(context.Background(), "check-id", "https://example.com", domain.CheckBasic); e == nil {
		t.Fatal("missing task id accepted")
	}
}

func TestInspectorTaskIdentityAndEscaping(t *testing.T) {
	escaped := ""
	ins := testInspector(func(r *http.Request) (*http.Response, error) {
		escaped = r.URL.EscapedPath()
		return response(200, `{"task_id":"task/a","status":"RUNNING"}`), nil
	})
	if _, e := ins.poll(context.Background(), "task/a"); e != nil {
		t.Fatal(e)
	}
	if escaped != "/api/v1/inspections/task%2Fa" {
		t.Fatalf("escaped path=%q", escaped)
	}
	bad := testInspector(func(*http.Request) (*http.Response, error) {
		return response(200, `{"task_id":"another","status":"RUNNING"}`), nil
	})
	if _, e := bad.poll(context.Background(), "expected"); e == nil {
		t.Fatal("mismatched task id accepted")
	}
	badSubmit := testInspector(func(*http.Request) (*http.Response, error) {
		return response(202, `{"task_id":"x","status":"RUNNING"}`), nil
	})
	if _, e := badSubmit.submit(context.Background(), "local", "https://example.com", domain.CheckBasic); e == nil {
		t.Fatal("invalid submit status accepted")
	}
}

func TestPoolStopsAfterCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	p := New(&testutil.Store{}, testInspector(func(*http.Request) (*http.Response, error) { return response(500, ""), nil }), 2, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.Run(ctx)
	cancel()
	done := make(chan struct{})
	go func() { p.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("pool did not stop")
	}
}
