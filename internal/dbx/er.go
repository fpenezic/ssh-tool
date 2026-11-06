package dbx

import (
	"context"
	"database/sql"
	"strings"
)

// The ER diagram of a schema: its tables with their columns (key and
// nullability marked) and the foreign keys going out of them, read in two
// queries per engine so a schema of a few hundred tables comes back in one
// round trip each, not one describe per table.

// SchemaGraph is a schema's tables and the foreign keys between them.
type SchemaGraph struct {
	Tables []GraphTable `json:"tables"`
	// The foreign keys of the schema's tables, the same shape as a table's
	// (Database / Schema / Table the referencing table, Ref* the
	// referenced one, which may live in another schema).
	ForeignKeys []ForeignKey `json:"foreign_keys"`
}

// GraphTable is one table of the diagram.
type GraphTable struct {
	Name    string        `json:"name"`
	Columns []GraphColumn `json:"columns"`
}

// GraphColumn is one column: its type as the server names it, whether it
// is part of the primary key and whether it takes NULL.
type GraphColumn struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Key      bool   `json:"key,omitempty"`
	Nullable bool   `json:"nullable,omitempty"`
}

type grapher interface {
	graph(ctx context.Context, database, schema string) (*SchemaGraph, error)
}

// ErrNoGraph: the engine has no foreign keys to draw (ClickHouse,
// OpenSearch, Redis).
var ErrNoGraph = errString("an ER diagram needs an engine with foreign keys")

type errString string

func (e errString) Error() string { return string(e) }

// SchemaGraph reads the diagram of one schema. database and schema are
// what the tree passes for a table there (MySQL, Oracle and Db2 name the
// schema as the database).
func (h *Handle) SchemaGraph(ctx context.Context, database, schema string) (*SchemaGraph, error) {
	g, ok := h.eng.(grapher)
	if !ok {
		return nil, ErrNoGraph
	}
	return g.graph(ctx, database, schema)
}

// sqlGraph runs the two queries (each with its own arguments: Oracle
// binds by position): columns as (table, column, type,
// nullable 0/1, key 0/1) in table and column order, foreign keys as
// (constraint, table, referenced schema, referenced table, column,
// referenced column) in table, constraint and key order. fk fills in the
// names a ForeignKey needs from the referencing table and the referenced
// schema.
func sqlGraph(ctx context.Context, db *sql.DB, errf func(error) error, colQ, fkQ string, colArgs, fkArgs []any, fk func(table, refSchema, refTable string) ForeignKey) (*SchemaGraph, error) {
	g := &SchemaGraph{Tables: []GraphTable{}, ForeignKeys: []ForeignKey{}}
	rows, err := db.QueryContext(ctx, colQ, colArgs...)
	if err != nil {
		return nil, errf(err)
	}
	for rows.Next() {
		var t, c, ty string
		var null, key int
		if err := rows.Scan(&t, &c, &ty, &null, &key); err != nil {
			rows.Close()
			return nil, errf(err)
		}
		g.addColumn(t, GraphColumn{Name: c, Type: ty, Key: key != 0, Nullable: null != 0})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, errf(err)
	}
	rows, err = db.QueryContext(ctx, fkQ, fkArgs...)
	if err != nil {
		return nil, errf(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n, t, rs, rt, c, rc string
		if err := rows.Scan(&n, &t, &rs, &rt, &c, &rc); err != nil {
			return nil, errf(err)
		}
		g.addFK(n, fk(t, rs, rt), c, rc)
	}
	if err := rows.Err(); err != nil {
		return nil, errf(err)
	}
	return g, nil
}

func (g *SchemaGraph) addColumn(table string, c GraphColumn) {
	if k := len(g.Tables) - 1; k >= 0 && g.Tables[k].Name == table {
		g.Tables[k].Columns = append(g.Tables[k].Columns, c)
		return
	}
	g.Tables = append(g.Tables, GraphTable{Name: table, Columns: []GraphColumn{c}})
}

// addFK appends one key column; rows of one constraint come together.
func (g *SchemaGraph) addFK(name string, f ForeignKey, col, refCol string) {
	if k := len(g.ForeignKeys) - 1; k >= 0 {
		last := &g.ForeignKeys[k]
		if last.Name == name && last.Table == f.Table && last.Schema == f.Schema {
			last.Columns = append(last.Columns, col)
			last.RefColumns = append(last.RefColumns, refCol)
			return
		}
	}
	f.Name, f.Columns, f.RefColumns = name, []string{col}, []string{refCol}
	g.ForeignKeys = append(g.ForeignKeys, f)
}

// trimGraph drops the blanks Db2's SYSCAT pads names with.
func trimGraph(g *SchemaGraph) {
	tr := func(s string) string { return strings.TrimRight(s, " ") }
	for i := range g.Tables {
		t := &g.Tables[i]
		t.Name = tr(t.Name)
		for j := range t.Columns {
			t.Columns[j].Name, t.Columns[j].Type = tr(t.Columns[j].Name), tr(t.Columns[j].Type)
		}
	}
	for i := range g.ForeignKeys {
		f := &g.ForeignKeys[i]
		f.Name, f.Database, f.Table, f.RefDatabase, f.RefTable = tr(f.Name), tr(f.Database), tr(f.Table), tr(f.RefDatabase), tr(f.RefTable)
		for j := range f.Columns {
			f.Columns[j] = tr(f.Columns[j])
		}
		for j := range f.RefColumns {
			f.RefColumns[j] = tr(f.RefColumns[j])
		}
	}
}

func (e *pgEngine) graph(ctx context.Context, database, schema string) (*SchemaGraph, error) {
	p, err := e.pool(ctx, database)
	if err != nil {
		return nil, err
	}
	g := &SchemaGraph{Tables: []GraphTable{}, ForeignKeys: []ForeignKey{}}
	// Tables and partitioned tables; not the partitions, whose keys are
	// copies of their parent's. A table may have no columns (a NULL row
	// keeps it), and a NOT NULL domain makes its column NOT NULL, as in
	// information_schema.columns.
	rows, err := p.Query(ctx, `
		SELECT c.relname, a.attname, format_type(a.atttypid, a.atttypmod),
		       NOT (a.attnotnull OR (ty.typtype = 'd' AND ty.typnotnull)),
		       COALESCE(a.attnum = ANY(pk.conkey), false)
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		LEFT JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		LEFT JOIN pg_type ty ON ty.oid = a.atttypid
		LEFT JOIN pg_constraint pk ON pk.conrelid = c.oid AND pk.contype = 'p'
		WHERE n.nspname = $1 AND c.relkind IN ('r', 'p') AND NOT c.relispartition
		ORDER BY c.relname, a.attnum`, schema)
	if err != nil {
		return nil, pgErr(err)
	}
	for rows.Next() {
		var t string
		var c, ty *string
		var null, key *bool
		if err := rows.Scan(&t, &c, &ty, &null, &key); err != nil {
			rows.Close()
			return nil, pgErr(err)
		}
		if c == nil {
			g.Tables = append(g.Tables, GraphTable{Name: t, Columns: []GraphColumn{}})
			continue
		}
		g.addColumn(t, GraphColumn{Name: *c, Type: *ty, Key: key != nil && *key, Nullable: null != nil && *null})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, pgErr(err)
	}
	rows, err = p.Query(ctx, `
		SELECT c.conname, t.relname, rn.nspname, rt.relname,
		       ARRAY(SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY k(n, o)
		             JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.n ORDER BY k.o),
		       ARRAY(SELECT a.attname::text FROM unnest(c.confkey) WITH ORDINALITY k(n, o)
		             JOIN pg_attribute a ON a.attrelid = c.confrelid AND a.attnum = k.n ORDER BY k.o)
		FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid JOIN pg_namespace n ON n.oid = t.relnamespace
		JOIN pg_class rt ON rt.oid = c.confrelid JOIN pg_namespace rn ON rn.oid = rt.relnamespace
		WHERE c.contype = 'f' AND n.nspname = $1 AND NOT t.relispartition AND c.conparentid = 0
		ORDER BY t.relname, c.conname`, schema)
	if err != nil {
		return nil, pgErr(err)
	}
	defer rows.Close()
	for rows.Next() {
		f := ForeignKey{Database: database, Schema: schema, RefDatabase: database}
		if err := rows.Scan(&f.Name, &f.Table, &f.RefSchema, &f.RefTable, &f.Columns, &f.RefColumns); err != nil {
			return nil, pgErr(err)
		}
		g.ForeignKeys = append(g.ForeignKeys, f)
	}
	if err := rows.Err(); err != nil {
		return nil, pgErr(err)
	}
	return g, nil
}

// flatFK is the ForeignKey of an engine whose schema is the database
// (MySQL, Oracle, Db2): Schema stays "", as Browse takes it.
func flatFK(database string) func(table, refSchema, refTable string) ForeignKey {
	return func(table, refSchema, refTable string) ForeignKey {
		return ForeignKey{Database: database, Table: table, RefDatabase: refSchema, RefTable: refTable}
	}
}

func (e *myEngine) graph(ctx context.Context, database, _ string) (*SchemaGraph, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	return sqlGraph(ctx, db, myErr, `
		SELECT c.TABLE_NAME, c.COLUMN_NAME, c.COLUMN_TYPE,
		       CASE WHEN c.IS_NULLABLE = 'YES' THEN 1 ELSE 0 END, CASE WHEN c.COLUMN_KEY = 'PRI' THEN 1 ELSE 0 END
		FROM information_schema.COLUMNS c
		JOIN information_schema.TABLES t ON t.TABLE_SCHEMA = c.TABLE_SCHEMA AND t.TABLE_NAME = c.TABLE_NAME
		WHERE c.TABLE_SCHEMA = ? AND t.TABLE_TYPE = 'BASE TABLE'
		ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION`, `
		SELECT CONSTRAINT_NAME, TABLE_NAME, REFERENCED_TABLE_SCHEMA, REFERENCED_TABLE_NAME, COLUMN_NAME, REFERENCED_COLUMN_NAME
		FROM information_schema.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = ? AND REFERENCED_TABLE_NAME IS NOT NULL
		ORDER BY TABLE_NAME, CONSTRAINT_NAME, ORDINAL_POSITION`, []any{database}, []any{database}, flatFK(database))
}

func (e *oraEngine) graph(ctx context.Context, database, _ string) (*SchemaGraph, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	// The recycle bin's tables (DROPPED) and the ones other schemas own are
	// left out; the primary key comes from one join, not a lookup per
	// column (the dictionary views are slow).
	return sqlGraph(ctx, db, oraErr, `
		SELECT c.TABLE_NAME, c.COLUMN_NAME, c.DATA_TYPE,
		       CASE WHEN c.NULLABLE = 'Y' THEN 1 ELSE 0 END, CASE WHEN p.COLUMN_NAME IS NULL THEN 0 ELSE 1 END
		FROM ALL_TAB_COLUMNS c
		JOIN ALL_TABLES t ON t.OWNER = c.OWNER AND t.TABLE_NAME = c.TABLE_NAME AND t.DROPPED = 'NO'
		LEFT JOIN (SELECT kc.TABLE_NAME, kc.COLUMN_NAME FROM ALL_CONSTRAINTS k
		           JOIN ALL_CONS_COLUMNS kc ON kc.OWNER = k.OWNER AND kc.CONSTRAINT_NAME = k.CONSTRAINT_NAME
		           WHERE k.OWNER = :1 AND k.CONSTRAINT_TYPE = 'P') p
		  ON p.TABLE_NAME = c.TABLE_NAME AND p.COLUMN_NAME = c.COLUMN_NAME
		WHERE c.OWNER = :2
		ORDER BY c.TABLE_NAME, c.COLUMN_ID`, `
		SELECT c.CONSTRAINT_NAME, c.TABLE_NAME, r.OWNER, r.TABLE_NAME, cc.COLUMN_NAME, rc.COLUMN_NAME
		FROM ALL_CONSTRAINTS c
		JOIN ALL_CONSTRAINTS r ON r.OWNER = c.R_OWNER AND r.CONSTRAINT_NAME = c.R_CONSTRAINT_NAME
		JOIN ALL_CONS_COLUMNS cc ON cc.OWNER = c.OWNER AND cc.CONSTRAINT_NAME = c.CONSTRAINT_NAME
		JOIN ALL_CONS_COLUMNS rc ON rc.OWNER = r.OWNER AND rc.CONSTRAINT_NAME = r.CONSTRAINT_NAME AND rc.POSITION = cc.POSITION
		WHERE c.OWNER = :1 AND c.CONSTRAINT_TYPE = 'R'
		ORDER BY c.TABLE_NAME, c.CONSTRAINT_NAME, cc.POSITION`, []any{database, database}, []any{database}, flatFK(database))
}

func (e *msEngine) graph(ctx context.Context, database, schema string) (*SchemaGraph, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	d := msIdent(database)
	return sqlGraph(ctx, db, msErr, `
		SELECT t.name, c.name, ty.name, CASE WHEN c.is_nullable = 1 THEN 1 ELSE 0 END,
		       CASE WHEN ic.column_id IS NULL THEN 0 ELSE 1 END
		FROM `+d+`.sys.tables t
		JOIN `+d+`.sys.schemas s ON s.schema_id = t.schema_id
		JOIN `+d+`.sys.columns c ON c.object_id = t.object_id
		JOIN `+d+`.sys.types ty ON ty.user_type_id = c.user_type_id
		LEFT JOIN `+d+`.sys.indexes i ON i.object_id = t.object_id AND i.is_primary_key = 1
		LEFT JOIN `+d+`.sys.index_columns ic ON ic.object_id = i.object_id AND ic.index_id = i.index_id AND ic.column_id = c.column_id
		WHERE s.name = @p1
		ORDER BY t.name, c.column_id`, `
		SELECT fk.name, pt.name, rs.name, rt.name, pc.name, rc.name
		FROM `+d+`.sys.foreign_keys fk
		JOIN `+d+`.sys.foreign_key_columns fkc ON fkc.constraint_object_id = fk.object_id
		JOIN `+d+`.sys.tables pt ON pt.object_id = fkc.parent_object_id
		JOIN `+d+`.sys.schemas ps ON ps.schema_id = pt.schema_id
		JOIN `+d+`.sys.columns pc ON pc.object_id = fkc.parent_object_id AND pc.column_id = fkc.parent_column_id
		JOIN `+d+`.sys.tables rt ON rt.object_id = fkc.referenced_object_id
		JOIN `+d+`.sys.schemas rs ON rs.schema_id = rt.schema_id
		JOIN `+d+`.sys.columns rc ON rc.object_id = fkc.referenced_object_id AND rc.column_id = fkc.referenced_column_id
		WHERE ps.name = @p1
		ORDER BY pt.name, fk.name, fkc.constraint_column_id`, []any{schema}, []any{schema},
		func(table, refSchema, refTable string) ForeignKey {
			return ForeignKey{Database: database, Schema: schema, Table: table, RefDatabase: database, RefSchema: refSchema, RefTable: refTable}
		})
}

func (e *db2Engine) graph(ctx context.Context, database, _ string) (*SchemaGraph, error) {
	db, err := e.db()
	if err != nil {
		return nil, err
	}
	g, err := sqlGraph(ctx, db, db2Err, `
		SELECT c.TABNAME, c.COLNAME, c.TYPENAME, CASE WHEN c.NULLS = 'Y' THEN 1 ELSE 0 END,
		       CASE WHEN c.KEYSEQ IS NULL THEN 0 ELSE 1 END
		FROM SYSCAT.COLUMNS c
		JOIN SYSCAT.TABLES t ON t.TABSCHEMA = c.TABSCHEMA AND t.TABNAME = c.TABNAME AND t.TYPE = 'T'
		WHERE c.TABSCHEMA = ?
		ORDER BY c.TABNAME, c.COLNO`, `
		SELECT r.CONSTNAME, r.TABNAME, r.REFTABSCHEMA, r.REFTABNAME, k.COLNAME, p.COLNAME
		FROM SYSCAT.REFERENCES r
		JOIN SYSCAT.KEYCOLUSE k ON k.CONSTNAME = r.CONSTNAME AND k.TABSCHEMA = r.TABSCHEMA AND k.TABNAME = r.TABNAME
		JOIN SYSCAT.KEYCOLUSE p ON p.CONSTNAME = r.REFKEYNAME AND p.TABSCHEMA = r.REFTABSCHEMA
		  AND p.TABNAME = r.REFTABNAME AND p.COLSEQ = k.COLSEQ
		WHERE r.TABSCHEMA = ?
		ORDER BY r.TABNAME, r.CONSTNAME, k.COLSEQ`, []any{database}, []any{database}, flatFK(database))
	if err != nil {
		return nil, err
	}
	trimGraph(g)
	return g, nil
}
