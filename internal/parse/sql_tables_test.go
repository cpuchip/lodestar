package parse

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cpuchip/lodestar/internal/graph"
)

// tables parses one file and returns the set of table names emitted as
// KindDataEntity nodes.
func tables(t *testing.T, filename, src string) map[string]bool {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	g, err := ParseDir("svc", dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, n := range g.Nodes {
		if n.Kind == graph.KindDataEntity {
			got[n.Name] = true
		}
	}
	return got
}

// The shared-DB oracle: table names following FROM/JOIN/INTO/UPDATE/TABLE in SQL
// string literals surface as lowercased KindDataEntity nodes; a string that merely
// contains the word "from" (no SQL keyword) yields nothing.
func TestExtractGoSQLTables(t *testing.T) {
	got := tables(t, "db.go", `package svc
func f() {
	db.Query("SELECT id, name FROM Orders o JOIN order_items i ON o.id = i.oid")
	db.Exec(`+"`INSERT INTO shipments (id) VALUES (1)`"+`)
	db.Exec("UPDATE payments SET status = 'paid'")
	log.Print("received response from the upstream service")  // has "from", not SQL → skipped
	greet("welcome home")                                     // no SQL keyword at all
}`)
	for _, want := range []string{"orders", "order_items", "shipments", "payments"} {
		if !got[want] {
			t.Errorf("recall: missing table %q (got %v)", want, keys(got))
		}
	}
	// precision — lowercased (Orders → orders), and the non-SQL "from" string is ignored
	if got["Orders"] {
		t.Error("precision: table name should be lowercased")
	}
	if got["the"] || got["upstream"] {
		t.Error("precision: a non-SQL string containing \"from\" must not yield a table")
	}
}

func TestExtractPythonSQLTables(t *testing.T) {
	got := tables(t, "db.py", `def f():
    cur.execute("SELECT * FROM products WHERE price > 0")
    cur.execute("DELETE FROM cart_items WHERE user_id = %s")
    msg = "this came from marketing"   # not SQL → skipped
`)
	for _, want := range []string{"products", "cart_items"} {
		if !got[want] {
			t.Errorf("recall: missing table %q (got %v)", want, keys(got))
		}
	}
	if got["marketing"] {
		t.Error("precision: non-SQL \"from\" string must not yield a table")
	}
}

func TestExtractTSSQLTables(t *testing.T) {
	// kafka/JS SQL is commonly a backtick template literal; a static one is read,
	// an interpolated one is dynamic and skipped.
	got := tables(t, "db.ts", "function f() {\n"+
		"  pool.query(`SELECT * FROM accounts WHERE id = 1`);\n"+
		"  pool.query(\"INSERT INTO ledger (amount) VALUES (10)\");\n"+
		"  pool.query(`SELECT * FROM ${dynamicTable} WHERE x = 1`);\n"+ // interpolated → skipped
		"}")
	for _, want := range []string{"accounts", "ledger"} {
		if !got[want] {
			t.Errorf("recall: missing table %q (got %v)", want, keys(got))
		}
	}
	if got["dynamictable"] || got["${dynamictable}"] {
		t.Error("precision: an interpolated template SQL must not yield a table")
	}
}
