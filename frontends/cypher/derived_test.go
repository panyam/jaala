package cypher_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/internal/guide"
	"github.com/panyam/jaala/ns"
)

// namedBase is codeBase with Declaire's name.of in miniature: short(id, name), the part of an id after
// its colon, computed rather than stored and answered both ways. It names no f:help, whose attr name
// is still there, so a read through attr and one through short answer differently.
func namedBase(t *testing.T) (*ns.Vocabulary, *datalog.Base, cypher.Graph) {
	t.Helper()
	v, b, g := codeBase(t)
	ids := []string{"f:main", "f:parse", "f:lex", "file:a.go"}
	name := func(id string) string { return id[strings.Index(id, ":")+1:] }
	err := v.AddPredicate("short", ns.Builtin{
		Arity:  2,
		Labels: []string{"id", "name"},
		Types:  []ns.ArgType{{Type: ns.TypeString}, {Type: ns.TypeString}},
		Modes:  [][]bool{{true, false}, {false, true}},
		Gen: func(_ context.Context, _ ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
			if !args[0].Bound && !args[1].Bound {
				return errors.New("short needs its id or its name")
			}
			for _, id := range ids {
				if (args[0].Bound && args[0].Value.S != id) || (args[1].Bound && args[1].Value.S != name(id)) {
					continue
				}
				if err := emit([]ns.Value{ns.S(id), ns.S(name(id))}, nil); err != nil {
					return err
				}
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	g.Derived = map[string]cypher.PropSource{"name": {Relation: "short", Node: 0, Value: 1}}
	if err := g.Validate(v); err != nil {
		t.Fatal(err)
	}
	return v, b, g
}

// A property the host computes is an atom over its relation wherever it is read: matched by value,
// returned, tested, and absent where the relation names nothing. Other properties still read attr.
var namedCases = []compileCase{
	{`MATCH (f:Function {name: 'parse'})<-[:CALLS]-(g) RETURN g.name`,
		`node(?f, "Function"), edge(?g, "CALLS", ?f), short(?f, "parse"), short(?g, ?g_name) => ?g_name`,
		[][]string{{"main"}}},
	{`MATCH (f:Function) WHERE f.name ENDS WITH 'x' RETURN f`,
		`node(?f, "Function"), short(?f, ?f_name), str.suffix(?f_name, "x") => ?f`, [][]string{{"f:lex"}}},
	{`MATCH (f:Function) WHERE f.name IS NULL RETURN f`,
		`node(?f, "Function"), not short(?f, _) => ?f`, [][]string{{"f:help"}}},
	{`MATCH (f:Function) WHERE f.doc IS NOT NULL OR f.name = 'lex' RETURN f`,
		"cypher_match1(?f) :- node(?f, \"Function\"), attr(?f, \"doc\", _);\ncypher_match1(?f) :- node(?f, \"Function\"), short(?f, ?f_name), ?f_name = \"lex\";\n" +
			`cypher_match1(?f) => ?f`, [][]string{{"f:lex"}, {"f:main"}}},
	{`MATCH (f:Function) RETURN f.name + f.name AS twice`,
		`node(?f, "Function"), short(?f, ?f_name), ?_c = str.concat(?f_name, ?f_name) => ?_c`,
		[][]string{{"lexlex"}, {"mainmain"}, {"parseparse"}}},
	{`MATCH (x) WHERE x.name = 'a.go' RETURN x`,
		"cypher_node1(?n) :- node(?n, \"Function\");\ncypher_node1(?n) :- node(?n, \"File\");\n" +
			`short(?x, "a.go"), cypher_node1(?x) => ?x`, [][]string{{"file:a.go"}}},
}

func TestADerivedPropertyIsReadThroughItsRelation(t *testing.T) {
	v, b, g := namedBase(t)
	for _, c := range namedCases {
		check(t, v, b, g, c)
	}
	// A source's Fixed arguments are written in: here a doc read as its own property, attr(id, "doc", v).
	g.Derived["summary"] = cypher.PropSource{Relation: "attr", Node: 0, Value: 2, Fixed: map[int]string{1: "doc"}}
	check(t, v, b, g, compileCase{`MATCH (f:Function) RETURN f.summary`,
		`node(?f, "Function"), attr(?f, "doc", ?f_summary) => ?f_summary`, [][]string{{"starts it all"}}})
	// control: without Derived the same query reads attr, which names f:help too.
	g.Derived = nil
	check(t, v, b, g, compileCase{`MATCH (f:Function) WHERE f.name IS NULL RETURN f`,
		`node(?f, "Function"), not attr(?f, "name", _) => ?f`, nil})
}

// A label's own column wins over a Derived property: the guide's Package is keyed by its name.
func TestALabelsColumnWinsOverADerivedProperty(t *testing.T) {
	v, b := guide.Deps(t)
	g := depsGraph
	g.Derived = map[string]cypher.PropSource{"name": {Relation: "str.upper", Node: 0, Value: 1}, "loud": {Relation: "str.upper", Node: 0, Value: 1}}
	check(t, v, b, g, compileCase{`MATCH (p:Package {name: 'app'}) RETURN p.loud`,
		`pkg("app", _), str.upper("app", ?p_loud) => ?p_loud`, [][]string{{"APP"}}})
}

func TestAStoreCarriesItsDerivedProperties(t *testing.T) {
	v, b, want := namedBase(t)
	s := codeStore
	s.Derived = want.Derived
	g := s.Graph([]string{"Function", "File"}, []string{"CALLS", "CONTAINS"})
	p, err := cypher.Compile(v, g, `MATCH (f:Function) WHERE f.name IS NULL RETURN f`)
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := rowsOf(t, p, b); err != nil || len(rows) != 1 || rows[0][0] != "f:help" {
		t.Errorf("%s answered %v (%v), want f:help, which short doesn't name", p.Text, rows, err)
	}
}
