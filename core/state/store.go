// Package state provides the local durable GDS state, journal, and lease store.
package state

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 8

//go:embed migrations/*.sql
var migrationFiles embed.FS

type Store struct {
	db                            *authorityDatabase
	path                          string
	readOnly                      bool
	authority                     *stateAuthority
	closeOnce                     sync.Once
	closeErr                      error
	webhookMaintenanceMu          sync.Mutex
	webhookMaintenanceAt          time.Time
	webhookMaintenanceMaxAttempts int
	beforeAuthorityCommit         func()
	now                           func() time.Time
}

// SetNow overrides the store clock. It is intended for deterministic tests;
// production callers leave the default time.Now set at construction.
func (store *Store) SetNow(now func() time.Time) {
	if now == nil {
		now = time.Now
	}
	store.now = now
}

type Info struct {
	Path          string `json:"path"`
	SchemaVersion int    `json:"schema_version"`
	JournalMode   string `json:"journal_mode"`
	ForeignKeys   bool   `json:"foreign_keys"`
	QueryOnly     bool   `json:"query_only"`
}

type Summary struct {
	Plans            int `json:"plans"`
	Operations       int `json:"operations"`
	Steps            int `json:"steps"`
	Events           int `json:"events"`
	Locks            int `json:"locks"`
	Webhooks         int `json:"webhooks"`
	Observations     int `json:"observations"`
	Reconciliations  int `json:"reconciliations"`
	RemoteRefreshes  int `json:"remote_refreshes"`
	AcceptedBundles  int `json:"accepted_bundles"`
	Rollouts         int `json:"rollouts"`
	RolloutTargets   int `json:"rollout_targets"`
	RolloutEvents    int `json:"rollout_events"`
	PlanEnablements  int `json:"plan_enablements"`
	DeviceEvidence   int `json:"device_evidence"`
	TelemetryOutbox  int `json:"telemetry_outbox"`
	TelemetryPending int `json:"telemetry_pending"`
	TelemetrySent    int `json:"telemetry_sent"`
	TelemetryDropped int `json:"telemetry_dropped"`
}

func Open(ctx context.Context, path string) (*Store, error) {
	store, err := openWritable(ctx, path)
	if err != nil {
		return nil, err
	}
	info, err := store.Info(ctx)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if info.SchemaVersion != schemaVersion {
		_ = store.Close()
		return nil, fmt.Errorf(
			"state schema version %d requires explicit migration to %d",
			info.SchemaVersion, schemaVersion,
		)
	}
	return store, nil
}

func Initialize(ctx context.Context, path string) (*Store, error) {
	absolute, err := createStatePath(path)
	if err != nil {
		return nil, err
	}
	store, err := openWritable(ctx, absolute)
	if err != nil {
		_ = os.Remove(absolute)
		return nil, err
	}
	if err := store.migrate(ctx); err != nil {
		_ = store.Close()
		_ = os.Remove(absolute)
		return nil, err
	}
	if err := store.verifyPermissions(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func openWritable(ctx context.Context, absolute string) (*Store, error) {
	authority, err := openStateAuthority(absolute)
	if err != nil {
		return nil, err
	}
	absolute = authority.path()
	database, err := sql.Open("sqlite", sqliteDSN(absolute, false))
	if err != nil {
		_ = authority.Close()
		return nil, fmt.Errorf("open state database: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	store := &Store{db: newAuthorityDatabase(database, authority), path: absolute, authority: authority, now: time.Now}
	if err := store.db.PingContext(ctx); err != nil {
		_ = database.Close()
		_ = authority.Close()
		return nil, fmt.Errorf("ping state database: %w", err)
	}
	if err := store.verifyPermissions(); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	authority, err := openStateAuthority(path)
	if err != nil {
		return nil, err
	}
	absolute := authority.path()
	database, err := sql.Open("sqlite", sqliteDSN(absolute, true))
	if err != nil {
		_ = authority.Close()
		return nil, fmt.Errorf("open read-only state database: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	store := &Store{
		db: newAuthorityDatabase(database, authority), path: absolute, readOnly: true, authority: authority,
	}
	if err := store.db.PingContext(ctx); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("ping read-only state database: %w", err)
	}
	if err := store.verifyPermissions(); err != nil {
		_ = store.Close()
		return nil, err
	}
	info, err := store.Info(ctx)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if info.SchemaVersion != schemaVersion {
		_ = store.Close()
		return nil, fmt.Errorf("unsupported state schema version %d", info.SchemaVersion)
	}
	return store, nil
}

func (store *Store) Close() error {
	if store == nil {
		return nil
	}
	store.closeOnce.Do(func() {
		var databaseErr error
		if store.db != nil {
			databaseErr = store.db.Close()
		}
		var authorityErr error
		if store.authority != nil {
			authorityErr = store.authority.Close()
		}
		store.closeErr = errors.Join(databaseErr, authorityErr)
	})
	return store.closeErr
}

func (store *Store) Path() string { return store.path }

func (store *Store) Info(ctx context.Context) (Info, error) {
	info := Info{Path: store.path}
	if err := store.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&info.SchemaVersion); err != nil {
		return Info{}, fmt.Errorf("read state schema version: %w", err)
	}
	if err := store.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&info.JournalMode); err != nil {
		return Info{}, fmt.Errorf("read journal mode: %w", err)
	}
	var foreignKeys int
	if err := store.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return Info{}, fmt.Errorf("read foreign key mode: %w", err)
	}
	info.ForeignKeys = foreignKeys == 1
	var queryOnly int
	if err := store.db.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly); err != nil {
		return Info{}, fmt.Errorf("read query-only mode: %w", err)
	}
	info.QueryOnly = queryOnly == 1
	return info, nil
}

func (store *Store) Summary(ctx context.Context) (Summary, error) {
	queries := []struct {
		name   string
		target *int
	}{
		{"plans", nil},
		{"operations", nil},
		{"operation_steps", nil},
		{"operation_events", nil},
		{"locks", nil},
		{"webhook_deliveries", nil},
		{"repository_observations", nil},
		{"reconciliation_runs", nil},
		{"remote_refreshes", nil},
		{"accepted_bundles", nil},
		{"rollouts", nil},
		{"rollout_targets", nil},
		{"rollout_events", nil},
		{"plan_enablements", nil},
		{"device_evidence", nil},
		{"telemetry_outbox", nil},
	}
	summary := Summary{}
	queries[0].target = &summary.Plans
	queries[1].target = &summary.Operations
	queries[2].target = &summary.Steps
	queries[3].target = &summary.Events
	queries[4].target = &summary.Locks
	queries[5].target = &summary.Webhooks
	queries[6].target = &summary.Observations
	queries[7].target = &summary.Reconciliations
	queries[8].target = &summary.RemoteRefreshes
	queries[9].target = &summary.AcceptedBundles
	queries[10].target = &summary.Rollouts
	queries[11].target = &summary.RolloutTargets
	queries[12].target = &summary.RolloutEvents
	queries[13].target = &summary.PlanEnablements
	queries[14].target = &summary.DeviceEvidence
	queries[15].target = &summary.TelemetryOutbox
	for _, query := range queries {
		if err := store.db.QueryRowContext(
			ctx, "SELECT COUNT(*) FROM "+quoteIdentifier(query.name),
		).Scan(query.target); err != nil {
			return Summary{}, fmt.Errorf("count state table %s: %w", query.name, err)
		}
	}
	if err := store.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(CASE WHEN status='pending' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='sent' THEN 1 ELSE 0 END),0),
		COALESCE(SUM(CASE WHEN status='dropped' THEN 1 ELSE 0 END),0)
		FROM telemetry_outbox`).Scan(&summary.TelemetryPending, &summary.TelemetrySent, &summary.TelemetryDropped); err != nil {
		return Summary{}, fmt.Errorf("summarize telemetry outbox: %w", err)
	}
	return summary, nil
}

func (store *Store) migrate(ctx context.Context) error {
	var version int
	if err := store.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return fmt.Errorf("read state schema version: %w", err)
	}
	if version > schemaVersion {
		return fmt.Errorf("state schema version %d is newer than supported %d", version, schemaVersion)
	}
	entries, err := migrationEntries()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		migrationVersion, err := migrationVersion(entry.Name())
		if err != nil {
			return err
		}
		if migrationVersion <= version {
			continue
		}
		if migrationVersion != version+1 {
			return fmt.Errorf("state migration gap from %d to %d", version, migrationVersion)
		}
		raw, err := migrationFiles.ReadFile(filepath.ToSlash(filepath.Join("migrations", entry.Name())))
		if err != nil {
			return fmt.Errorf("read state migration %s: %w", entry.Name(), err)
		}
		transaction, err := store.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin state migration: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, string(raw)); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("apply state migration %s: %w", entry.Name(), err)
		}
		if _, err := transaction.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", migrationVersion)); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("record state migration %s: %w", entry.Name(), err)
		}
		if err := store.commit(transaction); err != nil {
			return fmt.Errorf("commit state migration %s: %w", entry.Name(), err)
		}
		version = migrationVersion
	}
	if version != schemaVersion {
		return fmt.Errorf("state schema ended at %d, expected %d", version, schemaVersion)
	}
	return nil
}

func migrationEntries() ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded state migrations: %w", err)
	}
	filtered := make([]fs.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			filtered = append(filtered, entry)
		}
	}
	sort.Slice(filtered, func(left, right int) bool {
		return filtered[left].Name() < filtered[right].Name()
	})
	return filtered, nil
}

func migrationVersion(name string) (int, error) {
	version, err := strconv.Atoi(strings.SplitN(name, "-", 2)[0])
	if err != nil {
		return 0, fmt.Errorf("invalid state migration name %q", name)
	}
	return version, nil
}

func createStatePath(path string) (string, error) {
	if path == "" {
		return "", errors.New("state database path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve state path: %w", err)
	}
	parent := filepath.Dir(absolute)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", fmt.Errorf("create state directory: %w", err)
	}
	parent, err = filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve state parent: %w", err)
	}
	absolute = filepath.Join(parent, filepath.Base(absolute))
	root, _, err := openPrivateStateRoot(parent)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := root.OpenFile(filepath.Base(absolute), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err == nil {
		if closeErr := file.Close(); closeErr != nil {
			return "", closeErr
		}
		return absolute, nil
	}
	if os.IsExist(err) {
		return "", fmt.Errorf("state database already exists: %s", absolute)
	}
	return "", fmt.Errorf("create state database: %w", err)
}

func CurrentSchemaVersion() int { return schemaVersion }

func validateExistingStatePath(path string) error {
	authority, err := openStateAuthority(path)
	if err != nil {
		return err
	}
	return authority.Close()
}

func (store *Store) verifyPermissions() error {
	if store == nil || store.authority == nil {
		return errors.New("state database authority is unavailable")
	}
	return store.authority.verify()
}

func sqliteDSN(path string, readOnly bool) string {
	resource := &url.URL{Scheme: "file", Path: path}
	query := resource.Query()
	query.Add("_pragma", "busy_timeout(5000)")
	query.Add("_pragma", "foreign_keys(ON)")
	if readOnly {
		query.Set("mode", "ro")
		query.Add("_pragma", "query_only(ON)")
	} else {
		query.Add("_pragma", "journal_mode(WAL)")
		query.Add("_pragma", "synchronous(FULL)")
	}
	resource.RawQuery = query.Encode()
	return resource.String()
}

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
