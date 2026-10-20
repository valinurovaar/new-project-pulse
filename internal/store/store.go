// Package store defines persistence operations used by HTTP and background services.
package store

import (
	"context"
	"errors"
	"github.com/valinurovaar/new-project-pulse/internal/domain"
	"time"
)

// Persistence sentinel errors are mapped to stable HTTP responses.
var (
	ErrNotFound            = errors.New("not found")
	ErrConflict            = errors.New("conflict")
	ErrArchived            = errors.New("project archived")
	ErrInsufficientCredits = errors.New("insufficient credits")
)

// ProjectFilter controls project listing and pagination.
type ProjectFilter struct {
	Status          *domain.ProjectStatus
	IncludeArchived bool
	Limit, Offset   int
}

// CheckFilter controls audit listing and pagination.
type CheckFilter struct {
	Status        *domain.CheckStatus
	Type          *domain.CheckType
	ProjectID     *string
	Limit, Offset int
}

// Balance contains the current and lifetime credit totals.
type Balance struct{ Current, Earned, Spent int }

// Store is the transactional persistence contract used by the API and workers.
type Store interface {
	CreateUser(context.Context, string, string) (domain.User, error)
	UserByLogin(context.Context, string) (domain.User, error)
	CreateProject(context.Context, string, string, string) (domain.Project, error)
	ListProjects(context.Context, string, ProjectFilter) ([]domain.Project, error)
	ProjectByID(context.Context, string) (domain.Project, error)
	ArchiveProject(context.Context, string) error
	CreateBasicCheck(context.Context, string) (domain.Check, bool, error)
	CreateAudit(context.Context, domain.Project, domain.CheckType, int) (domain.Check, error)
	ListProjectChecks(context.Context, string) ([]domain.Check, error)
	ListAudits(context.Context, string, CheckFilter) ([]domain.Check, error)
	Balance(context.Context, string) (Balance, error)
	Transactions(context.Context, string, int, int) ([]domain.Transaction, error)
	DueChecks(context.Context, int) ([]domain.Check, error)
	MarkSubmitted(context.Context, string, string, time.Time) error
	MarkQueued(context.Context, string, time.Time) error
	MarkRunning(context.Context, string, time.Time) error
	RetryLater(context.Context, string, time.Time, string) error
	FailCheck(context.Context, string, string) error
	CompleteCheck(context.Context, string, int, []byte) error
	ScheduleChecks(context.Context, time.Duration, int) (int64, error)
	Close()
}
