package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/valinurovaar/new-project-pulse/internal/auth"
	"github.com/valinurovaar/new-project-pulse/internal/domain"
	"github.com/valinurovaar/new-project-pulse/internal/store"
	"github.com/valinurovaar/new-project-pulse/internal/testutil"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type publicResolver struct{}

func (publicResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	return []net.IP{net.ParseIP("93.184.216.34")}, nil
}

func TestRegisterAndLogin(t *testing.T) {
	var saved domain.User
	db := &testutil.Store{}
	db.CreateUserFn = func(_ context.Context, login, hash string) (domain.User, error) {
		saved = domain.User{ID: "user-id", Login: login, PasswordHash: hash}
		return saved, nil
	}
	db.UserByLoginFn = func(context.Context, string) (domain.User, error) { return saved, nil }
	db.BalanceFn = func(context.Context, string) (store.Balance, error) {
		return store.Balance{Current: 30, Earned: 30}, nil
	}
	tokens := auth.NewManager(strings.Repeat("z", 32), time.Hour)
	h := New(db, tokens).Handler()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/users/register", bytes.NewBufferString(`{"login":"developer","password":"strong-password"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("register code=%d body=%s", w.Code, w.Body.String())
	}
	var tok map[string]string
	json.Unmarshal(w.Body.Bytes(), &tok)
	if tok["token"] == "" {
		t.Fatal("missing token")
	}
	r = httptest.NewRequest(http.MethodPost, "/api/v1/users/login", bytes.NewBufferString(`{"login":"developer","password":"strong-password"}`))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("login code=%d body=%s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodGet, "/api/v1/balance", nil)
	r.Header.Set("Authorization", "bearer "+tok["token"])
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"current":30`) {
		t.Fatalf("balance code=%d body=%s", w.Code, w.Body.String())
	}
}
func TestAuthenticationAndErrors(t *testing.T) {
	db := &testutil.Store{}
	db.UserByLoginFn = func(context.Context, string) (domain.User, error) { return domain.User{}, store.ErrNotFound }
	h := New(db, auth.NewManager(strings.Repeat("z", 32), time.Hour)).Handler()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/balance", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("code=%d content-type=%s", w.Code, w.Header().Get("Content-Type"))
	}
	r = httptest.NewRequest(http.MethodPost, "/api/v1/users/login", bytes.NewBufferString(`{"login":"nobody","password":"bad-password"}`))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("login code=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodPost, "/api/v1/users/login", bytes.NewBufferString(`{}`))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 400 {
		t.Fatalf("missing fields code=%d", w.Code)
	}
	r = httptest.NewRequest(http.MethodGet, "/missing", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 404 || !strings.Contains(w.Body.String(), "NOT_FOUND") {
		t.Fatalf("fallback code=%d body=%s", w.Code, w.Body.String())
	}
	r = httptest.NewRequest(http.MethodPut, "/api/v1/users/login", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 405 || !strings.Contains(w.Body.String(), "METHOD_NOT_ALLOWED") {
		t.Fatalf("method code=%d body=%s", w.Code, w.Body.String())
	}
	if w.Header().Get("Allow") == "" {
		t.Fatal("405 response omitted Allow header")
	}
}

func TestLoginDatabaseFailure(t *testing.T) {
	db := &testutil.Store{}
	db.UserByLoginFn = func(context.Context, string) (domain.User, error) {
		return domain.User{}, errors.New("database unavailable")
	}
	h := New(db, auth.NewManager(strings.Repeat("z", 32), time.Hour)).Handler()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/users/login", bytes.NewBufferString(`{"login":"developer","password":"strong-password"}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 500 {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}
func TestAuditInsufficientCreditsAndOwnership(t *testing.T) {
	db := &testutil.Store{}
	db.ProjectByIDFn = func(context.Context, string) (domain.Project, error) {
		return domain.Project{ID: "978b7809-1ae8-4f85-a0a9-847d76de74ae", OwnerID: "user-id", Status: domain.ProjectHealthy}, nil
	}
	db.CreateAuditFn = func(context.Context, domain.Project, domain.CheckType, int) (domain.Check, error) {
		return domain.Check{}, store.ErrInsufficientCredits
	}
	m := auth.NewManager(strings.Repeat("z", 32), time.Hour)
	token, _ := m.Issue("user-id", time.Now())
	h := New(db, m).Handler()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/projects/978b7809-1ae8-4f85-a0a9-847d76de74ae/audits", bytes.NewBufferString(`{"type":"SECURITY"}`))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 402 || !strings.Contains(w.Body.String(), "INSUFFICIENT_CREDITS") {
		t.Fatalf("code=%d body=%s", w.Code, w.Body.String())
	}
}

func TestProjectAndCheckRoutes(t *testing.T) {
	id := "978b7809-1ae8-4f85-a0a9-847d76de74ae"
	now := time.Now()
	project := domain.Project{ID: id, OwnerID: "user-id", Name: "Site", URL: "https://example.com/", Status: domain.ProjectHealthy, CreatedAt: now}
	check := domain.Check{ID: "check", ProjectID: id, Type: domain.CheckBasic, Status: domain.CheckNew, CreatedAt: now}
	db := &testutil.Store{}
	db.CreateProjectFn = func(_ context.Context, owner, name, url string) (domain.Project, error) {
		if owner != "user-id" || name != "Site" || url != "https://example.com/" {
			t.Fatalf("wrong create args")
		}
		return project, nil
	}
	db.ListProjectsFn = func(context.Context, string, store.ProjectFilter) ([]domain.Project, error) {
		return []domain.Project{project}, nil
	}
	db.ProjectByIDFn = func(context.Context, string) (domain.Project, error) { return project, nil }
	db.CreateBasicFn = func(context.Context, string) (domain.Check, bool, error) { return check, true, nil }
	db.ListChecksFn = func(context.Context, string) ([]domain.Check, error) { return []domain.Check{check}, nil }
	db.CreateAuditFn = func(context.Context, domain.Project, domain.CheckType, int) (domain.Check, error) {
		return domain.Check{ID: "audit", ProjectID: id, Type: domain.CheckPerformance, Status: domain.CheckNew, CreatedAt: now}, nil
	}
	db.ListAuditsFn = func(context.Context, string, store.CheckFilter) ([]domain.Check, error) { return []domain.Check{}, nil }
	db.TransactionsFn = func(context.Context, string, int, int) ([]domain.Transaction, error) {
		return []domain.Transaction{}, nil
	}
	m := auth.NewManager(strings.Repeat("z", 32), time.Hour)
	token, _ := m.Issue("user-id", time.Now())
	server := New(db, m)
	server.resolver = publicResolver{}
	h := server.Handler()
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("POST", "/api/v1/projects", `{"name":"Site","url":"https://Example.com"}`); w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if w := call("GET", "/api/v1/projects?status=HEALTHY&limit=10", ""); w.Code != 200 {
		t.Fatalf("list: %d", w.Code)
	}
	if w := call("GET", "/api/v1/projects/"+id, ""); w.Code != 200 {
		t.Fatalf("get: %d", w.Code)
	}
	if w := call("POST", "/api/v1/projects/"+id+"/checks", ""); w.Code != 202 {
		t.Fatalf("check: %d", w.Code)
	}
	if w := call("GET", "/api/v1/projects/"+id+"/checks", ""); w.Code != 200 {
		t.Fatalf("history: %d", w.Code)
	}
	if w := call("POST", "/api/v1/projects/"+id+"/audits", `{"type":"PERFORMANCE"}`); w.Code != 202 {
		t.Fatalf("audit: %d %s", w.Code, w.Body.String())
	}
	if w := call("POST", "/api/v1/projects/"+id+"/audits", `{}`); w.Code != 400 {
		t.Fatalf("missing audit type: %d %s", w.Code, w.Body.String())
	}
	if w := call("GET", "/api/v1/audits?type=SECURITY&status=COMPLETED", ""); w.Code != 200 {
		t.Fatalf("audits: %d", w.Code)
	}
	if w := call("GET", "/api/v1/transactions", ""); w.Code != 200 {
		t.Fatalf("transactions: %d", w.Code)
	}
	if w := call("DELETE", "/api/v1/projects/"+id, ""); w.Code != 204 {
		t.Fatalf("archive: %d", w.Code)
	}
}
func TestParameterAndOwnershipFailures(t *testing.T) {
	id := "978b7809-1ae8-4f85-a0a9-847d76de74ae"
	db := &testutil.Store{}
	db.ProjectByIDFn = func(context.Context, string) (domain.Project, error) {
		return domain.Project{ID: id, OwnerID: "someone-else"}, nil
	}
	m := auth.NewManager(strings.Repeat("z", 32), time.Hour)
	token, _ := m.Issue("user-id", time.Now())
	h := New(db, m).Handler()
	call := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	if w := call("/api/v1/projects/not-a-uuid"); w.Code != 400 {
		t.Fatalf("bad uuid=%d", w.Code)
	}
	if w := call("/api/v1/projects/" + id); w.Code != 403 {
		t.Fatalf("other owner=%d", w.Code)
	}
	if w := call("/api/v1/projects?limit=0"); w.Code != 400 {
		t.Fatalf("bad paging=%d", w.Code)
	}
	if w := call("/api/v1/projects?status=WRONG"); w.Code != 400 {
		t.Fatalf("bad status=%d", w.Code)
	}
}
