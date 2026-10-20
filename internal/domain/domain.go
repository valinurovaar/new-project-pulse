// Package domain contains the core ProjectPulse entities and rules.
package domain

import (
	"encoding/json"
	"time"
)

// ProjectStatus is the current aggregate health of a project.
type ProjectStatus string

const (
	ProjectUnknown  ProjectStatus = "UNKNOWN"
	ProjectHealthy  ProjectStatus = "HEALTHY"
	ProjectDegraded ProjectStatus = "DEGRADED"
	ProjectCritical ProjectStatus = "CRITICAL"
	ProjectArchived ProjectStatus = "ARCHIVED"
)

// CheckType identifies a free check or paid audit kind.
type CheckType string

const (
	CheckBasic         CheckType = "BASIC"
	CheckPerformance   CheckType = "PERFORMANCE"
	CheckSecurity      CheckType = "SECURITY"
	CheckConfiguration CheckType = "CONFIGURATION"
)

// CheckStatus is a durable inspection lifecycle state.
type CheckStatus string

const (
	CheckNew       CheckStatus = "NEW"
	CheckQueued    CheckStatus = "QUEUED"
	CheckRunning   CheckStatus = "RUNNING"
	CheckCompleted CheckStatus = "COMPLETED"
	CheckFailed    CheckStatus = "FAILED"
	CheckCancelled CheckStatus = "CANCELLED"
)

// TransactionType identifies why credits changed.
type TransactionType string

const (
	TxWelcome TransactionType = "WELCOME_BONUS"
	TxHealth  TransactionType = "HEALTH_REWARD"
	TxPayment TransactionType = "AUDIT_PAYMENT"
	TxRefund  TransactionType = "AUDIT_REFUND"
)

// User is an authenticated account with a diagnostic-credit balance.
type User struct {
	ID, Login, PasswordHash string
	CreatedAt               time.Time
	Balance                 int
}

// Project is a public web target owned by a user.
type Project struct {
	ID, OwnerID, Name, URL string
	Status                 ProjectStatus
	Score                  *int
	CreatedAt              time.Time
	LastCheckedAt          *time.Time
}

// Check is a durable local representation of an external inspection.
type Check struct {
	ID, ProjectID          string
	Type                   CheckType
	Status                 CheckStatus
	ExternalTaskID         *string
	Score                  *int
	Result                 json.RawMessage
	CreatedAt              time.Time
	StartedAt, CompletedAt *time.Time
	NextAttemptAt          time.Time
	Attempts               int
	ChargedCredits         int
	Refunded               bool
}

// Transaction is an immutable credit-ledger entry.
type Transaction struct {
	ID           string          `json:"id"`
	UserID       string          `json:"-"`
	Type         TransactionType `json:"type"`
	Amount       int             `json:"amount"`
	BalanceAfter int             `json:"balance_after"`
	Description  string          `json:"description"`
	CheckID      *string         `json:"-"`
	CreatedAt    time.Time       `json:"created_at"`
}

// AuditCost returns the credit cost and whether the type is a paid audit.
func AuditCost(t CheckType) (int, bool) {
	switch t {
	case CheckPerformance:
		return 10, true
	case CheckSecurity:
		return 20, true
	case CheckConfiguration:
		return 15, true
	}
	return 0, false
}

// StatusFromScore maps an inspector score to the configured health bands.
func StatusFromScore(score int) (ProjectStatus, bool) {
	if score < 0 || score > 100 {
		return "", false
	}
	if score >= 80 {
		return ProjectHealthy, true
	}
	if score >= 50 {
		return ProjectDegraded, true
	}
	return ProjectCritical, true
}

// IsFinal reports whether no further check transition is allowed.
func IsFinal(s CheckStatus) bool {
	return s == CheckCompleted || s == CheckFailed || s == CheckCancelled
}
