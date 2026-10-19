package datalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/panyam/jaala/ns"
)

// counted is a MemDerivedStore counting its calls, optionally failing reads or writes, or handing
// back tuples of the wrong arity.
type counted struct {
	ns.MemDerivedStore
	reads, writes atomic.Int64
	failRead      bool
	failWrite     bool
	short         bool
}

var errStore = errors.New("store down")

func (s *counted) Derived(ctx context.Context, key ns.DerivedKey) ([]ns.Tuple, bool, error) {
	s.reads.Add(1)
	if s.failRead {
		return nil, false, errStore
	}
	t, ok, err := s.MemDerivedStore.Derived(ctx, key)
	if s.short && ok {
		t = []ns.Tuple{{Vals: t[0].Vals[:1]}}
	}
	return t, ok, err
}

func (s *counted) KeepDerived(ctx context.Context, key ns.DerivedKey, tuples []ns.Tuple) error {
	s.writes.Add(1)
	if s.failWrite {
		return errStore
	}
	return s.MemDerivedStore.KeepDerived(ctx, key, tuples)
}

// storedBase is a Base over src keeping its derived relations in store.
func storedBase(v *ns.Vocabulary, src ns.Source, store ns.DerivedStore) *Base {
	b := MustBase(v, src)
	b.KeepDerivedIn(store)
	return b
}

// A second Base over the same version reads what the first derived from the store, and answers as a
// fresh Base does (#220). control: a second Base without the store derives it again.
func TestADerivedRelationIsReadFromTheHostStoreOnAFreshBase(t *testing.T) {
	mem := reversedLine(40)
	src := &versioned{MemSource: mem, v: "1"}
	v := std(mem)
	store := &counted{}
	first, w1 := answer(t, storedBase(v, src, store), SemiNaive{}, allReach)
	if store.writes.Load() == 0 {
		t.Fatalf("the first Base wrote nothing to the store")
	}
	if kept, ok := store.Relation("reach", "1"); !ok || len(kept) != len(first) {
		t.Errorf("the store holds %d reach tuples at version 1, want the answer's %d", len(kept), len(first))
	}
	second, w2 := answer(t, storedBase(v, src, store), SemiNaive{}, allReach)
	if !sameBinds(first, second) || w2 >= w1/2 {
		t.Errorf("second Base: %d rows for work %d after %d; want the same rows for under half the work", len(second), w2, w1)
	}
	if _, wc := answer(t, MustBase(v, src), SemiNaive{}, allReach); wc != w1 {
		t.Errorf("control: a Base without the store took %d, want the first's %d", wc, w1)
	}
}

// With the Base's own memory off, the store is the only tier, so one Base reads its own relation back.
func TestTheStoreIsReadWhenTheBaseKeepsNothing(t *testing.T) {
	mem := reversedLine(40)
	src := &versioned{MemSource: mem, v: "1"}
	b := storedBase(std(mem), src, &counted{})
	b.LimitDerivedCache(0)
	first, w1 := answer(t, b, SemiNaive{}, allReach)
	second, w2 := answer(t, b, SemiNaive{}, allReach)
	if !sameBinds(first, second) || w2 >= w1/2 {
		t.Errorf("second query: work %d after %d; want the same rows for under half", w2, w1)
	}
}

// A moved version is a new key: the Base derives over the new facts and stores them beside the old.
// control: before the move, a second Base reads what was stored.
func TestAMovedVersionStoresANewEntry(t *testing.T) {
	mem := reversedLine(20)
	src := &versioned{MemSource: mem, v: "1"}
	v := std(mem)
	store := &counted{}
	before, _ := answer(t, storedBase(v, src, store), SemiNaive{}, allReach)
	if _, w := answer(t, storedBase(v, src, store), SemiNaive{}, allReach); w >= reachWork(t, v, src)/2 {
		t.Fatalf("control: a second Base at version 1 took %d, so it didn't read the store", w)
	}
	extend(mem, 20)
	src.v = "2"
	got, _ := answer(t, storedBase(v, src, store), SemiNaive{}, allReach)
	want, _ := fresh(t, v, SemiNaive{}, allReach)
	if !sameBinds(got, want) || len(got) == len(before) {
		t.Errorf("after the version moved: %d rows, want %d (version 1 had %d)", len(got), len(want), len(before))
	}
	old, ok1 := store.Relation("reach", "1")
	now, ok2 := store.Relation("reach", "2")
	if !ok1 || !ok2 || len(old) != len(before) || len(now) != len(want) {
		t.Errorf("the store holds %d tuples at 1 and %d at 2, want %d and %d", len(old), len(now), len(before), len(want))
	}
}

// reachWork is what allReach costs on a Base that keeps nothing.
func reachWork(t *testing.T, v *ns.Vocabulary, src ns.Source) int64 {
	b := MustBase(v, src)
	b.LimitDerivedCache(0)
	_, w := answer(t, b, SemiNaive{}, allReach)
	return w
}

// A witnessed Eval, Naive, and the written order neither read the store nor write it, as with the
// Base's memory. control: a plain SemiNaive Eval does both.
func TestOnlyAPlainSemiNaiveEvalUsesTheStore(t *testing.T) {
	mem := reversedLine(30)
	src := &versioned{MemSource: mem, v: "1"}
	v := std(mem)
	for _, c := range []struct {
		name string
		ev   Evaluator
		opts []Option
	}{
		{"witnessed", SemiNaive{}, []Option{Witnesses()}},
		{"canonical", SemiNaive{}, []Option{CanonicalCites()}},
		{"Naive", Naive{}, nil},
		{"written order", SemiNaive{WrittenOrder: true}, nil},
	} {
		store := &counted{}
		answer(t, storedBase(v, src, store), SemiNaive{}, allReach)
		reads, writes := store.reads.Load(), store.writes.Load()
		got, w := answer(t, storedBase(v, src, store), c.ev, allReach, c.opts...)
		want, wf := fresh(t, v, c.ev, allReach, c.opts...)
		if !sameBinds(got, want) || w != wf || store.reads.Load() != reads || store.writes.Load() != writes {
			t.Errorf("%s: work %d (fresh %d), %d reads and %d writes after the first Eval's %d and %d",
				c.name, w, wf, store.reads.Load(), store.writes.Load(), reads, writes)
		}
	}
	store := &counted{}
	answer(t, storedBase(v, src, store), SemiNaive{}, allReach)
	answer(t, storedBase(v, src, store), SemiNaive{}, allReach)
	if store.reads.Load() == 0 || store.writes.Load() == 0 {
		t.Errorf("control: plain Evals read %d times and wrote %d", store.reads.Load(), store.writes.Load())
	}
}

// An unversioned Source's facts have no name to file a relation under, so the store is never asked.
// control: the same facts behind a version are.
func TestAnUnversionedSourceNeverReachesTheStore(t *testing.T) {
	mem := reversedLine(30)
	v := std(mem)
	store := &counted{}
	answer(t, storedBase(v, mem, store), SemiNaive{}, allReach)
	answer(t, storedBase(v, mem, store), SemiNaive{}, allReach)
	if store.reads.Load() != 0 || store.writes.Load() != 0 {
		t.Errorf("an unversioned Source read the store %d times and wrote it %d", store.reads.Load(), store.writes.Load())
	}
	answer(t, storedBase(v, &versioned{MemSource: mem, v: "1"}, store), SemiNaive{}, allReach)
	if store.writes.Load() == 0 {
		t.Errorf("control: a versioned Source wrote nothing")
	}
}

// A store that fails, or hands back tuples of the wrong arity, costs the work of deriving and never
// changes an answer. A failed write doesn't fail the Eval.
func TestAFailingStoreDerives(t *testing.T) {
	mem := reversedLine(30)
	src := &versioned{MemSource: mem, v: "1"}
	v := std(mem)
	want, wf := fresh(t, v, SemiNaive{}, allReach)
	for _, c := range []struct {
		name  string
		store *counted
	}{
		{"failing read", &counted{failRead: true}},
		{"short tuples", &counted{short: true}},
		{"failing write", &counted{failWrite: true}},
	} {
		answer(t, storedBase(v, src, c.store), SemiNaive{}, allReach)
		got, w := answer(t, storedBase(v, src, c.store), SemiNaive{}, allReach)
		if !sameBinds(got, want) || w != wf {
			t.Errorf("%s: %d rows for work %d, want %d for a fresh Base's %d", c.name, len(got), w, len(want), wf)
		}
		if c.store.reads.Load() == 0 {
			t.Errorf("control: %s was never read", c.name)
		}
	}
}

// A relation the Base held before it had a store is written there by the next Eval reading it, once.
func TestAHeldRelationIsWrittenToAStoreGivenLater(t *testing.T) {
	mem := reversedLine(30)
	src := &versioned{MemSource: mem, v: "1"}
	b := MustBase(std(mem), src)
	answer(t, b, SemiNaive{}, allReach)
	store := &counted{}
	b.KeepDerivedIn(store)
	answer(t, b, SemiNaive{}, allReach)
	if _, ok := store.Relation("reach", "1"); !ok {
		t.Fatalf("the held reach wasn't written to the store")
	}
	writes := store.writes.Load()
	answer(t, b, SemiNaive{}, allReach)
	if store.writes.Load() != writes {
		t.Errorf("a third Eval wrote %d more times, want none", store.writes.Load()-writes)
	}
}

// Materialize derives a relation in full and keeps it, and what it reads, so a fresh Base's query
// reads it from the store. It is the way to put a relation inlining would otherwise never derive, a
// single-rule one, in the store. control: an Eval asking for the same relation leaves it out.
func TestMaterializeFillsTheStore(t *testing.T) {
	mem := reversedLine(30)
	src := &versioned{MemSource: mem, v: "1"}
	v := std(mem)
	const rules = closure + `far(?a, ?b) :- reach(?a, ?b), node(?b); `
	const ask = rules + `far(?a, ?b) => ?a, ?b`

	evaled := &counted{}
	answer(t, storedBase(v, src, evaled), SemiNaive{}, ask)
	if _, ok := evaled.Relation("far", "1"); ok {
		t.Fatalf("control: an Eval stored far, which inlining should have folded into the goal")
	}

	store := &counted{}
	for _, ev := range []SemiNaive{{}, {WrittenOrder: true}} {
		store = &counted{}
		if err := ev.Materialize(bg, mustParse(t, rules+`far(?a, ?b) => ?a`), storedBase(v, src, store), []string{"far"}); err != nil {
			t.Fatalf("Materialize (written order %v): %v", ev.WrittenOrder, err)
		}
		want, _ := fresh(t, v, SemiNaive{}, ask)
		for _, rel := range []string{"far", "reach"} {
			if _, ok := store.Relation(rel, "1"); !ok {
				t.Errorf("written order %v: Materialize didn't store %s", ev.WrittenOrder, rel)
			}
		}
		if far, _ := store.Relation("far", "1"); len(far) != len(want) {
			t.Errorf("written order %v: stored %d far tuples, want %d", ev.WrittenOrder, len(far), len(want))
		}
	}
	got, w := answer(t, storedBase(v, src, store), SemiNaive{}, closure+`reach(?a, ?b) => ?a, ?b`)
	want, wf := fresh(t, v, SemiNaive{}, allReach)
	if !sameBinds(got, want) || w >= wf/2 {
		t.Errorf("after Materialize: %d rows for work %d, want %d for under half a fresh Base's %d", len(got), w, len(want), wf)
	}
}

func TestMaterializeKeepsAModuleMember(t *testing.T) {
	mem := reversedLine(10)
	v := std(mem)
	if err := v.AddModule("hop", LanguageName, reachModule, ""); err != nil {
		t.Fatal(err)
	}
	src := &versioned{MemSource: mem, v: "1"}
	store := &counted{}
	if err := (SemiNaive{}).Materialize(bg, Query{}, storedBase(v, src, store), []string{"hop.reach"}); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Relation("hop.reach", "1"); !ok {
		t.Errorf("hop.reach wasn't stored")
	}
}

func TestMaterializeRefusesWhatItCannotKeep(t *testing.T) {
	mem := reversedLine(10)
	src := &versioned{MemSource: mem, v: "1"}
	v := std(mem)
	for _, rel := range []string{"edge", "nowhere"} {
		err := (SemiNaive{}).Materialize(bg, mustParse(t, allReach), storedBase(v, src, &counted{}), []string{rel})
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("query: Materialize: %q is not a derived relation", rel)) {
			t.Errorf("Materialize %s: %v", rel, err)
		}
	}
	err := (SemiNaive{}).Materialize(bg, mustParse(t, allReach), storedBase(v, src, &counted{failWrite: true}), []string{"reach"})
	if !errors.Is(err, errStore) || !strings.Contains(err.Error(), "query: Materialize: keeping reach") {
		t.Errorf("Materialize into a failing store: %v", err)
	}
	if err := (SemiNaive{}).Materialize(bg, mustParse(t, allReach), storedBase(v, src, &counted{}), []string{"reach"}); err != nil {
		t.Errorf("control: Materialize into a working store: %v", err)
	}
}

// Bases sharing one store evaluate at once while another goroutine drops their caches and moves no
// version. Run under -race, which CI does.
func TestConcurrentBasesShareOneStore(t *testing.T) {
	mem := reversedLine(40)
	src := &versioned{MemSource: mem, v: "1"}
	v := std(mem)
	queries := []string{allReach, closure + `reach("v3", ?b) => ?b`, closure + `reach(?a, "v30") => ?a`}
	want := make([][]Row, len(queries))
	for i, q := range queries {
		want[i], _ = fresh(t, v, SemiNaive{}, q)
	}
	store := &counted{}
	bases := []*Base{storedBase(v, src, store), storedBase(v, src, store), storedBase(v, src, store)}
	var wg sync.WaitGroup
	errs := make(chan string, 64)
	for g := 0; g < 6; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 12; i++ {
				b, k := bases[(g+i)%len(bases)], (g+2*i)%len(queries)
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
