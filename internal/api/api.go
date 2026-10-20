// Package api exposes the versioned ProjectPulse HTTP API.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/valinurovaar/new-project-pulse/internal/auth"
	"github.com/valinurovaar/new-project-pulse/internal/domain"
	"github.com/valinurovaar/new-project-pulse/internal/store"
	"github.com/valinurovaar/new-project-pulse/internal/urlcheck"
)

// Server binds HTTP transport to the ProjectPulse store and token manager.
type Server struct {
	store             store.Store
	tokens            *auth.Manager
	resolver          urlcheck.Resolver
	now               func() time.Time
	dummyPasswordHash string
}

// New constructs an API server.
func New(s store.Store, t *auth.Manager) *Server {
	dummy, _ := auth.HashPassword("projectpulse-dummy-password")
	return &Server{store: s, tokens: t, resolver: net.DefaultResolver, now: time.Now, dummyPasswordHash: dummy}
}

type ctxKey struct{}
type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func fail(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var e apiError
	e.Error.Code = code
	e.Error.Message = msg
	_ = json.NewEncoder(w).Encode(e)
}
func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return errors.New("request must contain one JSON value")
	}
	return nil
}
func uid(r *http.Request) string { return r.Context().Value(ctxKey{}).(string) }

// Handler returns the complete versioned API router.
func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /api/v1/users/register", s.register)
	m.HandleFunc("POST /api/v1/users/login", s.login)
	m.Handle("POST /api/v1/projects", s.require(http.HandlerFunc(s.createProject)))
	m.Handle("GET /api/v1/projects", s.require(http.HandlerFunc(s.listProjects)))
	m.Handle("GET /api/v1/projects/{project_id}", s.require(http.HandlerFunc(s.getProject)))
	m.Handle("DELETE /api/v1/projects/{project_id}", s.require(http.HandlerFunc(s.archiveProject)))
	m.Handle("POST /api/v1/projects/{project_id}/checks", s.require(http.HandlerFunc(s.createCheck)))
	m.Handle("GET /api/v1/projects/{project_id}/checks", s.require(http.HandlerFunc(s.listChecks)))
	m.Handle("POST /api/v1/projects/{project_id}/audits", s.require(http.HandlerFunc(s.createAudit)))
	m.Handle("GET /api/v1/audits", s.require(http.HandlerFunc(s.listAudits)))
	m.Handle("GET /api/v1/balance", s.require(http.HandlerFunc(s.balance)))
	m.Handle("GET /api/v1/transactions", s.require(http.HandlerFunc(s.transactions)))
	return recoverer(jsonFallback(m))
}

type capturedResponse struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (c *capturedResponse) Header() http.Header { return c.header }
func (c *capturedResponse) WriteHeader(status int) {
	if c.status == 0 {
		c.status = status
	}
}
func (c *capturedResponse) Write(b []byte) (int, error) {
	if c.status == 0 {
		c.status = 200
	}
	return c.body.Write(b)
}
func jsonFallback(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := &capturedResponse{header: make(http.Header)}
		next.ServeHTTP(c, r)
		if c.status == 0 {
			c.status = 200
		}
		if (c.status == 404 || c.status == 405) && strings.HasPrefix(c.header.Get("Content-Type"), "text/plain") {
			code, msg := "NOT_FOUND", "Endpoint not found"
			if c.status == 405 {
				code, msg = "METHOD_NOT_ALLOWED", "Method not allowed"
				if allow := c.header.Get("Allow"); allow != "" {
					w.Header().Set("Allow", allow)
				}
			}
			fail(w, c.status, code, msg)
			return
		}
		for k, values := range c.header {
			for _, v := range values {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(c.status)
		_, _ = w.Write(c.body.Bytes())
	})
}
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recover() != nil {
				fail(w, 500, "INTERNAL_ERROR", "Internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}
func (s *Server) require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			fail(w, 401, "UNAUTHORIZED", "Authentication required")
			return
		}
		id, e := s.tokens.Parse(parts[1], s.now())
		if e != nil {
			fail(w, 401, "UNAUTHORIZED", "Invalid or expired token")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, id)))
	})
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if decode(r, &q) != nil || !validLogin(q.Login) {
		fail(w, 400, "INVALID_REQUEST", "Invalid login or request body")
		return
	}
	passwordRunes := utf8.RuneCountInString(q.Password)
	if !utf8.ValidString(q.Password) || passwordRunes < 8 || passwordRunes > 1024 || len(q.Password) > 4096 {
		fail(w, 400, "INVALID_REQUEST", "Password must be 8..1024 characters")
		return
	}
	hash, e := auth.HashPassword(q.Password)
	if e != nil {
		fail(w, 500, "INTERNAL_ERROR", "Internal server error")
		return
	}
	u, e := s.store.CreateUser(r.Context(), q.Login, hash)
	if errors.Is(e, store.ErrConflict) {
		fail(w, 409, "LOGIN_TAKEN", "Login is already taken")
		return
	}
	if e != nil {
		fail(w, 500, "INTERNAL_ERROR", "Internal server error")
		return
	}
	token, _ := s.tokens.Issue(u.ID, s.now())
	write(w, 200, map[string]string{"token": token})
}
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Login    string `json:"login"`
		Password string `json:"password"`
	}
	if decode(r, &q) != nil || !validLogin(q.Login) || len(q.Password) == 0 || !utf8.ValidString(q.Password) || utf8.RuneCountInString(q.Password) > 1024 || len(q.Password) > 4096 {
		fail(w, 400, "INVALID_REQUEST", "Invalid request body")
		return
	}
	u, e := s.store.UserByLogin(r.Context(), q.Login)
	if errors.Is(e, store.ErrNotFound) {
		_ = auth.VerifyPassword(s.dummyPasswordHash, q.Password)
		fail(w, 401, "INVALID_CREDENTIALS", "Invalid login or password")
		return
	}
	if e == nil && !auth.VerifyPassword(u.PasswordHash, q.Password) {
		fail(w, 401, "INVALID_CREDENTIALS", "Invalid login or password")
		return
	}
	if e != nil {
		fail(w, 500, "INTERNAL_ERROR", "Internal server error")
		return
	}
	token, _ := s.tokens.Issue(u.ID, s.now())
	write(w, 200, map[string]string{"token": token})
}

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-5][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

func validLogin(v string) bool {
	if !utf8.ValidString(v) || v != strings.TrimSpace(v) {
		return false
	}
	n := utf8.RuneCountInString(v)
	if n < 1 || n > 100 {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

type projectJSON struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	URL           string               `json:"url"`
	Status        domain.ProjectStatus `json:"status"`
	Score         *int                 `json:"score"`
	LastCheckedAt *time.Time           `json:"last_checked_at,omitempty"`
	CreatedAt     time.Time            `json:"created_at"`
}

func pj(p domain.Project) projectJSON {
	return projectJSON{p.ID, p.Name, p.URL, p.Status, p.Score, p.LastCheckedAt, p.CreatedAt}
}
func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	}
	if decode(r, &q) != nil || strings.TrimSpace(q.Name) == "" || len([]rune(q.Name)) > 100 {
		fail(w, 400, "INVALID_REQUEST", "Name and URL are required")
		return
	}
	u, e := urlcheck.Normalize(r.Context(), q.URL, s.resolver)
	if e != nil {
		fail(w, 422, "INVALID_URL", e.Error())
		return
	}
	p, e := s.store.CreateProject(r.Context(), uid(r), strings.TrimSpace(q.Name), u)
	if errors.Is(e, store.ErrConflict) {
		fail(w, 409, "PROJECT_EXISTS", "Project with this URL already exists")
		return
	}
	if e != nil {
		fail(w, 500, "INTERNAL_ERROR", "Internal server error")
		return
	}
	write(w, 201, pj(p))
}
func paging(r *http.Request) (int, int, error) {
	limit, offset := 50, 0
	var e error
	if v := r.URL.Query().Get("limit"); v != "" {
		limit, e = strconv.Atoi(v)
		if e != nil || limit < 1 || limit > 200 {
			return 0, 0, errors.New("invalid limit")
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		offset, e = strconv.Atoi(v)
		if e != nil || offset < 0 {
			return 0, 0, errors.New("invalid offset")
		}
	}
	return limit, offset, nil
}
func validProjectStatus(v string) bool {
	switch domain.ProjectStatus(v) {
	case domain.ProjectUnknown, domain.ProjectHealthy, domain.ProjectDegraded, domain.ProjectCritical, domain.ProjectArchived:
		return true
	}
	return false
}
func validCheckStatus(v string) bool {
	switch domain.CheckStatus(v) {
	case domain.CheckNew, domain.CheckQueued, domain.CheckRunning, domain.CheckCompleted, domain.CheckFailed, domain.CheckCancelled:
		return true
	}
	return false
}
func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	l, o, e := paging(r)
	if e != nil {
		fail(w, 400, "INVALID_PARAMETERS", e.Error())
		return
	}
	includeArchived := false
	if raw := r.URL.Query().Get("include_archived"); raw != "" {
		var parseErr error
		includeArchived, parseErr = strconv.ParseBool(raw)
		if parseErr != nil {
			fail(w, 400, "INVALID_PARAMETERS", "Invalid include_archived")
			return
		}
	}
	f := store.ProjectFilter{Limit: l, Offset: o, IncludeArchived: includeArchived}
	if v := r.URL.Query().Get("status"); v != "" {
		if !validProjectStatus(v) {
			fail(w, 400, "INVALID_PARAMETERS", "Unknown status")
			return
		}
		x := domain.ProjectStatus(v)
		f.Status = &x
		if x == domain.ProjectArchived {
			f.IncludeArchived = true
		}
	}
	ps, e := s.store.ListProjects(r.Context(), uid(r), f)
	if e != nil {
		fail(w, 500, "INTERNAL_ERROR", "Internal server error")
		return
	}
	out := make([]projectJSON, 0, len(ps))
	for _, p := range ps {
		out = append(out, pj(p))
	}
	write(w, 200, out)
}
func (s *Server) owned(r *http.Request) (domain.Project, int, error) {
	id := r.PathValue("project_id")
	if !uuidRE.MatchString(id) {
		return domain.Project{}, 400, errors.New("invalid project ID")
	}
	p, e := s.store.ProjectByID(r.Context(), id)
	if errors.Is(e, store.ErrNotFound) {
		return p, 404, e
	}
	if e != nil {
		return p, 500, e
	}
	if p.OwnerID != uid(r) {
		return p, 403, errors.New("forbidden")
	}
	return p, 0, nil
}
func ownershipFail(w http.ResponseWriter, status int) {
	switch status {
	case 400:
		fail(w, 400, "INVALID_ID", "Invalid project ID")
	case 403:
		fail(w, 403, "FORBIDDEN", "Project belongs to another user")
	case 404:
		fail(w, 404, "PROJECT_NOT_FOUND", "Project not found")
	default:
		fail(w, 500, "INTERNAL_ERROR", "Internal server error")
	}
}
func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	p, st, e := s.owned(r)
	if e != nil {
		ownershipFail(w, st)
		return
	}
	write(w, 200, pj(p))
}
func (s *Server) archiveProject(w http.ResponseWriter, r *http.Request) {
	p, st, e := s.owned(r)
	if e != nil {
		ownershipFail(w, st)
		return
	}
	e = s.store.ArchiveProject(r.Context(), p.ID)
	if errors.Is(e, store.ErrConflict) {
		fail(w, 409, "CHECK_IN_PROGRESS", "Project has an active check")
		return
	}
	if e != nil {
		fail(w, 500, "INTERNAL_ERROR", "Internal server error")
		return
	}
	w.WriteHeader(204)
}

type checkJSON struct {
	ID          string             `json:"id"`
	ProjectID   string             `json:"project_id,omitempty"`
	Type        domain.CheckType   `json:"type"`
	Status      domain.CheckStatus `json:"status"`
	Score       *int               `json:"score,omitempty"`
	Result      json.RawMessage    `json:"result,omitempty"`
	CreatedAt   time.Time          `json:"created_at"`
	CompletedAt *time.Time         `json:"completed_at,omitempty"`
}

func cj(c domain.Check) checkJSON {
	return checkJSON{c.ID, c.ProjectID, c.Type, c.Status, c.Score, c.Result, c.CreatedAt, c.CompletedAt}
}
func (s *Server) createCheck(w http.ResponseWriter, r *http.Request) {
	p, st, e := s.owned(r)
	if e != nil {
		ownershipFail(w, st)
		return
	}
	if p.Status == domain.ProjectArchived {
		fail(w, 409, "PROJECT_ARCHIVED", "Project is archived")
		return
	}
	c, created, e := s.store.CreateBasicCheck(r.Context(), p.ID)
	if errors.Is(e, store.ErrArchived) {
		fail(w, 409, "PROJECT_ARCHIVED", "Project is archived")
		return
	}
	if e != nil {
		fail(w, 500, "INTERNAL_ERROR", "Internal server error")
		return
	}
	if created {
		write(w, 202, cj(c))
	} else {
		write(w, 200, cj(c))
	}
}
func (s *Server) listChecks(w http.ResponseWriter, r *http.Request) {
	p, st, e := s.owned(r)
	if e != nil {
		ownershipFail(w, st)
		return
	}
	cs, e := s.store.ListProjectChecks(r.Context(), p.ID)
	if e != nil {
		fail(w, 500, "INTERNAL_ERROR", "Internal server error")
		return
	}
	out := make([]checkJSON, 0, len(cs))
	for _, c := range cs {
		out = append(out, cj(c))
	}
	write(w, 200, out)
}
func (s *Server) createAudit(w http.ResponseWriter, r *http.Request) {
	p, st, e := s.owned(r)
	if e != nil {
		ownershipFail(w, st)
		return
	}
	if p.Status == domain.ProjectArchived {
		fail(w, 409, "PROJECT_ARCHIVED", "Project is archived")
		return
	}
	var q struct {
		Type domain.CheckType `json:"type"`
	}
	if decode(r, &q) != nil {
		fail(w, 400, "INVALID_REQUEST", "Invalid request body")
		return
	}
	if q.Type == "" {
		fail(w, 400, "INVALID_REQUEST", "Audit type is required")
		return
	}
	cost, ok := domain.AuditCost(q.Type)
	if !ok {
		fail(w, 422, "UNKNOWN_AUDIT_TYPE", "Unknown audit type")
		return
	}
	c, e := s.store.CreateAudit(r.Context(), p, q.Type, cost)
	if errors.Is(e, store.ErrArchived) {
		fail(w, 409, "PROJECT_ARCHIVED", "Project is archived")
		return
	}
	if errors.Is(e, store.ErrInsufficientCredits) {
		fail(w, 402, "INSUFFICIENT_CREDITS", "Not enough diagnostic credits")
		return
	}
	if errors.Is(e, store.ErrConflict) {
		fail(w, 409, "AUDIT_IN_PROGRESS", "An audit of this type is already running")
		return
	}
	if e != nil {
		fail(w, 500, "INTERNAL_ERROR", "Internal server error")
		return
	}
	write(w, 202, cj(c))
}
func (s *Server) listAudits(w http.ResponseWriter, r *http.Request) {
	l, o, e := paging(r)
	if e != nil {
		fail(w, 400, "INVALID_PARAMETERS", e.Error())
		return
	}
	f := store.CheckFilter{Limit: l, Offset: o}
	if v := r.URL.Query().Get("status"); v != "" {
		if !validCheckStatus(v) {
			fail(w, 400, "INVALID_PARAMETERS", "Unknown status")
			return
		}
		x := domain.CheckStatus(v)
		f.Status = &x
	}
	if v := r.URL.Query().Get("type"); v != "" {
		x := domain.CheckType(v)
		if _, ok := domain.AuditCost(x); !ok {
			fail(w, 400, "INVALID_PARAMETERS", "Unknown audit type")
			return
		}
		f.Type = &x
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		if !uuidRE.MatchString(v) {
			fail(w, 400, "INVALID_PARAMETERS", "Invalid project ID")
			return
		}
		f.ProjectID = &v
	}
	cs, e := s.store.ListAudits(r.Context(), uid(r), f)
	if e != nil {
		fail(w, 500, "INTERNAL_ERROR", "Internal server error")
		return
	}
	out := make([]checkJSON, 0, len(cs))
	for _, c := range cs {
		out = append(out, cj(c))
	}
	write(w, 200, out)
}
func (s *Server) balance(w http.ResponseWriter, r *http.Request) {
	b, e := s.store.Balance(r.Context(), uid(r))
	if e != nil {
		fail(w, 500, "INTERNAL_ERROR", "Internal server error")
		return
	}
	write(w, 200, map[string]int{"current": b.Current, "earned": b.Earned, "spent": b.Spent})
}
func (s *Server) transactions(w http.ResponseWriter, r *http.Request) {
	l, o, e := paging(r)
	if e != nil {
		fail(w, 400, "INVALID_PARAMETERS", e.Error())
		return
	}
	ts, e := s.store.Transactions(r.Context(), uid(r), l, o)
	if e != nil {
		fail(w, 500, "INTERNAL_ERROR", "Internal server error")
		return
	}
	write(w, 200, ts)
}
