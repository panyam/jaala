package cypher_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/internal/guide"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// depsGraph is the guide's vocabulary (internal/guide) as a graph: packages keyed by name with their
// license as a column, and imports and the deps module's reach as relationship types.
var depsGraph = cypher.Graph{
	Nodes: []cypher.Node{{Label: "Package", Relation: "pkg", Key: 0, Props: map[string]int{"name": 0, "license": 1}}},
	Edges: []cypher.Edge{
		{Type: "IMPORTS", Relation: "imports", From: 0, To: 1},
		{Type: "DEPENDS_ON", Relation: "deps.depends_on", From: 0, To: 1},
	},
}

// codeBase is Declaire's shape: every node in node(id, kind), every relationship in edge(from, kind,
// to), properties as attr(id, key, value). main calls parse twice over (direct and through a
// helper), parse calls lex, a.go contains main and parse, and only main has a doc.
func codeBase(t *testing.T) (*ns.Vocabulary, *datalog.Base, cypher.Graph) {
	t.Helper()
	src := ns.NewMemSource().
		DeclareSchema("node", ns.Schema{Arity: 2, Labels: []string{"id", "kind"}}).
		DeclareSchema("edge", ns.Schema{Arity: 3, Labels: []string{"from", "kind", "to"}}).
		DeclareSchema("attr", ns.Schema{Arity: 3, Labels: []string{"id", "key", "value"}})
	add := func(rel string, vals ...string) {
		tu := ns.Tuple{}
		for _, v := range vals {
			tu.Vals = append(tu.Vals, ns.S(v))
		}
		src.Add(rel, tu)
	}
	for _, f := range []string{"f:main", "f:parse", "f:lex", "f:help"} {
		add("node", f, "Function")
		add("attr", f, "name", f[2:])
	}
	add("node", "file:a.go", "File")
	add("attr", "file:a.go", "name", "a.go")
	add("attr", "f:main", "doc", "starts it all")
	for _, e := range [][3]string{{"f:main", "CALLS", "f:parse"}, {"f:main", "CALLS", "f:help"}, {"f:help", "CALLS", "f:parse"},
		{"f:parse", "CALLS", "f:lex"}, {"file:a.go", "CONTAINS", "f:main"}, {"file:a.go", "CONTAINS", "f:parse"}} {
		add("edge", e[0], e[1], e[2])
	}
	v, err := ns.NewVocabulary(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := stdlib.Register(v); err != nil {
		t.Fatal(err)
	}
	b, err := datalog.NewBase(v, src)
	if err != nil {
		t.Fatal(err)
	}
	g := cypher.Graph{
		Nodes: []cypher.Node{
			{Label: "Function", Relation: "node", Key: 0, Fixed: map[int]string{1: "Function"}},
			{Label: "File", Relation: "node", Key: 0, Fixed: map[int]string{1: "File"}},
		},
		Edges: []cypher.Edge{
			{Type: "CALLS", Relation: "edge", From: 0, To: 2, Fixed: map[int]string{1: "CALLS"}},
			{Type: "CONTAINS", Relation: "edge", From: 0, To: 2, Fixed: map[int]string{1: "CONTAINS"}},
		},
		Props: &cypher.PropRelation{Relation: "attr", Node: 0, Key: 1, Value: 2},
	}
	return v, b, g
}

// A view naming a relation the vocabulary lacks, or an argument past its arity, is refused before any
// query is read. control: the guide's view passes.
func TestAGraphIsCheckedAgainstItsVocabulary(t *testing.T) {
	v, _ := guide.Deps(t)
	if err := depsGraph.Validate(v); err != nil {
		t.Fatalf("control: %v", err)
	}
	for name, g := range map[string]cypher.Graph{
		"unknown relation": {Nodes: []cypher.Node{{Label: "P", Relation: "package"}}},
		"past arity":       {Nodes: []cypher.Node{{Label: "P", Relation: "pkg", Props: map[string]int{"x": 2}}}},
		"label twice":      {Nodes: []cypher.Node{{Label: "P", Relation: "pkg"}, {Label: "P", Relation: "imports"}}},
		"one endpoint":     {Edges: []cypher.Edge{{Type: "T", Relation: "imports", From: 1, To: 1}}},
		"derived unknown":  {Derived: map[string]cypher.PropSource{"loud": {Relation: "str.shout", Value: 1}}},
		"derived arity":    {Derived: map[string]cypher.PropSource{"loud": {Relation: "str.upper", Value: 2}}},
		"derived one arg":  {Derived: map[string]cypher.PropSource{"loud": {Relation: "str.upper"}}},
		"derived fixed":    {Derived: map[string]cypher.PropSource{"loud": {Relation: "str.upper", Value: 1, Fixed: map[int]string{1: "X"}}}},
	} {
		if err := g.Validate(v); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if _, err := cypher.Compile(v, g, `MATCH (a) RETURN a`); err == nil {
			t.Errorf("%s: Compile accepted the view", name)
		}
	}
}

// rowsOf evaluates p over b with every evaluator, which must agree row for row, and returns the rows
// as text in the projection's column order.
func rowsOf(t *testing.T, p cypher.Program, b *datalog.Base, opts ...datalog.Option) ([][]string, error) {
	t.Helper()
	var out [][][]string
	var errs []error
	for _, ev := range []datalog.Evaluator{datalog.Naive{}, datalog.SemiNaive{WrittenOrder: true}, datalog.SemiNaive{}} {
		rows, err := ev.Eval(context.Background(), p.Query, b, opts...)
		errs = append(errs, err)
		var got [][]string
		for _, r := range rows {
			var vals []string
			for _, c := range p.Query.Columns() {
				v := r.Bind[c]
				if v.Absent {
					vals = append(vals, "absent")
				} else {
					vals = append(vals, v.S)
				}
			}
			got = append(got, vals)
		}
		out = append(out, got)
	}
	for i := 1; i < len(out); i++ {
		if (errs[i] == nil) != (errs[0] == nil) || len(out[i]) != len(out[0]) {
			t.Fatalf("evaluators disagree on %s: %v (%v) against %v (%v)", p.Text, out[i], errs[i], out[0], errs[0])
		}
		for j := range out[i] {
			if len(out[i][j]) != len(out[0][j]) {
				t.Fatalf("evaluators disagree on %s: %v against %v", p.Text, out[i], out[0])
			}
			for k := range out[i][j] {
				if out[i][j][k] != out[0][j][k] {
					t.Fatalf("evaluators disagree on %s: %v against %v", p.Text, out[i], out[0])
				}
			}
		}
	}
	return out[0], errors.Join(errs[0])
}

// An OR's alternatives keep every binding of the match, so count(*) and collect count what a Cypher
// engine counts (#439: the alternatives' rule kept only what RETURN read, and main's two walks, through
// parse and through help, became one tuple). control: the same match without an OR counts 2.
func TestAnOrKeepsEveryBindingOfTheMatch(t *testing.T) {
	_, b, g := codeBase(t)
	const match = `MATCH (a:Function)-[:CALLS]->(b)-[:CALLS]->(c) `
	for _, c := range []struct{ query, want string }{
		{match + `WHERE a.name = 'main' RETURN a.name AS n, count(*) AS k`, "[[main 2]]"},
		{match + `WHERE c.name STARTS WITH 'l' OR a.name = 'main' RETURN a.name AS n, count(*) AS k ORDER BY n`, "[[help 1] [main 2]]"},
		{match + `WHERE c.name STARTS WITH 'l' OR a.name = 'main' RETURN a.name AS n, collect(a.name) AS ns ORDER BY n`, `[[help ["help"]] [main ["main","main"]]]`},
		{match + `WHERE c.name STARTS WITH 'l' OR a.name = 'main' RETURN DISTINCT a.name AS n ORDER BY n`, "[[help] [main]]"},
	} {
		p, err := cypher.Compile(b.Vocabulary(), g, c.query)
		if err != nil {
			t.Fatalf("%s: %v", c.query, err)
		}
		rows, err := rowsOf(t, p, b)
		if err != nil {
			t.Fatalf("%s: %v", c.query, err)
		}
		if got := fmt.Sprint(rows); got != c.want {
			t.Errorf("%s = %s, want %s\n%s", c.query, got, c.want, p.Text)
		}
	}
}

// calledBase is codeBase's shape with an id on every relationship: main calls parse twice (two call
// sites, c1 and c2), parse calls lex, a.go contains main, and a.go imports b.go on a line held in
// imports' third column. c1's site is a property held as attr(c1, "site", ...), keyed by its id.
func calledBase(t *testing.T) (*datalog.Base, cypher.Graph) {
	t.Helper()
	src := ns.NewMemSource().
		DeclareSchema("node", ns.Schema{Arity: 2, Labels: []string{"id", "kind"}}).
		DeclareSchema("edge", ns.Schema{Arity: 3, Labels: []string{"from", "kind", "to"}}).
		DeclareSchema("imports", ns.Schema{Arity: 3, Labels: []string{"from", "to", "line"}}).
		DeclareSchema("attr", ns.Schema{Arity: 3, Labels: []string{"id", "key", "value"}})
	add := func(rel, id string, vals ...string) {
		tu := ns.Tuple{ID: id}
		for _, v := range vals {
			tu.Vals = append(tu.Vals, ns.S(v))
		}
		src.Add(rel, tu)
	}
	for _, f := range []string{"f:main", "f:parse", "f:lex"} {
		add("node", "", f, "Function")
		add("attr", "", f, "name", f[2:])
	}
	for _, f := range []string{"file:a.go", "file:b.go"} {
		add("node", "", f, "File")
		add("attr", "", f, "name", f[5:])
	}
	add("edge", "c1", "f:main", "CALLS", "f:parse")
	add("edge", "c2", "f:main", "CALLS", "f:parse")
	add("edge", "c3", "f:parse", "CALLS", "f:lex")
	add("edge", "k1", "file:a.go", "CONTAINS", "f:main")
	add("imports", "i1", "file:a.go", "file:b.go", "7")
	add("attr", "", "c1", "site", "a.go:3")
	v, err := ns.NewVocabulary(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := stdlib.Register(v); err != nil {
		t.Fatal(err)
	}
	b, err := datalog.NewBase(v, src)
	if err != nil {
		t.Fatal(err)
	}
	return b, cypher.Graph{
		Nodes: []cypher.Node{
			{Label: "Function", Relation: "node", Key: 0, Fixed: map[int]string{1: "Function"}},
			{Label: "File", Relation: "node", Key: 0, Fixed: map[int]string{1: "File"}},
		},
		Edges: []cypher.Edge{
			{Type: "CALLS", Relation: "edge", From: 0, To: 2, Fixed: map[int]string{1: "CALLS"}},
			{Type: "CONTAINS", Relation: "edge", From: 0, To: 2, Fixed: map[int]string{1: "CONTAINS"}},
			{Type: "IMPORTS", Relation: "imports", From: 0, To: 1, Props: map[string]int{"line": 2}},
		},
		Props: &cypher.PropRelation{Relation: "attr", Node: 0, Key: 1, Value: 2},
	}
}

// A relationship variable binds its edge's fact (#441, Declaire's x10b asked type(r)): RETURN r
// answers its id, type(r) its type, startNode and endNode its ends whichever way the pattern reads
// it, r.prop a column of its type or Props keyed by its id, and count(r) counts main's two calls of
// parse. control: RETURN f, g with the relationship unnamed answers main and parse once, as doc.go
// says a program's distinct rows do, and naming r answers a row per call.
func TestARelationshipVariableIsItsFact(t *testing.T) {
	b, g := calledBase(t)
	for _, c := range []struct{ query, want string }{
		{`MATCH (f:Function)-[r:CALLS]->(g) RETURN type(r) AS t`, "[[CALLS]]"},
		{`MATCH (f:Function)-[r:CALLS]->(g) RETURN r ORDER BY r`, "[[c1] [c2] [c3]]"},
		{`MATCH (f:Function)-[r:CALLS]->(g) RETURN count(r) AS n`, "[[3]]"},
		{`MATCH (f:Function)-[:CALLS]->(g) RETURN f, g ORDER BY f`, "[[f:main f:parse] [f:parse f:lex]]"},
		{`MATCH (f:Function)-[r:CALLS]->(g) RETURN f, g, r ORDER BY r`, "[[f:main f:parse c1] [f:main f:parse c2] [f:parse f:lex c3]]"},
		{`MATCH (a)<-[r:CALLS]-(b) WHERE a.name = 'lex' RETURN startNode(r) AS s, endNode(r) AS e`, "[[f:parse f:lex]]"},
		{`MATCH (a)-[r]-(b) WHERE a.name = 'parse' RETURN type(r) AS t, startNode(r) AS s, endNode(r) AS e, r ORDER BY r`,
			"[[CALLS f:main f:parse c1] [CALLS f:main f:parse c2] [CALLS f:parse f:lex c3]]"},
		{`MATCH (a)-[r:CALLS|CONTAINS]->(b) WHERE b.name = 'main' RETURN type(r) AS t, r`, "[[CONTAINS k1]]"},
		{`MATCH ()-[r:CALLS]->() RETURN r.site AS s`, "[[a.go:3]]"},
		{`MATCH ()-[r:CALLS]->() WHERE r.site = 'a.go:3' RETURN r`, "[[c1]]"},
		{`MATCH ()-[r:IMPORTS]->() RETURN r.line AS l, r`, "[[7 i1]]"},
		{`MATCH (a)-[r]->(b) WHERE type(r) = 'IMPORTS' RETURN a.name AS n`, "[[a.go]]"},
		{`MATCH ()-[r:CALLS]->() WHERE r.site IS NULL RETURN r ORDER BY r`, "[[c2] [c3]]"},
		{`MATCH ()-[r:CALLS]->() WHERE r.site IS NOT NULL RETURN r`, "[[c1]]"},
	} {
		p, err := cypher.Compile(b.Vocabulary(), g, c.query)
		if err != nil {
			t.Errorf("%s: %v", c.query, err)
			continue
		}
		rows, err := rowsOf(t, p, b)
		if err != nil {
			t.Errorf("%s: %v", c.query, err)
			continue
		}
		if got := fmt.Sprint(rows); got != c.want {
			t.Errorf("%s = %s, want %s\n%s", c.query, got, c.want, p.Text)
		}
	}
}

// A relationship variable carries through a WITH with its fact, its ends and its type (#445), through
// one stage or two, under an aggregate, and over several types; and a subquery binds one of its own
// (its WHERE reads it: main's two calls of parse count one without a site, two in all) or shares the
// MATCH's, meaning that relationship. control: EXISTS of a call with a site between a and b holds for
// c1 alone over the MATCH's r, and for c1 and c2 over a fresh q, so sharing is what narrows it. A
// one-relationship path binds its relationship too: main's two calls of parse are two relationships
// and one path, as a path is its nodes.
func TestARelationshipVariableCarriesThroughWithAndIntoSubqueries(t *testing.T) {
	b, g := calledBase(t)
	for _, c := range []struct{ query, want string }{
		{`MATCH (f:Function)-[r:CALLS]->(g) WITH r RETURN type(r) AS t`, "[[CALLS]]"},
		{`MATCH (f:Function)-[r:CALLS]->(g) WITH r RETURN startNode(r) AS s, endNode(r) AS e, r ORDER BY r`,
			"[[f:main f:parse c1] [f:main f:parse c2] [f:parse f:lex c3]]"},
		{`MATCH (a)-[r]-(b) WHERE a.name = 'parse' WITH r RETURN type(r) AS t, startNode(r) AS s, endNode(r) AS e, r ORDER BY r`,
			"[[CALLS f:main f:parse c1] [CALLS f:main f:parse c2] [CALLS f:parse f:lex c3]]"},
		{`MATCH (a)-[r:CALLS|CONTAINS]->(b) WHERE b.name = 'main' WITH r RETURN type(r) AS t, r`, "[[CONTAINS k1]]"},
		{`MATCH ()-[r:CALLS]->() WITH r RETURN r.site AS s`, "[[a.go:3]]"},
		{`MATCH ()-[r:IMPORTS]->() WITH r RETURN r.line AS l, endNode(r) AS e`, "[[7 file:b.go]]"},
		{`MATCH (f)-[r:CALLS]->(g) WITH r, count(*) AS k WITH r RETURN type(r) AS t, count(r) AS n`, "[[CALLS 3]]"},
		{`MATCH (a:Function) WHERE EXISTS { (a)-[r:CALLS]->(b) WHERE r.site = 'a.go:3' } RETURN a`, "[[f:main]]"},
		{`MATCH (a:Function) WHERE NOT EXISTS { (a)-[r:CALLS]->() WHERE r.site IS NULL } RETURN a ORDER BY a`, "[[f:lex]]"},
		{`MATCH (a:Function) RETURN a, COUNT { (a)-[r:CALLS]->() WHERE r.site IS NULL } AS n ORDER BY a`, "[[f:lex 0] [f:main 1] [f:parse 1]]"},
		{`MATCH (a:Function) RETURN a, COUNT { (a)-[r:CALLS]->() } AS n ORDER BY a`, "[[f:lex 0] [f:main 2] [f:parse 1]]"},
		{`MATCH (a)-[r:CALLS]->(b) WHERE EXISTS { ()-[r]->(:Function {name: 'lex'}) } RETURN r`, "[[c3]]"},
		{`MATCH (a)-[r:CALLS]->(b) WHERE NOT EXISTS { (a)-[r]->(:Function {name: 'parse'}) } RETURN r`, "[[c3]]"},
		{`MATCH (a)-[r:CALLS]->(b) WHERE EXISTS { (a)-[r]->(b) WHERE r.site IS NOT NULL } RETURN r ORDER BY r`, "[[c1]]"},
		{`MATCH (a)-[r:CALLS]->(b) WHERE EXISTS { (a)-[q:CALLS]->(b) WHERE q.site IS NOT NULL } RETURN r ORDER BY r`, "[[c1] [c2]]"},
		{`MATCH p = (a)-[r:CALLS]->(b) WHERE a.name = 'main' RETURN r, length(p) AS n ORDER BY r`, "[[c1 1] [c2 1]]"},
		{`MATCH p = (a)-[r:CALLS]->(b) RETURN count(r) AS n, count(DISTINCT p) AS ps`, "[[3 2]]"},
	} {
		p, err := cypher.Compile(b.Vocabulary(), g, c.query)
		if err != nil {
			t.Errorf("%s: %v", c.query, err)
			continue
		}
		rows, err := rowsOf(t, p, b)
		if err != nil {
			t.Errorf("%s: %v\n%s", c.query, err, p.Text)
			continue
		}
		if got := fmt.Sprint(rows); got != c.want {
			t.Errorf("%s = %s, want %s\n%s", c.query, got, c.want, p.Text)
		}
	}
	for _, q := range []string{
		`MATCH (a)-[r:CALLS]->(b) WHERE (a)-[q:CALLS]->() RETURN a`,
	} {
		if _, err := cypher.Compile(b.Vocabulary(), g, q); err == nil || !strings.Contains(err.Error(), "can't introduce q") {
			t.Errorf("%s: %v, want a refusal: a pattern in WHERE can't introduce q", q, err)
		}
	}
}

// A type the vocabulary derives has no facts, so reading its relationship's id or property is refused
// by name, while its type and ends, which need no fact, still answer. control: the guide's base type
// IMPORTS answers RETURN r.
func TestADerivedTypesRelationshipHasNoId(t *testing.T) {
	v, b := guide.Deps(t)
	for _, q := range []string{`MATCH (a)-[r:DEPENDS_ON]->(b) RETURN r`, `MATCH (a)-[r:DEPENDS_ON]->(b) RETURN count(r)`} {
		if _, err := cypher.Compile(v, depsGraph, q); err == nil || !strings.Contains(err.Error(), "DEPENDS_ON is derived") {
			t.Errorf("%s: %v, want a refusal naming DEPENDS_ON as derived", q, err)
		}
	}
	for _, c := range []struct{ query, want string }{
		{`MATCH (a)-[r:DEPENDS_ON]->(b) WHERE a.name = 'app' RETURN DISTINCT type(r) AS t`, "[[DEPENDS_ON]]"},
		{`MATCH (a)-[r:IMPORTS]->(b) WHERE a.name = 'app' RETURN startNode(r) AS s, count(r) AS n`, "[[app 1]]"},
	} {
		p, err := cypher.Compile(v, depsGraph, c.query)
		if err != nil {
			t.Errorf("%s: %v", c.query, err)
			continue
		}
		rows, err := rowsOf(t, p, b)
		if err != nil || fmt.Sprint(rows) != c.want {
			t.Errorf("%s = %v, %v; want %s\n%s", c.query, rows, err, c.want, p.Text)
		}
	}
}

// A relationship an OPTIONAL MATCH binds may be null, and its fact binds only inside the braces, so
// reading it is refused naming #400 rather than answering f:lex a type it has no relationship of.
func TestAnOptionalRelationshipVariableIsRefusedWhenRead(t *testing.T) {
	b, g := calledBase(t)
	for _, q := range []string{
		`MATCH (a:Function) OPTIONAL MATCH (a)-[r:CALLS]->(b) RETURN a, r`,
		`MATCH (a:Function) OPTIONAL MATCH (a)-[r:CALLS]->(b) RETURN a, type(r)`,
		`MATCH (a:Function) OPTIONAL MATCH (a)-[r:CALLS]->(b) RETURN a, r.site`,
	} {
		_, err := cypher.Compile(b.Vocabulary(), g, q)
		var ce *cypher.Error
		if !asError(err, &ce) || ce.Gap != 400 {
			t.Errorf("%s: %v, want a refusal naming #400", q, err)
		}
	}
}
