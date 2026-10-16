package datalog

import (
	"fmt"
	"testing"

	"github.com/panyam/jaala/ns"
)

// pairs is n tuples pair(i, i+1), so pair(?x, ?x) tries every one and keeps none.
func pairs(n int) *Base {
	src := ns.NewMemSource().Declare("pair", "a", "b")
	for i := 0; i < n; i++ {
		src.Add("pair", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprint(i)), ns.S(fmt.Sprint(i + 1))}})
	}
	return baseFor(std(src))
}

func TestACandidateThatFailsToUnifyAllocatesNothing(t *testing.T) {
	q := mustParse(t, `pair(?x, ?x) => ?x`)
	allocs := func(n int) float64 {
		b := pairs(n)
		if rows, err := (SemiNaive{}).Eval(bg, q, b); err != nil || len(rows) != 0 {
			t.Fatalf("pair(?x, ?x) over %d pairs: %v, %v", n, rows, err)
		}
		return testing.AllocsPerRun(5, func() { _, _ = (SemiNaive{}).Eval(bg, q, b) })
	}
	small, large := allocs(100), allocs(2000)
	// A query has a fixed cost (parsing aside: linking, rewriting, the answer); 1900 more candidates
	// that all fail to unify add nothing to it. Cloning a binding per candidate added two each.
	if large-small > 50 {
		t.Errorf("1900 more failing candidates allocated %.0f more times (%.0f against %.0f), want none", large-small, large, small)
	}
}

func TestResetRestoresASpellingTheExtensionReplaced(t *testing.T) {
	b := newBinding()
	one := 1.0
	padded := ns.Value{S: "01", Num: &one}
	if !bindArg(b, Term{Var: "x"}, padded) {
		t.Fatal("binding ?x failed")
	}
	m := b.mark()
	if !bindArg(b, Term{Var: "x"}, ns.N(1)) || !bindArg(b, Term{Var: "y"}, ns.S("z")) {
		t.Fatal("extending failed")
	}
	if v, _ := b.get("x"); v.S != "1" {
		t.Fatalf("control: the shorter spelling should replace 01 while extended, got %q", v.S)
	}
	b.reset(m)
	if v, _ := b.get("x"); v.S != "01" {
		t.Errorf("after reset ?x is %q, want the 01 it had at the mark", v.S)
	}
	if _, ok := b.get("y"); ok {
		t.Error("after reset ?y is still bound")
	}
}
