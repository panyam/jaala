package bolt

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/jaala/frontends/cypher"
	"github.com/panyam/jaala/ns"
)

// code is a small code graph in Declaire's shape: node(id, kind) under two labels, edge(from, kind,
// to) under two types with a weight on each call, attr(id, key, value) for the rest, and size(id, n)
// a label's own number column.
func code() (cypher.Graph, *ns.MemSource) {
	src := ns.NewMemSource().
		DeclareSchema("node", ns.Schema{Arity: 2, Labels: []string{"id", "kind"}, Types: []ns.ArgType{{Kind: "node"}, {Type: ns.TypeString}}}).
		DeclareSchema("edge", ns.Schema{Arity: 4, Labels: []string{"from", "kind", "to", "weight"}, Types: []ns.ArgType{{Kind: "node"}, {Type: ns.TypeString}, {Kind: "node"}, {Type: ns.TypeNumber}}}).
		DeclareSchema("attr", ns.Schema{Arity: 3, Labels: []string{"id", "key", "value"}, Types: []ns.ArgType{{Kind: "node"}, {Type: ns.TypeString}}}).
		DeclareSchema("size", ns.Schema{Arity: 2, Labels: []string{"id", "lines"}, Types: []ns.ArgType{{Kind: "node"}, {Type: ns.TypeNumber}}}).
		DeclareSchema("tag", ns.Schema{Arity: 2, Labels: []string{"id", "tag"}, Types: []ns.ArgType{{Kind: "node"}, {Type: ns.TypeString}}})
	add := func(rel, id string, cites []string, vals ...ns.Value) {
		src.Add(rel, ns.Tuple{Vals: vals, ID: id, Cites: cites})
	}
	s := ns.S
	add("node", "", []string{"scan:1"}, s("main"), s("Function"))
	add("node", "", nil, s("parse"), s("Function"))
	add("node", "", nil, s("lex"), s("Function"))
	add("node", "n:app", nil, s("app"), s("Package"))
	add("edge", "c1", []string{"calls.go:3"}, s("main"), s("calls"), s("parse"), ns.N(2))
	add("edge", "", nil, s("parse"), s("calls"), s("lex"), ns.Value{S: "01", Num: ptr(1)})
	add("edge", "", nil, s("parse"), s("calls"), s("parse"), ns.N(math.Copysign(0, -1)))
	add("edge", "", nil, s("app"), s("contains"), s("main"), ns.Absent())
	add("edge", "", nil, s("lex"), s("calls"), s("ghost"), ns.N(3)) // ghost has no node fact
	add("tag", "", nil, s("main"), s("entry"))
	add("tag", "", nil, s("parse"), ns.Absent())
	add("tag", "", nil, s("lex"), ns.Absent())
	add("attr", "", []string{"doc:main"}, s("main"), s("doc"), s("entry point"))
	add("attr", "", nil, s("parse"), s("volts"), ns.Value{S: "3.3V nominal", Num: ptr(3.3), BaseUnit: "V"})
	add("attr", "", nil, s("lex"), s("doc"), s("01"))
	add("size", "", nil, s("main"), ns.N(40))
	add("size", "", nil, s("parse"), ns.NU(1.5, "kV"))
	add("size", "", nil, s("lex"), ns.Absent())
	g := cypher.Store{Node: "node", Edge: "edge", Attr: "attr"}.Graph([]string{"Function", "Package"}, []string{"calls", "contains"})
	for i := range g.Edges {
		g.Edges[i].Props = map[string]int{"weight": 3}
	}
	g.Nodes = append(g.Nodes, cypher.Node{Label: "Sized", Relation: "size", Key: 0, Props: map[string]int{"lines": 1}},
		cypher.Node{Label: "Tagged", Relation: "tag", Key: 0, Props: map[string]int{"tag": 1}})
	return g, src
}

func ptr(f float64) *float64 { return &f }

func schemasOf(src ns.Source) map[string]ns.Schema {
	out := map[string]ns.Schema{}
	for _, rel := range src.Relations() {
		out[rel], _ = src.Schema(rel)
	}
	return out
}

func load(src ns.Source) ns.Changes {
	var c ns.Changes
	for _, rel := range src.Relations() {
		for _, t := range src.Tuples(rel) {
			c.Add(rel, t)
		}
	}
	return c
}

// fact writes a tuple whole, values by kind, number, unit and spelling, with its id and citations,
// so two tuples compare by everything the store keeps.
func fact(t ns.Tuple) string {
	var b strings.Builder
	for _, v := range t.Vals {
		switch {
		case v.Absent:
			b.WriteString("[absent]")
		case v.Num != nil:
			fmt.Fprintf(&b, "[%q %s %q]", v.S, ftoa(*v.Num), v.BaseUnit)
		default:
			fmt.Fprintf(&b, "[%q]", v.S)
		}
	}
	fmt.Fprintf(&b, " id=%q cites=%v", t.ID, t.Cites)
	return b.String()
}

func facts(ts []ns.Tuple) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = fact(t)
	}
	slices.Sort(out)
	return out
}

// Every fact comes back as it went in: its values' kinds, numbers, units and spellings (01, 3.3V,
// -0), an absent value, its id and its citations, through each label, type and the Props relation.
func TestEveryFactComesBackAsItWentIn(t *testing.T) {
	for _, sv := range servers(t) {
		t.Run(sv.name, func(t *testing.T) {
			g, src := code()
			s := open(t, sv.driver, g, schemasOf(src))
			if err := s.Apply(bg, load(src)); err != nil {
				t.Fatal(err)
			}
			for _, rel := range src.Relations() {
				got, err := s.TuplesContext(bg, rel)
				if err != nil {
					t.Fatal(err)
				}
				if want := facts(src.Tuples(rel)); !slices.Equal(facts(got), want) {
					t.Errorf("%s:\n got  %q\n want %q", rel, facts(got), want)
				}
			}
			// Applying the same facts again writes nothing new.
			if err := s.Apply(bg, load(src)); err != nil {
				t.Fatal(err)
			}
			if got, _ := s.TuplesContext(bg, "edge"); len(got) != len(src.Tuples("edge")) {
				t.Errorf("applying twice holds %d edges, want %d", len(got), len(src.Tuples("edge")))
			}
		})
	}
}

// matches is whether a stored fact holds vals at positions, as the engine compares them: a number by
// value and unit, text by its text.
func matches(t ns.Tuple, positions []int, vals []ns.Value) bool {
	for j, p := range positions {
		if valueKey(t.Vals[p]) != valueKey(vals[j]) {
			return false
		}
	}
	return true
}

// A lookup never leaves out a fact that matches, at every pattern of bound arguments and every
// value a fact holds there, nor one a value nothing holds; a batch answers one result per value,
// in order. The control is that the server filters some of them: a lookup of one key returns fewer
// facts than its relation holds.
func TestALookupNeverLeavesOutAMatch(t *testing.T) {
	for _, sv := range servers(t) {
		t.Run(sv.name, func(t *testing.T) {
			g, src := code()
			s := open(t, sv.driver, g, schemasOf(src))
			if err := s.Apply(bg, load(src)); err != nil {
				t.Fatal(err)
			}
			filtered := false
			for _, rel := range src.Relations() {
				all := src.Tuples(rel)
				sc, _ := src.Schema(rel)
				for mask := 1; mask < 1<<sc.Arity; mask++ {
					var positions []int
					for p := range sc.Arity {
						if mask&(1<<p) != 0 {
							positions = append(positions, p)
						}
					}
					var values [][]ns.Value
					for _, f := range all {
						vs := make([]ns.Value, len(positions))
						for j, p := range positions {
							vs[j] = f.Vals[p]
						}
						values = append(values, vs)
					}
					missing := make([]ns.Value, len(positions))
					for j := range missing {
						missing[j] = ns.S("nothing-holds-this")
					}
					values = append(values, missing)
					got, err := s.LookupBatch(bg, rel, positions, values)
					if err != nil {
						t.Fatal(err)
					}
					if len(got) != len(values) {
						t.Fatalf("%s at %v: %d results for %d values", rel, positions, len(got), len(values))
					}
					for k, vs := range values {
						var want []ns.Tuple
						for _, f := range all {
							if matches(f, positions, vs) {
								want = append(want, f)
							}
						}
						var kept []ns.Tuple
						for _, f := range got[k] {
							if matches(f, positions, vs) {
								kept = append(kept, f)
							}
						}
						if !slices.Equal(facts(kept), facts(want)) {
							t.Errorf("%s at %v = %s: kept %q, want %q", rel, positions, show(vs), facts(kept), facts(want))
						}
						if len(got[k]) < len(all) {
							filtered = true
						}
					}
					one, err := s.Lookup(bg, rel, map[int]ns.Value{positions[0]: values[0][0]})
					if err != nil {
						t.Fatal(err)
					}
					for _, f := range all {
						if matches(f, positions[:1], values[0][:1]) && !slices.Contains(facts(one), fact(f)) {
							t.Errorf("%s: Lookup at %d = %s left out %s", rel, positions[0], show(values[0][:1]), fact(f))
						}
					}
				}
			}
			if !filtered {
				t.Error("control: no lookup returned fewer facts than its relation holds, so none was filtered by the server")
			}
		})
	}
}

// Indexed is true for a key, an endpoint or a Props node, and false for what scans a label or type:
// a fixed kind alone, or a number.
func TestIndexedNamesTheLookupsTheNodeIndexAnswers(t *testing.T) {
	sv := servers(t)[0]
	g, src := code()
	s := open(t, sv.driver, g, schemasOf(src))
	for _, c := range []struct {
		rel   string
		bound []int
		want  bool
	}{
		{"node", []int{0}, true}, {"node", []int{1}, false},
		{"edge", []int{0}, true}, {"edge", []int{2}, true}, {"edge", []int{1}, false}, {"edge", []int{3}, false},
		{"attr", []int{0}, true}, {"attr", []int{1}, false},
		{"size", []int{0}, true}, {"size", []int{1}, false},
	} {
		if got := s.Indexed(c.rel, c.bound); got != c.want {
			t.Errorf("Indexed(%s, %v) = %v, want %v", c.rel, c.bound, got, c.want)
		}
	}
}

// What the store can't keep is refused, and a refused change set writes nothing, the parts it could
// keep included.
func TestApplyRefusesWhatItCantKeepAndWritesNothing(t *testing.T) {
	for _, sv := range servers(t) {
		t.Run(sv.name, func(t *testing.T) {
			g, src := code()
			s := open(t, sv.driver, g, schemasOf(src))
			if err := s.Apply(bg, load(src)); err != nil {
				t.Fatal(err)
			}
			before := map[string][]string{}
			for _, rel := range src.Relations() {
				ts, _ := s.TuplesContext(bg, rel)
				before[rel] = facts(ts)
			}
			fresh := func() *ns.Changes {
				c := &ns.Changes{}
				return c.Add("node", ns.Tuple{Vals: []ns.Value{ns.S("new"), ns.S("Function")}})
			}
			for _, c := range []struct {
				name    string
				changes *ns.Changes
				want    string
			}{
				{"a second size for main", fresh().Add("size", ns.Tuple{Vals: []ns.Value{ns.S("main"), ns.N(41)}}), "already holds a fact for main"},
				{"two sizes in one set", fresh().Add("size", ns.Tuple{Vals: []ns.Value{ns.S("new"), ns.N(1)}}).Add("size", ns.Tuple{Vals: []ns.Value{ns.S("new"), ns.N(2)}}), "two facts for new"},
				{"a second doc for main", fresh().Add("attr", ns.Tuple{Vals: []ns.Value{ns.S("main"), ns.S("doc"), ns.S("other")}}), "already holds a value for main doc"},
				{"a kind no label holds", fresh().Add("node", ns.Tuple{Vals: []ns.Value{ns.S("x"), ns.S("Widget")}}), "no label or type of node holds"},
				{"a reserved property", fresh().Add("attr", ns.Tuple{Vals: []ns.Value{ns.S("main"), ns.S("_key"), ns.S("v")}}), "the store's own"},
				{"a label's column as a property", fresh().Add("attr", ns.Tuple{Vals: []ns.Value{ns.S("main"), ns.S("lines"), ns.S("v")}}), "a label's column"},
				{"an unknown relation", fresh().Add("pkg", ns.Tuple{Vals: []ns.Value{ns.S("x")}}), `doesn't hold "pkg"`},
			} {
				err := s.Apply(bg, *c.changes)
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Errorf("%s: Apply = %v, want an error saying %q", c.name, err, c.want)
				}
			}
			for _, rel := range src.Relations() {
				ts, _ := s.TuplesContext(bg, rel)
				if !slices.Equal(facts(ts), before[rel]) {
					t.Errorf("%s changed under refused change sets:\n got  %q\n want %q", rel, facts(ts), before[rel])
				}
			}
			// A replacement is a removal and an addition in one set, which the store takes.
			var c ns.Changes
			c.Remove("size", src.Tuples("size")[0]).Add("size", ns.Tuple{Vals: []ns.Value{ns.S("main"), ns.N(41)}})
			if err := s.Apply(bg, c); err != nil {
				t.Fatalf("replacing main's size: %v", err)
			}
			got, _ := s.Lookup(bg, "size", map[int]ns.Value{0: ns.S("main")})
			if len(got) != 1 || got[0].Vals[1].S != "41" {
				t.Errorf("after replacing, main's size is %q, want 41", facts(got))
			}
		})
	}
}

// New refuses a graph the store can't lay out.
func TestNewRefusesAGraphItCantLayOut(t *testing.T) {
	sv := servers(t)[0]
	_, src := code()
	sc := schemasOf(src)
	for _, c := range []struct {
		name string
		g    cypher.Graph
		want string
	}{
		{"an unplaced argument", cypher.Graph{Nodes: []cypher.Node{{Label: "Sized", Relation: "size", Key: 0}}}, "nowhere to be kept"},
		{"a reserved property", cypher.Graph{Nodes: []cypher.Node{{Label: "Sized", Relation: "size", Key: 0, Props: map[string]int{"_n": 1}}}}, "the store's own"},
		{"a number as key", cypher.Graph{Nodes: []cypher.Node{{Label: "Sized", Relation: "size", Key: 1, Props: map[string]int{"id2": 0}}}}, "is a number"},
		{"no schema", cypher.Graph{Nodes: []cypher.Node{{Label: "P", Relation: "pkg", Key: 0}}}, "has no schema"},
	} {
		_, err := New(bg, sv.driver, c.g, sc, Options{})
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: New = %v, want an error saying %q", c.name, err, c.want)
		}
	}
}
