package datalog

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// walkChain is next(n0, n1) … next(n<k-1>, n<k>), each fact citing its source, then z1's two facts,
// which no walk from n0 reaches; the last is z1 to z2.
func walkChain(k int) *ns.MemSource {
	m := ns.NewMemSource().Declare("next", "src", "dst").Declare("jump", "src", "dst")
	for i := range k {
		m.Add("next", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprint("n", i)), ns.S(fmt.Sprint("n", i+1))}, Cites: []string{fmt.Sprint("e", i)}})
	}
	m.Add("next", ns.Tuple{Vals: []ns.Value{ns.S("z1"), ns.S("z3")}})
	m.Add("next", ns.Tuple{Vals: []ns.Value{ns.S("z1"), ns.S("z2")}})
	m.Add("jump", ns.Tuple{Vals: []ns.Value{ns.S("n2"), ns.S("z1")}})
	return m
}

const walkQuestion = `walk(?a, ?b) :- next(?a, ?b); walk(?a, ?c) :- walk(?a, ?b), next(?b, ?c); walk("n0", ?x) => ?x`

// A walk the store follows itself is one call, and answers, cites and witnesses as the rounds' lookups
// do. control: a store that doesn't walk is looked up a level at a time.
func TestAWalkIsOneCall(t *testing.T) {
	q := mustParse(t, walkQuestion)
	want, err := both(q, baseFor(std(walkChain(6))))
	if err != nil || len(want) != 6 {
		t.Fatalf("in memory: %v %v", binds(want), err)
	}
	src := pushingAt(walkChain(6))
	got, err := SemiNaive{}.Eval(bg, q, MustBase(std(walkChain(6)), src))
	if err != nil || !reflect.DeepEqual(binds(got), binds(want)) {
		t.Errorf("walked: %v %v, in memory %v", binds(got), err, binds(want))
	}
	if src.walks() != 1 || len(src.lookups) != 0 {
		t.Errorf("walked %d times and looked up %v, want one walk and no lookups", src.walks(), src.lookups)
	}
	if w := src.walked[0]; w.Relation != "next" || w.From != 0 || w.To != 1 || len(w.Start) != 1 || w.Start[0].S != "n0" {
		t.Errorf("walked %+v, want next from 0 to 1 starting at n0", w)
	}
	uncached := pushingAt(walkChain(6))
	nb := MustBase(std(walkChain(6)), uncached)
	nb.LimitLookupCache(0)
	if _, err := (SemiNaive{}).Eval(bg, q, nb); err != nil || uncached.walks() != 1 || len(uncached.lookups) != 0 {
		t.Errorf("with the Base keeping no lookups, walked %d times and looked up %v, want the Eval's own to answer every round: %v", uncached.walks(), uncached.lookups, err)
	}
	for _, opts := range [][]Option{{Witnesses()}, {Witnesses(), CanonicalCites()}} {
		w, _ := Naive{}.Eval(bg, q, baseFor(std(walkChain(6))), opts...)
		g, err := SemiNaive{}.Eval(bg, q, MustBase(std(walkChain(6)), pushingAt(walkChain(6))), opts...)
		if err != nil || !reflect.DeepEqual(w, g) {
			t.Errorf("with %d options: walked %v %v, in memory %v", len(opts), g, err, w)
		}
	}
	unwalked := pushingAt(walkChain(6))
	unwalked.noWalk = true
	if _, err := (SemiNaive{}).Eval(bg, q, MustBase(std(walkChain(6)), unwalked)); err != nil || unwalked.walks() != 0 || unwalked.lookups["next"] < 6 {
		t.Errorf("control: a store that doesn't walk walked %d times and looked up next %d times, want a lookup a level: %v", unwalked.walks(), unwalked.lookups["next"], err)
	}
}

// A fact the store sends that the walk doesn't reach is never kept as its value's whole answer: z1's
// one fact the sloppy store adds would otherwise answer next("z1", ?x) for later queries on the Base.
func TestAWalkKeepsOnlyWhatItReached(t *testing.T) {
	src := pushingAt(walkChain(4))
	src.sloppy = true
	b := MustBase(std(walkChain(4)), src)
	if _, err := (SemiNaive{}).Eval(bg, mustParse(t, walkQuestion), b); err != nil || src.walks() != 1 {
		t.Fatalf("%d walks: %v", src.walks(), err)
	}
	got, err := SemiNaive{}.Eval(bg, mustParse(t, `next("z1", ?x) => ?x`), b)
	if err != nil || col(got, "x") != "z2,z3" {
		t.Errorf("after the walk, z1 leads to %q %v, want z2,z3", col(got, "x"), err)
	}
	if len(src.lookups) == 0 {
		t.Errorf("control: z1 was answered without a lookup")
	}
}

// A recursion that also grows through a relation the walk doesn't follow walks again from the values
// it reaches that way, and answers as in memory.
func TestARecursionLeavingTheWalkWalksAgain(t *testing.T) {
	q := mustParse(t, `r(?a, ?b) :- next(?a, ?b); r(?a, ?c) :- r(?a, ?b), next(?b, ?c); r(?a, ?c) :- r(?a, ?b), jump(?b, ?c); r("n0", ?x) => ?x`)
	want, err := both(q, baseFor(std(walkChain(4))))
	if err != nil || !slices.ContainsFunc(want, func(r Row) bool { return r.Bind["x"].S == "z3" }) {
		t.Fatalf("control: in memory, %v %v, want z3 reached through jump", binds(want), err)
	}
	src := pushingAt(walkChain(4))
	got, err := SemiNaive{}.Eval(bg, q, MustBase(std(walkChain(4)), src))
	if err != nil || !reflect.DeepEqual(binds(got), binds(want)) {
		t.Errorf("walked %v %v, in memory %v", binds(got), err, binds(want))
	}
	if src.walks() < 2 {
		t.Errorf("walked %d times, want again from z1", src.walks())
	}
}

// A walk is charged to the budget fact by fact, and a failed walk fails the query, wrapped with the
// relation, and isn't kept.
func TestAWalkStopsAtTheBudgetAndFailsAsALookup(t *testing.T) {
	q := mustParse(t, walkQuestion)
	src := pushingAt(walkChain(500))
	_, err := SemiNaive{}.Eval(bg, q, MustBase(std(walkChain(500)), src), Budget(50))
	if over := (*BudgetExceeded)(nil); !errors.As(err, &over) || src.yielded > 51 {
		t.Errorf("under a budget of 50 the walk yielded %d facts: %v, want it stopped at the budget", src.yielded, err)
	}
	whole := pushingAt(walkChain(500))
	if _, err := (SemiNaive{}).Eval(bg, q, MustBase(std(walkChain(500)), whole)); err != nil || whole.yielded < 500 {
		t.Errorf("control: unbudgeted, the walk yielded %d facts: %v", whole.yielded, err)
	}
	broken := errors.New("cursor closed")
	failing := pushingAt(walkChain(4))
	failing.walkFail = broken
	b := MustBase(std(walkChain(4)), failing)
	if _, err := (SemiNaive{}).Eval(bg, q, b); !errors.Is(err, broken) || !strings.Contains(err.Error(), "query: looking up next: ") {
		t.Fatalf("got %v, want the store's error wrapped with the relation", err)
	}
	failing.walkFail = nil
	if got, err := (SemiNaive{}).Eval(bg, q, b); err != nil || len(got) != 4 || failing.walks() != 2 {
		t.Errorf("after the failure, %v %v in %d walks, want four rows from a second walk", binds(got), err, failing.walks())
	}
}

// Explain reports a walk as one call fetching its facts.
func TestExplainReportsAWalk(t *testing.T) {
	var r Report
	if _, err := (SemiNaive{}).Eval(bg, mustParse(t, walkQuestion), MustBase(std(walkChain(6)), pushingAt(walkChain(6))), Explain(&r)); err != nil {
		t.Fatal(err)
	}
	if r.Walks != 1 || r.Fetched != 6 || r.Lookups != 0 {
		t.Errorf("reported %d walks fetching %d facts and %d lookups, want 1, 6 and 0:\n%s", r.Walks, r.Fetched, r.Lookups, r.String())
	}
	walked := false
	for _, br := range r.Rules {
		for _, l := range br.Literals {
			walked = walked || slices.Contains(l.Access, "walked (src)")
		}
	}
	if !walked {
		t.Errorf("no literal was read by a walk:\n%s", r.String())
	}
}

// A walk over calls with a constant follows only the facts matching it: a sloppy store's refs fact
// to q1 doesn't make q1 reached, so its calls are looked up rather than kept as none.
func TestAWalkFollowsOnlyItsConstants(t *testing.T) {
	m := ns.NewMemSource().Declare("edge", "src", "kind", "dst")
	add := func(s, k, d string) { m.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(s), ns.S(k), ns.S(d)}}) }
	add("n0", "calls", "n1")
	add("q1", "calls", "q2")
	add("n1", "refs", "q1")
	q := mustParse(t, `r(?a, ?b) :- edge(?a, "calls", ?b); r(?a, ?c) :- r(?a, ?b), edge(?b, "calls", ?c); r("n0", ?x) => ?x`)
	src := pushingAt(m)
	src.sloppy = true
	b := MustBase(std(m), src)
	if got, err := (SemiNaive{}).Eval(bg, q, b); err != nil || col(got, "x") != "n1" || src.walks() != 1 || len(src.lookups) != 0 {
		t.Fatalf("walked %d times, looked up %v, answered %q %v, want n1 from one walk", src.walks(), src.lookups, col(got, "x"), err)
	}
	if w := src.walked[0]; len(w.Bound) != 1 || w.Bound[1].S != "calls" {
		t.Errorf("walked %+v, want the kind bound to calls", w)
	}
	got, err := SemiNaive{}.Eval(bg, mustParse(t, `edge("q1", "calls", ?x) => ?x`), b)
	if err != nil || col(got, "x") != "q2" {
		t.Errorf("after the walk, q1 calls %q %v, want q2", col(got, "x"), err)
	}
}
