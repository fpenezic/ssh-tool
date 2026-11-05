package dbx

import (
	"reflect"
	"strconv"
	"testing"
)

// The filter quotes each column as an identifier and binds each value; a
// nil value is IS NULL and takes no placeholder.
func TestWhereClause(t *testing.T) {
	v1, v2 := "42", `x" OR 1=1 --`
	w, args := whereClause([]Cond{{Column: `id`, Value: &v1}, {Column: `odd"col`, Value: nil}, {Column: "name", Value: &v2}},
		pgIdent, func(n int) string { return "$" + strconv.Itoa(n) })
	if want := ` WHERE "id" = $1 AND "odd""col" IS NULL AND "name" = $2`; w != want {
		t.Fatalf("where %q, want %q", w, want)
	}
	if !reflect.DeepEqual(args, []any{"42", `x" OR 1=1 --`}) {
		t.Fatalf("args %v", args)
	}
	if w, args := whereClause(nil, pgIdent, nil); w != "" || args != nil {
		t.Fatalf("empty filter: %q %v", w, args)
	}
}
