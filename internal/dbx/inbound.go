package dbx

import (
	"context"
	"database/sql"
	"strings"

	"github.com/jackc/pgx/v5"
)

// The foreign keys of other tables that reference a table ("Referenced
// by"): each comes back as a ForeignKey of the referencing table, Database
// / Schema / Table naming that table the way Browse takes it, Columns its
// key columns and RefColumns the referenced table's, in key order.

// ReferencedBy lists the foreign keys that point at a table. Engines
// without foreign keys return none.
func (h *Handle) ReferencedBy(ctx context.Context, database, schema, name string) ([]ForeignKey, error) {
	ib, ok := h.eng.(inbounder)
	if !ok {
		return nil, nil
	}
	return ib.inbound(ctx, database, schema, name)
}

type inbounder interface {
	inbound(ctx context.Context, database, schema, name string) ([]ForeignKey, error)
}

// sqlInbound reads rows of (constraint, database, schema, table, column,
// referenced column) ordered by table, constraint and key position into
// one ForeignKey per constraint.
func sqlInbound(ctx context.Context, db *sql.DB, errf func(error) error, q string, args ...any) ([]ForeignKey, error) {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, errf(err)
	}
	defer rows.Close()
	var out []ForeignKey
	for rows.Next() {
		var n, d, s, t, c, rc string
		if err := rows.Scan(&n, &d, &s, &t, &c, &rc); err != nil {
			return nil, errf(err)
		}
		if k := len(out) - 1; k >= 0 && out[k].Name == n && out[k].Database == d && out[k].Schema == s && out[k].Table == t {
			out[k].Columns = append(out[k].Columns, c)
			out[k].RefColumns = append(out[k].RefColumns, rc)
			continue
		}
		out = append(out, ForeignKey{Name: n, Database: d, Schema: s, Table: t, Columns: []string{c}, RefColumns: []string{rc}})
	}
	if err := rows.Err(); err != nil {
		return nil, errf(err)
	}
	return out, nil
}

// clearSchema blanks Schema where the database is the schema (MySQL,
// Oracle, Db2): the query returns it twice to keep one row shape.
func clearSchema(fks []ForeignKey) []ForeignKey {
	for i := range fks {
		fks[i].Schema = ""
	}
	return fks
}

func (e *pgEngine) inbound(ctx context.Context, database, schema, name string) ([]ForeignKey, error) {
	p, err := e.pool(ctx, database)
	if err != nil {
		return nil, err
	}
	rows, err := p.Query(ctx, `
		SELECT c.conname, n.nspname, t.relname,
		       ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(n, o)
		             JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.n ORDER BY k.o),
		       ARRAY(SELECT a.attname::text FROM unnest(c.confkey) WITH ORDINALITY k(n, o)
		             JOIN pg_attribute a ON a.attrelid = c.confrelid AND a.attnum = k.n ORDER BY k.o)
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace
		JOIN pg_class rt ON rt.oid = c.confrelid JOIN pg_namespace rn ON rn.oid = rt.relnamespace
		WHERE c.contype = 'f' AND rn.nspname = $1 AND rt.relname = $2
		ORDER BY n.nspname, t.relname, c.conname`, schema, name)
	if err != nil {
		return nil, pgErr(err)
	}
	fks, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ForeignKey, error) {
		fk := ForeignKey{Database: database, RefDatabase: database, RefSchema: schema, RefTable: name}
		err := r.Scan(&fk.Name, &fk.Schema, &fk.Table, &fk.Columns, &fk.RefColumns)
		return fk, err
	})
	if err != nil {
		return nil, pgErr(err)
	}
	return fks, nil
}

func (e *myEngine) inbound(ctx context.Context, database, _, name string) ([]ForeignKey, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	fks, err := sqlInbound(ctx, db, myErr, `
		SELECT CONSTRAINT_NAME, TABLE_SCHEMA, TABLE_SCHEMA, TABLE_NAME, COLUMN_NAME, REFERENCED_COLUMN_NAME
		FROM information_schema.KEY_COLUMN_USAGE
		WHERE REFERENCED_TABLE_SCHEMA = ? AND REFERENCED_TABLE_NAME = ?
		ORDER BY TABLE_SCHEMA, TABLE_NAME, CONSTRAINT_NAME, ORDINAL_POSITION`, database, name)
	return withRef(clearSchema(fks), database, "", name), err
}

func (e *oraEngine) inbound(ctx context.Context, database, _, name string) ([]ForeignKey, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	fks, err := sqlInbound(ctx, db, oraErr, `
		SELECT c.CONSTRAINT_NAME, c.OWNER, c.OWNER, c.TABLE_NAME, cc.COLUMN_NAME, rc.COLUMN_NAME
		FROM ALL_CONSTRAINTS r
		JOIN ALL_CONSTRAINTS c ON c.R_OWNER = r.OWNER AND c.R_CONSTRAINT_NAME = r.CONSTRAINT_NAME AND c.CONSTRAINT_TYPE = 'R'
		JOIN ALL_CONS_COLUMNS cc ON cc.OWNER = c.OWNER AND cc.CONSTRAINT_NAME = c.CONSTRAINT_NAME
		JOIN ALL_CONS_COLUMNS rc ON rc.OWNER = r.OWNER AND rc.CONSTRAINT_NAME = r.CONSTRAINT_NAME AND rc.POSITION = cc.POSITION
		WHERE r.OWNER = :1 AND r.TABLE_NAME = :2 AND r.CONSTRAINT_TYPE IN ('P', 'U')
		ORDER BY c.OWNER, c.TABLE_NAME, c.CONSTRAINT_NAME, cc.POSITION`, database, name)
	return withRef(clearSchema(fks), database, "", name), err
}

func (e *msEngine) inbound(ctx context.Context, database, schema, name string) ([]ForeignKey, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	d := msIdent(database)
	fks, err := sqlInbound(ctx, db, msErr, `
		SELECT fk.name, ps.name, ps.name, pt.name, pc.name, rc.name
		FROM `+d+`.sys.foreign_keys fk
		JOIN `+d+`.sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
		JOIN `+d+`.sys.tables pt ON pt.object_id = fkc.parent_object_id
		JOIN `+d+`.sys.schemas ps ON ps.schema_id = pt.schema_id
		JOIN `+d+`.sys.columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
		JOIN `+d+`.sys.tables rt ON rt.object_id = fkc.referenced_object_id
		JOIN `+d+`.sys.schemas rs ON rs.schema_id = rt.schema_id
		JOIN `+d+`.sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
		WHERE rs.name = @p1 AND rt.name = @p2
		ORDER BY ps.name, pt.name, fk.name, fkc.constraint_column_id`, schema, name)
	for i := range fks {
		fks[i].Database = database // the query repeats the schema there
	}
	return withRef(fks, database, schema, name), err
}

func (e *db2Engine) inbound(ctx context.Context, database, _, name string) ([]ForeignKey, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	fks, err := sqlInbound(ctx, db, db2Err, `
		SELECT r.CONSTNAME, r.TABSCHEMA, r.TABSCHEMA, r.TABNAME, k.COLNAME, p.COLNAME
		FROM SYSCAT.REFERENCES r
		JOIN SYSCAT.KEYCOLUSE k ON k.CONSTNAME = r.CONSTNAME AND k.TABSCHEMA = r.TABSCHEMA AND k.TABNAME = r.TABNAME
		JOIN SYSCAT.KEYCOLUSE p ON p.CONSTNAME = r.REFKEYNAME AND p.TABSCHEMA = r.REFTABSCHEMA
		  AND p.TABNAME = r.REFTABNAME AND p.COLSEQ = k.COLSEQ
		WHERE r.REFTABSCHEMA = ? AND r.REFTABNAME = ?
		ORDER BY r.TABSCHEMA, r.TABNAME, r.CONSTNAME, k.COLSEQ`, database, name)
	// SYSCAT pads some names to their fixed width; only Db2's: another
	// engine's delimited name may end in a blank of its own.
	for i := range fks {
		f := &fks[i]
		f.Name, f.Database, f.Table = strings.TrimRight(f.Name, " "), strings.TrimRight(f.Database, " "), strings.TrimRight(f.Table, " ")
		for j := range f.Columns {
			f.Columns[j] = strings.TrimRight(f.Columns[j], " ")
		}
		for j := range f.RefColumns {
			f.RefColumns[j] = strings.TrimRight(f.RefColumns[j], " ")
		}
	}
	return withRef(clearSchema(fks), database, "", name), err
}

// withRef fills in the referenced table, the one asked about.
func withRef(fks []ForeignKey, database, schema, name string) []ForeignKey {
	for i := range fks {
		fks[i].RefDatabase, fks[i].RefSchema, fks[i].RefTable = database, schema, name
	}
	return fks
}
