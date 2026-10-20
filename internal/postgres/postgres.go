// Package postgres implements persistent, transactional ProjectPulse storage.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valinurovaar/new-project-pulse/internal/domain"
	"github.com/valinurovaar/new-project-pulse/internal/store"
)

// DB is a PostgreSQL implementation of store.Store.
type DB struct{ pool *pgxpool.Pool }

// Open creates and verifies a PostgreSQL connection pool.
func Open(ctx context.Context, uri string) (*DB, error) {
	p, err := pgxpool.New(ctx, uri)
	if err != nil {
		return nil, err
	}
	if err = p.Ping(ctx); err != nil {
		p.Close()
		return nil, err
	}
	return &DB{p}, nil
}

// Close releases all database connections.
func (d *DB) Close() { d.pool.Close() }
func conflict(err error) error {
	var p *pgconn.PgError
	if errors.As(err, &p) && p.Code == "23505" {
		return store.ErrConflict
	}
	return err
}

func (d *DB) CreateUser(ctx context.Context, login, hash string) (domain.User, error) {
	tx, e := d.pool.Begin(ctx)
	if e != nil {
		return domain.User{}, e
	}
	defer tx.Rollback(ctx)
	var u domain.User
	e = tx.QueryRow(ctx, `INSERT INTO users(login,password_hash,balance) VALUES($1,$2,30) RETURNING id,login,password_hash,created_at,balance`, login, hash).Scan(&u.ID, &u.Login, &u.PasswordHash, &u.CreatedAt, &u.Balance)
	if e != nil {
		return u, conflict(e)
	}
	_, e = tx.Exec(ctx, `INSERT INTO credit_transactions(user_id,type,amount,balance_after,description) VALUES($1,'WELCOME_BONUS',30,30,'Welcome bonus')`, u.ID)
	if e != nil {
		return u, e
	}
	return u, tx.Commit(ctx)
}
func (d *DB) UserByLogin(ctx context.Context, login string) (domain.User, error) {
	var u domain.User
	e := d.pool.QueryRow(ctx, `SELECT id,login,password_hash,created_at,balance FROM users WHERE login=$1`, login).Scan(&u.ID, &u.Login, &u.PasswordHash, &u.CreatedAt, &u.Balance)
	if errors.Is(e, pgx.ErrNoRows) {
		e = store.ErrNotFound
	}
	return u, e
}

const projectCols = `id,owner_id,name,url,status,score,created_at,last_checked_at`

func scanProject(r pgx.Row) (domain.Project, error) {
	var p domain.Project
	e := r.Scan(&p.ID, &p.OwnerID, &p.Name, &p.URL, &p.Status, &p.Score, &p.CreatedAt, &p.LastCheckedAt)
	return p, e
}
func (d *DB) CreateProject(ctx context.Context, owner, name, url string) (domain.Project, error) {
	tx, e := d.pool.Begin(ctx)
	if e != nil {
		return domain.Project{}, e
	}
	defer tx.Rollback(ctx)
	p, e := scanProject(tx.QueryRow(ctx, `INSERT INTO projects(owner_id,name,url,normalized_url) VALUES($1,$2,$3,$3) RETURNING `+projectCols, owner, name, url))
	if e != nil {
		return p, conflict(e)
	}
	_, e = tx.Exec(ctx, `INSERT INTO checks(project_id,type) VALUES($1,'BASIC')`, p.ID)
	if e != nil {
		return p, e
	}
	return p, tx.Commit(ctx)
}
func (d *DB) ListProjects(ctx context.Context, owner string, f store.ProjectFilter) ([]domain.Project, error) {
	q := `SELECT ` + projectCols + ` FROM projects WHERE owner_id=$1`
	args := []any{owner}
	n := 2
	if !f.IncludeArchived {
		q += ` AND status<>'ARCHIVED'`
	}
	if f.Status != nil {
		q += fmt.Sprintf(` AND status=$%d`, n)
		args = append(args, *f.Status)
		n++
	}
	q += fmt.Sprintf(` ORDER BY created_at DESC LIMIT $%d OFFSET $%d`, n, n+1)
	args = append(args, f.Limit, f.Offset)
	rows, e := d.pool.Query(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []domain.Project{}
	for rows.Next() {
		p, e := scanProject(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (d *DB) ProjectByID(ctx context.Context, id string) (domain.Project, error) {
	p, e := scanProject(d.pool.QueryRow(ctx, `SELECT `+projectCols+` FROM projects WHERE id=$1`, id))
	if errors.Is(e, pgx.ErrNoRows) {
		e = store.ErrNotFound
	}
	return p, e
}
func (d *DB) ArchiveProject(ctx context.Context, id string) error {
	tx, e := d.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var status domain.ProjectStatus
	e = tx.QueryRow(ctx, `SELECT status FROM projects WHERE id=$1 FOR UPDATE`, id).Scan(&status)
	if errors.Is(e, pgx.ErrNoRows) {
		return store.ErrNotFound
	}
	if e != nil {
		return e
	}
	var active bool
	if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM checks WHERE project_id=$1 AND status IN('NEW','QUEUED','RUNNING'))`, id).Scan(&active); e != nil {
		return e
	}
	if active {
		return store.ErrConflict
	}
	if status != domain.ProjectArchived {
		if _, e = tx.Exec(ctx, `UPDATE projects SET status='ARCHIVED' WHERE id=$1`, id); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

const checkCols = `id,project_id,type,status,external_task_id,score,result,created_at,started_at,completed_at,next_attempt_at,attempts,charged_credits,refunded`

func scanCheck(r pgx.Row) (domain.Check, error) {
	var c domain.Check
	var b []byte
	e := r.Scan(&c.ID, &c.ProjectID, &c.Type, &c.Status, &c.ExternalTaskID, &c.Score, &b, &c.CreatedAt, &c.StartedAt, &c.CompletedAt, &c.NextAttemptAt, &c.Attempts, &c.ChargedCredits, &c.Refunded)
	c.Result = b
	return c, e
}
func (d *DB) CreateBasicCheck(ctx context.Context, pid string) (domain.Check, bool, error) {
	tx, e := d.pool.Begin(ctx)
	if e != nil {
		return domain.Check{}, false, e
	}
	defer tx.Rollback(ctx)
	var status domain.ProjectStatus
	e = tx.QueryRow(ctx, `SELECT status FROM projects WHERE id=$1 FOR UPDATE`, pid).Scan(&status)
	if errors.Is(e, pgx.ErrNoRows) {
		return domain.Check{}, false, store.ErrNotFound
	}
	if e != nil {
		return domain.Check{}, false, e
	}
	if status == domain.ProjectArchived {
		return domain.Check{}, false, store.ErrArchived
	}
	c, e := scanCheck(tx.QueryRow(ctx, `SELECT `+checkCols+` FROM checks WHERE project_id=$1 AND type='BASIC' AND status IN('NEW','QUEUED','RUNNING') LIMIT 1`, pid))
	if e == nil {
		return c, false, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return c, false, e
	}
	c, e = scanCheck(tx.QueryRow(ctx, `INSERT INTO checks(project_id,type) VALUES($1,'BASIC') RETURNING `+checkCols, pid))
	if e != nil {
		return c, false, e
	}
	return c, true, tx.Commit(ctx)
}
func (d *DB) CreateAudit(ctx context.Context, p domain.Project, t domain.CheckType, cost int) (domain.Check, error) {
	expectedCost, ok := domain.AuditCost(t)
	if !ok {
		return domain.Check{}, fmt.Errorf("unsupported audit type %q", t)
	}
	cost = expectedCost
	tx, e := d.pool.Begin(ctx)
	if e != nil {
		return domain.Check{}, e
	}
	defer tx.Rollback(ctx)
	var status domain.ProjectStatus
	e = tx.QueryRow(ctx, `SELECT owner_id,name,status FROM projects WHERE id=$1 FOR UPDATE`, p.ID).Scan(&p.OwnerID, &p.Name, &status)
	if errors.Is(e, pgx.ErrNoRows) {
		return domain.Check{}, store.ErrNotFound
	}
	if e != nil {
		return domain.Check{}, e
	}
	if status == domain.ProjectArchived {
		return domain.Check{}, store.ErrArchived
	}
	c, e := scanCheck(tx.QueryRow(ctx, `INSERT INTO checks(project_id,type,charged_credits) VALUES($1,$2,$3) RETURNING `+checkCols, p.ID, t, cost))
	if e != nil {
		return c, conflict(e)
	}
	var bal int
	e = tx.QueryRow(ctx, `UPDATE users SET balance=balance-$1 WHERE id=$2 AND balance >= $1 RETURNING balance`, cost, p.OwnerID).Scan(&bal)
	if errors.Is(e, pgx.ErrNoRows) {
		return c, store.ErrInsufficientCredits
	}
	if e != nil {
		return c, e
	}
	_, e = tx.Exec(ctx, `INSERT INTO credit_transactions(user_id,type,amount,balance_after,description,check_id,project_id) VALUES($1,'AUDIT_PAYMENT',$2,$3,$4,$5,$6)`, p.OwnerID, -cost, bal, fmt.Sprintf("%s audit for %s", strings.ToLower(string(t)), p.Name), c.ID, p.ID)
	if e != nil {
		return c, e
	}
	return c, tx.Commit(ctx)
}
func rowsChecks(rows pgx.Rows) ([]domain.Check, error) {
	defer rows.Close()
	o := []domain.Check{}
	for rows.Next() {
		c, e := scanCheck(rows)
		if e != nil {
			return nil, e
		}
		o = append(o, c)
	}
	return o, rows.Err()
}
func (d *DB) ListProjectChecks(ctx context.Context, pid string) ([]domain.Check, error) {
	r, e := d.pool.Query(ctx, `SELECT `+checkCols+` FROM checks WHERE project_id=$1 ORDER BY created_at DESC`, pid)
	if e != nil {
		return nil, e
	}
	return rowsChecks(r)
}
func qualify(s string) string { return "c." + strings.ReplaceAll(s, ",", ",c.") }
func (d *DB) ListAudits(ctx context.Context, uid string, f store.CheckFilter) ([]domain.Check, error) {
	q := `SELECT ` + qualify(checkCols) + ` FROM checks c JOIN projects p ON p.id=c.project_id WHERE p.owner_id=$1 AND c.type<>'BASIC'`
	args := []any{uid}
	n := 2
	if f.Status != nil {
		q += fmt.Sprintf(` AND c.status=$%d`, n)
		args = append(args, *f.Status)
		n++
	}
	if f.Type != nil {
		q += fmt.Sprintf(` AND c.type=$%d`, n)
		args = append(args, *f.Type)
		n++
	}
	if f.ProjectID != nil {
		q += fmt.Sprintf(` AND c.project_id=$%d`, n)
		args = append(args, *f.ProjectID)
		n++
	}
	q += fmt.Sprintf(` ORDER BY c.created_at DESC LIMIT $%d OFFSET $%d`, n, n+1)
	args = append(args, f.Limit, f.Offset)
	r, e := d.pool.Query(ctx, q, args...)
	if e != nil {
		return nil, e
	}
	return rowsChecks(r)
}
func (d *DB) Balance(ctx context.Context, uid string) (store.Balance, error) {
	var b store.Balance
	e := d.pool.QueryRow(ctx, `SELECT u.balance,COALESCE(sum(t.amount) FILTER(WHERE t.type IN ('WELCOME_BONUS','HEALTH_REWARD')),0),COALESCE(-sum(t.amount) FILTER(WHERE t.type='AUDIT_PAYMENT'),0) FROM users u LEFT JOIN credit_transactions t ON t.user_id=u.id WHERE u.id=$1 GROUP BY u.id`, uid).Scan(&b.Current, &b.Earned, &b.Spent)
	return b, e
}
func (d *DB) Transactions(ctx context.Context, uid string, limit, offset int) ([]domain.Transaction, error) {
	r, e := d.pool.Query(ctx, `SELECT id,user_id,type,amount,balance_after,description,check_id,created_at FROM credit_transactions WHERE user_id=$1 ORDER BY created_at DESC LIMIT $2 OFFSET $3`, uid, limit, offset)
	if e != nil {
		return nil, e
	}
	defer r.Close()
	o := []domain.Transaction{}
	for r.Next() {
		var t domain.Transaction
		if e = r.Scan(&t.ID, &t.UserID, &t.Type, &t.Amount, &t.BalanceAfter, &t.Description, &t.CheckID, &t.CreatedAt); e != nil {
			return nil, e
		}
		o = append(o, t)
	}
	return o, r.Err()
}

func (d *DB) DueChecks(ctx context.Context, limit int) ([]domain.Check, error) {
	r, e := d.pool.Query(ctx, `WITH due AS (SELECT id FROM checks WHERE status IN('NEW','QUEUED','RUNNING') AND next_attempt_at<=now() ORDER BY next_attempt_at LIMIT $1 FOR UPDATE SKIP LOCKED) UPDATE checks c SET next_attempt_at=now()+interval '30 seconds' FROM due WHERE c.id=due.id RETURNING `+qualify(checkCols), limit)
	if e != nil {
		return nil, e
	}
	return rowsChecks(r)
}
func (d *DB) MarkSubmitted(ctx context.Context, id, external string, next time.Time) error {
	_, e := d.pool.Exec(ctx, `UPDATE checks SET status='QUEUED',external_task_id=$2,started_at=COALESCE(started_at,now()),next_attempt_at=$3,attempts=0 WHERE id=$1 AND status='NEW'`, id, external, next)
	return e
}
func (d *DB) MarkQueued(ctx context.Context, id string, next time.Time) error {
	_, e := d.pool.Exec(ctx, `UPDATE checks SET next_attempt_at=$2,attempts=0 WHERE id=$1 AND status IN('QUEUED','RUNNING')`, id, next)
	return e
}
func (d *DB) MarkRunning(ctx context.Context, id string, next time.Time) error {
	_, e := d.pool.Exec(ctx, `UPDATE checks SET status='RUNNING',next_attempt_at=$2,attempts=0 WHERE id=$1 AND status IN('QUEUED','RUNNING')`, id, next)
	return e
}
func (d *DB) RetryLater(ctx context.Context, id string, next time.Time, msg string) error {
	_, e := d.pool.Exec(ctx, `UPDATE checks SET next_attempt_at=$2,error=$3,attempts=attempts+1 WHERE id=$1 AND status IN('NEW','QUEUED','RUNNING')`, id, next, msg)
	return e
}
func (d *DB) FailCheck(ctx context.Context, id, msg string) error {
	return d.finishFailure(ctx, id, msg)
}
func (d *DB) finishFailure(ctx context.Context, id, msg string) error {
	tx, e := d.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var c domain.Check
	var uid, name string
	e = tx.QueryRow(ctx, `SELECT c.id,c.status,c.charged_credits,c.refunded,p.owner_id,p.name FROM checks c JOIN projects p ON p.id=c.project_id WHERE c.id=$1 FOR UPDATE OF c`, id).Scan(&c.ID, &c.Status, &c.ChargedCredits, &c.Refunded, &uid, &name)
	if e != nil {
		return e
	}
	if domain.IsFinal(c.Status) {
		return tx.Commit(ctx)
	}
	_, e = tx.Exec(ctx, `UPDATE checks SET status='FAILED',error=$2,completed_at=now() WHERE id=$1`, id, msg)
	if e != nil {
		return e
	}
	if c.ChargedCredits > 0 && !c.Refunded {
		var bal int
		e = tx.QueryRow(ctx, `UPDATE users SET balance=balance+$1 WHERE id=$2 RETURNING balance`, c.ChargedCredits, uid).Scan(&bal)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO credit_transactions(user_id,type,amount,balance_after,description,check_id) VALUES($1,'AUDIT_REFUND',$2,$3,$4,$5) ON CONFLICT DO NOTHING`, uid, c.ChargedCredits, bal, "Refund for failed audit of "+name, id)
		if e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE checks SET refunded=true WHERE id=$1`, id)
		if e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
func (d *DB) CompleteCheck(ctx context.Context, id string, score int, result []byte) error {
	status, ok := domain.StatusFromScore(score)
	if !ok {
		return fmt.Errorf("score outside 0..100")
	}
	tx, e := d.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var c domain.Check
	var uid, name string
	e = tx.QueryRow(ctx, `SELECT c.id,c.type,c.status,c.project_id,p.owner_id,p.name FROM checks c JOIN projects p ON p.id=c.project_id WHERE c.id=$1 FOR UPDATE OF c`, id).Scan(&c.ID, &c.Type, &c.Status, &c.ProjectID, &uid, &name)
	if e != nil {
		return e
	}
	if domain.IsFinal(c.Status) {
		return tx.Commit(ctx)
	}
	_, e = tx.Exec(ctx, `UPDATE checks SET status='COMPLETED',score=$2,result=$3,completed_at=now() WHERE id=$1`, id, score, result)
	if e != nil {
		return e
	}
	if c.Type == domain.CheckBasic {
		_, e = tx.Exec(ctx, `UPDATE projects SET status=$2,score=$3,last_checked_at=now() WHERE id=$1 AND status<>'ARCHIVED'`, c.ProjectID, status, score)
		if e != nil {
			return e
		}
	}
	if c.Type == domain.CheckBasic && status == domain.ProjectHealthy {
		var bal int
		e = tx.QueryRow(ctx, `SELECT balance FROM users WHERE id=$1 FOR UPDATE`, uid).Scan(&bal)
		if e != nil {
			return e
		}
		var earned int
		e = tx.QueryRow(ctx, `SELECT COALESCE(sum(amount),0) FROM credit_transactions WHERE user_id=$1 AND type='HEALTH_REWARD' AND reward_day=(now() AT TIME ZONE 'UTC')::date`, uid).Scan(&earned)
		if e != nil {
			return e
		}
		var exists bool
		e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM credit_transactions WHERE project_id=$1 AND type='HEALTH_REWARD' AND reward_day=(now() AT TIME ZONE 'UTC')::date)`, c.ProjectID).Scan(&exists)
		if e != nil {
			return e
		}
		if !exists && earned < 20 {
			reward := 5
			if earned+reward > 20 {
				reward = 20 - earned
			}
			bal += reward
			_, e = tx.Exec(ctx, `UPDATE users SET balance=$2 WHERE id=$1`, uid, bal)
			if e != nil {
				return e
			}
			_, e = tx.Exec(ctx, `INSERT INTO credit_transactions(user_id,type,amount,balance_after,description,check_id,project_id,reward_day) VALUES($1,'HEALTH_REWARD',$2,$3,$4,$5,$6,(now() AT TIME ZONE 'UTC')::date)`, uid, reward, bal, "Daily reward for healthy project "+name, id, c.ProjectID)
			if e != nil {
				return e
			}
		}
	}
	return tx.Commit(ctx)
}
func (d *DB) ScheduleChecks(ctx context.Context, interval time.Duration, limit int) (int64, error) {
	tag, e := d.pool.Exec(ctx, `WITH candidates AS (SELECT p.id FROM projects p WHERE p.status<>'ARCHIVED' AND NOT EXISTS(SELECT 1 FROM checks recent WHERE recent.project_id=p.id AND recent.type='BASIC' AND COALESCE(recent.completed_at,recent.created_at) >= now()-($1 * interval '1 second')) AND NOT EXISTS(SELECT 1 FROM checks active WHERE active.project_id=p.id AND active.type='BASIC' AND active.status IN('NEW','QUEUED','RUNNING')) ORDER BY p.last_checked_at NULLS FIRST LIMIT $2 FOR UPDATE OF p SKIP LOCKED) INSERT INTO checks(project_id,type) SELECT id,'BASIC' FROM candidates ON CONFLICT DO NOTHING`, int64(interval/time.Second), limit)
	return tag.RowsAffected(), e
}

var _ store.Store = (*DB)(nil)
