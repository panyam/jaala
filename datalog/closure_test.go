package datalog

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// cyclicEdges is a graph with a cycle and two paths of one length: a->b->c->a, a->d->c, then c->e->f->g.
// a->d comes first, so a walk out from a finds c through d before the path through b, which sorts
// first.
var cyclicEdges = [][2]string{{"a", "d"}, {"a", "b"}, {"b", "c"}, {"c", "a"}, {"d", "c"}, {"c", "e"}, {"e", "f"}, {"f", "g"}}

func cyclicGraph() *ns.MemSource {
	src := ns.NewMemSource().Declare("edge", "from", "to").Declare("link", "from", "to").Declare("node", "name")
	for _, e := range cyclicEdges {
		src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(e[0]), ns.S(e[1])}, Cites: []string{e[0] + "-" + e[1]}})
	}
	for _, n := range "abcdefg" {
		src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(string(n))}})
	}
	return src
}

// walkLengths is, for each node, the fewest steps of a walk from start of lo to hi steps over
// cyclicEdges (hi 0: any), by counting walks of each length, written apart from the engine's search.
func walkLengths(start string, lo, hi int) map[string]int {
	if hi == 0 {
		hi = lo + 8 // past every state of the seven-node graph
	}
	out := map[string]int{}
	at := map[string]bool{start: true}
	for l := 0; l <= hi; l++ {
		if l >= lo {
			for n := range at {
				if _, ok := out[n]; !ok {
					out[n] = l
				}
			}
		}
		next := map[string]bool{}
		for n := range at {
			for _, e := range cyclicEdges {
				if e[0] == n {
					next[e[1]] = true
				}
			}
		}
		at = next
	}
	return out
}

func closureAnswer(rows []Row, col string) map[string]int {
	out := map[string]int{}
	for _, r := range rows {
		out[r.Bind[Var(col)].S] = int(*r.Bind["d"].Num)
	}
	return out
}

// Out from a bound start, back from a bound end, and over a derived relation, each pair of bounds gives
// the walks a count of walks gives, at their fewest steps, in every evaluator and under
// CanonicalCites (both and agree check those).
func TestClosureAgreesWithCountingWalks(t *testing.T) {
	src := cyclicGraph()
	b := baseFor(std(src))
	for _, start := range []string{"a", "c", "g"} {
		for _, bounds := range [][2]int{{1, 1}, {1, 2}, {1, 3}, {1, 0}, {0, 2}, {2, 0}, {3, 4}, {4, 4}} {
			want := walkLengths(start, bounds[0], bounds[1])
			for _, q := range []string{
				fmt.Sprintf(`graph.closure("edge", %q, ?y, %d, %d, ?d) => ?y, ?d`, start, bounds[0], bounds[1]),
				fmt.Sprintf(`step(?a, ?b) :- edge(?a, ?b); step(?a, ?b) :- link(?a, ?b); graph.closure("step", %q, ?y, %d, %d, ?d) => ?y, ?d`, start, bounds[0], bounds[1]),
			} {
				rows, err := both(mustParse(t, q), b, Witnesses())
				if err != nil {
					t.Fatalf("%s: %v", q, err)
				}
				if got := closureAnswer(rows, "y"); !reflect.DeepEqual(got, want) {
					t.Errorf("%s: %v, want %v", q, got, want)
				}
			}
			// back from each node the walks reach: start is among what reaches it, at the same length
			for y, d := range want {
				q := fmt.Sprintf(`graph.closure("edge", ?x, %q, %d, %d, ?d) => ?x, ?d`, y, bounds[0], bounds[1])
				rows, err := both(mustParse(t, q), b)
				if err != nil {
					t.Fatalf("%s: %v", q, err)
				}
				if got := closureAnswer(rows, "x"); got[start] != d {
					t.Errorf("%s: %s at %d, want %d (all: %v)", q, start, got[start], d, got)
				}
			}
		}
	}
}

// Each answer cites a path of its fewest steps, in order from ?from to ?to, and its witness has a child
// per step; under CanonicalCites it is the path whose steps come first, whichever end was bound.
func TestClosureCitesAShortestPathInOrder(t *testing.T) {
	b := baseFor(std(cyclicGraph()))
	for _, q := range []string{
		`graph.closure("edge", "a", "f", 1, 0, ?d)`,
		`graph.closure("edge", ?x, "f", 1, 0, ?d), node(?x), ?x = "a"`,
		`node(?x), ?x = "a", graph.closure("edge", ?x, ?y, 1, 0, ?d), ?y = "f"`,
	} {
		rows, err := both(mustParse(t, q), b, Witnesses(), CanonicalCites())
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s: %v %v", q, rows, err)
		}
		var w *Witness
		for _, n := range rows[0].Witness {
			if n.Relation == closureName {
				w = n
			}
		}
		if w == nil {
			t.Fatalf("%s: no closure witness in %v", q, rows[0].Witness)
		}
		if want := []string{"a-b", "b-c", "c-e", "e-f"}; !reflect.DeepEqual(w.Cites, want) {
			t.Errorf("%s: path %v, want %v (a-b sorts before a-d)", q, w.Cites, want)
		}
		if len(w.Children) != 4 || w.Children[1].Relation != "edge" || w.Children[1].Values[0].S != "b" {
			t.Errorf("%s: children %v, want a leaf per step, b-c second", q, w.Children)
		}
		if rows[0].Bind["d"].S != "4" {
			t.Errorf("%s: ?d = %s, want 4", q, rows[0].Bind["d"].S)
		}
	}
}

// A bounded closure stops at its bound: on a 40-long chain, the work of three steps out doesn't grow
// with the chain, where no bound walks it all.
func TestClosureDoesNoWorkPastItsBound(t *testing.T) {
	chain := func(n int) ns.Source {
		src := ns.NewMemSource().Declare("edge", "from", "to")
		for i := 0; i < n; i++ {
			src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprint("n", i)), ns.S(fmt.Sprint("n", i+1))}})
		}
		return src
	}
	bounded := `graph.closure("edge", "n0", ?y, 1, 3, ?d) => ?y`
	short, long := workOf(t, SemiNaive{}, chain(40), bounded), workOf(t, SemiNaive{}, chain(160), bounded)
	if short != long {
		t.Errorf("three steps out cost %d on a 40-long chain and %d on a 160-long one, want the same", short, long)
	}
	if all := workOf(t, SemiNaive{}, chain(40), `graph.closure("edge", "n0", ?y, 1, 0, ?d) => ?y`); all < 4*long {
		t.Errorf("control: the whole 40-long walk cost %d, three steps %d", all, long)
	}
}

func TestClosureRefusesWhatItCantWalk(t *testing.T) {
	src := cyclicGraph()
	for _, c := range []struct{ q, want string }{
		{`graph.closure(?r, "a", ?y, 1, 2, ?d)`, `query: graph.closure's relation must be a text constant naming one, not ?r`},
		{`graph.closure("edge", "a", ?y, 1, 2)`, `query: graph.closure takes 6 args, got 5`},
		{`graph.closure("edgy", "a", ?y, 1, 2, ?d)`, `query: graph.closure over unknown relation "edgy"`},
		{`graph.closure("node", "a", ?y, 1, 2, ?d)`, `query: graph.closure needs a relation of 2 args, from and to; node takes 1`},
		{`graph.closure("str.contains", "a", ?y, 1, 2, ?d)`, `query: graph.closure can't walk str.contains, a predicate: it walks a relation's facts`},
		{`graph.closure("edge", "a", ?y, 1.5, 2, ?d)`, `query: graph.closure's min must be a whole number of steps, 0 or more, not 1.5`},
		{`graph.closure("edge", "a", ?y, 3, 2, ?d)`, `query: graph.closure's max must be a whole number of steps, 0 for no bound or at least min, not 2`},
		{`graph.closure("edge", "a", ?y, ?lo, 2, ?d)`, `nothing binds what graph.closure needs first`},
		{`r(?x) :- node(?x), graph.closure("s", ?x, _, 1, 0, _); s(?a, ?b) :- edge(?a, ?b), r(?a); r(?x)`, `query: rules are not stratifiable (recursion through graph.closure: r, s)`},
		{`next(?a, ?b) :- ?b = ?a + 1; graph.closure("next", 1, ?y, 1, 3, _)`, `query: graph.closure can't walk next, which takes an argument from its caller`},
	} {
		err := evalErr(src, c.q)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s:\n err  %v\n want %s", c.q, err, c.want)
			continue
		}
		if verr := Validate(mustParse(t, c.q), std(src)); verr == nil || verr.Error() != err.Error() {
			t.Errorf("%s: Validate says %v, Eval %v", c.q, verr, err)
		}
		var ce *CheckError
		if !errors.As(err, &ce) {
			t.Errorf("%s: %T is no CheckError", c.q, err)
		}
	}
}

// A module's rules walk its own relation by its bare name, and a query walks it by its path.
func TestClosureWalksAModuleRelation(t *testing.T) {
	v := std(cyclicGraph())
	if err := v.AddModule("deps", LanguageName, `next(?a, ?b) :- edge(?a, ?b); far(?a, ?b) :- node(?a), graph.closure("next", ?a, ?b, 3, 3, _);`, ""); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`deps.far("a", ?y) => ?y`, `graph.closure("deps.next", "a", ?y, 3, 3, _) => ?y`} {
		rows, err := both(mustParse(t, q), baseFor(v))
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if got, want := col(rows, "y"), "a,e"; got != want {
			t.Errorf("%s = %s, want %s", q, got, want)
		}
	}
}

// With neither end bound a closure walks from every value a step leads from, and with min 0 from every
// value one leads to as well, so each pair is the walk from its ?from.
func TestClosureWithNeitherEndBoundWalksFromEveryStart(t *testing.T) {
	b := baseFor(std(cyclicGraph()))
	for _, bounds := range [][2]int{{1, 1}, {1, 0}, {0, 2}, {2, 3}} {
		q := fmt.Sprintf(`graph.closure("edge", ?x, ?y, %d, %d, ?d) => ?x, ?y, ?d`, bounds[0], bounds[1])
		rows, err := both(mustParse(t, q), b)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		got := map[string]map[string]int{}
		for _, r := range rows {
			x := r.Bind["x"].S
			if got[x] == nil {
				got[x] = map[string]int{}
			}
			got[x][r.Bind["y"].S] = int(*r.Bind["d"].Num)
		}
		for _, x := range []string{"a", "b", "c", "d", "e", "f", "g"} {
			want := walkLengths(x, bounds[0], bounds[1])
			if len(want) == 0 {
				want = nil
			}
			if !reflect.DeepEqual(got[x], want) {
				t.Errorf("%s: from %s %v, want %v", q, x, got[x], want)
			}
		}
	}
}

// A path can hold in no one version though each step holds in some, so Across refuses a closure rather
// than answer one.
func TestClosureIsRefusedAcrossVersions(t *testing.T) {
	b := baseFor(std(versionedGraph()))
	_, err := SemiNaive{}.Eval(bg, mustParse(t, `graph.closure("edge", "a", ?x, 1, 0, _) => ?x`), b, Across(ns.Span(0, 3)))
	if err == nil || !strings.Contains(err.Error(), "query: graph.closure isn't evaluated across versions") {
		t.Errorf("err = %v", err)
	}
	if rows, err := (SemiNaive{}).Eval(bg, mustParse(t, `graph.closure("edge", "a", ?x, 1, 0, _) => ?x`), b); err != nil || len(rows) == 0 {
		t.Errorf("control: without Across it answers %v, %v", rows, err)
	}
}

// Explain names a closure as it was written, and counts its walk against it.
func TestExplainShowsAClosureAsWritten(t *testing.T) {
	var r Report
	b := baseFor(std(cyclicGraph()))
	if _, err := (SemiNaive{}).Eval(bg, mustParse(t, `graph.closure("edge", "a", ?y, 1, 2, ?d) => ?y`), b, Explain(&r)); err != nil {
		t.Fatal(err)
	}
	text := r.String()
	if !strings.Contains(text, `graph.closure("edge", "a", ?y, 1, 2, ?d)`) || strings.Contains(text, "\x00") {
		t.Errorf("report:\n%s", text)
	}
}

// A closure over a relation whose rules each read one base relation walks those atoms, so it reads
// only what the walk reaches rather than deriving its relation over the whole chain first.
// control: witnessed, the relation is derived (a witness shows its node), and costs the chain.
func TestClosureWalksAOneAtomRelationThroughItsAtom(t *testing.T) {
	chain := ns.NewMemSource().Declare("edge", "from", "to")
	for i := 0; i < 160; i++ {
		chain.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprint("n", i)), ns.S(fmt.Sprint("n", i+1))}})
	}
	q := `step(?a, ?b) :- edge(?a, ?b); graph.closure("step", "n0", ?y, 1, 3, ?d) => ?y`
	direct := workOf(t, SemiNaive{}, chain, `graph.closure("edge", "n0", ?y, 1, 3, ?d) => ?y`)
	if got := workOf(t, SemiNaive{}, chain, q); got != direct {
		t.Errorf("through step: work %d, want the %d of walking edge itself", got, direct)
	}
	b := baseFor(std(chain))
	if _, err := (SemiNaive{}).Eval(bg, mustParse(t, q), b, Witnesses()); err != nil {
		t.Fatal(err)
	}
	if b.Work() < 160 {
		t.Errorf("control: witnessed, work %d, which should derive step's 160 tuples", b.Work())
	}
	if rows := eval(t, chain, q); col(rows, "y") != "n1,n2,n3" {
		t.Errorf("answers %s", col(rows, "y"))
	}
}

// A closure whose one end the goal binds walks from that end, though a relation could bind the other:
// node(?t) first, or the closure first with its constants counted as bound, walks out from each of
// 161 nodes.
func TestClosureRunsFromItsBoundEnd(t *testing.T) {
	src := ns.NewMemSource().Declare("edge", "from", "to").Declare("node", "name")
	for i := 0; i <= 160; i++ {
		src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprint("n", i))}})
		if i < 160 {
			src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprint("n", i)), ns.S(fmt.Sprint("n", i+1))}})
		}
	}
	q := `node(?t), graph.closure("edge", ?t, ?h, 1, 0, _), ?h = "n5" => ?t`
	if got := workOf(t, SemiNaive{}, src, q); got > 40 {
		t.Errorf("work %d, want the walk back from n5's few steps", got)
	}
	if rows := eval(t, src, q); col(rows, "t") != "n0,n1,n2,n3,n4" {
		t.Errorf("answers %s", col(rows, "t"))
	}
}

// A closure or paths call with neither end bound starts from every value its step leads from, read
// through the step's walked rules when walkSteps walks them (#438: the planned SemiNaive read the
// renamed step itself and erred unknown relation "\x00w:s"; Cypher writes this for any variable-length
// match from an unlabelled node). control: with ?a bound first, all three answered before.
func TestAnUnboundClosureOverAWalkedStepStartsFromItsRules(t *testing.T) {
	src := ns.NewMemSource().Declare("edge", "from", "kind", "to").Declare("node", "id")
	for _, e := range [][3]string{{"a", "calls", "b"}, {"b", "calls", "c"}, {"c", "calls", "d"}, {"a", "imports", "d"}} {
		src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(e[0]), ns.S(e[1]), ns.S(e[2])}})
	}
	for _, n := range []string{"a", "b", "c", "d"} {
		src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(n)}})
	}
	v := std(src)
	const step = `s(?x, ?y) :- edge(?x, "calls", ?y); `
	for _, c := range []struct{ goal, col, want string }{
		{`graph.closure("s", ?a, ?b, 1, 3, _) => ?a, ?b`, "b", "b,c,c,d,d,d"},
		{`graph.paths("s", ?a, ?b, "trail", 1, 3, ?p) => ?a, ?b, ?p`, "b", "b,c,c,d,d,d"},
		{`node(?a), graph.closure("s", ?a, ?b, 1, 3, _) => ?a, ?b`, "b", "b,c,c,d,d,d"},
	} {
		rows, err := both(mustParse(t, step+c.goal), baseFor(v))
		if err != nil || col(rows, c.col) != c.want || col(rows, "a") != "a,a,a,b,b,c" {
			t.Errorf("%s = %v from %v, %v; want %s from a,a,a,b,b,c", c.goal, col(rows, c.col), col(rows, "a"), err, c.want)
		}
	}
}
