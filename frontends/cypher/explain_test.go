package cypher_test

import (
	"context"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// typed is a vocabulary whose arguments refuse values: a package's lines are a number and its tier one
// of two, and an edge's kind one of two, so a query can name what no data holds.
func typed(t *testing.T) (*ns.Vocabulary, *datalog.Base, cypher.Graph) {
	t.Helper()
	src := ns.NewMemSource().
		DeclareSchema("pkg", ns.Schema{Arity: 3, Labels: []string{"name", "loc", "tier"},
			Types: []ns.ArgType{{}, {Type: ns.TypeNumber}, {Domain: []string{"core", "edge"}}}}).
		DeclareSchema("dep", ns.Schema{Arity: 3, Labels: []string{"from", "kind", "to"},
			Types: []ns.ArgType{{}, {Domain: []string{"imports", "embeds"}}, {}}})
	for _, p := range [][3]string{{"app", "300", "edge"}, {"log", "150", "core"}, {"util", "100", "core"}} {
		src.Add("pkg", ns.Tuple{Vals: []ns.Value{ns.S(p[0]), ns.S(p[1]), ns.S(p[2])}})
	}
	for _, d := range [][2]string{{"app", "log"}, {"log", "util"}} {
		src.Add("dep", ns.Tuple{Vals: []ns.Value{ns.S(d[0]), ns.S("imports"), ns.S(d[1])}})
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
		Nodes: []cypher.Node{{Label: "Package", Relation: "pkg", Key: 0, Props: map[string]int{"name": 0, "loc": 1, "tier": 2}}},
		Edges: []cypher.Edge{
			{Type: "IMPORTS", Relation: "dep", From: 0, To: 2, Fixed: map[int]string{1: "imports"}},
			// a kind the Domain doesn't hold, so any program reading it fails its check
			{Type: "LINKS", Relation: "dep", From: 0, To: 2, Fixed: map[int]string{1: "links"}},
		},
	}
	return v, b, g
}

// A check error in the compiled program is placed at the Cypher that piece was written for, not at the
// program's text. control: the same error's place in the program is somewhere else entirely.
func TestACheckErrorIsLocatedInTheCypher(t *testing.T) {
	v, b, g := typed(t)
	for _, c := range []struct{ name, query, at string }{
		{"a comparison of unlike types", "MATCH (p:Package)\nWHERE p.loc > 'big' RETURN p", "p.loc > 'big'"},
		{"a value a Domain refuses, written inline", "MATCH (p:Package {tier: 'middle'}) RETURN p", "tier: 'middle'"},
		{"the equality, not the first node of its label", "MATCH (p:Package)-[:IMPORTS]->(q:Package)\nWHERE q.tier = 'middle' RETURN p", "q.tier = 'middle'"},
		{"text where a number goes", "MATCH (p:Package) WHERE p.loc = 'big' RETURN p.name", "p.loc = 'big'"},
		{"a helper rule, at its relationship", "MATCH (a:Package)-[:LINKS*]->(b:Package) RETURN a, b", "-[:LINKS*]->"},
		{"a relationship", "MATCH (a:Package)-[:LINKS]->(b:Package) RETURN a, b", "-[:LINKS]->"},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, err := cypher.Compile(v, g, c.query)
			if err != nil {
				t.Fatal(err)
			}
			_, err = datalog.SemiNaive{}.Eval(context.Background(), p.Query, b)
			if err == nil {
				t.Fatalf("%s ran", p.Text)
			}
			pos, ok := p.Locate(err)
			want := strings.Index(c.query, c.at)
			if !ok || pos.Offset != want {
				t.Fatalf("Locate = %+v %v, want offset %d (%q) in\n%s\nfor %v (program %s)", pos, ok, want, c.at, c.query, err, p.Text)
			}
			if jl, ok := datalog.Locate(p.Text, err); !ok || jl.Offset == pos.Offset && p.Text != c.query {
				t.Logf("control: the program's own place is %+v %v", jl, ok)
			}
		})
	}
}

// Explain says it in the query's terms: the Cypher name for a variable, the query's line with a caret,
// the fix. A run error with no place still explains, without a caret.
func TestExplainSpeaksCypher(t *testing.T) {
	v, b, g := typed(t)
	q := "MATCH (p:Package)\nWHERE p.loc > 'big' RETURN p"
	p, err := cypher.Compile(v, g, q)
	if err != nil {
		t.Fatal(err)
	}
	_, err = datalog.SemiNaive{}.Eval(context.Background(), p.Query, b)
	got := cypher.Explain(q, p, err)
	want := "query: p.loc cannot be compared with \"big\": it is a number (pkg's \"loc\" argument)\nline 2, column 7:\n  WHERE p.loc > 'big' RETURN p\n        ^\n"
	if !strings.HasPrefix(got, want) {
		t.Errorf("Explain =\n%s\nwant it to start\n%s", got, want)
	}
	if !strings.Contains(err.Error(), "?p_loc") {
		t.Errorf("control: the engine's own message names %q, not the jaala variable", err.Error())
	}

	q = "MATCH (p:Package {tier: 'middle'}) RETURN p"
	p, _ = cypher.Compile(v, g, q)
	_, err = datalog.SemiNaive{}.Eval(context.Background(), p.Query, b)
	if got := cypher.Explain(q, p, err); !strings.Contains(got, "fix: use one of: core, edge") && !strings.Contains(got, "fix: did you mean") {
		t.Errorf("Explain of a Domain refusal has no fix:\n%s", got)
	}

	q = "MATCH (a:Package)-[:IMPORTS*]->(b:Package) RETURN a, b"
	p, _ = cypher.Compile(v, g, q)
	_, err = datalog.SemiNaive{}.Eval(context.Background(), p.Query, b, datalog.Budget(2))
	if got := cypher.Explain(q, p, err); !strings.Contains(got, "budget") || strings.Contains(got, "^") {
		t.Errorf("Explain of a spent budget:\n%s", got)
	}

	_, err = cypher.Compile(v, g, "MATCH (p:Package) OPTIONAL MATCH (p)-[:IMPORTS]->(q) RETURN p")
	if got := cypher.Explain("MATCH (p:Package) OPTIONAL MATCH (p)-[:IMPORTS]->(q) RETURN p", cypher.Program{}, err); !strings.Contains(got, "jaala#282") ||
		!strings.Contains(got, "line 1, column 19:") || !strings.Contains(got, "Write the query without it") {
		t.Errorf("Explain of a gap:\n%s", got)
	}
}
