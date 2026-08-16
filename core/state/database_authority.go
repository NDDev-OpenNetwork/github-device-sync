package state

import (
	"context"
	"database/sql"
	"errors"
)

type authorityDatabase struct {
	raw       *sql.DB
	authority *stateAuthority
}

type authorityRow struct {
	row       *sql.Row
	authority *stateAuthority
	beforeErr error
}

type authorityRows struct {
	rows      *sql.Rows
	authority *stateAuthority
	err       error
}

func (row *authorityRow) Scan(destinations ...any) error {
	if row == nil {
		return errors.New("state query row is unavailable")
	}
	if row.beforeErr != nil {
		return row.beforeErr
	}
	return errors.Join(row.row.Scan(destinations...), row.authority.verify())
}

func newAuthorityDatabase(raw *sql.DB, authority *stateAuthority) *authorityDatabase {
	return &authorityDatabase{raw: raw, authority: authority}
}

func (database *authorityDatabase) check() error {
	if database == nil || database.raw == nil || database.authority == nil {
		return errors.New("state database authority is unavailable")
	}
	return database.authority.verify()
}

func (database *authorityDatabase) PingContext(ctx context.Context) error {
	if err := database.check(); err != nil {
		return err
	}
	return errors.Join(database.raw.PingContext(ctx), database.check())
}

func (database *authorityDatabase) ExecContext(
	ctx context.Context,
	query string,
	arguments ...any,
) (sql.Result, error) {
	if err := database.check(); err != nil {
		return nil, err
	}
	result, operationErr := database.raw.ExecContext(ctx, query, arguments...)
	return result, errors.Join(operationErr, database.check())
}

func (database *authorityDatabase) QueryContext(
	ctx context.Context,
	query string,
	arguments ...any,
) (rowsScanner, error) {
	if err := database.check(); err != nil {
		return nil, err
	}
	rows, operationErr := database.raw.QueryContext(ctx, query, arguments...)
	authorityErr := database.check()
	if authorityErr != nil && rows != nil {
		_ = rows.Close()
	}
	if operationErr != nil || authorityErr != nil {
		return nil, errors.Join(operationErr, authorityErr)
	}
	return &authorityRows{rows: rows, authority: database.authority}, nil
}

func (rows *authorityRows) check() error {
	if rows == nil || rows.rows == nil || rows.authority == nil {
		return errors.New("state query rows authority is unavailable")
	}
	if rows.err != nil {
		return rows.err
	}
	rows.err = rows.authority.verify()
	return rows.err
}

func (rows *authorityRows) Next() bool {
	if rows.check() != nil {
		return false
	}
	hasNext := rows.rows.Next()
	if err := rows.authority.verify(); err != nil {
		rows.err = err
		return false
	}
	return hasNext
}

func (rows *authorityRows) Scan(destinations ...any) error {
	if err := rows.check(); err != nil {
		return err
	}
	return errors.Join(rows.rows.Scan(destinations...), rows.authority.verify())
}

func (rows *authorityRows) Err() error {
	if rows == nil || rows.rows == nil || rows.authority == nil {
		return errors.New("state query rows authority is unavailable")
	}
	return errors.Join(rows.err, rows.rows.Err(), rows.authority.verify())
}

func (rows *authorityRows) Close() error {
	if rows == nil || rows.rows == nil || rows.authority == nil {
		return errors.New("state query rows authority is unavailable")
	}
	return errors.Join(rows.rows.Close(), rows.authority.verify())
}

func (database *authorityDatabase) QueryRowContext(
	ctx context.Context,
	query string,
	arguments ...any,
) rowScanner {
	beforeErr := database.check()
	if beforeErr != nil {
		return &authorityRow{authority: database.authority, beforeErr: beforeErr}
	}
	return &authorityRow{
		row: database.raw.QueryRowContext(ctx, query, arguments...), authority: database.authority,
	}
}

func (database *authorityDatabase) BeginTx(
	ctx context.Context,
	options *sql.TxOptions,
) (*sql.Tx, error) {
	if err := database.check(); err != nil {
		return nil, err
	}
	return database.raw.BeginTx(ctx, options)
}

func (database *authorityDatabase) Conn(ctx context.Context) (*sql.Conn, error) {
	if err := database.check(); err != nil {
		return nil, err
	}
	return database.raw.Conn(ctx)
}

func (database *authorityDatabase) Close() error {
	if database == nil || database.raw == nil {
		return nil
	}
	return database.raw.Close()
}

func (store *Store) commit(transaction *sql.Tx) error {
	if store == nil || store.db == nil || transaction == nil {
		return errors.New("state transaction authority is unavailable")
	}
	if store.beforeAuthorityCommit != nil {
		store.beforeAuthorityCommit()
	}
	if err := store.db.check(); err != nil {
		_ = transaction.Rollback()
		return err
	}
	return errors.Join(transaction.Commit(), store.db.check())
}
