package parse

import (
	"regexp"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"

	"github.com/cpuchip/lodestar/internal/graph"
)

// Shared-DB extraction: two services that touch the SAME table are coupled at the
// heaviest weight in the model (db = 3.0) — a shared database is the archetypal
// distributed-monolith seam, because neither service can be redeployed or reshaped
// without regard for the other's reads and writes. This is a SYMMETRIC coupling
// (no producer/consumer): every service touching a table is a peer.
//
// SQL appears as string literals regardless of the host language, so one extractor
// serves Go, Python, TS and JS: walk every string-ish literal, gate to strings that
// actually look like SQL, then pull table names that follow FROM/JOIN/INTO/UPDATE/
// TABLE. Table names are keyed lowercased. Precision is defended in three layers:
// the SQL gate (a plain-English string containing the word "from" is not SQL and
// yields nothing), the isTableNoise denylist at resolve time (information_schema,
// migrations, ...), and the maxWorlds cap (a table in 7+ services is shared infra,
// not a meaningful coupling).

// sqlKeywordGate matches strings that contain a SQL statement keyword. Without this
// gate the tableRE below would read "our" out of the English sentence "...providing
// recommendations ... from our catalog" (FROM our → "our"); requiring a statement
// keyword filters non-SQL prose. Precision over recall.
//
// WITH (the CTE keyword) is deliberately excluded: it is an extremely common English
// preposition ("tasked WITH providing ... FROM our catalog" is what leaked "our"
// out of an LLM prompt), and it adds no real recall — a CTE always contains a SELECT,
// which already opens the gate. The remaining keywords are rare in prose.
var sqlKeywordGate = regexp.MustCompile(`(?i)\b(SELECT|INSERT|UPDATE|DELETE|CREATE|ALTER|DROP|TRUNCATE|REPLACE|MERGE)\b`)

// tableRE captures the identifier following a table-introducing SQL keyword. The
// optional quote/backtick handles quoted identifiers ("orders", `orders`); the
// captured token allows a schema qualifier (public.orders) which is lowercased and
// noise-checked on its leading segment at resolve time.
var tableRE = regexp.MustCompile(`(?i)\b(?:FROM|JOIN|INTO|UPDATE|TABLE)\s+["'` + "`" + `]?([A-Za-z_][A-Za-z0-9_.]*)`)

// extractSQLTables walks every string literal in a file and emits a KindDataEntity
// node for each table name it can pull out of a SQL statement. Shared across all
// languages (wired into every Language.contracts).
func extractSQLTables(p *fileCtx, root *sitter.Node) {
	walk(root, func(n *sitter.Node) {
		s, ok := p.anyStringLit(n)
		if !ok || len(s) < 12 { // a real SQL statement is never a handful of chars
			return
		}
		if !sqlKeywordGate.MatchString(s) {
			return // not SQL — a string that merely contains "from" is skipped
		}
		for _, m := range tableRE.FindAllStringSubmatch(s, -1) {
			name := sqlTableName(m[1])
			if name == "" {
				continue
			}
			p.addContract(graph.KindDataEntity, name, map[string]string{"table": name})
		}
	})
}

// sqlTableName normalizes a captured table token: strip a stray leading/trailing
// quote or backtick, then lowercase. Schema qualifiers are kept (public.orders →
// public.orders) — the resolve-time noise check inspects the leading segment.
func sqlTableName(raw string) string {
	raw = strings.Trim(raw, "\"'`")
	return strings.ToLower(raw)
}

// anyStringLit returns the static text of any string-literal node across the
// languages lodestar parses, or ("",false) for a non-string or a dynamic
// (interpolated / template-substituted) string. It unifies Go's interpreted/raw
// literals, Python's string (string_content), and TS/JS's string (string_fragment)
// and static template_string, so extractSQLTables can be language-agnostic.
func (p *fileCtx) anyStringLit(n *sitter.Node) (string, bool) {
	switch n.Type() {
	case "interpreted_string_literal", "raw_string_literal":
		return strings.Trim(n.Content(p.src), "\"`"), true
	case "string":
		// Python (string_content) or TS/JS (string_fragment); bail on interpolation.
		var sb strings.Builder
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			switch c.Type() {
			case "interpolation":
				return "", false // f-string → dynamic
			case "string_content", "string_fragment":
				sb.WriteString(c.Content(p.src))
			}
		}
		return sb.String(), true
	case "template_string":
		// A backtick string with no ${...} is static — common for multi-line SQL in
		// JS/TS; a substitution makes it dynamic, so bail (precision over recall).
		var sb strings.Builder
		for i := 0; i < int(n.NamedChildCount()); i++ {
			c := n.NamedChild(i)
			if c.Type() == "template_substitution" {
				return "", false
			}
			if c.Type() == "string_fragment" {
				sb.WriteString(c.Content(p.src))
			}
		}
		return sb.String(), true
	}
	return "", false
}
