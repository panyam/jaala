package bolt

import (
	"fmt"
	"math/rand"
	"slices"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/internal/corpus"
	"github.com/panyam/jaala/ns"
)

// goals are questions a store answers whole: counts, a count per group, a sum over numbers in units,
// pages by id with and without a cursor, text comparisons, and joins it pushes. The corpus's own
// goals rarely qualify, as datalog's goalQuestions found.
var goals = []string{
	`edge(?a, ?b) => count(?a)`,
	`edge(?a, ?b) => ?a, count(?b)`,
	`edge(?a, ?b), edge(?b, ?c) => ?a, count(?c)`,
	`edge(?a, ?b), weight(?b, ?w) => ?a, sum(?w)`,
	`weight(?n, ?w) => ?w, count(?n)`,
	`node(?n) => ?n order by ?n limit 3`,
	`node(?n) => ?n order by ?n desc limit 2`,
	`edge(?a, ?b) => ?a, ?b order by ?a, ?b limit 4`,
	`edge(?a, ?b) => ?a, ?b order by ?b desc, ?a limit 3`,
	`node(?n) => ?n order by ?n after "v3" limit 2`,
	`edge(?a, ?b) => ?a, ?b order by ?a desc, ?b after "v4", "v2" limit 3`,
	`edge("v0", ?b), edge(?b, ?c) => ?c order by ?c limit 2`,
	`edge(?a, ?b), ?a != ?b => ?a, ?b`,
	`edge(?a, ?b), ?a = "v1" => ?b`,
	`edge(?a, ?b), edge(?b, ?a) => ?a, ?b`,
	`edge(?a, ?a) => ?a`,
	`edge(?a, ?b), not edge(?b, ?a) => ?a, ?b`,
}

// Every goal over every seed's graph answers over the server as over memory, through the planned
// SemiNaive, which pushes, summarizes and pages; the control is that it did each on some seed.
func TestGoalsTheServerAnswersWholeAgree(t *testing.T) {
	seeds := envInt("JAALA_BOLT_GOAL_SEEDS", 40)
	for _, sv := range servers(t) {
		t.Run(sv.name, func(t *testing.T) {
			var pushes, summaries, pages int
			for seed := int64(1); seed <= int64(seeds); seed++ {
				facts := corpus.Graph(rand.New(rand.NewSource(seed)))
				mem := facts.Source()
				s := open(t, sv.driver, corpusGraph, schemasOf(mem))
				if err := s.Apply(bg, load(mem)); err != nil {
					t.Fatal(err)
				}
				for _, text := range goals {
					q, err := datalog.Parse(text)
					if err != nil {
						t.Fatalf("%s: %v", text, err)
					}
					want, werr := (datalog.Naive{}).Eval(bg, q, base(t, mem))
					st := &counting{Store: s}
					got, err := datalog.SemiNaive{}.Eval(bg, q, base(t, st))
					if fmt.Sprint(err) != fmt.Sprint(werr) || !slices.Equal(rows(got), rows(want)) {
						t.Fatalf("seed %d, %s: over bolt %q (%v), over memory %q (%v)\n facts: %s", seed, text, rows(got), err, rows(want), werr, facts)
					}
					pushes, summaries, pages = pushes+st.pushes, summaries+st.summaries, pages+st.pages
				}
			}
			t.Logf("%d seeds × %d goals: %d pushes, %d summaries, %d pages", seeds, len(goals), pushes, summaries, pages)
			if pushes == 0 || summaries == 0 || pages == 0 {
				t.Errorf("control: %d pushes, %d summaries and %d pages, and each should be some", pushes, summaries, pages)
			}
		})
	}
}

// A page orders ids as jaala does, by their bytes, on a server that orders strings otherwise: Neo4j
// puts 😀 (a surrogate pair in UTF-16) before U+FFFD, where jaala and Memgraph put it after. The
// control is that the page went to the server.
func TestAPageOrdersIdsAsJaalaDoes(t *testing.T) {
	src := ns.NewMemSource().DeclareSchema("node", ns.Schema{Arity: 1, Labels: []string{"name"}, Types: []ns.ArgType{{Kind: "node"}}})
	for _, id := range []string{"Z", "😀", "�", "a", "é", "\U0001F680"} {
		src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(id)}})
	}
	g := cypher.Graph{Nodes: []cypher.Node{{Label: "Node", Relation: "node", Key: 0}}}
	for _, sv := range servers(t) {
		t.Run(sv.name, func(t *testing.T) {
			s := open(t, sv.driver, g, schemasOf(src))
			if err := s.Apply(bg, load(src)); err != nil {
				t.Fatal(err)
			}
			pages := 0
			for _, text := range []string{
				`node(?n) => ?n order by ?n limit 4`,
				`node(?n) => ?n order by ?n desc limit 3`,
				"node(?n) => ?n order by ?n after \"�\" limit 2",
				`node(?n) => ?n order by ?n desc after "😀" limit 2`,
			} {
				q, err := datalog.Parse(text)
				if err != nil {
					t.Fatal(err)
				}
				want, _ := (datalog.Naive{}).Eval(bg, q, base(t, src))
				st := &counting{Store: s}
				got, err := datalog.SemiNaive{}.Eval(bg, q, base(t, st))
				if err != nil || !slices.Equal(rows(got), rows(want)) {
					t.Errorf("%s: over bolt %q (%v), over memory %q", text, rows(got), err, rows(want))
				}
				pages += st.pages
			}
			if pages == 0 {
				t.Error("control: no page went to the server")
			}
		})
	}
}

// codeGoals reach what the corpus's graph can't: a constant at a label's fixed kind, a number
// constant (01 is 1), a join over numbers (0 and -0), a group over spellings, an absent weight, and
// a count whose number constant the server can't match exactly, so it isn't summarized there.
var codeGoals = []string{
	`edge(?a, "calls", ?b, _), edge(?b, "calls", ?c, _) => ?a, ?c`,
	`edge(?a, "calls", ?b, ?w) => ?a, sum(?w)`,
	`edge(?a, "calls", ?b, 1) => ?a, ?b`,
	`edge(?a, "calls", ?b, 1) => count(?a)`,
	`edge(?a, ?k, ?b, ?w), edge(?c, ?k, ?d, ?w) => ?a, ?c`,
	`size(?f, ?n), size(?g, ?n) => ?f, ?g`,
	`attr(?f, "doc", ?d) => ?d, count(?f)`,
	`node(?f, "Function"), not attr(?f, "doc", _) => ?f`,
	`node(?f, "Function"), attr(?f, "doc", ?d) => ?f, ?d order by ?f limit 1`,
	`edge("app", "contains", ?m, ?w), node(?m, ?k) => ?m, ?k, ?w`,
	`size(?f, 1500) => ?f`,
	`size(?f, 1500), node(?f, ?k) => ?f, ?k`,
	`attr(?f, "volts", ?v), node(?f, "Function") => ?v, count(?f)`,
	`tag(?f, ?t), tag(?g, ?t) => ?f, ?g`,
	`tag(?f, ?t), ?t != "entry" => ?f`,
	`tag(?f, ?t), tag(?g, ?u), ?t = ?u => ?f, ?g`,
	`edge(?a, "calls", ?b, _) => ?b order by ?b limit 1`,
	`edge(?a, "calls", ?b, _) => ?a, ?b order by ?b desc, ?a limit 2`,
}

func TestCodeGoalsAgree(t *testing.T) {
	for _, sv := range servers(t) {
		t.Run(sv.name, func(t *testing.T) {
			g, src := code()
			s := open(t, sv.driver, g, schemasOf(src))
			if err := s.Apply(bg, load(src)); err != nil {
				t.Fatal(err)
			}
			st := &counting{Store: s}
			for _, text := range codeGoals {
				q, err := datalog.Parse(text)
				if err != nil {
					t.Fatalf("%s: %v", text, err)
				}
				want, werr := (datalog.Naive{}).Eval(bg, q, base(t, src))
				got, err := datalog.SemiNaive{}.Eval(bg, q, base(t, st))
				if fmt.Sprint(err) != fmt.Sprint(werr) || !slices.Equal(rows(got), rows(want)) {
					t.Errorf("%s: over bolt %q (%v), over memory %q (%v)", text, rows(got), err, rows(want), werr)
				}
			}
			if st.pushes == 0 || st.summaries == 0 || st.pages == 0 {
				t.Errorf("control: %d pushes, %d summaries and %d pages, and each should be some", st.pushes, st.summaries, st.pages)
			}
		})
	}
}

// A push answering several bindings sends each match once, though it matches more than one of them:
// a binding the server can't filter by (a number) asks for every match, as the contract allows, but
// a row repeated would count twice.
func TestAPushSendsAMatchOnce(t *testing.T) {
	for _, sv := range servers(t) {
		t.Run(sv.name, func(t *testing.T) {
			g, src := code()
			s := open(t, sv.driver, g, schemasOf(src))
			if err := s.Apply(bg, load(src)); err != nil {
				t.Fatal(err)
			}
			kind := ns.S("calls")
			calls := &kind
			c := ns.Conjunction{
				Atoms: []ns.PushAtom{
					{Relation: "edge", Args: []ns.PushArg{{Var: 1}, {Const: calls}, {Var: 2}, {Var: 3}}},
					{Relation: "edge", Args: []ns.PushArg{{Var: 2}, {Const: calls}, {Var: 4}, {}}},
				},
				Inputs:   []int{3},
				Bindings: [][]ns.Value{{ns.N(2)}, {ns.N(1)}, {ns.N(0)}},
			}
			if !s.Pushes(c) {
				t.Fatal("control: the store doesn't push the conjunction")
			}
			seen, n := map[string]bool{}, 0
			for row, err := range s.Push(bg, c) {
				if err != nil {
					t.Fatal(err)
				}
				n++
				if k := rowKey(row); seen[k] {
					t.Errorf("a match came back twice: %q", facts(row))
				} else {
					seen[k] = true
				}
			}
			if n == 0 {
				t.Error("control: the push answered nothing")
			}
		})
	}
}

// A push never leaves out a match over a number constant the server can't compare as jaala does:
// the conjunction's 1500 V is the 1.5 kV the store holds, read folded, so the server must return it
// (or more) rather than match the number itself.
func TestAPushNeverLeavesOutAMatchOnANumber(t *testing.T) {
	for _, sv := range servers(t) {
		t.Run(sv.name, func(t *testing.T) {
			g, src := code()
			s := open(t, sv.driver, g, schemasOf(src))
			if err := s.Apply(bg, load(src)); err != nil {
				t.Fatal(err)
			}
			folded, kind := ns.NU(1500, "V"), ns.S("calls")
			c := ns.Conjunction{Atoms: []ns.PushAtom{
				{Relation: "size", Args: []ns.PushArg{{Var: 1}, {Const: &folded}}},
				{Relation: "edge", Args: []ns.PushArg{{Var: 2}, {Const: &kind}, {Var: 1}, {}}},
			}}
			if !s.Pushes(c) {
				t.Fatal("control: the store doesn't push the conjunction")
			}
			found := false
			for row, err := range s.Push(bg, c) {
				if err != nil {
					t.Fatal(err)
				}
				found = found || row[0].Vals[0].S == "parse"
			}
			if !found {
				t.Error("the push left out parse's size, 1.5 kV, which is the constant's 1500 V folded")
			}
		})
	}
}
