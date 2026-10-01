package postgres

import (
	"context"
	"crypto/sha256"
	"embed"
	"fmt"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrate applies forward-only, checksummed SQL in one transaction. A
// transaction-scoped advisory lock serializes competing migrators. Existing
// migration files must not be edited after release; add a new numbered file.
func (s *Store) Migrate(ctx context.Context) error {
	return s.migrate(ctx, migrations)
}

func (s *Store) migrate(ctx context.Context, source fs.FS) error {
	names, err := fs.Glob(source, "migrations/*.sql")
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("no embedded migrations")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin migration: %w", err)
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(734192001)`); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
 name text PRIMARY KEY, checksum text NOT NULL, applied_at timestamptz NOT NULL DEFAULT clock_timestamp())`); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT name, checksum FROM schema_migrations ORDER BY name`)
	if err != nil {
		return err
	}
	applied := make([]struct{ name, checksum string }, 0)
	for rows.Next() {
		var m struct{ name, checksum string }
		if err := rows.Scan(&m.name, &m.checksum); err != nil {
			rows.Close()
			return err
		}
		applied = append(applied, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(applied) > len(names) {
		return fmt.Errorf("database has migrations unknown to this binary")
	}
	for i, name := range names {
		sql, err := fs.ReadFile(source, name)
		if err != nil {
			return err
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(sql))
		if i < len(applied) {
			if applied[i].name != name || applied[i].checksum != checksum {
				return fmt.Errorf("migration history mismatch: %s", name)
			}
			continue
		}
		if _, err = tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations (name, checksum) VALUES ($1, $2)`, name, checksum); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}
