package dbx

import (
	"context"
	"database/sql"
)

// The engines' liveness checks (Handle.Ping).

func (e *pgEngine) ping(ctx context.Context) error {
	p, err := e.pool(ctx, "")
	if err != nil {
		return err
	}
	var one int
	if err := p.QueryRow(ctx, `SELECT 1`).Scan(&one); err != nil {
		return pgErr(err)
	}
	return nil
}

func (e *myEngine) ping(ctx context.Context) error {
	db, err := e.db()
	if err != nil {
		return err
	}
	return sqlPing(ctx, db, myErr, `SELECT 1`)
}

func (e *oraEngine) ping(ctx context.Context) error {
	db, err := e.db()
	if err != nil {
		return err
	}
	return sqlPing(ctx, db, oraErr, `SELECT 1 FROM DUAL`)
}

func (e *msEngine) ping(ctx context.Context) error {
	db, err := e.db()
	if err != nil {
		return err
	}
	return sqlPing(ctx, db, msErr, `SELECT 1`)
}

func (e *db2Engine) ping(ctx context.Context) error {
	db, err := e.db()
	if err != nil {
		return err
	}
	return sqlPing(ctx, db, db2Err, `SELECT 1 FROM SYSIBM.SYSDUMMY1`)
}

// sqlPing runs q on a pooled connection.
func sqlPing(ctx context.Context, db *sql.DB, errf func(error) error, q string) error {
	var one any
	if err := db.QueryRowContext(ctx, q).Scan(&one); err != nil {
		return errf(err)
	}
	return nil
}
