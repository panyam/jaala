package datalog

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/panyam/jaala/ns"
)

// answer runs one query on b and returns its rows and the work it alone took.
func answer(t *testing.T, b *Base, ev Evaluator, text string, opts ...Option) ([]Row, int64) {
	t.Helper()
	before := b.Work()
	rows, err := ev.Eval(bg, mustParse(t, text), b, opts...)
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return rows, b.Work() - before
}

// fresh answers text on a Base of its own over v, as the reference for what a held relation must give.
func fresh(t *testing.T, v *ns.Vocabulary, ev Evaluator, text string, opts ...Option) ([]Row, int64) {
	t.Helper()
	return answer(t, baseFor(v), ev, text, opts...)
}

func sameBinds(a, b []Row) bool { return reflect.DeepEqual(binds(a), binds(b)) }

const allReach = closure + `reach(?a, ?b) => ?a, ?b`

// A second query calling a relation the first evaluated in full reads it instead of deriving it
// (#140), and answers as a fresh Base does. control: a Base that keeps nothing derives it twice.
func TestASecondQueryReadsAHeldRelation(t *testing.T) {
	v := std(reversedLine(60))
	b := baseFor(v)
	first, w1 := answer(t, b, SemiNaive{}, allReach)
	second, w2 := answer(t, b, SemiNaive{}, allReach)
	if !sameBinds(first, second) || w2 >= w1/2 {
		t.Errorf("second query: %d rows, work %d after %d; want the same rows for under half the work", len(second), w2, w1)
	}
	// Held in full, a bound call reads the relation by index rather than demanding it again.
	bound := closure + `reach("v7", ?b) => ?b`
	got, w := answer(t, b, SemiNaive{}, bound)
	want, wf := fresh(t, v, SemiNaive{}, bound)
	if !sameBinds(got, want) || w > wf {
		t.Errorf("bound call over the held relation: rows %v, work %d; want %v for no more than demand's %d", col(got, "b"), w, col(want, "b"), wf)
	}
	off := baseFor(v)
	off.LimitDerivedCache(0)
	_, c1 := answer(t, off, SemiNaive{}, allReach)
	if _, c2 := answer(t, off, SemiNaive{}, allReach); c2 != c1 {
		t.Errorf("control: with nothing kept, the second query took %d, want the first's %d", c2, c1)
	}
}

// A relation's key covers what it reads: far's own rules are the same in both queries, but the reach
// under it is not, so the second query derives its own far.
func TestARelationReadingARedefinedOneIsDerivedAgain(t *testing.T) {
	v := std(reversedLine(30))
	b := baseFor(v)
	const far = `far(?a, ?b) :- reach(?a, ?b), node(?a); far(?a, ?b) :- reach(?a, ?b), node(?b); far(?a, ?b) => ?a, ?b`
	const oneHop = `reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?b) :- edge(?a, ?b), node(?b); `
	all, _ := answer(t, b, SemiNaive{}, closure+far)
	got, _ := answer(t, b, SemiNaive{}, oneHop+far)
	want, _ := fresh(t, v, SemiNaive{}, oneHop+far)
	if !sameBinds(got, want) || len(got) == len(all) {
		t.Errorf("far over one-hop reach: %d rows, want %d (the closure's far has %d)", len(got), len(want), len(all))
	}
}

// A library member is held like a query's own rule, which is what a host answering many queries over
// one Base calls most.
func TestAModuleMemberIsHeld(t *testing.T) {
	b := baseFor(withModules(t, "path", reachModule))
	const q = `path.reach(?a, ?b) => ?a, ?b`
	first, w1 := answer(t, b, SemiNaive{}, q)
	second, w2 := answer(t, b, SemiNaive{}, q)
	if !sameBinds(first, second) || w2 >= w1 {
		t.Errorf("second query: work %d after %d; want the same rows for less", w2, w1)
	}
}

// A relation derived under demand holds only what one query asked for, so it is never kept: a later
// query asking for all of it gets all of it. control: the first query really was demanded, costing
// less than the whole relation.
func TestADemandedRelationIsNotKept(t *testing.T) {
	v := std(reversedLine(60))
	b := baseFor(v)
	_, w1 := answer(t, b, SemiNaive{}, closure+`reach("v0", ?b) => ?b`)
	got, w2 := answer(t, b, SemiNaive{}, allReach)
	want, wf := fresh(t, v, SemiNaive{}, allReach)
	if !sameBinds(got, want) || w2 != wf {
		t.Errorf("after a demanded call: %d rows for work %d, want %d rows for %d", len(got), w2, len(want), wf)
	}
	if w1 >= wf {
		t.Errorf("control: the demanded call took %d, no less than the whole relation's %d", w1, wf)
	}
}

// versioned is a MemSource reporting a version a test moves when it changes the facts.
type versioned struct {
	*ns.MemSource
	v string
}

func (s *versioned) Version() string { return s.v }

func extend(src *ns.MemSource, n int) {
	src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("v%d", n))}})
	src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("v%d", n-1)), ns.S(fmt.Sprintf("v%d", n))}})
}

// When a Versioned Source's version moves, the Base reads it again and derives again. control: changed
// with the version left alone, the Base still answers from what it held, so the version is what made
// the difference.
func TestAMovedVersionDropsWhatTheBaseHeld(t *testing.T) {
	mem := reversedLine(20)
	src := &versioned{MemSource: mem, v: "1"}
	v := std(mem)
	b := MustBase(v, src)
	before, _ := answer(t, b, SemiNaive{}, allReach)
	extend(mem, 20)
	if stale, _ := answer(t, b, SemiNaive{}, allReach); !sameBinds(stale, before) {
		t.Fatalf("control: unversioned change seen (%d rows, then %d), so nothing was held", len(before), len(stale))
	}
	src.v = "2"
	got, _ := answer(t, b, SemiNaive{}, allReach)
	want, _ := fresh(t, v, SemiNaive{}, allReach)
	if !sameBinds(got, want) || len(got) == len(before) {
		t.Errorf("after the version moved: %d rows, want %d", len(got), len(want))
	}
}

// Forget does for any Source what a moved version does for a Versioned one.
func TestForgetDropsWhatTheBaseHeld(t *testing.T) {
	mem := reversedLine(20)
	v := std(mem)
	b := baseFor(v)
	before, _ := answer(t, b, SemiNaive{}, allReach)
	extend(mem, 20)
	if stale, _ := answer(t, b, SemiNaive{}, allReach); !sameBinds(stale, before) {
		t.Fatalf("control: the change was seen before Forget, so nothing was held")
	}
	b.Forget()
	got, _ := answer(t, b, SemiNaive{}, allReach)
	want, _ := fresh(t, v, SemiNaive{}, allReach)
	if !sameBinds(got, want) || len(got) == len(before) {
		t.Errorf("after Forget: %d rows, want %d", len(got), len(want))
	}
}

// stepper registers step(from, to), following edges from a bound start, and counts its calls.
func stepper(t *testing.T, v *ns.Vocabulary, volatile bool) *int {
	t.Helper()
	calls := 0
	err := v.AddPredicate("step", ns.Builtin{Arity: 2, Volatile: volatile, Modes: [][]bool{{false, false}}, Gen: func(_ context.Context, src ns.Source, _ []ns.Arg, emit func([]ns.Value, []string) error) error {
		calls++
		for _, e := range src.Tuples("edge") {
			if err := emit(e.Vals, nil); err != nil {
				return err
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &calls
}

// A relation reading a Volatile predicate, directly or through another derived relation, is derived
// by every query. control: the same predicate not marked Volatile is called by the first query only.
func TestARelationReadingAVolatilePredicateIsNeverKept(t *testing.T) {
	const rules = `r(?a, ?b) :- step(?a, ?b); r(?a, ?c) :- r(?a, ?b), step(?b, ?c); ` +
		`s(?a) :- r(?a, _), node(?a); s(?a) :- r(_, ?a), node(?a); `
	for _, volatile := range []bool{true, false} {
		for _, goal := range []string{`r(?a, ?b) => ?a, ?b`, `s(?a) => ?a`} {
			v := std(line(10))
			calls := stepper(t, v, volatile)
			b := baseFor(v)
			answer(t, b, SemiNaive{}, rules+goal)
			*calls = 0
			answer(t, b, SemiNaive{}, rules+goal)
			if called := *calls > 0; called != volatile {
				t.Errorf("%s, Volatile %v: the second query called step %d times", goal, volatile, *calls)
			}
		}
	}
}

// The Base keeps no more derived tuples than its limit, dropping the relation used least recently. A
// relation larger than the limit is never kept.
func TestTheDerivedCacheKeepsToItsLimit(t *testing.T) {
	const back = `back(?a, ?b) :- edge(?b, ?a); back(?a, ?c) :- back(?a, ?b), edge(?c, ?b); back(?a, ?b) => ?a, ?b`
	v := std(reversedLine(30)) // reach and back each hold 435 tuples
	held := func(b *Base, q string, full int64) bool {
		_, w := answer(t, b, SemiNaive{}, q)
		return w < full/2
	}
	_, reachFull := fresh(t, v, SemiNaive{}, allReach)
	_, backFull := fresh(t, v, SemiNaive{}, closure+back)

	b := baseFor(v)
	b.LimitDerivedCache(500)
	answer(t, b, SemiNaive{}, allReach)
	answer(t, b, SemiNaive{}, closure+back) // reach is dropped to make room
	if !held(b, closure+back, backFull) {
		t.Errorf("control: back, the latest, should be held")
	}
	if held(b, allReach, reachFull) {
		t.Errorf("reach should have been dropped for back")
	}

	// Used least recently, not stored first: reach, read again, outlives back when a third arrives.
	const hops = `hops(?a, ?b) :- edge(?a, ?b); hops(?a, ?c) :- hops(?a, ?b), edge(?b, ?c), node(?c); hops(?a, ?b) => ?a, ?b`
	_, hopsFull := fresh(t, v, SemiNaive{}, hops)
	lru := baseFor(v)
	lru.LimitDerivedCache(900)
	answer(t, lru, SemiNaive{}, allReach)
	answer(t, lru, SemiNaive{}, closure+back)
	answer(t, lru, SemiNaive{}, allReach)
	answer(t, lru, SemiNaive{}, hops)
	// Each check reads what it checks, and a miss derives and keeps it, so the miss goes last.
	if !held(lru, hops, hopsFull) {
		t.Errorf("control: hops, the latest, should be held")
	}
	if !held(lru, allReach, reachFull) {
		t.Errorf("reach, used after back, was dropped for hops")
	}
	if held(lru, closure+back, backFull) {
		t.Errorf("back, used least recently, should have been dropped for hops")
	}

	small := baseFor(v)
	small.LimitDerivedCache(400)
	answer(t, small, SemiNaive{}, allReach)
	if held(small, allReach, reachFull) {
		t.Errorf("reach (435 tuples) was kept under a limit of 400")
	}
}

// A witnessed query neither reads a held relation, whose tuples carry no witnesses, nor keeps its
// own. Naive, the reference, does neither either.
func TestOnlyAPlainSemiNaiveQueryUsesTheCache(t *testing.T) {
	v := std(reversedLine(30))
	for _, c := range []struct {
		name         string
		first, then  Evaluator
		fOpts, tOpts []Option
	}{
		{"witnessed after plain", SemiNaive{}, SemiNaive{}, nil, []Option{Witnesses()}},
		{"plain after witnessed", SemiNaive{}, SemiNaive{}, []Option{Witnesses()}, nil},
		{"Naive after SemiNaive", SemiNaive{}, Naive{}, nil, nil},
		{"SemiNaive after Naive", Naive{}, SemiNaive{}, nil, nil},
		{"SemiNaive after written order", SemiNaive{WrittenOrder: true}, SemiNaive{}, nil, nil},
	} {
		b := baseFor(v)
		answer(t, b, c.first, allReach, c.fOpts...)
		got, w := answer(t, b, c.then, allReach, c.tOpts...)
		want, wf := fresh(t, v, c.then, allReach, c.tOpts...)
		if !sameBinds(got, want) || w != wf {
			t.Errorf("%s: work %d, want a fresh Base's %d", c.name, w, wf)
		}
	}
}

// A query that stops before its fixpoint ends keeps nothing.
func TestAStoppedQueryKeepsNothing(t *testing.T) {
	v := std(reversedLine(30))
	b := baseFor(v)
	if _, err := (SemiNaive{}).Eval(bg, mustParse(t, allReach), b, Budget(100)); err == nil {
		t.Fatal("control: a budget of 100 should stop the closure")
	}
	_, w := answer(t, b, SemiNaive{}, allReach)
	if _, wf := fresh(t, v, SemiNaive{}, allReach); w != wf {
		t.Errorf("after a stopped query: work %d, want a fresh Base's %d", w, wf)
	}
}

// Queries sharing a Base keep and read derived relations at once, while another goroutine drops them.
// Run under -race, which CI does.
func TestConcurrentQueriesShareTheDerivedCache(t *testing.T) {
	v := std(reversedLine(40))
	queries := []string{allReach, closure + `reach("v3", ?b) => ?b`, closure + `reach(?a, "v30") => ?a`}
	want := make([][]Row, len(queries))
	for i, q := range queries {
		want[i], _ = fresh(t, v, SemiNaive{}, q)
	}
	b := baseFor(v)
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 12; i++ {
				k := (g + i) % len(queries)
				if g == 0 && i%4 == 0 {
					b.Forget()
				}
				rows, err := (SemiNaive{}).Eval(bg, mustParse(t, queries[k]), b)
				if err != nil || !sameBinds(rows, want[k]) {
					errs <- fmt.Sprintf("%s: %d rows, %v", queries[k], len(rows), err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
}

// An Eval that began before the caches were dropped finishes on what it read, and leaves nothing
// behind: neither the derived relations it evaluated nor an index over the tuples it read, whose
// positions would point into a relation the next Eval reads again.
func TestAnEvalStartedBeforeADropKeepsNothing(t *testing.T) {
	c := newDerivedCache()
	gen := c.gen
	c.drop()
	c.put(gen, "k", 1, []idbTuple{{vals: []ns.Value{ns.S("a")}}})
	if _, ok := c.get("k"); ok {
		t.Errorf("a relation derived before the drop was kept")
	}
	c.put(c.gen, "k", 1, []idbTuple{{vals: []ns.Value{ns.S("a")}}})
	if _, ok := c.get("k"); !ok {
		t.Errorf("control: a relation derived after the drop should be kept")
	}

	src := reversedLine(40)
	e := newEDBCache()
	old, err := e.tuples(nil, "edge", src, nil, nil)
	if err != nil || len(old) < indexMinFacts {
		t.Fatalf("control: read %d edges, %v; want enough to index", len(old), err)
	}
	e.reset()
	e.get("edge", old, 1)
	if len(e.idx) != 0 {
		t.Errorf("an index over tuples read before the reset was filed")
	}
	now, _ := e.tuples(nil, "edge", src, nil, nil)
	e.get("edge", now, 1)
	if len(e.idx) != 1 {
		t.Errorf("control: an index over the tuples read again should be filed")
	}
}
