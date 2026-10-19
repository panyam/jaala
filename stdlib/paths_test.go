package stdlib_test

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// parallel is cyclicEdges with a second edge from a to b, each edge named, and wireBack is the first,
// a to b, as an undirected edge's second fact runs: b to a under the same name, which a trail takes
// once either way.
var (
	parallel = append(append([]string(nil), cyclicEdges...), "a-b")
	wireBack = [3]string{"b", "w0", "a"}
)

// pathsSetup is a vocabulary with the standard library, cyclicEdges as edge(from, to) and parallel as
// wire(from, id, to), and graph.hostpaths and graph.hostwires as Paths and EdgePaths over the same
// edges, counting the nodes each was asked about.
func pathsSetup(t *testing.T) (*ns.Vocabulary, *ns.MemSource, *int) {
	t.Helper()
	src := ns.NewMemSource().Declare("edge", "from", "to").Declare("wire", "from", "id", "to")
	for _, e := range cyclicEdges {
		from, to, _ := strings.Cut(e, "-")
		src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(from), ns.S(to)}, Cites: []string{e}})
	}
	for i, e := range parallel {
		from, to, _ := strings.Cut(e, "-")
		src.Add("wire", ns.Tuple{Vals: []ns.Value{ns.S(from), ns.S(fmt.Sprint("w", i)), ns.S(to)}, Cites: []string{fmt.Sprint("w", i)}})
	}
	src.Add("wire", ns.Tuple{Vals: []ns.Value{ns.S(wireBack[0]), ns.S(wireBack[1]), ns.S(wireBack[2])}, Cites: []string{"w0b"}})
	v := ns.MustVocabulary(src)
	if err := v.AddLanguage(datalog.Language); err != nil {
		t.Fatal(err)
	}
	if err := stdlib.Register(v); err != nil {
		t.Fatal(err)
	}
	asked := new(int)
	g := &cyclic{}
	step := func(ctx context.Context, node ns.Value, out bool, extra []ns.Value, each func(ns.Value, []string) error) error {
		*asked++
		return g.step(ctx, node, out, extra, each)
	}
	wires := func(ctx context.Context, node ns.Value, out bool, _ []ns.Value, each func(ns.Value, ns.Value, []string) error) error {
		*asked++
		for i, e := range parallel {
			from, to, _ := strings.Cut(e, "-")
			if !out {
				from, to = to, from
			}
			if from == node.S {
				if err := each(ns.S(to), ns.S(fmt.Sprint("w", i)), []string{fmt.Sprint("w", i)}); err != nil {
					return err
				}
			}
		}
		from, to := wireBack[0], wireBack[2]
		if !out {
			from, to = to, from
		}
		if from == node.S {
			return each(ns.S(to), ns.S(wireBack[1]), []string{"w0b"})
		}
		return ctx.Err()
	}
	if err := v.AddPredicate("graph.hostpaths", stdlib.Paths(step)); err != nil {
		t.Fatal(err)
	}
	if err := v.AddPredicate("graph.hostwires", stdlib.EdgePaths(wires)); err != nil {
		t.Fatal(err)
	}
	return v, src, asked
}

func pathTexts(rows []datalog.Row) []string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Bind["p"].S)
	}
	sort.Strings(out)
	return out
}

// Paths and EdgePaths over a host's step give the paths graph.paths gives over the same edges as a
// relation, in every mode, out from a bound start and back from a bound end, with the same shortest.
func TestHostPathsAgreeWithTheEngines(t *testing.T) {
	v, src, _ := pathsSetup(t)
	for _, c := range []struct{ host, rel string }{{"graph.hostpaths", "edge"}, {"graph.hostwires", "wire"}} {
		for _, mode := range []string{"walk", "trail", "acyclic", "simple", "shortest"} {
			for _, within := range []int{0, 1, 3, 5} {
				if mode == "walk" && within == 0 {
					continue
				}
				for _, end := range []string{`"a", ?y`, `?y, "c"`, `"c", "a"`} {
					host := pathTexts(eval(t, v, src, fmt.Sprintf(`%s(%s, %q, %d, ?p) => ?p`, c.host, end, mode, within)))
					engine := pathTexts(eval(t, v, src, fmt.Sprintf(`graph.paths(%q, %s, %q, 1, %d, ?p) => ?p`, c.rel, end, mode, within)))
					if !reflect.DeepEqual(host, engine) {
						t.Errorf("%s(%s, %s, %d):\n host   %v\n engine %v", c.host, end, mode, within, host, engine)
					}
					if len(engine) == 0 {
						t.Errorf("control: %s(%s, %s, %d) found no paths", c.rel, end, mode, within)
					}
				}
			}
		}
	}
}

// A host's steps are sorted before the shortest path is chosen, so it is the one whose nodes come
// first though the host lists a->d before a->b, as graph.paths chooses it.
func TestHostShortestIsTheOneWhoseNodesComeFirst(t *testing.T) {
	v := ns.MustVocabulary(ns.NewMemSource())
	if err := v.AddLanguage(datalog.Language); err != nil {
		t.Fatal(err)
	}
	edges := []string{"a-d", "a-b", "b-c", "d-c"}
	step := func(ctx context.Context, node ns.Value, out bool, _ []ns.Value, each func(ns.Value, []string) error) error {
		for _, e := range edges {
			from, to, _ := strings.Cut(e, "-")
			if !out {
				from, to = to, from
			}
			if from == node.S {
				if err := each(ns.S(to), []string{e}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := v.AddPredicate("graph.hostpaths", stdlib.Paths(step)); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{`graph.hostpaths("a", "c", "shortest", 0, ?p) => ?p`, `graph.hostpaths(?x, "c", "shortest", 0, ?p), ?x = "a" => ?p`} {
		if got := col(eval(t, v, ns.NewMemSource(), q), "p"); got != `["a","b","c"]` {
			t.Errorf("%s = %s", q, got)
		}
	}
}

// Each answer cites its steps in order from ?from to ?to, a backward walk's too.
func TestHostPathsCiteTheirStepsInOrder(t *testing.T) {
	v, src, _ := pathsSetup(t)
	for _, q := range []string{
		`graph.hostwires("a", "f", "shortest", 0, ?p) => ?p`,
		`graph.hostwires(?x, "f", "shortest", 0, ?p), ?x = "a" => ?p`,
	} {
		rows := eval(t, v, src, q, datalog.Witnesses())
		var got []string
		for _, r := range rows {
			if strings.HasPrefix(r.Bind["p"].S, `{"nodes":["a"`) {
				got = r.Witness[0].Cites
			}
		}
		if want := []string{"w0", "w1", "w5", "w6"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: cites %v, want %v (rows %v)", q, got, want, rows)
		}
	}
}

// A limit stops the walk: the first path asks about one node, where every path asks about all of them.
func TestHostPathsStopWhenTheQueryHasEnough(t *testing.T) {
	v, src, asked := pathsSetup(t)
	eval(t, v, src, `graph.hostpaths("a", ?y, "trail", 0, ?p) => ?p limit 1`)
	first := *asked
	*asked = 0
	eval(t, v, src, `graph.hostpaths("a", ?y, "trail", 0, ?p) => ?p`)
	if first != 1 || *asked <= first {
		t.Errorf("asked about %d nodes for the first path, %d for all of them", first, *asked)
	}
}

func TestHostPathsRefuseABadModeOrWithin(t *testing.T) {
	v, src, _ := pathsSetup(t)
	for _, c := range []struct{ query, want string }{
		{`graph.hostpaths("a", ?y, "cycle", 2, ?p)`, "mode must be one of"},
		{`graph.hostpaths("a", ?y, "walk", 0, ?p)`, "needs a within"},
		{`graph.hostpaths("a", ?y, "trail", 1.5, ?p)`, "whole number"},
	} {
		_, err := datalog.SemiNaive{}.Eval(bg, datalog.MustParse(c.query), datalog.MustBase(v, src))
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), "query: ") {
			t.Errorf("%s: err = %v, want a query: error saying %q", c.query, err, c.want)
		}
	}
}

// The path.* functions read a path's length, nodes, steps and edges, whichever walk made it.
func TestPathFunctionsReadAPath(t *testing.T) {
	v, src, _ := pathsSetup(t)
	for _, c := range []struct {
		query string
		vars  []datalog.Var
		want  string
	}{
		{`graph.paths("edge", "a", "f", "shortest", 1, 0, ?p), path.length(?p, ?n) => ?n`, []datalog.Var{"n"}, "4"},
		{`graph.paths("edge", "a", "f", "acyclic", 1, 0, ?p), path.node(?p, _, "d"), path.length(?p, ?n) => ?n`, []datalog.Var{"n"}, "4"},
		{`graph.paths("edge", "a", "f", "acyclic", 1, 0, ?p), not path.node(?p, _, "d") => ?p`, []datalog.Var{"p"}, `["a","b","c","e","f"]`},
		{`graph.paths("edge", "a", "c", "shortest", 1, 0, ?p), path.node(?p, ?i, ?n) => ?i, ?n`, []datalog.Var{"i", "n"}, "0 a,1 b,2 c"},
		{`graph.paths("edge", "a", "c", "shortest", 1, 0, ?p), path.node(?p, 1, ?n) => ?n`, []datalog.Var{"n"}, "b"},
		{`graph.paths("wire", "a", "c", "trail", 2, 2, ?p), path.edge(?p, 0, ?e) => ?e`, []datalog.Var{"e"}, "w0,w3,w8"},
		{`graph.paths("wire", "a", "c", "shortest", 1, 0, ?p), path.step(?p, ?i, ?x, ?y) => ?i, ?x, ?y`, []datalog.Var{"i", "x", "y"}, "0 a b,1 b c"},
		{`graph.paths("wire", "a", "c", "shortest", 1, 0, ?p), path.nodes(?p, ?l), path.edges(?p, ?e) => ?l, ?e`, []datalog.Var{"l", "e"}, `["a","b","c"] ["w0","w1"]`},
		{`graph.paths("edge", "a", "c", "shortest", 1, 0, ?p), path.nodes(?p, ?l), ?l = ?p => ?l`, []datalog.Var{"l"}, `["a","b","c"]`},
	} {
		var got []string
		for _, r := range eval(t, v, src, c.query) {
			var vals []string
			for _, x := range c.vars {
				vals = append(vals, r.Bind[x].S)
			}
			got = append(got, strings.Join(vals, " "))
		}
		if strings.Join(got, ",") != c.want {
			t.Errorf("%s = %v, want %s", c.query, got, c.want)
		}
	}
}

// A path function gives no row for a value that isn't a path, absent included, and path.edge and
// path.edges none for a path naming no edges.
func TestPathFunctionsAnswerNothingForWhatIsntAPath(t *testing.T) {
	v, src, _ := pathsSetup(t)
	for _, q := range []string{
		`path.length("a,b", ?n) => ?n`,
		`path.length(3, ?n) => ?n`,
		`path.node("{\"nodes\":[]}", ?i, ?n) => ?n`,
		`graph.paths("edge", "a", "c", "shortest", 1, 0, ?p), path.edge(?p, _, ?e) => ?e`,
		`graph.paths("edge", "a", "c", "shortest", 1, 0, ?p), path.edges(?p, ?e) => ?e`,
		`?p = min(?x) : { edge(?x, "zz") }, path.length(?p, ?n) => ?n`,
	} {
		if rows := eval(t, v, src, q); len(rows) != 0 {
			t.Errorf("%s: %v", q, rows)
		}
	}
	// control: over its relation of three arguments the same path names its edges
	if rows := eval(t, v, src, `graph.paths("wire", "a", "c", "shortest", 1, 0, ?p), path.edges(?p, ?e) => ?e`); len(rows) != 1 {
		t.Errorf("control: %v", rows)
	}
}
