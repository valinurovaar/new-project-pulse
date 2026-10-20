// Package worker submits and polls durable inspection jobs.
package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/valinurovaar/new-project-pulse/internal/domain"
	"github.com/valinurovaar/new-project-pulse/internal/store"
	"github.com/valinurovaar/new-project-pulse/internal/urlcheck"
)

// Inspector is the timeout-bound client for the trusted diagnostic service.
type Inspector struct {
	base   *url.URL
	client *http.Client
}

// NewInspector validates the base address and creates an external client.
func NewInspector(raw string, timeout time.Duration) (*Inspector, error) {
	u, e := url.Parse(raw)
	if e != nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, errors.New("invalid inspector address")
	}
	return &Inspector{u, &http.Client{Timeout: timeout}}, nil
}

type external struct {
	TaskID string             `json:"task_id"`
	Status domain.CheckStatus `json:"status"`
	Score  *int               `json:"score"`
	Result json.RawMessage    `json:"result"`
}
type temporaryError struct {
	after time.Duration
	err   error
}

func (e temporaryError) Error() string { return e.err.Error() }
func (i *Inspector) submit(ctx context.Context, checkID, target string, t domain.CheckType) (external, error) {
	b, _ := json.Marshal(map[string]any{"target": target, "type": t})
	u := i.base.ResolveReference(&url.URL{Path: "/api/v1/inspections"})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", checkID)
	resp, e := i.client.Do(req)
	if e != nil {
		return external{}, temporaryError{time.Second * 5, e}
	}
	defer resp.Body.Close()
	if resp.StatusCode == 202 {
		var x external
		e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&x)
		if e == nil && x.TaskID == "" {
			e = errors.New("inspector omitted task_id")
		}
		if e == nil && x.Status != domain.CheckQueued {
			e = errors.New("inspector returned invalid submit status")
		}
		return x, e
	}
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		return external{}, temporaryError{retryAfter(resp.Header.Get("Retry-After")), fmt.Errorf("inspector returned %d", resp.StatusCode)}
	}
	return external{}, fmt.Errorf("inspector rejected task: %d", resp.StatusCode)
}
func (i *Inspector) poll(ctx context.Context, id string) (external, error) {
	escapedPath := "/api/v1/inspections/" + url.PathEscape(id)
	u := i.base.ResolveReference(&url.URL{Path: "/api/v1/inspections/" + id, RawPath: escapedPath})
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, e := i.client.Do(req)
	if e != nil {
		return external{}, temporaryError{time.Second * 5, e}
	}
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		var x external
		e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&x)
		if e == nil && x.TaskID != id {
			e = errors.New("inspector returned mismatched task_id")
		}
		return x, e
	}
	if resp.StatusCode == 429 || resp.StatusCode >= 500 {
		return external{}, temporaryError{retryAfter(resp.Header.Get("Retry-After")), fmt.Errorf("inspector returned %d", resp.StatusCode)}
	}
	return external{}, fmt.Errorf("poll rejected: %d", resp.StatusCode)
}
func retryAfter(v string) time.Duration {
	if n, e := strconv.Atoi(strings.TrimSpace(v)); e == nil && n > 0 {
		return time.Duration(n) * time.Second
	}
	return 5 * time.Second
}

// Pool runs durable check workers and the automatic-check scheduler.
type Pool struct {
	store         store.Store
	inspector     *Inspector
	workers       int
	checkInterval time.Duration
	log           *slog.Logger
	resolver      urlcheck.Resolver
	wg            sync.WaitGroup
}

// New creates a worker pool.
func New(s store.Store, i *Inspector, n int, interval time.Duration, log *slog.Logger) *Pool {
	return &Pool{store: s, inspector: i, workers: n, checkInterval: interval, log: log, resolver: net.DefaultResolver}
}

// Run starts workers until the context is cancelled.
func (p *Pool) Run(ctx context.Context) {
	for n := 0; n < p.workers; n++ {
		p.wg.Add(1)
		go p.loop(ctx, n)
	}
	p.wg.Add(1)
	go p.scheduler(ctx)
}

// Wait blocks until all worker goroutines have stopped.
func (p *Pool) Wait() { p.wg.Wait() }
func (p *Pool) loop(ctx context.Context, n int) {
	defer p.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cs, e := p.store.DueChecks(ctx, 1)
			if e != nil {
				p.log.Error("claim checks", "error", e, "worker", n)
				continue
			}
			for _, c := range cs {
				if e = p.process(ctx, c); e != nil {
					p.log.Error("process check", "check_id", c.ID, "error", e, "worker", n)
				}
			}
		}
	}
}
func (p *Pool) process(ctx context.Context, c domain.Check) error {
	if c.Status == domain.CheckNew {
		project, e := p.store.ProjectByID(ctx, c.ProjectID)
		if e != nil {
			return e
		}
		target, e := urlcheck.Normalize(ctx, project.URL, p.resolver)
		if e != nil {
			return p.store.FailCheck(ctx, c.ID, "target is no longer a public address: "+e.Error())
		}
		x, e := p.inspector.submit(ctx, c.ID, target, c.Type)
		if e != nil {
			return p.handleError(ctx, c, e)
		}
		return p.store.MarkSubmitted(ctx, c.ID, x.TaskID, time.Now().Add(2*time.Second))
	}
	if c.ExternalTaskID == nil {
		return p.store.FailCheck(ctx, c.ID, "external task id missing")
	}
	x, e := p.inspector.poll(ctx, *c.ExternalTaskID)
	if e != nil {
		return p.handleError(ctx, c, e)
	}
	switch x.Status {
	case domain.CheckQueued:
		return p.store.MarkQueued(ctx, c.ID, time.Now().Add(2*time.Second))
	case domain.CheckRunning:
		return p.store.MarkRunning(ctx, c.ID, time.Now().Add(2*time.Second))
	case domain.CheckCompleted:
		if x.Score == nil {
			return p.store.FailCheck(ctx, c.ID, "inspector omitted score")
		}
		if _, ok := domain.StatusFromScore(*x.Score); !ok {
			return p.store.FailCheck(ctx, c.ID, "inspector returned score outside 0..100")
		}
		var resultObject map[string]json.RawMessage
		if len(x.Result) == 0 || json.Unmarshal(x.Result, &resultObject) != nil || resultObject == nil {
			return p.store.FailCheck(ctx, c.ID, "inspector omitted or returned invalid result")
		}
		return p.store.CompleteCheck(ctx, c.ID, *x.Score, x.Result)
	case domain.CheckFailed:
		return p.store.FailCheck(ctx, c.ID, "inspector reported technical failure")
	default:
		return p.store.FailCheck(ctx, c.ID, "inspector returned unknown status")
	}
}
func (p *Pool) handleError(ctx context.Context, c domain.Check, e error) error {
	var t temporaryError
	if errors.As(e, &t) && c.Attempts < 5 {
		return p.store.RetryLater(ctx, c.ID, time.Now().Add(t.after), e.Error())
	}
	return p.store.FailCheck(ctx, c.ID, e.Error())
}
func (p *Pool) scheduler(ctx context.Context) {
	defer p.wg.Done()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		_, e := p.store.ScheduleChecks(ctx, p.checkInterval, 500)
		if e != nil {
			p.log.Error("schedule checks", "error", e)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
