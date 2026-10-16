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

// A recursive rule reading the walked relation through a helper that only renames it walks as one
// reading it itself (#319): the helper is inlined into the recursive rule. control: a helper that also
// compares stays a derived relation there, so its relation is looked up a level at a time.
func TestARecursionThroughARenamingHelperWalks(t *testing.T) {
	const rec = `walk(?a, ?b) :- _step(?a, ?b); walk(?a, ?c) :- walk(?a, ?b), _step(?b, ?c); `
	for _, c := range []struct {
		name, step      string
		walks, lookedUp bool
	}{
		{"renames", `_step(?a, ?b) :- next(?a, ?b); `, true, false},
		{"compares", `_step(?a, ?b) :- next(?a, ?b), ?a != ?b; `, false, true},
	} {
		q := mustParse(t, rec+c.step+`walk("n0", ?x) => ?x`)
		want, err := both(q, baseFor(std(walkChain(6))))
		if err != nil || len(want) != 6 {
			t.Fatalf("%s, in memory: %v %v", c.name, binds(want), err)
		}
		src := pushingAt(walkChain(6))
		got, err := SemiNaive{}.Eval(bg, q, MustBase(std(walkChain(6)), src))
		if err != nil || !reflect.DeepEqual(binds(got), binds(want)) {
			t.Errorf("%s: walked %v %v, in memory %v", c.name, binds(got), err, binds(want))
		}
		if (src.walks() == 1) != c.walks || (src.lookups["next"] >= 6) != c.lookedUp {
			t.Errorf("%s: walked %d times and looked up next %d times", c.name, src.walks(), src.lookups["next"])
		}
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

// stepChain is a call graph two relations a step: edge(n<i>, f<i>) from a node to its function and
// call(f<i>, n<i+1>) on to the next node, n0 to n8; and z1's edges to g1 and g2, which call z2 and z3,
// which no walk from n0 reaches. The last call listed is g2's.
func stepChain() *ns.MemSource {
	m := ns.NewMemSource().Declare("edge", "from", "to").Declare("call", "from", "to")
	for i := range 8 {
		f := fmt.Sprint("f", i)
		m.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprint("n", i)), ns.S(f)}, Cites: []string{fmt.Sprint("e", i)}})
		m.Add("call", ns.Tuple{Vals: []ns.Value{ns.S(f), ns.S(fmt.Sprint("n", i+1))}, Cites: []string{fmt.Sprint("c", i)}})
	}
	for i, g := range []string{"g1", "g2"} {
		m.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("z1"), ns.S(g)}})
		m.Add("call", ns.Tuple{Vals: []ns.Value{ns.S(g), ns.S(fmt.Sprint("z", i+2))}})
	}
	return m
}

const stepQuestion = `r(?a, ?c) :- edge(?a, ?m), call(?m, ?c); r(?a, ?c) :- r(?a, ?b), edge(?b, ?m), call(?m, ?c); r("n0", ?x) => ?x`

// A walk whose step is several literals is one call (#250), the demanded base rule's step included,
// and answers, cites and witnesses as the rounds do. control: a store that doesn't walk a step is
// pushed a batch a round (#246).
func TestAStepOfSeveralLiteralsIsWalkedInOneCall(t *testing.T) {
	q := mustParse(t, stepQuestion)
	want, err := both(q, baseFor(std(stepChain())))
	if err != nil || len(want) < 2 {
		t.Fatalf("in memory: %v %v", binds(want), err)
	}
	for _, opts := range [][]Option{nil, {Witnesses(), CanonicalCites()}} {
		w, _ := Naive{}.Eval(bg, q, baseFor(std(stepChain())), opts...)
		src := pushingAt(stepChain())
		got, err := SemiNaive{}.Eval(bg, q, MustBase(std(stepChain()), src), opts...)
		if err != nil || !reflect.DeepEqual(got, w) {
			t.Errorf("with %d options: walked %v %v, in memory %v", len(opts), got, err, w)
		}
		if src.stepWalks != 1 || src.pushes() != 0 || len(src.lookups) != 0 {
			t.Errorf("with %d options: %d step walks, %d pushes, lookups %v, want one walk and nothing else", len(opts), src.stepWalks, src.pushes(), src.lookups)
		}
	}
	unwalked := pushingAt(stepChain())
	unwalked.noStepWalk = true
	if _, err := (SemiNaive{}).Eval(bg, q, MustBase(std(stepChain()), unwalked)); err != nil || unwalked.stepWalks != 0 || unwalked.pushes() < 2 {
		t.Errorf("control: a store that doesn't walk a step was pushed %d times and walked %d: %v", unwalked.pushes(), unwalked.stepWalks, err)
	}
}

// A match the store sends that the walk doesn't reach is never kept as its value's whole answer: z1's
// one match the sloppy store adds (through g2) would otherwise answer the step from z1, which has
// two, for later queries.
func TestAStepWalkKeepsOnlyWhatItReached(t *testing.T) {
	src := pushingAt(stepChain())
	src.sloppy = true
	b := MustBase(std(stepChain()), src)
	first, _ := both(mustParse(t, stepQuestion), baseFor(std(stepChain())))
	if got, err := (SemiNaive{}).Eval(bg, mustParse(t, stepQuestion), b); err != nil || src.stepWalks != 1 || !reflect.DeepEqual(binds(got), binds(first)) {
		t.Fatalf("%d step walks answered %v %v, want %v: a row whose join fails is no match", src.stepWalks, binds(got), err, binds(first))
	}
	q := mustParse(t, `edge("z1", ?m), call(?m, ?c) => ?c`)
	want, _ := both(q, baseFor(std(stepChain())))
	got, err := SemiNaive{}.Eval(bg, q, b)
	if err != nil || len(want) != 2 || !reflect.DeepEqual(binds(got), binds(want)) {
		t.Errorf("after the walk, z1's step answered %v %v, want %v", binds(got), err, binds(want))
	}
	if src.pushes() == 0 {
		t.Errorf("control: z1's step was answered without asking the store")
	}
}

// A failed step walk fails the query, wrapped with the step, and isn't kept; a step walk is charged to
// the budget row by row.
func TestAFailedStepWalkIsReportedAndNotKept(t *testing.T) {
	src := pushingAt(stepChain())
	broken := errors.New("cursor closed")
	src.walkFail = broken
	b := MustBase(std(stepChain()), src)
	q := mustParse(t, stepQuestion)
	if _, err := (SemiNaive{}).Eval(bg, q, b); !errors.Is(err, broken) || !strings.Contains(err.Error(), "query: pushing edge(") {
		t.Fatalf("got %v, want the store's error wrapped with the step", err)
	}
	src.walkFail = nil
	if got, err := (SemiNaive{}).Eval(bg, q, b); err != nil || len(got) < 2 || src.stepWalks != 2 {
		t.Errorf("after the failure, %v %v in %d step walks, want a second walk", binds(got), err, src.stepWalks)
	}
	long := ns.NewMemSource().Declare("edge", "from", "to").Declare("call", "from", "to")
	for i := range 300 {
		long.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprint("n", i)), ns.S(fmt.Sprint("f", i))}})
		long.Add("call", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprint("f", i)), ns.S(fmt.Sprint("n", i+1))}})
	}
	big := pushingAt(long)
	_, err := SemiNaive{}.Eval(bg, q, MustBase(std(long), big), Budget(50))
	if over := (*BudgetExceeded)(nil); !errors.As(err, &over) || big.yielded > 51 {
		t.Errorf("under a budget of 50 the step walk yielded %d rows: %v, want it stopped there", big.yielded, err)
	}
}
