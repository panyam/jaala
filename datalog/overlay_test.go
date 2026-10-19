package datalog

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/jaala/ns"
)

// overlaidSeed varies which relations agreeOverlaid's session touches, and its store.
var overlaidSeed int

// overlaidPushed, overlaidBatched and overlaidLookedUp count the programs whose evaluation over a
// session pushed a conjunction to the store beneath it, asked it for a batch, and looked up a relation
// the session touched (#288), for the corpus's control.
var overlaidPushed, overlaidBatched, overlaidLookedUp int

// agreeOverlaid runs q over a session that undoes edits made to b's facts (#288): a store holding them
// with some facts gone and junk added, under a change set adding the facts back, removing the junk and
// adding facts the store holds, so every evaluator over the overlay must answer as Naive did over b.
// Two relations in three are left untouched, which the store answers itself. It runs over a store that
// pushes and over one that batches.
func agreeOverlaid(q Query, b *Base, want []Row, werr error, opts []Option) string {
	overlaidSeed++
	store, edits, touched := undoneSession(b.src, overlaidSeed)
	p, bt := pushingAt(store), batchingAt(store)
	for _, src := range []ns.Source{p, bt} {
		if diff := agreeOver(q, b, MustBase(b.reg, ns.Overlay(src, edits)), "overlaid", want, werr, opts); diff != "" {
			return diff
		}
	}
	if len(p.pushed) > 0 {
		overlaidPushed++
	}
	if len(bt.batches) > 0 {
		overlaidBatched++
	}
	if slices.ContainsFunc(touched, func(rel string) bool { return p.looked(rel)+bt.looked(rel) > 0 }) {
		overlaidLookedUp++
	}
	return ""
}

// undoneSession is src's facts with some of each touched relation's facts removed and a junk fact
// added, and the change set that undoes it, adding to that a fact the store still holds. Only a fact no
// other fact of its relation could equal is removed or re-added, since a session names a fact by its
// values and replaces every copy, and a fact served twice is two facts.
func undoneSession(src ns.Source, seed int) (*ns.MemSource, ns.Changes, []string) {
	store := ns.NewMemSource()
	var edits ns.Changes
	var touched []string
	for i, rel := range src.Relations() {
		s, _ := src.Schema(rel)
		store.DeclareSchema(rel, s)
		ts := src.Tuples(rel)
		if (i+seed/2)%3 != 0 || len(ts) == 0 {
			for _, t := range ts {
				store.Add(rel, t)
			}
			continue
		}
		touched = append(touched, rel)
		seen := map[string]int{}
		for _, t := range ts {
			for _, k := range looseKeys(t.Vals) {
				seen[k]++
			}
		}
		for k, t := range ts {
			alone := !slices.ContainsFunc(looseKeys(t.Vals), func(k string) bool { return seen[k] > 1 })
			switch {
			case alone && k%3 == seed%3:
				edits.Add(rel, t)
				continue
			case alone && k%4 == 0:
				edits.Add(rel, t)
			}
			store.Add(rel, t)
		}
		junk := make([]ns.Value, s.Arity)
		for j := range junk {
			junk[j] = ns.S("zz junk")
		}
		store.Add(rel, ns.Tuple{Vals: junk, Cites: []string{"junk"}})
		edits.Remove(rel, ns.Tuple{Vals: junk})
	}
	return store, edits, touched
}

// looseKeys are two keys of a fact, its values as text and as numbers where they read as one, so two
// facts whose keys all differ are two facts however the engine reads their arguments' types.
func looseKeys(vals []ns.Value) []string {
	var text, num strings.Builder
	for _, v := range vals {
		text.WriteString(strconv.Quote(v.S))
		switch {
		case v.Num != nil:
			num.WriteString("n" + strconv.FormatFloat(*v.Num+0, 'g', -1, 64))
		default:
			if f, err := strconv.ParseFloat(v.S, 64); err == nil {
				num.WriteString("n" + strconv.FormatFloat(f+0, 'g', -1, 64))
			} else {
				num.WriteString(strconv.Quote(v.S))
			}
		}
		text.WriteByte(0)
		num.WriteByte(0)
	}
	return []string{"t" + text.String(), "n" + num.String()}
}

func TestASessionAndItsBaseAnswerApartConcurrently(t *testing.T) {
	src := graph()
	var edits ns.Changes
	edits.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("d"), ns.S("z")}})
	v := std(src)
	plain, session := MustBase(v, src), MustBase(v, ns.Overlay(src, edits))
	q := mustParse(t, `reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c); reach("a", ?b) => ?b`)
	var wg sync.WaitGroup
	got := make([]int, 8)
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b := plain
			if i%2 == 1 {
				b = session
			}
			rows, err := (SemiNaive{}).Eval(bg, q, b)
			if err != nil {
				t.Error(err)
			}
			got[i] = len(rows)
		}()
	}
	wg.Wait()
	for i, n := range got {
		if want := 3 + i%2; n != want {
			t.Errorf("Eval %d answered %d rows, want %d", i, n, want)
		}
	}
	// The session's edit didn't reach the base.
	if rows, _ := (SemiNaive{}).Eval(bg, q, plain); len(rows) != 3 {
		t.Errorf("the base answers %v", rows)
	}
}

func TestACommittedSessionIsTheVersionItsBaseDiffsTo(t *testing.T) {
	src := versionedGraph()
	b := baseFor(std(src))
	q := mustParse(t, `edge(?a, ?b) => ?a, ?b`)
	before, err := Naive{}.Eval(bg, q, b, At(2))
	if err != nil {
		t.Fatal(err)
	}
	var edits ns.Changes
	edits.Remove("edge", ns.Tuple{Vals: []ns.Value{ns.S("a"), ns.S("b")}}).
		Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("d"), ns.S("e")}, Cites: []string{"edit:de"}}).
		Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("b"), ns.S("c")}, Cites: []string{"edit:again"}}). // held already
		Remove("edge", ns.Tuple{Vals: []ns.Value{ns.S("x"), ns.S("y")}})                              // never held
	session := ns.Overlay(src, edits) // a TimedMemSource serves its last version, 2
	sessionRows, err := Naive{}.Eval(bg, q, MustBase(std(src), session))
	if err != nil {
		t.Fatal(err)
	}
	effective, err := session.Effective(bg)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Commit(edits, 2, 3); err != nil {
		t.Fatal(err)
	}
	after, err := Naive{}.Eval(bg, q, b, At(3))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(binds(after), binds(sessionRows)) {
		t.Errorf("at 3: %v, the session answered %v", binds(after), binds(sessionRows))
	}
	if again, _ := (Naive{}).Eval(bg, q, b, At(2)); !reflect.DeepEqual(binds(again), binds(before)) {
		t.Errorf("at 2 after the commit: %v, before it %v", binds(again), binds(before))
	}
	d, err := b.Diff(bg, "edge", 2, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := tupleTexts(d.Added), tupleTexts(effective.Added("edge")); !slices.Equal(got, want) || len(got) != 1 {
		t.Errorf("added %v, the session's effective %v", got, want)
	}
	want := []string{"a b [edge:ab]"}
	if got := tupleTexts(d.Removed); !slices.Equal(got, want) || len(effective.Removed("edge")) != 1 {
		t.Errorf("removed %v, want %v; the session's effective %v", got, want, tupleTexts(effective.Removed("edge")))
	}
}
