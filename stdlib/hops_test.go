package stdlib_test

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/jaala/datalog"
	"github.com/panyam/jaala/ns"
	"github.com/panyam/jaala/stdlib"
)

// cyclic is a graph with a cycle and two paths of one length: a->b->c->a, a->d->c, then c->e->f->g. It
// records each node its step was asked about.
type cyclic struct{ asked []string }

var cyclicEdges = []string{"a-b", "b-c", "c-a", "a-d", "d-c", "c-e", "e-f", "f-g"}

func (g *cyclic) step(ctx context.Context, node ns.Value, out bool, extra []ns.Value, each func(ns.Value, []string) error) error {
	g.asked = append(g.asked, node.S)
	for _, e := range cyclicEdges {
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
	return ctx.Err()
}

func hopsSetup(t *testing.T) (*ns.Vocabulary, *ns.MemSource, *cyclic) {
	t.Helper()
	src := ns.NewMemSource().Declare("node", "name")
	for _, n := range "abcdefg" {
		src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(string(n))}})
	}
	v := ns.MustVocabulary(src)
	if err := v.AddLanguage(datalog.Language); err != nil {
		t.Fatal(err)
	}
	g := &cyclic{}
	if err := v.AddPredicate("graph.hops", stdlib.Hops(g.step)); err != nil {
		t.Fatal(err)
	}
	return v, src, g
}

// bfsDistances is the fewest steps from start to each node over cyclicEdges, within limit, by a plain
// breadth-first search written independently of Hops.
func bfsDistances(start string, limit int) map[string]int {
	dist := map[string]int{}
	frontier := []string{start}
	for d := 1; d <= limit; d++ {
		var next []string
		for _, n := range frontier {
			for _, e := range cyclicEdges {
				from, to, _ := strings.Cut(e, "-")
				if _, ok := dist[to]; from == n && !ok {
					dist[to] = d
					next = append(next, to)
				}
			}
		}
		frontier = next
	}
	return dist
}

func TestHopsAgreesWithABreadthFirstSearch(t *testing.T) {
	for _, start := range []string{"a", "c", "g"} {
		for limit := 1; limit <= 6; limit++ {
			v, src, _ := hopsSetup(t)
			rows := eval(t, v, src, `graph.hops("`+start+`", ?y, `+string(rune('0'+limit))+`, ?d) => ?y, ?d`)
			got := map[string]int{}
			for _, r := range rows {
				got[r.Bind["y"].S] = int(*r.Bind["d"].Num)
			}
			if want := bfsDistances(start, limit); !reflect.DeepEqual(got, want) {
				t.Errorf("from %s within %d: %v, want %v", start, limit, got, want)
			}
		}
	}
	// control: the cycle brings a back to itself, at its length, and only once within reaches it.
	v, src, _ := hopsSetup(t)
	if rows := eval(t, v, src, `graph.hops("a", "a", 2, ?d) => ?d`); len(rows) != 0 {
		t.Errorf("a reaches itself within 2: %v, want nothing (the cycle is 3 long)", rows)
	}
	if rows := eval(t, v, src, `graph.hops("a", "a", 3, ?d) => ?d`); len(rows) != 1 || rows[0].Bind["d"].S != "3" {
		t.Errorf("a reaches itself within 3: %v, want at 3", rows)
	}
}

// Within 2 from a asks for a's neighbours and theirs (b and d), and nothing past them: the walk stops at
// its bound rather than walking on and dropping what's too far.
func TestHopsStopsAtItsBound(t *testing.T) {
	v, src, g := hopsSetup(t)
	eval(t, v, src, `graph.hops("a", ?y, 2, ?d) => ?y`)
	if want := []string{"a", "b", "d"}; !reflect.DeepEqual(g.asked, want) {
		t.Errorf("within 2 asked about %v, want %v", g.asked, want)
	}
	v, src, g = hopsSetup(t)
	eval(t, v, src, `graph.hops("a", ?y, 9, ?d) => ?y`)
	if len(g.asked) <= 3 {
		t.Errorf("control: within 9 asked about %v, which should be more than within 2's", g.asked)
	}
}

// Each answer cites one shortest path in order from ?from to ?to, out from a bound ?from or back from a
// bound ?to, and with both bound the walk stops at ?to.
func TestHopsCitesAShortestPathInOrder(t *testing.T) {
	for _, c := range []struct {
		query, from string
		path        []string
		asked       []string
	}{
		{`graph.hops("a", "f", 5, ?d) => ?d`, "", []string{"a-b", "b-c", "c-e", "e-f"}, []string{"a", "b", "d", "c", "e"}},
		{`graph.hops(?x, "f", 5, ?d) => ?x, ?d`, "a", []string{"a-b", "b-c", "c-e", "e-f"}, []string{"f", "e", "c", "b", "d", "a"}},
		{`graph.hops("d", "a", 5, ?d) => ?d`, "", []string{"d-c", "c-a"}, []string{"d", "c"}},
	} {
		v, src, g := hopsSetup(t)
		rows := eval(t, v, src, c.query, datalog.Witnesses())
		var row *datalog.Row
		for i, r := range rows {
			if c.from == "" || r.Bind["x"].S == c.from {
				row = &rows[i]
			}
		}
		if row == nil {
			t.Fatalf("%s: no row in %v", c.query, rows)
		}
		if got := row.Witness[0].Cites; !reflect.DeepEqual(got, c.path) {
			t.Errorf("%s: path %v, want %v", c.query, got, c.path)
		}
		if d := len(c.path); row.Bind["d"].S != string(rune('0'+d)) {
			t.Errorf("%s: ?d = %s, want %d", c.query, row.Bind["d"].S, d)
		}
		if !reflect.DeepEqual(g.asked, c.asked) {
			t.Errorf("%s: asked about %v, want %v", c.query, g.asked, c.asked)
		}
	}
}

func TestHopsRefusesABadBound(t *testing.T) {
	for _, c := range []struct{ query, want string }{
		{`graph.hops("a", ?y, 0, ?d)`, "at least 1"},
		{`graph.hops("a", ?y, 1.5, ?d)`, "whole number"},
		{`graph.hops("a", ?y, "x", ?d)`, "number"},
	} {
		v, src, _ := hopsSetup(t)
		_, err := datalog.SemiNaive{}.Eval(bg, datalog.MustParse(c.query), datalog.MustBase(v, src))
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), "query: ") {
			t.Errorf("%s: err = %v, want a query: error saying %q", c.query, err, c.want)
		}
	}
	v, _, _ := hopsSetup(t)
	if err := datalog.Validate(datalog.MustParse(`graph.hops("a", ?y, ?w, ?d)`), v); err == nil {
		t.Error("an unbound within validated")
	}
}

// A node is one node however its number is spelled: s steps to both -0 and 0, which the walk visits
// once, so it asks about one of them and not the other.
func TestHopsVisitsANumberOnce(t *testing.T) {
	var asked int
	step := func(ctx context.Context, node ns.Value, out bool, extra []ns.Value, each func(ns.Value, []string) error) error {
		if node.S != "s" {
			asked++
			return nil
		}
		if err := each(ns.N(math.Copysign(0, -1)), []string{"s-z1"}); err != nil {
			return err
		}
		return each(ns.N(0), []string{"s-z2"})
	}
	v := ns.MustVocabulary(ns.NewMemSource())
	if err := v.AddLanguage(datalog.Language); err != nil {
		t.Fatal(err)
	}
	if err := v.AddPredicate("graph.hops", stdlib.Hops(step)); err != nil {
		t.Fatal(err)
	}
	eval(t, v, ns.NewMemSource(), `graph.hops("s", ?y, 2, ?d) => ?y`)
	if asked != 1 {
		t.Errorf("asked about the zero node %d times, want once", asked)
	}
}
