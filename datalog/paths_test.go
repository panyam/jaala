package datalog

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// wireEdges are cyclicEdges with an id each, and a second wire from a to b beside a->b's, so a trail
// over wire tells two paths through a->b apart where one over edge sees one step.
var wireEdges = func() [][3]string {
	var out [][3]string
	for i, e := range cyclicEdges {
		out = append(out, [3]string{e[0], fmt.Sprint("w", i), e[1]})
	}
	return append(out, [3]string{"a", "w9", "b"})
}()

func pathsGraph() *ns.MemSource {
	src := cyclicGraph().Declare("wire", "from", "id", "to")
	for _, e := range wireEdges {
		src.Add("wire", ns.Tuple{Vals: []ns.Value{ns.S(e[0]), ns.S(e[1]), ns.S(e[2])}, Cites: []string{e[1]}})
	}
	return src
}

// bruteForcePaths is every path text from start over edges (each from, id, to; ids "" for a relation of
// two arguments) in lo to hi steps that mode allows, by listing every sequence of edges up to the
// longest the mode can take, written apart from the engine's walk. For shortest it keeps, per end, the
// fewest steps whose (node, edge) sequence sorts first.
func bruteForcePaths(edges [][3]string, start, mode string, lo, hi int) map[string]bool {
	limit := hi
	if limit == 0 {
		limit = len(edges) + 1 // a trail uses each edge once, an acyclic path each node
	}
	type seq struct {
		nodes, ids []string
	}
	all := []seq{{nodes: []string{start}}}
	var found []seq
	for frontier := all; len(frontier) > 0; {
		var next []seq
		for _, s := range frontier {
			if len(s.ids) >= lo {
				found = append(found, s)
			}
			if len(s.ids) == limit || mode == "simple" && len(s.ids) > 0 && s.nodes[len(s.nodes)-1] == start {
				continue // a simple path that closed its cycle goes no further
			}
			for i, e := range edges {
				if e[0] != s.nodes[len(s.nodes)-1] {
					continue
				}
				id := fmt.Sprint(i)
				if mode == "trail" && slices.Contains(s.ids, id) || mode == "acyclic" && slices.Contains(s.nodes, e[2]) || mode == "simple" && e[2] != start && slices.Contains(s.nodes, e[2]) {
					continue
				}
				next = append(next, seq{append(slices.Clone(s.nodes), e[2]), append(slices.Clone(s.ids), id)})
			}
		}
		frontier = next
	}
	text := func(s seq) string {
		q := func(xs []string) string {
			out := make([]string, len(xs))
			for i, x := range xs {
				out[i] = strconv.Quote(x)
			}
			return "[" + strings.Join(out, ",") + "]"
		}
		if edges[0][1] == "" {
			return q(s.nodes)
		}
		ids := make([]string, len(s.ids))
		for i, id := range s.ids {
			n, _ := strconv.Atoi(id)
			ids[i] = edges[n][1]
		}
		return `{"nodes":` + q(s.nodes) + `,"edges":` + q(ids) + `}`
	}
	out := map[string]bool{}
	if mode != "shortest" {
		for _, s := range found {
			out[text(s)] = true
		}
		return out
	}
	best := map[string]seq{}
	key := func(s seq) []string {
		var k []string
		for i := range s.ids {
			n, _ := strconv.Atoi(s.ids[i])
			k = append(k, s.nodes[i+1], edges[n][1])
		}
		return k
	}
	for _, s := range found {
		end := s.nodes[len(s.nodes)-1]
		b, ok := best[end]
		if !ok || len(s.ids) < len(b.ids) || len(s.ids) == len(b.ids) && slices.Compare(key(s), key(b)) < 0 {
			best[end] = s
		}
	}
	for _, s := range best {
		out[text(s)] = true
	}
	return out
}

func twoArg(edges [][2]string) [][3]string {
	var out [][3]string
	for _, e := range edges {
		out = append(out, [3]string{e[0], "", e[1]})
	}
	return out
}

func pathSet(rows []Row) map[string]bool {
	out := map[string]bool{}
	for _, r := range rows {
		out[r.Bind["p"].S] = true
	}
	return out
}

// Out from a bound start, over two arguments and three, each mode and pair of bounds gives the paths a
// brute-force listing gives, in every evaluator and under CanonicalCites; back from each end it reaches,
// the paths from start are the same ones, so which end the plan binds doesn't change the answer, the
// shortest path included.
func TestPathsAgreeWithABruteForceListing(t *testing.T) {
	b := baseFor(std(pathsGraph()))
	for _, rel := range []struct {
		name  string
		edges [][3]string
	}{{"edge", twoArg(cyclicEdges)}, {"wire", wireEdges}} {
		for _, mode := range pathModes {
			for _, bounds := range [][2]int{{1, 0}, {0, 2}, {1, 4}, {2, 3}, {3, 3}} {
				if mode == "walk" && bounds[1] == 0 {
					continue
				}
				for _, start := range []string{"a", "c", "g"} {
					want := bruteForcePaths(rel.edges, start, mode, bounds[0], bounds[1])
					q := fmt.Sprintf(`graph.paths(%q, %q, ?y, %q, %d, %d, ?p) => ?p`, rel.name, start, mode, bounds[0], bounds[1])
					rows, err := both(mustParse(t, q), b, Witnesses())
					if err != nil {
						t.Fatalf("%s: %v", q, err)
					}
					if got := pathSet(rows); !reflect.DeepEqual(got, want) {
						t.Errorf("%s:\n got  %v\n want %v", q, keys(got), keys(want))
					}
					ends := map[string]bool{}
					for p := range want {
						path, _ := ns.ParsePath(p)
						ends[path.Nodes[len(path.Nodes)-1].S] = true
					}
					for end := range ends {
						q := fmt.Sprintf(`graph.paths(%q, ?x, %q, %q, %d, %d, ?p), ?x = %q => ?p`, rel.name, end, mode, bounds[0], bounds[1], start)
						rows, err := both(mustParse(t, q), b)
						if err != nil {
							t.Fatalf("%s: %v", q, err)
						}
						for p := range want {
							if path, _ := ns.ParsePath(p); path.Nodes[len(path.Nodes)-1].S == end && !pathSet(rows)[p] {
								t.Errorf("%s: no %s among %v", q, p, keys(pathSet(rows)))
							}
						}
						if n := len(pathSet(rows)); n != countEndingAt(want, end) {
							t.Errorf("%s: %d paths, want %d", q, n, countEndingAt(want, end))
						}
					}
				}
			}
		}
	}
	// control: the fixture tells the modes apart, and the parallel wire makes paths edge can't
	walk, trail, acyclic := bruteForcePaths(twoArg(cyclicEdges), "a", "walk", 1, 4), bruteForcePaths(twoArg(cyclicEdges), "a", "trail", 1, 4), bruteForcePaths(twoArg(cyclicEdges), "a", "acyclic", 1, 4)
	simple := bruteForcePaths(twoArg(cyclicEdges), "a", "simple", 1, 4)
	if len(walk) <= len(trail) || len(trail) <= len(simple) || len(simple) <= len(acyclic) {
		t.Errorf("control: walk %d, trail %d, simple %d, acyclic %d paths, want each more than the next", len(walk), len(trail), len(simple), len(acyclic))
	}
	if w, e := bruteForcePaths(wireEdges, "a", "acyclic", 1, 0), bruteForcePaths(twoArg(cyclicEdges), "a", "acyclic", 1, 0); len(w) <= len(e) {
		t.Errorf("control: wire has %d acyclic paths from a, edge %d", len(w), len(e))
	}
}

func countEndingAt(paths map[string]bool, end string) int {
	n := 0
	for p := range paths {
		if path, _ := ns.ParsePath(p); path.Nodes[len(path.Nodes)-1].S == end {
			n++
		}
	}
	return n
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// With neither end bound a paths call walks from every start, so its paths are each start's.
func TestPathsWithNeitherEndBoundWalkFromEveryStart(t *testing.T) {
	b := baseFor(std(pathsGraph()))
	for _, bounds := range [][2]int{{1, 2}, {0, 1}} {
		q := fmt.Sprintf(`graph.paths("edge", ?x, ?y, "acyclic", %d, %d, ?p) => ?p`, bounds[0], bounds[1])
		rows, err := both(mustParse(t, q), b)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		want := map[string]bool{}
		for _, x := range "abcdefg" {
			for p := range bruteForcePaths(twoArg(cyclicEdges), string(x), "acyclic", bounds[0], bounds[1]) {
				want[p] = true
			}
		}
		if got := pathSet(rows); !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got  %v\n want %v", q, keys(got), keys(want))
		}
	}
}

// A path answer binds its ends, cites its facts in order from ?from to ?to, and its witness has a child
// per step, whichever end the walk started from.
func TestAPathCitesItsStepsInOrder(t *testing.T) {
	b := baseFor(std(pathsGraph()))
	want := `{"nodes":["a","d","c","e","f"],"edges":["w0","w4","w5","w6"]}`
	for _, q := range []string{
		`graph.paths("wire", "a", "f", "acyclic", 1, 0, ?p) => ?p`,
		`graph.paths("wire", ?x, "f", "acyclic", 1, 0, ?p), ?x = "a" => ?p`,
	} {
		rows, err := both(mustParse(t, q), b, Witnesses(), CanonicalCites())
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		var w *Witness
		for _, r := range rows {
			if r.Bind["p"].S != want {
				continue
			}
			for _, n := range r.Witness {
				if n.Relation == pathsName {
					w = n
				}
			}
		}
		if w == nil {
			t.Fatalf("%s: no graph.paths node for %s in %v", q, want, rows)
		}
		if want := []string{"w0", "w4", "w5", "w6"}; !reflect.DeepEqual(w.Cites, want) {
			t.Errorf("%s: cites %v, want %v", q, w.Cites, want)
		}
		if len(w.Children) != 4 || w.Children[0].Values[0].S != "a" || w.Children[3].Values[2].S != "f" {
			t.Errorf("%s: children %v", q, w.Children)
		}
	}
}

// The shortest path is the one whose nodes come first: a reaches c through b and through d in two
// steps, and b sorts first though a->d is the first fact.
func TestTheShortestPathIsTheOneWhoseNodesComeFirst(t *testing.T) {
	b := baseFor(std(pathsGraph()))
	for _, q := range []string{
		`graph.paths("edge", "a", "c", "shortest", 1, 0, ?p) => ?p`,
		`graph.paths("edge", ?x, "c", "shortest", 1, 0, ?p), ?x = "a" => ?p`,
	} {
		rows, err := both(mustParse(t, q), b)
		if err != nil {
			t.Fatal(err)
		}
		if got := col(rows, "p"); got != `["a","b","c"]` {
			t.Errorf("%s = %s", q, got)
		}
	}
	// control: the trail through d is as short
	if rows := eval(t, pathsGraph(), `graph.paths("edge", "a", "c", "trail", 2, 2, ?p) => ?p`); len(rows) != 2 {
		t.Errorf("control: two paths of two steps, got %v", rows)
	}
}

func TestPathsRefuseWhatTheyCantWalk(t *testing.T) {
	src := pathsGraph().Declare("four", "a", "b", "c", "d")
	for _, c := range []struct{ q, want string }{
		{`graph.paths("edge", "a", ?y, "trail", 1, 2)`, `query: graph.paths takes 7 args, got 6`},
		{`graph.paths("edge", "a", ?y, "cycle", 1, 2, ?p)`, `query: graph.paths's mode must be one of walk, trail, acyclic, simple, shortest, not cycle`},
		{`graph.paths("edge", "a", ?y, "walk", 1, 0, ?p)`, `query: graph.paths in mode walk needs a max: around a cycle its paths never end`},
		{`graph.paths("four", "a", ?y, "trail", 1, 2, ?p)`, `query: graph.paths needs a relation of 2 args, from and to, or 3, from, edge and to; four takes 4`},
		{`graph.paths("edge", "a", ?y, "trail", 3, 2, ?p)`, `query: graph.paths's max must be a whole number of steps, 0 for no bound or at least min, not 2`},
		{`graph.paths("edge", "a", ?y, ?m, 1, 2, ?p)`, `nothing binds what graph.paths needs first`},
		{`r(?x) :- node(?x), graph.paths("s", ?x, _, "trail", 1, 0, _); s(?a, ?b) :- edge(?a, ?b), r(?a); r(?x)`, `query: rules are not stratifiable (recursion through graph.paths: r, s)`},
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
	// A mode or bound that comes from data, known only when the walk runs, walks nothing: which data
	// reaches a call depends on the plan, so an error there failed one evaluator and not another.
	for _, q := range []string{
		`mode("cycle") :- node("a"); mode(?m), graph.paths("edge", "a", ?y, ?m, 1, 2, ?p) => ?p`,
		`mode("walk") :- node("a"); mode(?m), graph.paths("edge", "a", ?y, ?m, 1, 0, ?p) => ?p`,
		`node(?m), graph.paths("edge", "a", ?y, "trail", 1, ?m, ?p) => ?p`,
		`node(?m), graph.closure("edge", "a", ?y, 1, ?m, _) => ?y`,
	} {
		if rows := eval(t, src, q); len(rows) != 0 {
			t.Errorf("%s: %v", q, rows)
		}
	}
}

// Paths multiply around cycles, so the walk is charged a unit per step it tries and stops at the
// budget, and a limit stops it once it has found enough.
func TestAPathWalkStopsAtItsBudgetAndItsLimit(t *testing.T) {
	src := ns.NewMemSource().Declare("edge", "from", "to")
	for i := 0; i < 12; i++ {
		for j := 0; j < 12; j++ {
			if i != j {
				src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprint("n", i)), ns.S(fmt.Sprint("n", j))}})
			}
		}
	}
	q := `graph.paths("edge", "n0", ?y, "acyclic", 1, 0, ?p) => ?p`
	_, err := SemiNaive{}.Eval(bg, mustParse(t, q), MustBase(std(src), src), Budget(5000))
	var be *BudgetExceeded
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want the budget", err)
	}
	limited := workOf(t, SemiNaive{}, src, `graph.paths("edge", "n0", ?y, "acyclic", 1, 0, ?p) => ?p limit 3`)
	if limited > 200 {
		t.Errorf("limit 3: work %d, want the few steps to its first paths", limited)
	}
	// A walk toward a node it never reaches finds no path to charge, so each step it tries is charged:
	// over 8 nodes that is some 96,000 steps, past a budget of 5000.
	small := ns.NewMemSource().Declare("edge", "from", "to").Declare("node", "name")
	for i := 0; i < 8; i++ {
		for j := 0; j < 8; j++ {
			if i != j {
				small.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprint("n", i)), ns.S(fmt.Sprint("n", j))}})
			}
		}
	}
	q = `graph.paths("edge", "n0", "nowhere", "acyclic", 1, 0, ?p) => ?p`
	if _, err := (SemiNaive{}).Eval(bg, mustParse(t, q), MustBase(std(small), small), Budget(5000)); !errors.As(err, &be) {
		t.Errorf("toward nowhere: err = %v, want the budget", err)
	}
	if rows, err := (SemiNaive{}).Eval(bg, mustParse(t, q), MustBase(std(small), small)); err != nil || len(rows) != 0 {
		t.Errorf("control: without a budget it answers nothing: %v, %v", rows, err)
	}
}

// A paths call over a relation whose rules each read one base atom walks the atom, three arguments
// included, and Explain shows it as written.
func TestPathsWalkAOneAtomRelationAndExplainAsWritten(t *testing.T) {
	src := pathsGraph()
	q := `hop(?a, ?w, ?b) :- wire(?a, ?w, ?b); graph.paths("hop", "a", ?y, "trail", 1, 2, ?p) => ?p`
	direct := `graph.paths("wire", "a", ?y, "trail", 1, 2, ?p) => ?p`
	if got, want := workOf(t, SemiNaive{}, src, q), workOf(t, SemiNaive{}, src, direct); got != want {
		t.Errorf("through hop: work %d, want the %d of walking wire itself", got, want)
	}
	if got, want := col(eval(t, src, q), "p"), col(eval(t, src, direct), "p"); got != want {
		t.Errorf("through hop %s, want %s", got, want)
	}
	var r Report
	if _, err := (SemiNaive{}).Eval(bg, mustParse(t, direct), baseFor(std(src)), Explain(&r)); err != nil {
		t.Fatal(err)
	}
	if text := r.String(); !strings.Contains(text, `graph.paths("wire", "a", ?y, "trail", 1, 2, ?p)`) || strings.Contains(text, "\x00") {
		t.Errorf("report:\n%s", text)
	}
}

// A path's ends are typed as the relation it walks, in ColumnKinds and a rule's signature, and a
// constant at one is read as that type.
func TestAPathsEndsAreTypedByItsRelation(t *testing.T) {
	src := ns.NewMemSource().DeclareSchema("next", ns.Schema{Arity: 2, Labels: []string{"from", "to"}, Types: []ns.ArgType{{Type: ns.TypeNumber}, {Type: ns.TypeNumber}}})
	for i := 1; i < 4; i++ {
		src.Add("next", ns.Tuple{Vals: []ns.Value{ns.N(float64(i)), ns.N(float64(i + 1))}})
	}
	rows := eval(t, src, `graph.paths("next", "1", ?y, "trail", 1, 0, ?p) => ?p`)
	if got := col(rows, "p"); got != "[1,2,3,4],[1,2,3],[1,2]" {
		t.Errorf("paths from \"1\": %s", got)
	}
	got, err := ColumnKinds(mustParse(t, `far(?x, ?y, ?p) :- graph.paths("next", ?x, ?y, "trail", 2, 0, ?p); far(?x, ?y, ?p) => ?x, ?y, ?p`), std(src))
	want := []ColumnKind{{Type: ns.TypeNumber}, {Type: ns.TypeNumber}, {Type: ns.TypeString}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("%+v, %v; want %+v", got, err, want)
	}
	// and a closure's ends the same way (#281's were typed only inside a module until #280)
	got, err = ColumnKinds(mustParse(t, `graph.closure("next", ?x, ?y, 1, 0, ?d) => ?x, ?y, ?d`), std(src))
	want = []ColumnKind{{Type: ns.TypeNumber}, {Type: ns.TypeNumber}, {Type: ns.TypeNumber}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("closure: %+v, %v; want %+v", got, err, want)
	}
}

// A trail over a relation naming its edges takes each edge once whichever way it runs, as an
// undirected relationship's two facts, one each way, share one edge; over two arguments the two
// facts are two steps.
func TestATrailTakesAnEdgeOnceEitherWay(t *testing.T) {
	src := ns.NewMemSource().Declare("both", "from", "id", "to").Declare("pair", "from", "to")
	for _, f := range [][3]string{{"a", "e1", "b"}, {"b", "e1", "a"}} {
		src.Add("both", ns.Tuple{Vals: []ns.Value{ns.S(f[0]), ns.S(f[1]), ns.S(f[2])}})
		src.Add("pair", ns.Tuple{Vals: []ns.Value{ns.S(f[0]), ns.S(f[2])}})
	}
	if got := col(eval(t, src, `graph.paths("both", "a", ?y, "trail", 1, 0, ?p) => ?p`), "p"); got != `{"nodes":["a","b"],"edges":["e1"]}` {
		t.Errorf("over both: %s", got)
	}
	if got := col(eval(t, src, `graph.paths("pair", "a", ?y, "trail", 1, 0, ?p) => ?p`), "p"); got != `["a","b","a"],["a","b"]` {
		t.Errorf("control: over pair: %s", got)
	}
}

// The shortest path is chosen walking out from its start whichever end the plan binds: walking back
// from t would take p, which sorts before q, and so a,c,p,t; walking out from a takes b, and a,b,q,t.
func TestTheShortestPathIsTheSameFromEitherEnd(t *testing.T) {
	src := ns.NewMemSource().Declare("e", "from", "to")
	for _, f := range [][2]string{{"a", "b"}, {"b", "q"}, {"q", "t"}, {"a", "c"}, {"c", "p"}, {"p", "t"}} {
		src.Add("e", ns.Tuple{Vals: []ns.Value{ns.S(f[0]), ns.S(f[1])}})
	}
	for _, q := range []string{
		`graph.paths("e", "a", ?y, "shortest", 1, 0, ?p), ?y = "t" => ?p`,
		`graph.paths("e", ?x, "t", "shortest", 1, 0, ?p), ?x = "a" => ?p`,
		`graph.paths("e", ?x, "t", "shortest", 1, 0, ?p) => ?p`,
	} {
		rows := eval(t, src, q)
		if got := col(rows, "p"); !strings.Contains(got, `["a","b","q","t"]`) || strings.Contains(got, `["a","c","p","t"]`) {
			t.Errorf("%s = %s", q, got)
		}
	}
}

// A fact served twice is one step: its paths are found once, so a count of them doesn't double.
func TestAFactServedTwiceIsWalkedOnce(t *testing.T) {
	src := ns.NewMemSource().Declare("e", "from", "to")
	for _, f := range [][3]string{{"a", "b", "s1"}, {"a", "b", "s2"}, {"b", "c", "s3"}} {
		src.Add("e", ns.Tuple{Vals: []ns.Value{ns.S(f[0]), ns.S(f[1])}, Cites: []string{f[2]}})
	}
	if got := col(eval(t, src, `graph.paths("e", "a", ?y, "trail", 1, 0, ?p) => count(?p)`), "count(p)"); got != "2" {
		t.Errorf("count = %s, want 2 paths", got)
	}
	// control: the fact served twice is two facts to a relation's own count
	if got := col(eval(t, src, `e("a", ?y) => count(?y)`), "count(y)"); got != "2" {
		t.Errorf("control: e(a, _) counts %s, want 2", got)
	}
}

// A node reaches itself in no steps only when the relation holds it, whichever end is bound: walking
// from every start, as a rule derived whole does, starts only from what the facts hold, so a bound z,
// as demand binds it, mustn't add a path (the corpus found the planned evaluator answering z).
func TestNoStepsReachOnlyANodeTheRelationHolds(t *testing.T) {
	src := cyclicGraph()
	src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S("z")}})
	for _, q := range []string{
		`r(?x, ?y) :- graph.paths("edge", ?x, ?y, "trail", 0, 0, _); node(?n), r(?n, ?n), ?n > "f" => ?n`,
		`r(?x, ?y) :- graph.closure("edge", ?x, ?y, 0, 0, _); node(?n), r(?n, ?n), ?n > "f" => ?n`,
		`node(?n), graph.paths("edge", ?n, ?n, "acyclic", 0, 1, _), ?n > "f" => ?n`,
		`node(?n), graph.paths("edge", ?n, ?n, "shortest", 0, 1, _), ?n > "f" => ?n`,
		`node(?n), graph.closure("edge", ?n, ?n, 0, 1, _), ?n > "f" => ?n`,
	} {
		if got := col(eval(t, src, q), "n"); got != "g" {
			t.Errorf("%s = %s, want g alone", q, got)
		}
	}
}

// A walk whose bounds a later relation binds runs once they are, in Naive too, which called
// it where written and erred on a body the planned evaluator answered (#407).
func TestAWalkWaitsForItsBoundsWrittenAfterIt(t *testing.T) {
	for q, want := range map[string]string{
		`r(?a, ?h) :- graph.closure("edge", ?a, "c", 0, ?m, ?h), weight(?a, ?m); r(?a, ?x) => ?a, ?x`:         "b 1, c 0",
		`r(?a, ?p) :- graph.paths("edge", ?a, "c", "simple", 0, ?m, ?p), weight(?a, ?m); r(?a, ?x) => ?a, ?x`: `b ["b","c"], c ["c"]`,
	} {
		rows, err := both(mustParse(t, q), baseFor(std(graph())))
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		var got []string
		for _, r := range rows {
			got = append(got, r.Bind["a"].S+" "+r.Bind["x"].S)
		}
		if strings.Join(got, ", ") != want {
			t.Errorf("%s:\n got  %s\n want %s", q, strings.Join(got, ", "), want)
		}
	}
}
