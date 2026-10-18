package cypher_test

import (
	"context"
	"errors"
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
