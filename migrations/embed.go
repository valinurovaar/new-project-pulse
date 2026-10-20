// Package migrations embeds and applies the database schema.
package migrations

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed *.sql
var files embed.FS

// Apply serially applies every not-yet-recorded embedded SQL migration.
func Apply(ctx context.Context, uri string) error {
	p, e := pgxpool.New(ctx, uri)
	if e != nil {
		return e
	}
	defer p.Close()
	conn, e := p.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	if _, e = conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('projectpulse_migrations'))`); e != nil {
		return e
	}
	defer conn.Exec(context.Background(), `SELECT pg_advisory_unlock(hashtext('projectpulse_migrations'))`)
	_, e = conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations(name text PRIMARY KEY,checksum text,applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`)
	if e != nil {
		return e
	}
	if _, e = conn.Exec(ctx, `ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum text`); e != nil {
		return e
	}
	names, e := fs.Glob(files, "*.sql")
	if e != nil {
		return e
	}
	sort.Strings(names)
	for _, name := range names {
		body, e := files.ReadFile(name)
		if e != nil {
			return e
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(body, []byte("\r\n"), []byte("\n"))))
		var stored string
		e = conn.QueryRow(ctx, `SELECT COALESCE(checksum,'') FROM schema_migrations WHERE name=$1`, name).Scan(&stored)
		if e == nil {
			if stored == "" {
				if _, e = conn.Exec(ctx, `UPDATE schema_migrations SET checksum=$2 WHERE name=$1`, name, checksum); e != nil {
					return e
				}
				continue
			}
			if stored != checksum {
				return fmt.Errorf("migration %s checksum mismatch", name)
			}
			continue
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		tx, e := conn.Begin(ctx)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, string(body)); e != nil {
			tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, e)
		}
		if _, e = tx.Exec(ctx, `INSERT INTO schema_migrations(name,checksum) VALUES($1,$2)`, name, checksum); e != nil {
			tx.Rollback(ctx)
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
	}
	return nil
}
