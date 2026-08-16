package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type LifecycleSnapshot struct {
	Path          string `json:"path"`
	SchemaVersion int    `json:"schema_version"`
	LogicalDigest string `json:"logical_digest"`
	Integrity     string `json:"integrity"`
}

type LifecycleEvidence struct {
	Action         string    `json:"action"`
	PlanDigest     string    `json:"plan_digest"`
	ApprovalDigest string    `json:"approval_digest"`
	FromVersion    int       `json:"from_version"`
	ToVersion      int       `json:"to_version"`
	BackupPath     string    `json:"backup_path,omitempty"`
	AppliedAt      time.Time `json:"applied_at"`
}

type MigrationReport struct {
	Before          LifecycleSnapshot `json:"before"`
	After           LifecycleSnapshot `json:"after"`
	Backup          LifecycleSnapshot `json:"backup"`
	BackupRawDigest string            `json:"backup_raw_digest"`
	Evidence        LifecycleEvidence `json:"evidence"`
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func Snapshot(ctx context.Context, path string) (LifecycleSnapshot, error) {
	authority, err := openStateAuthority(path)
	if err != nil {
		return LifecycleSnapshot{}, err
	}
	defer authority.Close()
	absolute := authority.path()
	database, err := sql.Open("sqlite", sqliteDSN(absolute, true))
	if err != nil {
		return LifecycleSnapshot{}, fmt.Errorf("open state snapshot: %w", err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		return LifecycleSnapshot{}, fmt.Errorf("ping state snapshot: %w", err)
	}
	if err := authority.verify(); err != nil {
		return LifecycleSnapshot{}, err
	}
	snapshot, snapshotErr := snapshotWithQueryer(ctx, absolute, database)
	return snapshot, errors.Join(snapshotErr, authority.verify())
}

func DefaultBackupPath(snapshot LifecycleSnapshot) string {
	digest := strings.TrimPrefix(snapshot.LogicalDigest, "sha256:")
	if len(digest) > 16 {
		digest = digest[:16]
	}
	return fmt.Sprintf("%s.backup-v%d-%s.db", snapshot.Path, snapshot.SchemaVersion, digest)
}

func InitializeWithEvidence(
	ctx context.Context,
	path string,
	evidence LifecycleEvidence,
) (*Store, error) {
	store, err := Initialize(ctx, path)
	if err != nil {
		return nil, err
	}
	if evidence.PlanDigest == "" {
		return store, nil
	}
	evidence.FromVersion = 0
	evidence.ToVersion = schemaVersion
	if err := store.RecordLifecycleEvidence(ctx, evidence); err != nil {
		absolute := store.Path()
		_ = store.Close()
		_ = os.Remove(absolute)
		_ = os.Remove(absolute + "-wal")
		_ = os.Remove(absolute + "-shm")
		return nil, err
	}
	return store, nil
}

func Migrate(
	ctx context.Context,
	path string,
	expected LifecycleSnapshot,
	backupPath string,
	evidence LifecycleEvidence,
) (MigrationReport, error) {
	authority, err := openStateAuthority(path)
	if err != nil {
		return MigrationReport{}, err
	}
	absolute := authority.path()
	_ = authority.Close()
	if expected.Path != absolute || expected.SchemaVersion <= 0 ||
		expected.SchemaVersion >= schemaVersion || expected.LogicalDigest == "" ||
		expected.Integrity != "pass" {
		return MigrationReport{}, errors.New("invalid expected state migration snapshot")
	}
	backupAbsolute, err := validateBackupDestination(absolute, backupPath)
	if err != nil {
		return MigrationReport{}, err
	}
	store, err := openWritable(ctx, absolute)
	if err != nil {
		return MigrationReport{}, err
	}
	defer store.Close()
	connection, err := store.db.Conn(ctx)
	if err != nil {
		return MigrationReport{}, fmt.Errorf("reserve state migration connection: %w", err)
	}
	defer connection.Close()
	if err := store.db.check(); err != nil {
		return MigrationReport{}, err
	}
	var busy, logFrames, checkpointed int
	if err := connection.QueryRowContext(
		ctx, "PRAGMA wal_checkpoint(TRUNCATE)",
	).Scan(&busy, &logFrames, &checkpointed); err != nil {
		return MigrationReport{}, fmt.Errorf("checkpoint state before migration: %w", err)
	}
	if busy != 0 {
		return MigrationReport{}, fmt.Errorf("state migration checkpoint is busy")
	}
	if _, err := connection.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		return MigrationReport{}, fmt.Errorf("begin exclusive state migration: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.WithoutCancel(ctx), "ROLLBACK")
		}
	}()
	current, err := snapshotWithQueryer(ctx, absolute, connection)
	if err != nil {
		return MigrationReport{}, err
	}
	if current.SchemaVersion != expected.SchemaVersion ||
		current.LogicalDigest != expected.LogicalDigest {
		return MigrationReport{}, ErrStateConflict
	}
	backupRawDigest, err := copyDatabaseBackup(absolute, backupAbsolute)
	if err != nil {
		return MigrationReport{}, err
	}
	backup, err := Snapshot(ctx, backupAbsolute)
	if err != nil {
		return MigrationReport{}, fmt.Errorf("verify state migration backup: %w", err)
	}
	if backup.SchemaVersion != current.SchemaVersion || backup.LogicalDigest != current.LogicalDigest {
		return MigrationReport{}, fmt.Errorf("state migration backup does not match the source snapshot")
	}
	if err := applyMigrationsOnConnection(ctx, connection, current.SchemaVersion); err != nil {
		return MigrationReport{}, err
	}
	evidence.FromVersion = current.SchemaVersion
	evidence.ToVersion = schemaVersion
	evidence.BackupPath = backupAbsolute
	if err := recordLifecycleEvidence(ctx, connection, evidence); err != nil {
		return MigrationReport{}, err
	}
	if err := store.db.check(); err != nil {
		return MigrationReport{}, err
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return MigrationReport{}, fmt.Errorf("commit state migration: %w", err)
	}
	if err := store.db.check(); err != nil {
		return MigrationReport{}, err
	}
	committed = true
	after, err := Snapshot(ctx, absolute)
	if err != nil {
		return MigrationReport{}, fmt.Errorf("verify migrated state: %w", err)
	}
	if after.SchemaVersion != schemaVersion || after.Integrity != "pass" {
		return MigrationReport{}, fmt.Errorf("migrated state failed target verification")
	}
	return MigrationReport{
		Before: current, After: after, Backup: backup,
		BackupRawDigest: backupRawDigest, Evidence: evidence,
	}, nil
}

func (store *Store) RecordLifecycleEvidence(
	ctx context.Context,
	evidence LifecycleEvidence,
) error {
	if store.readOnly {
		return ErrReadOnly
	}
	return recordLifecycleEvidence(ctx, store.db, evidence)
}

func (store *Store) LifecycleEvidence(ctx context.Context) (LifecycleEvidence, error) {
	var raw string
	err := store.db.QueryRowContext(
		ctx, `SELECT value FROM metadata WHERE key = 'state-lifecycle:last'`,
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return LifecycleEvidence{}, ErrNotFound
	}
	if err != nil {
		return LifecycleEvidence{}, fmt.Errorf("load state lifecycle evidence: %w", err)
	}
	var evidence LifecycleEvidence
	if err := json.Unmarshal([]byte(raw), &evidence); err != nil {
		return LifecycleEvidence{}, fmt.Errorf("decode state lifecycle evidence: %w", err)
	}
	return evidence, nil
}

func recordLifecycleEvidence(
	ctx context.Context,
	executor interface {
		ExecContext(context.Context, string, ...any) (sql.Result, error)
	},
	evidence LifecycleEvidence,
) error {
	if evidence.Action == "" || evidence.PlanDigest == "" || evidence.ApprovalDigest == "" ||
		evidence.ToVersion != schemaVersion || evidence.AppliedAt.IsZero() {
		return errors.New("invalid state lifecycle evidence")
	}
	raw, err := json.Marshal(evidence)
	if err != nil {
		return fmt.Errorf("encode state lifecycle evidence: %w", err)
	}
	if _, err := executor.ExecContext(
		ctx,
		`INSERT INTO metadata(key, value, updated_at)
		 VALUES ('state-lifecycle:last', ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		string(raw), formatTime(evidence.AppliedAt),
	); err != nil {
		return fmt.Errorf("record state lifecycle evidence: %w", err)
	}
	return nil
}

func snapshotWithQueryer(
	ctx context.Context,
	path string,
	reader queryer,
) (LifecycleSnapshot, error) {
	var version int
	if err := reader.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return LifecycleSnapshot{}, fmt.Errorf("read state schema version: %w", err)
	}
	rows, err := reader.QueryContext(ctx, "PRAGMA quick_check")
	if err != nil {
		return LifecycleSnapshot{}, fmt.Errorf("check state integrity: %w", err)
	}
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			rows.Close()
			return LifecycleSnapshot{}, fmt.Errorf("decode state integrity result: %w", err)
		}
		if result != "ok" {
			rows.Close()
			return LifecycleSnapshot{}, fmt.Errorf("state integrity failed: %s", result)
		}
	}
	if err := rows.Close(); err != nil {
		return LifecycleSnapshot{}, err
	}
	foreignKeyRows, err := reader.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return LifecycleSnapshot{}, fmt.Errorf("check state foreign keys: %w", err)
	}
	if foreignKeyRows.Next() {
		foreignKeyRows.Close()
		return LifecycleSnapshot{}, fmt.Errorf("state foreign key integrity failed")
	}
	if err := foreignKeyRows.Err(); err != nil {
		foreignKeyRows.Close()
		return LifecycleSnapshot{}, fmt.Errorf("inspect state foreign key integrity: %w", err)
	}
	if err := foreignKeyRows.Close(); err != nil {
		return LifecycleSnapshot{}, err
	}
	digest, err := logicalDatabaseDigest(ctx, reader)
	if err != nil {
		return LifecycleSnapshot{}, err
	}
	return LifecycleSnapshot{
		Path: path, SchemaVersion: version, LogicalDigest: digest, Integrity: "pass",
	}, nil
}

func logicalDatabaseDigest(ctx context.Context, reader queryer) (string, error) {
	hasher := sha256.New()
	schemaRows, err := reader.QueryContext(
		ctx,
		`SELECT type, name, COALESCE(sql, '') FROM sqlite_schema
		 WHERE name NOT LIKE 'sqlite_%' OR name = 'sqlite_sequence'
		 ORDER BY type, name`,
	)
	if err != nil {
		return "", fmt.Errorf("read state schema for digest: %w", err)
	}
	tables := []string{}
	for schemaRows.Next() {
		var kind, name, statement string
		if err := schemaRows.Scan(&kind, &name, &statement); err != nil {
			schemaRows.Close()
			return "", fmt.Errorf("decode state schema for digest: %w", err)
		}
		writeDigestValue(hasher, kind)
		writeDigestValue(hasher, name)
		writeDigestValue(hasher, statement)
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	if err := schemaRows.Close(); err != nil {
		return "", err
	}
	sort.Strings(tables)
	for _, table := range tables {
		if err := digestTable(ctx, reader, hasher, table); err != nil {
			return "", err
		}
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func digestTable(ctx context.Context, reader queryer, hasher hash.Hash, table string) error {
	pragma := "PRAGMA table_info(" + quoteIdentifier(table) + ")"
	rows, err := reader.QueryContext(ctx, pragma)
	if err != nil {
		return fmt.Errorf("read state table %s columns: %w", table, err)
	}
	type column struct {
		name string
		cid  int
		pk   int
	}
	columns := []column{}
	for rows.Next() {
		var item column
		var declaredType string
		var notNull int
		var defaultValue any
		if err := rows.Scan(&item.cid, &item.name, &declaredType, &notNull, &defaultValue, &item.pk); err != nil {
			rows.Close()
			return fmt.Errorf("decode state table %s columns: %w", table, err)
		}
		columns = append(columns, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(columns) == 0 {
		return nil
	}
	selectColumns := make([]string, 0, len(columns))
	order := append([]column(nil), columns...)
	sort.SliceStable(order, func(left, right int) bool {
		leftRank, rightRank := order[left].pk, order[right].pk
		if leftRank == 0 {
			leftRank = len(columns) + order[left].cid + 1
		}
		if rightRank == 0 {
			rightRank = len(columns) + order[right].cid + 1
		}
		return leftRank < rightRank
	})
	orderColumns := make([]string, 0, len(order))
	for _, item := range columns {
		selectColumns = append(selectColumns, quoteIdentifier(item.name))
	}
	for _, item := range order {
		orderColumns = append(orderColumns, quoteIdentifier(item.name))
	}
	query := "SELECT " + strings.Join(selectColumns, ",") + " FROM " +
		quoteIdentifier(table) + " ORDER BY " + strings.Join(orderColumns, ",")
	dataRows, err := reader.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("read state table %s for digest: %w", table, err)
	}
	defer dataRows.Close()
	writeDigestValue(hasher, table)
	for dataRows.Next() {
		values := make([]any, len(columns))
		destinations := make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		if err := dataRows.Scan(destinations...); err != nil {
			return fmt.Errorf("decode state table %s row: %w", table, err)
		}
		for _, value := range values {
			writeDigestValue(hasher, value)
		}
	}
	if err := dataRows.Err(); err != nil {
		return fmt.Errorf("iterate state table %s rows: %w", table, err)
	}
	return nil
}

func writeDigestValue(writer io.Writer, value any) {
	var kind byte
	var payload []byte
	switch typed := value.(type) {
	case nil:
		kind = 'n'
	case int64:
		kind, payload = 'i', []byte(strconv.FormatInt(typed, 10))
	case float64:
		kind = 'f'
		payload = make([]byte, 8)
		binary.BigEndian.PutUint64(payload, math.Float64bits(typed))
	case string:
		kind, payload = 's', []byte(typed)
	case []byte:
		kind, payload = 'b', typed
	case bool:
		kind, payload = 't', []byte(strconv.FormatBool(typed))
	case time.Time:
		kind, payload = 'd', []byte(typed.UTC().Format(time.RFC3339Nano))
	default:
		kind, payload = 'x', []byte(fmt.Sprintf("%T:%v", typed, typed))
	}
	_, _ = writer.Write([]byte{kind})
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(payload)))
	_, _ = writer.Write(size[:])
	_, _ = writer.Write(payload)
}

func applyMigrationsOnConnection(
	ctx context.Context,
	connection *sql.Conn,
	version int,
) error {
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
		if _, err := connection.ExecContext(ctx, string(raw)); err != nil {
			return fmt.Errorf("apply state migration %s: %w", entry.Name(), err)
		}
		if _, err := connection.ExecContext(
			ctx, fmt.Sprintf("PRAGMA user_version = %d", migrationVersion),
		); err != nil {
			return fmt.Errorf("record state migration %s: %w", entry.Name(), err)
		}
		version = migrationVersion
	}
	if version != schemaVersion {
		return fmt.Errorf("state schema ended at %d, expected %d", version, schemaVersion)
	}
	return nil
}

func validateBackupDestination(statePath, backupPath string) (string, error) {
	if backupPath == "" {
		return "", errors.New("state migration backup path is empty")
	}
	absolute, err := filepath.Abs(backupPath)
	if err != nil {
		return "", fmt.Errorf("resolve state backup path: %w", err)
	}
	if absolute == statePath {
		return "", errors.New("state migration backup must differ from the state database")
	}
	parent := filepath.Dir(absolute)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("state backup parent must be an existing private real directory: %s", parent)
	}
	if _, err := os.Lstat(absolute); err == nil {
		return "", fmt.Errorf("state migration backup already exists: %s", absolute)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("inspect state migration backup: %w", err)
	}
	return absolute, nil
}

func copyDatabaseBackup(source, target string) (string, error) {
	input, err := os.Open(source)
	if err != nil {
		return "", fmt.Errorf("open state migration source: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("create state migration backup: %w", err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = output.Close()
		}
	}()
	hasher := sha256.New()
	if _, err := io.Copy(io.MultiWriter(output, hasher), input); err != nil {
		return "", fmt.Errorf("copy state migration backup: %w", err)
	}
	if err := output.Sync(); err != nil {
		return "", fmt.Errorf("sync state migration backup: %w", err)
	}
	if err := output.Close(); err != nil {
		return "", fmt.Errorf("close state migration backup: %w", err)
	}
	closed = true
	directory, err := os.Open(filepath.Dir(target))
	if err != nil {
		return "", fmt.Errorf("open state backup directory: %w", err)
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return "", fmt.Errorf("sync state backup directory: %w", err)
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil)), nil
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
