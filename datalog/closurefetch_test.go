package datalog

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/panyam/jaala/ns"
)

// treeEdges is a binary tree of edges, t<k> to t<2k+1> and t<2k+2>, n nodes deep.
func treeEdges(n int) *ns.MemSource {
	src := ns.NewMemSource().Declare("edge", "from", "to")
	for k := range n {
		for _, c := range []int{2*k + 1, 2*k + 2} {
			if c < n {
				src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("t%d", k)), ns.S(fmt.Sprintf("t%d", c))}})
			}
		}
	}
	return src
}

// fetchedClosures are calls whose walks prefetchClosures fetches (#396): from starts the literals
// before them bind, back from a bound end, a paths call, through a step relation's rules, and with
// both ends bound, stopping at the target unless it must be more than a step out. looked is the
// lookups made outside a batch.
var fetchedClosures = []struct {
	text    string
	batches []string
	looked  int
}{
	{`edge("t0", ?s), graph.closure("edge", ?s, ?x, 1, 2, _) => ?s, ?x`, []string{"edge×2", "edge×4"}, 1},
	{`graph.closure("edge", ?x, "t13", 1, 0, ?h) => ?x, ?h`, []string{"edge×1", "edge×1", "edge×1", "edge×1"}, 0},
	{`edge("t0", ?s), graph.paths("edge", ?s, ?x, "trail", 1, 2, ?p) => ?p`, []string{"edge×2", "edge×4"}, 1},
	{`down(?a, ?b) :- edge(?a, ?b); graph.closure("down", "t1", ?x, 1, 3, _) => ?x`, []string{"edge×1", "edge×2", "edge×4"}, 0},
	{`graph.closure("edge", "t0", "t2", 1, 0, ?h) => ?h`, []string{"edge×1"}, 0},
	{`graph.closure("edge", "t0", "t1", 2, 0, ?h) => ?h`, []string{"edge×1", "edge×2", "edge×4", "edge×8"}, 0},
}

// A closure's or a paths call's walk over a batching Source is asked one batch per level, from every
// start its call site has at once, rather than one lookup per node it reaches, and answers as Naive
// does over the facts in memory. control: a Source that doesn't batch is asked once per node.
func TestAClosureIsBatchedOncePerLevel(t *testing.T) {
	tree := treeEdges(15)
	for _, c := range fetchedClosures {
		q := mustParse(t, c.text)
		want, err := Naive{}.Eval(bg, q, MustBase(std(tree), tree))
		if err != nil {
			t.Fatal(err)
		}
		src := batchingAt(tree)
		got, err := SemiNaive{}.Eval(bg, q, MustBase(std(src), src))
		if err != nil || !reflect.DeepEqual(binds(got), binds(want)) {
			t.Errorf("%s: batched %v %v, want %v", c.text, binds(got), err, binds(want))
		}
		if !reflect.DeepEqual(src.batches, c.batches) || src.looked("edge") != c.looked {
			t.Errorf("%s: batched %v and looked up %d besides, want %v and %d", c.text, src.batches, src.looked("edge"), c.batches, c.looked)
		}
		looked := 0
		for _, b := range c.batches {
			var n int
			fmt.Sscanf(b, "edge×%d", &n)
			looked += n
		}
		plain := lookingAt(tree)
		if _, err := (SemiNaive{}).Eval(bg, q, MustBase(std(plain), plain)); err != nil || plain.looked("edge") < looked {
			t.Errorf("control: %s over a Source that doesn't batch looked up edge %d times, want at least %d: %v", c.text, plain.looked("edge"), looked, err)
		}
	}
}

// A walking Source is asked for a closure's whole walk in one call, bounded by its max (#396), and a
// value the walk reached at the bound isn't kept as a complete lookup, since its steps weren't asked
// for: asking further on the same Base looks up past it and answers as a fresh Base does.
func TestAClosureWalksAStoreToItsMax(t *testing.T) {
	tree := treeEdges(31)
	src := pushingAt(tree)
	b := MustBase(std(src), src)
	near := mustParse(t, `graph.closure("edge", "t0", ?x, 1, 2, _) => ?x`)
	if _, err := (SemiNaive{}).Eval(bg, near, b); err != nil {
		t.Fatal(err)
	}
	if len(src.walked) != 1 || src.walked[0].MaxDepth != 2 || len(src.walked[0].Start) != 1 {
		t.Fatalf("walked %+v, want one walk from t0 to depth 2", src.walked)
	}
	if src.looked("edge") != 0 {
		t.Errorf("the walk looked up edge %d times besides, want none", src.looked("edge"))
	}
	far := mustParse(t, `graph.closure("edge", "t0", ?x, 1, 4, _) => ?x`)
	want, err := Naive{}.Eval(bg, far, MustBase(std(tree), tree))
	if err != nil {
		t.Fatal(err)
	}
	got, err := SemiNaive{}.Eval(bg, far, b)
	if err != nil || !reflect.DeepEqual(binds(got), binds(want)) {
		t.Errorf("further on the same Base: %v %v, want %v", binds(got), err, binds(want))
	}
	if len(want) != 30 {
		t.Errorf("control: the tree has %d nodes below t0 within 4 steps, want 30", len(want))
	}
	plain := pushingAt(tree)
	plain.noWalk = true
	if _, err := (SemiNaive{}).Eval(bg, near, MustBase(std(plain), plain)); err != nil || len(plain.walked) != 0 || plain.looked("edge") == 0 {
		t.Errorf("control: a store that doesn't walk walked %d times and looked up %d: %v", len(plain.walked), plain.looked("edge"), err)
	}
}
