package dbx

import (
	"context"
	"database/sql"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// Shared by the database/sql engines whose drivers hand back Go values
// rather than the server's text (Oracle, SQL Server): sqlRead reads a
// result under the bounds and cellText prints each value the way the
// engine's own client would.

type sqlQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// cellFunc prints one value of a column of database type dbType.
type cellFunc func(dbType string, v any) string

// sqlDialect is how an engine prints its values, names its column types
// (typeName nil: the driver's name as is) and formats its errors.
type sqlDialect struct {
	cell     cellFunc
	typeName func(dbType string) string
	errf     func(error) error
}

// sqlRead runs stmt and reads its first result set that has columns; a
// later one is counted in Note, not shown (a T-SQL batch, sp_help). Past
// the bounds onCut stops the server when not nil; otherwise the rest is
// read and dropped.
func sqlRead(ctx context.Context, q sqlQuerier, stmt string, maxRows int, d sqlDialect, onCut func()) *Result {
	errf := d.errf
	r := &Result{RowsAffected: -1}
	rows, err := q.QueryContext(ctx, stmt)
	if err != nil {
		r.Error = errf(err).Error()
		return r
	}
	defer rows.Close()
	more := 0
	for set := 0; ; set++ {
		cts, err := rows.ColumnTypes()
		if err != nil {
			r.Error = errf(err).Error()
			return r
		}
		if len(cts) > 0 && !r.HasRows {
			if stop := readSet(rows, cts, r, maxRows, d, onCut); stop {
				return r
			}
		} else if len(cts) > 0 {
			more++
			for rows.Next() {
			}
		}
		if !rows.NextResultSet() {
			break
		}
	}
	if err := rows.Err(); err != nil {
		r.Error = errf(err).Error()
		return r
	}
	if more > 0 {
		r.Note = fmt.Sprintf("%d more result set%s not shown", more, plural(more))
	}
	return r
}

// readSet reads the current result set into r. stop: the run ends here
// (an error, or a cut that stopped the server).
func readSet(rows *sql.Rows, cts []*sql.ColumnType, r *Result, maxRows int, d sqlDialect, onCut func()) (stop bool) {
	r.HasRows = true
	types := make([]string, len(cts))
	for i, ct := range cts {
		types[i] = ct.DatabaseTypeName()
		name := types[i]
		if d.typeName != nil {
			name = d.typeName(name)
		}
		r.Columns = append(r.Columns, ResultColumn{Name: ct.Name(), Type: strings.ToLower(name)})
	}
	rr := &rowReader{r: r, maxRows: maxRows}
	vals := make([]any, len(cts))
	ptrs := make([]any, len(cts))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if rr.stopped {
			continue // drain
		}
		if err := rows.Scan(ptrs...); err != nil {
			r.Error = d.errf(err).Error()
			return true
		}
		raw := make([][]byte, len(vals))
		for i, v := range vals {
			if v != nil {
				raw[i] = []byte(d.cell(types[i], v))
			}
		}
		if !rr.add(raw) && onCut != nil {
			onCut()
			return true
		}
	}
	if !rr.stopped {
		r.RowsAffected = int64(len(r.Rows))
	}
	return false
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// cellText prints the Go value a driver returned. Binary is hex with
// the 0x prefix both servers' tools use.
func cellText(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return "0x" + hexString(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case int:
		return strconv.Itoa(x)
	case uint64:
		return strconv.FormatUint(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'g', -1, 32)
	case bool:
		if x {
			return "1"
		}
		return "0"
	case time.Time:
		return timeText(x, false)
	case *big.Int:
		return x.String()
	case fmt.Stringer:
		return x.String()
	}
	return fmt.Sprint(v)
}

// timeText prints a timestamp as the server stores it: no zone unless the
// type carries one, fractions only when there are any.
func timeText(t time.Time, zone bool) string {
	s := t.Format("2006-01-02 15:04:05.999999999")
	if zone {
		s += t.Format(" -07:00")
	}
	return s
}
