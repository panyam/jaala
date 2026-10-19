package bolt

import (
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/internal/guide"
	"github.com/panyam/jaala/ns"
)

// repo is a code graph in Declaire's shape, big enough that a whole read and a lookup cost visibly
// different amounts: n functions in files of fifty, each calling three later ones, every tenth with
// a doc.
func repo(n int) (cypher.Graph, *ns.MemSource) {
	src := ns.NewMemSource().
		DeclareSchema("node", ns.Schema{Arity: 2, Labels: []string{"id", "kind"}, Types: []ns.ArgType{{Kind: "node"}, {Type: ns.TypeString}}}).
		DeclareSchema("edge", ns.Schema{Arity: 3, Labels: []string{"from", "kind", "to"}, Types: []ns.ArgType{{Kind: "node"}, {Type: ns.TypeString}, {Kind: "node"}}}).
		DeclareSchema("attr", ns.Schema{Arity: 3, Labels: []string{"id", "key", "value"}, Types: []ns.ArgType{{Kind: "node"}, {Type: ns.TypeString}}})
	rnd := rand.New(rand.NewSource(1))
	f := func(i int) ns.Value { return ns.S(fmt.Sprintf("f%d", i)) }
	for i := range n {
		src.Add("node", ns.Tuple{Vals: []ns.Value{f(i), ns.S("Function")}})
		src.Add("attr", ns.Tuple{Vals: []ns.Value{f(i), ns.S("file"), ns.S(fmt.Sprintf("file%02d.go", i/50))}})
		if i%10 == 0 {
			src.Add("attr", ns.Tuple{Vals: []ns.Value{f(i), ns.S("doc"), ns.S(fmt.Sprintf("does %d", i))}})
		}
		called := map[int]bool{} // a call once: the store holds a set of facts, as a change set does
		for range 3 {
			if j := i + 1 + rnd.Intn(max(1, min(40, n-i-1))); j < n && !called[j] {
				called[j] = true
				src.Add("edge", ns.Tuple{Vals: []ns.Value{f(i), ns.S("calls"), f(j)}})
			}
		}
	}
	return cypher.Store{Node: "node", Edge: "edge", Attr: "attr"}.Graph([]string{"Function"}, []string{"calls"}), src
}

// The mission's exercise (#420): the guide's packages and a code graph, each kept in every server
// named, asked their questions over the server and over memory. Every row has to say same, and
// what the server shipped for a bound question is well under what it holds. Then a session: an edit
// over the server answers as over memory, and once applied, the server alone does.
//
// make exercise-graph-server runs it with -v, which prints the table.
func TestExercise(t *testing.T) {
	type question struct{ name, text string }
	graphs := []struct {
		name      string
		g         cypher.Graph
		src       *ns.MemSource
		vocab     func(testing.TB, ns.Source) *ns.Vocabulary
		questions []question
	}{
		{"guide", cypher.Graph{
			Nodes: []cypher.Node{{Label: "Package", Relation: "pkg", Key: 0, Props: map[string]int{"license": 1}}},
			Edges: []cypher.Edge{{Type: "IMPORTS", Relation: "imports", From: 0, To: 1}},
		}, guide.Source(), guide.Vocabulary, []question{
			{"direct", `imports("app", ?p) => ?p`},
			{"transitive", `deps.depends_on("app", ?p) => ?p`},
			{"license", `pkg("util", ?l) => ?l`},
			{"unstated", `pkg(?p, ?l), absent(?l) => ?p`},
			{"count", `deps.depends_on("app", ?p) => count(?p)`},
			{"reverse", `imports(?p, "util") => ?p`},
			{"columns", `pkg(?p, ?l) => ?p, ?l`},
		}},
		{"repo", cypher.Graph{}, nil, vocabulary, []question{
			{"callees", `edge("f17", "calls", ?g) => ?g`},
			{"two-hops", `edge("f17", "calls", ?m), edge(?m, "calls", ?g) => ?g`},
			{"reach", `calls(?a, ?b) :- edge(?a, "calls", ?b); reach(?a, ?b) :- calls(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), calls(?b, ?c); reach("f1900", ?g) => count(?g)`},
			{"callers-docs", `edge(?f, "calls", "f1500"), attr(?f, "doc", ?d) => ?f, ?d`},
			{"file-sizes", `attr(?f, "file", ?file) => ?file, count(?f) order by ?file limit 5`},
			{"page", `node(?f, "Function") => ?f order by ?f limit 10`},
			{"edges", `edge(?a, "calls", ?b) => count(?a)`},
		}},
	}
	graphs[1].g, graphs[1].src = repo(2000)
	for _, sv := range servers(t) {
		var table []string
		for _, gr := range graphs {
			s := open(t, sv.driver, gr.g, schemasOf(gr.src))
			if err := s.Apply(bg, load(gr.src)); err != nil {
				t.Fatal(err)
			}
			held := 0
			for _, rel := range gr.src.Relations() {
				held += len(gr.src.Tuples(rel))
			}
			for _, qq := range gr.questions {
				q, err := datalog.Parse(qq.text)
				if err != nil {
					t.Fatalf("%s: %v", qq.name, err)
				}
				want, err := datalog.SemiNaive{}.Eval(bg, q, mustBase(t, gr.vocab(t, gr.src), gr.src))
				if err != nil {
					t.Fatalf("%s over memory: %v", qq.name, err)
				}
				st := &counting{Store: s}
				got, err := datalog.SemiNaive{}.Eval(bg, q, mustBase(t, gr.vocab(t, st), st))
				same := "same"
				if err != nil || !slices.Equal(rows(got), rows(want)) {
					same = "DIFFERS"
					t.Errorf("%s %s/%s: bolt answers %q (%v), memory %q", sv.name, gr.name, qq.name, rows(got), err, rows(want))
				}
				table = append(table, fmt.Sprintf("  %-9s %-6s %-13s %-7s %4d rows %5d lookups %4d batches %3d reads %6d shipped of %d",
					sv.name, gr.name, qq.name, same, len(got), st.lookups, st.batches, st.whole, st.shipped, held))
			}
		}
		// A session: edit the code graph over the server, then commit it.
		_, src := graphs[1].g, graphs[1].src
		s := open(t, sv.driver, graphs[1].g, schemasOf(src))
		if err := s.Apply(bg, load(src)); err != nil {
			t.Fatal(err)
		}
		var c ns.Changes
		callees, _ := s.Lookup(bg, "edge", map[int]ns.Value{0: ns.S("f17")})
		c.Remove("edge", callees[0]).Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("f17"), ns.S("calls"), ns.S("f1999")}, Cites: []string{"session:1"}})
		q, _ := datalog.Parse(`edge("f17", "calls", ?g) => ?g`)
		ask := func(src ns.Source) []string {
			rs, err := datalog.SemiNaive{}.Eval(bg, q, mustBase(t, vocabulary(t, src), src))
			if err != nil {
				t.Fatal(err)
			}
			return rows(rs)
		}
		want := ask(ns.Overlay(src, c))
		overlay := ask(ns.Overlay(s, c))
		if err := s.Apply(bg, c); err != nil {
			t.Fatal(err)
		}
		committed := ask(s)
		session := "same"
		if !slices.Equal(overlay, want) || !slices.Equal(committed, want) || slices.Equal(want, ask(src)) {
			session = "DIFFERS"
			t.Errorf("%s session: overlay over bolt %q, committed %q, over memory %q, before %q", sv.name, overlay, committed, want, ask(src))
		}
		table = append(table, fmt.Sprintf("  %-9s %-6s %-13s %-7s overlay, commit, ask again", sv.name, "repo", "session", session))
		t.Logf("%s:\n%s", sv.name, strings.Join(table, "\n"))
	}
}

func mustBase(t testing.TB, v *ns.Vocabulary, src ns.Source) *datalog.Base {
	t.Helper()
	b, err := datalog.NewBase(v, src)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
