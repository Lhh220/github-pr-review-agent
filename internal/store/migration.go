package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.up.sql
var migrationFS embed.FS

var migrationNamePattern = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.up\.sql$`)

type migration struct {
	Version uint64
	Name    string
	Script  string
}

type MigrationStatus struct {
	Version   uint64
	Name      string
	Applied   bool
	AppliedAt sql.NullTime
}

func Migrate(ctx context.Context, db *sql.DB) error {
	if err := ensureMigrationTable(ctx, db); err != nil {
		return err
	}

	applied, err := appliedMigrations(ctx, db)
	if err != nil {
		return err
	}
	migrations, err := loadMigrations()
	if err != nil {
		return err
	}

	if len(applied) == 0 {
		if err := upgradeLegacySchema(ctx, db); err != nil {
			return err
		}
	}

	for _, m := range migrations {
		if applied[m.Version] {
			continue
		}
		if err := execMigrationScript(ctx, db, m.Script); err != nil {
			return fmt.Errorf("apply mysql migration %d_%s: %w", m.Version, m.Name, err)
		}
		if _, err := db.ExecContext(ctx, `
INSERT INTO schema_migrations (version, name)
VALUES (?, ?)`, m.Version, m.Name); err != nil {
			return fmt.Errorf("record mysql migration %d_%s: %w", m.Version, m.Name, err)
		}
		log.Printf("applied mysql migration: version=%d name=%s", m.Version, m.Name)
	}
	return nil
}

func MigrationStatuses(ctx context.Context, db *sql.DB) ([]MigrationStatus, error) {
	if err := ensureMigrationTable(ctx, db); err != nil {
		return nil, err
	}
	applied, err := appliedMigrationRows(ctx, db)
	if err != nil {
		return nil, err
	}
	migrations, err := loadMigrations()
	if err != nil {
		return nil, err
	}

	statuses := make([]MigrationStatus, 0, len(migrations))
	for _, m := range migrations {
		row, ok := applied[m.Version]
		if !ok {
			statuses = append(statuses, MigrationStatus{Version: m.Version, Name: m.Name})
			continue
		}
		statuses = append(statuses, MigrationStatus{
			Version:   m.Version,
			Name:      m.Name,
			Applied:   true,
			AppliedAt: row.AppliedAt,
		})
	}
	return statuses, nil
}

func ensureMigrationTable(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version BIGINT UNSIGNED PRIMARY KEY,
    name VARCHAR(255) NOT NULL,
    applied_at TIMESTAMP(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci`)
	if err != nil {
		return fmt.Errorf("ensure schema_migrations table: %w", err)
	}
	return nil
}

func appliedMigrations(ctx context.Context, db *sql.DB) (map[uint64]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("list applied mysql migrations: %w", err)
	}
	defer rows.Close()

	applied := map[uint64]bool{}
	for rows.Next() {
		var version uint64
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("scan applied mysql migration: %w", err)
		}
		applied[version] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied mysql migrations: %w", err)
	}
	return applied, nil
}

func appliedMigrationRows(ctx context.Context, db *sql.DB) (map[uint64]MigrationStatus, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, name, applied_at FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("list mysql migration rows: %w", err)
	}
	defer rows.Close()

	applied := map[uint64]MigrationStatus{}
	for rows.Next() {
		var status MigrationStatus
		if err := rows.Scan(&status.Version, &status.Name, &status.AppliedAt); err != nil {
			return nil, fmt.Errorf("scan mysql migration row: %w", err)
		}
		status.Applied = true
		applied[status.Version] = status
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mysql migration rows: %w", err)
	}
	return applied, nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.Glob(migrationFS, "migrations/*.up.sql")
	if err != nil {
		return nil, fmt.Errorf("find mysql migrations: %w", err)
	}

	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		name := strings.TrimPrefix(entry, "migrations/")
		match := migrationNamePattern.FindStringSubmatch(name)
		if match == nil {
			return nil, fmt.Errorf("invalid mysql migration filename: %s", name)
		}
		version, err := strconv.ParseUint(match[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse mysql migration version %q: %w", match[1], err)
		}
		script, err := migrationFS.ReadFile(entry)
		if err != nil {
			return nil, fmt.Errorf("read mysql migration %s: %w", name, err)
		}
		migrations = append(migrations, migration{
			Version: version,
			Name:    match[2],
			Script:  string(script),
		})
	}
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].Version < migrations[j].Version
	})
	for i := 1; i < len(migrations); i++ {
		if migrations[i].Version == migrations[i-1].Version {
			return nil, fmt.Errorf("duplicate mysql migration version: %d", migrations[i].Version)
		}
	}
	return migrations, nil
}

func execMigrationScript(ctx context.Context, db *sql.DB, script string) error {
	for _, statement := range strings.Split(script, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}
