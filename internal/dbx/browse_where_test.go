package dbx

import (
	"reflect"
	"testing"
)

// The filter quotes each column as an identifier and binds each value; a
// nil value is IS NULL and takes no placeholder, a hex cell binds its
// bytes.
func TestWhereClause(t *testing.T) {
	v1, v2, bin := "42", `x" OR 1=1 --`, "0xdead"
	w, args, err := whereClause([]Cond{{Column: `id`, Value: &v1}, {Column: `odd"col`, Value: nil}, {Column: "name", Value: &v2}, {Column: "b", Value: &bin, Hex: true}},
		pgIdent, numbered("$"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := ` WHERE "id" = $1 AND "odd""col" IS NULL AND "name" = $2 AND "b" = $3`; w != want {
		t.Fatalf("where %q, want %q", w, want)
	}
	if !reflect.DeepEqual(args, []any{"42", `x" OR 1=1 --`, []byte{0xde, 0xad}}) {
		t.Fatalf("args %v", args)
	}
	if w, args, err := whereClause(nil, pgIdent, numbered("$"), nil); w != "" || args != nil || err != nil {
		t.Fatalf("empty filter: %q %v %v", w, args, err)
	}
	pgBin, bad := `\xbeef`, "0xzz"
	if _, args, _ := whereClause([]Cond{{Column: "b", Value: &pgBin, Hex: true}}, pgIdent, qmark, nil); !reflect.DeepEqual(args, []any{[]byte{0xbe, 0xef}}) {
		t.Fatalf("pg bytea: %v", args)
	}
	if _, _, err := whereClause([]Cond{{Column: "b", Value: &bad, Hex: true}}, pgIdent, qmark, nil); err == nil {
		t.Fatal("bad hex accepted")
	}
}

// Comparisons other than =, IS NOT NULL from <> with no value, unknown
// operators and LIKE on binary refused; the asked sort comes before the
// primary key, which is not repeated.
func TestWhereOpsAndOrder(t *testing.T) {
	five, pat := "5", "a%"
	w, args, err := whereClause([]Cond{{Column: "n", Value: &five, Op: ">="}, {Column: "s", Value: &pat, Op: "NOT LIKE"}, {Column: "x", Op: "<>"}},
		pgIdent, numbered("$"), nil)
	if err != nil || w != ` WHERE "n" >= $1 AND "s" NOT LIKE $2 AND "x" IS NOT NULL` || len(args) != 2 {
		t.Fatalf("%q %v %v", w, args, err)
	}
	for _, c := range []Cond{{Column: "n", Value: &five, Op: "; DROP"}, {Column: "n", Op: ">"}, {Column: "b", Value: &five, Op: "LIKE", Hex: true}} {
		if _, _, err := whereClause([]Cond{c}, pgIdent, numbered("$"), nil); err == nil {
			t.Fatalf("accepted %+v", c)
		}
	}
	if got := orderList([]Order{{Column: "name", Desc: true}, {Column: "id"}}, []string{`"id"`}, pgIdent); got != `"name" DESC, "id"` {
		t.Fatalf("order %q", got)
	}
	if got := orderList([]Order{{Column: "name"}}, []string{`"id"`, `"k"`}, pgIdent); got != `"name", "id", "k"` {
		t.Fatalf("order then key %q", got)
	}
	if got := orderList(nil, []string{`"a"`, `"b"`}, pgIdent); got != `"a", "b"` {
		t.Fatalf("pk order %q", got)
	}
}
