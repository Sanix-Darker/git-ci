// Package store provides SQLite-backed persistence for git-ci.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const (
	sqliteDriver       = "sqlite"
	busyTimeoutMillis  = 5000
	maxOpenConnections = 8
)

// Store owns a pool of SQLite connections and the schema stored in it.
type Store struct {
	db *sql.DB

	closeOnce sync.Once
	closeErr  error
}

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Open opens a SQLite database at databasePath, configures connection-level
// safety settings, and applies embedded migrations in filename order.
func Open(ctx context.Context, databasePath string) (*Store, error) {
	if err := requireContext(ctx); err != nil {
		return nil, err
	}

	dsn, err := sqliteDSN(databasePath)
	if err != nil {
		return nil, err
	}

	db, err := sql.Open(sqliteDriver, dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open database: %w", err)
	}
	db.SetMaxOpenConns(maxOpenConnections)
	db.SetMaxIdleConns(maxOpenConnections)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: connect to database: %w", err)
	}
	if err := configureDatabase(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrateDatabase(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

// Close releases all database connections. It is safe to call more than once.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.closeErr = s.db.Close()
	})
	return s.closeErr
}

func requireContext(ctx context.Context) error {
	if ctx == nil {
		return invalidInput("context", "must not be nil")
	}
	return nil
}

func (s *Store) dbHandle() (*sql.DB, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store: nil Store")
	}
	return s.db, nil
}

func sqliteDSN(databasePath string) (string, error) {
	databasePath = strings.TrimSpace(databasePath)
	if databasePath == "" {
		return "", invalidInput("database path", "must not be empty")
	}

	if databasePath == ":memory:" {
		name, err := randomOpaqueID()
		if err != nil {
			return "", fmt.Errorf("store: generate in-memory database name: %w", err)
		}
		databasePath = "file:git-ci-store-" + name + "?mode=memory&cache=shared"
	}

	var databaseURL *url.URL
	if strings.HasPrefix(databasePath, "file:") {
		parsed, err := url.Parse(databasePath)
		if err != nil {
			return "", fmt.Errorf("store: parse database path: %w", err)
		}
		databaseURL = parsed
	} else {
		databaseURL = &url.URL{Scheme: "file", Path: databasePath}
	}

	query := databaseURL.Query()
	query.Del("_pragma")
	query.Add("_pragma", "journal_mode(WAL)")
	query.Add("_pragma", "foreign_keys(ON)")
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", busyTimeoutMillis))
	databaseURL.RawQuery = query.Encode()

	return databaseURL.String(), nil
}

func configureDatabase(ctx context.Context, db *sql.DB) error {
	statements := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		fmt.Sprintf("PRAGMA busy_timeout = %d", busyTimeoutMillis),
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("store: configure database: %w", err)
		}
	}
	return nil
}

type migration struct {
	version  string
	sql      string
	checksum string
}

func migrateDatabase(ctx context.Context, db *sql.DB) error {
	migrations, err := embeddedMigrations()
	if err != nil {
		return err
	}

	if _, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			checksum TEXT NOT NULL,
			applied_at INTEGER NOT NULL
		)
	`); err != nil {
		return fmt.Errorf("store: create migration table: %w", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin migration transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	rows, err := tx.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("store: read applied migrations: %w", err)
	}

	applied := make(map[string]string, len(migrations))
	for rows.Next() {
		var version, checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			_ = rows.Close()
			return fmt.Errorf("store: scan applied migration: %w", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("store: iterate applied migrations: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("store: close applied migrations: %w", err)
	}

	known := make(map[string]struct{}, len(migrations))
	for _, migration := range migrations {
		known[migration.version] = struct{}{}
	}
	for version := range applied {
		if _, ok := known[version]; !ok {
			return fmt.Errorf("store: database has unknown migration %q", version)
		}
	}

	for _, migration := range migrations {
		if checksum, ok := applied[migration.version]; ok {
			if checksum != migration.checksum {
				return fmt.Errorf("store: migration %q checksum changed", migration.version)
			}
			continue
		}

		if _, err := tx.ExecContext(ctx, migration.sql); err != nil {
			return fmt.Errorf("store: apply migration %q: %w", migration.version, err)
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO schema_migrations (version, checksum, applied_at) VALUES (?, ?, ?)`,
			migration.version,
			migration.checksum,
			nowUTC().UnixMilli(),
		); err != nil {
			return fmt.Errorf("store: record migration %q: %w", migration.version, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit migrations: %w", err)
	}
	return nil
}

func embeddedMigrations() ([]migration, error) {
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("store: read embedded migrations: %w", err)
	}

	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}

		filename := "migrations/" + entry.Name()
		contents, err := migrationFiles.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("store: read migration %q: %w", filename, err)
		}
		if strings.TrimSpace(string(contents)) == "" {
			return nil, fmt.Errorf("store: migration %q is empty", filename)
		}

		sum := sha256.Sum256(contents)
		migrations = append(migrations, migration{
			version:  strings.TrimSuffix(path.Base(entry.Name()), ".sql"),
			sql:      string(contents),
			checksum: hex.EncodeToString(sum[:]),
		})
	}

	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})
	for i := 1; i < len(migrations); i++ {
		if migrations[i-1].version == migrations[i].version {
			return nil, fmt.Errorf("store: duplicate migration version %q", migrations[i].version)
		}
	}
	return migrations, nil
}

func randomOpaqueID() (string, error) {
	var bytes [20]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(bytes[:]), nil
}

func nowUTC() time.Time {
	return time.UnixMilli(time.Now().UTC().UnixMilli()).UTC()
}

func timeFromMillis(millis int64) time.Time {
	return time.UnixMilli(millis).UTC()
}
