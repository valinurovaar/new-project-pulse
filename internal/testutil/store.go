// Package testutil contains test doubles shared by internal package tests.
package testutil

import (
	"context"
	"github.com/valinurovaar/new-project-pulse/internal/domain"
	"github.com/valinurovaar/new-project-pulse/internal/store"
	"time"
)

type Store struct {
	CreateUserFn     func(context.Context, string, string) (domain.User, error)
	UserByLoginFn    func(context.Context, string) (domain.User, error)
	CreateProjectFn  func(context.Context, string, string, string) (domain.Project, error)
	ListProjectsFn   func(context.Context, string, store.ProjectFilter) ([]domain.Project, error)
	ProjectByIDFn    func(context.Context, string) (domain.Project, error)
	ArchiveProjectFn func(context.Context, string) error
	CreateBasicFn    func(context.Context, string) (domain.Check, bool, error)
	CreateAuditFn    func(context.Context, domain.Project, domain.CheckType, int) (domain.Check, error)
	ListChecksFn     func(context.Context, string) ([]domain.Check, error)
	ListAuditsFn     func(context.Context, string, store.CheckFilter) ([]domain.Check, error)
	BalanceFn        func(context.Context, string) (store.Balance, error)
	TransactionsFn   func(context.Context, string, int, int) ([]domain.Transaction, error)
	DueChecksFn      func(context.Context, int) ([]domain.Check, error)
	CompleteCheckFn  func(context.Context, string, int, []byte) error
	FailCheckFn      func(context.Context, string, string) error
	MarkSubmittedFn  func(context.Context, string, string, time.Time) error
	MarkQueuedFn     func(context.Context, string, time.Time) error
	MarkRunningFn    func(context.Context, string, time.Time) error
	RetryLaterFn     func(context.Context, string, time.Time, string) error
}

func (s *Store) CreateUser(c context.Context, a, b string) (domain.User, error) {
	return s.CreateUserFn(c, a, b)
}
func (s *Store) UserByLogin(c context.Context, a string) (domain.User, error) {
	return s.UserByLoginFn(c, a)
}
func (s *Store) CreateProject(c context.Context, owner, name, url string) (domain.Project, error) {
	if s.CreateProjectFn != nil {
		return s.CreateProjectFn(c, owner, name, url)
	}
	return domain.Project{}, nil
}
func (s *Store) ListProjects(c context.Context, id string, f store.ProjectFilter) ([]domain.Project, error) {
	if s.ListProjectsFn != nil {
		return s.ListProjectsFn(c, id, f)
	}
	return []domain.Project{}, nil
}
func (s *Store) ProjectByID(c context.Context, id string) (domain.Project, error) {
	if s.ProjectByIDFn != nil {
		return s.ProjectByIDFn(c, id)
	}
	return domain.Project{}, store.ErrNotFound
}
func (s *Store) ArchiveProject(c context.Context, id string) error {
	if s.ArchiveProjectFn != nil {
		return s.ArchiveProjectFn(c, id)
	}
	return nil
}
func (s *Store) CreateBasicCheck(c context.Context, id string) (domain.Check, bool, error) {
	if s.CreateBasicFn != nil {
		return s.CreateBasicFn(c, id)
	}
	return domain.Check{}, true, nil
}
func (s *Store) CreateAudit(c context.Context, p domain.Project, t domain.CheckType, n int) (domain.Check, error) {
	return s.CreateAuditFn(c, p, t, n)
}
func (s *Store) ListProjectChecks(c context.Context, id string) ([]domain.Check, error) {
	if s.ListChecksFn != nil {
		return s.ListChecksFn(c, id)
	}
	return []domain.Check{}, nil
}
func (s *Store) ListAudits(c context.Context, id string, f store.CheckFilter) ([]domain.Check, error) {
	if s.ListAuditsFn != nil {
		return s.ListAuditsFn(c, id, f)
	}
	return []domain.Check{}, nil
}
func (s *Store) Balance(c context.Context, id string) (store.Balance, error) {
	return s.BalanceFn(c, id)
}
func (s *Store) Transactions(c context.Context, id string, l, o int) ([]domain.Transaction, error) {
	if s.TransactionsFn != nil {
		return s.TransactionsFn(c, id, l, o)
	}
	return []domain.Transaction{}, nil
}
func (s *Store) DueChecks(c context.Context, n int) ([]domain.Check, error) {
	if s.DueChecksFn != nil {
		return s.DueChecksFn(c, n)
	}
	return nil, nil
}
func (s *Store) MarkSubmitted(c context.Context, a, b string, t time.Time) error {
	if s.MarkSubmittedFn != nil {
		return s.MarkSubmittedFn(c, a, b, t)
	}
	return nil
}
func (s *Store) MarkQueued(c context.Context, a string, t time.Time) error {
	if s.MarkQueuedFn != nil {
		return s.MarkQueuedFn(c, a, t)
	}
	return nil
}
func (s *Store) MarkRunning(c context.Context, a string, t time.Time) error {
	if s.MarkRunningFn != nil {
		return s.MarkRunningFn(c, a, t)
	}
	return nil
}
func (s *Store) RetryLater(c context.Context, a string, t time.Time, m string) error {
	if s.RetryLaterFn != nil {
		return s.RetryLaterFn(c, a, t, m)
	}
	return nil
}
func (s *Store) FailCheck(c context.Context, a, b string) error {
	if s.FailCheckFn != nil {
		return s.FailCheckFn(c, a, b)
	}
	return nil
}
func (s *Store) CompleteCheck(c context.Context, a string, n int, b []byte) error {
	if s.CompleteCheckFn != nil {
		return s.CompleteCheckFn(c, a, n, b)
	}
	return nil
}
func (s *Store) ScheduleChecks(context.Context, time.Duration, int) (int64, error) { return 0, nil }
func (s *Store) Close()                                                            {}
