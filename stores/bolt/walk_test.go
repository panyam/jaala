package bolt

import (
	"slices"
	"testing"

	"github.com/panyam/jaala/ns"
)

// reached is every fact of rel matching bound out of a value reached from start, in fewer than
// depth steps (0 for any), from position from to position to: the walk a WalkSource must not cut
// short.
func reached(src ns.Source, rel string, from, to int, bound map[int]ns.Value, start []ns.Value, depth int) []ns.Tuple {
	var facts []ns.Tuple
	for _, t := range src.Tuples(rel) {
		ok := true
		for p, v := range bound {
			ok = ok && valueKey(t.Vals[p]) == valueKey(v)
		}
		if ok {
			facts = append(facts, t)
		}
	}
	seen := map[string]bool{}
	frontier := start
	var out []ns.Tuple
	for step := 0; len(frontier) > 0 && (depth == 0 || step < depth); step++ {
		var next []ns.Value
		for _, v := range frontier {
			if seen[valueKey(v)] {
				continue
			}
			seen[valueKey(v)] = true
			for _, t := range facts {
				if valueKey(t.Vals[from]) == valueKey(v) {
					out = append(out, t)
					next = append(next, t.Vals[to])
				}
			}
		}
		frontier = next
	}
	return out
}

// A walk returns every fact out of every value it reaches, forward and backward, from one start or
// several, at every depth bound, through a self-loop and an edge to a node no label holds, and only
// along the types its bound kind names. The control is that a bounded walk returns fewer facts than
// an unbounded one, so the bound reached the server.
func TestAWalkNeverLeavesOutAReachedFact(t *testing.T) {
	for _, sv := range servers(t) {
		t.Run(sv.name, func(t *testing.T) {
			g, src := code()
			s := open(t, sv.driver, g, schemasOf(src))
			if err := s.Apply(bg, load(src)); err != nil {
				t.Fatal(err)
			}
			calls := map[int]ns.Value{1: ns.S("calls")}
			if !s.Walks("edge", 0, 2, []int{1}) || !s.Walks("edge", 2, 0, []int{1}) || !s.Walks("edge", 0, 2, nil) {
				t.Fatal("control: the store doesn't walk edge")
			}
			if s.Walks("edge", 0, 2, []int{3}) || s.Walks("node", 0, 1, nil) || s.Walks("edge", 0, 1, []int{1}) {
				t.Error("the store walks a bound weight, a label's relation or a kind")
			}
			bounded, unbounded := 0, 0
			for _, c := range []struct {
				from, to int
				bound    map[int]ns.Value
				start    []string
			}{
				{0, 2, calls, []string{"main"}},
				{0, 2, calls, []string{"main", "lex"}},
				{2, 0, calls, []string{"ghost"}},
				{0, 2, nil, []string{"app"}},
				{0, 2, map[int]ns.Value{1: ns.S("nothing")}, []string{"main"}},
				{0, 2, calls, []string{"nowhere"}},
			} {
				var start []ns.Value
				for _, v := range c.start {
					start = append(start, ns.S(v))
				}
				for _, depth := range []int{0, 1, 2, 3} {
					var got []ns.Tuple
					for f, err := range s.Walk(bg, ns.Walk{Relation: "edge", From: c.from, To: c.to, Bound: c.bound, Start: start, MaxDepth: depth}) {
						if err != nil {
							t.Fatal(err)
						}
						got = append(got, f)
					}
					for _, want := range reached(src, "edge", c.from, c.to, c.bound, start, depth) {
						if !slices.Contains(facts(got), fact(want)) {
							t.Errorf("walk %d→%d %v from %v within %d left out %s", c.from, c.to, c.bound, c.start, depth, fact(want))
						}
					}
					if c.start[0] == "main" && len(c.start) == 1 && c.bound != nil && c.bound[1].S == "calls" {
						switch depth {
						case 1:
							bounded = len(got)
						case 0:
							unbounded = len(got)
						}
					}
				}
			}
			if bounded == 0 || bounded >= unbounded {
				t.Errorf("control: a walk from main within one step returned %d facts, unbounded %d", bounded, unbounded)
			}
		})
	}
}
