package datalog

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/jaala/ns"
)

// A wide is one relation edge(a, k, b) of n facts with no self-loops, made as they're read, so a
// read that stops early never makes the rest. It counts the facts it made.
type wide struct {
	n    int
	made int
}

func (w *wide) Schema(rel string) (ns.Schema, bool) {
	return ns.Schema{Arity: 3, Labels: []string{"a", "k", "b"}}, rel == "edge"
}

func (w *wide) Relations() []string { return []string{"edge"} }

func (w *wide) fact(i int) ns.Tuple {
	w.made++
	return ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("n%d", i)), ns.S("calls"), ns.S(fmt.Sprintf("n%d", i+1))}}
}

// Tuples makes every fact, as a Source returning one slice does.
func (w *wide) Tuples(string) []ns.Tuple {
	out := make([]ns.Tuple, w.n)
	for i := range out {
		out[i] = w.fact(i)
	}
	return out
}

// A streamingWide is a wide that streams.
type streamingWide struct{ *wide }

func (w streamingWide) TuplesSeq(_ context.Context, _ string) iter.Seq2[ns.Tuple, error] {
	return func(yield func(ns.Tuple, error) bool) {
		for i := range w.n {
			if !yield(w.fact(i), nil) {
				return
			}
		}
	}
}

// selfLoops is #233's question: an unbound call, so the relation is read whole.
const selfLoops = `edge(?a, ?k, ?a) => ?a, ?k`

// A relation a Source streams is charged to the budget as it's read, so a query over one larger than
// its budget stops having made and held at most that many facts, in every evaluator (#233). A
// Source returning one slice makes them all first.
func TestAWholeReadStopsAtTheBudget(t *testing.T) {
	const n, budget = 200_000, 1_000
	q := mustParse(t, selfLoops)
	for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
		w := &wide{n: n}
		var err error
		peak := peakHeapDuring(func() {
			_, err = ev.Eval(bg, q, MustBase(ns.MustVocabulary(streamingWide{w}), streamingWide{w}), Budget(budget))
		})
		var over *BudgetExceeded
		if !errors.As(err, &over) {
			t.Fatalf("%T: %v, want the budget exceeded", ev, err)
		}
		if w.made > budget+1 {
			t.Errorf("%T made %d facts under a budget of %d, want the read stopped at the budget", ev, w.made, budget)
		}

		slice := &wide{n: n}
		var sliceErr error
		slicePeak := peakHeapDuring(func() { _, sliceErr = ev.Eval(bg, q, MustBase(ns.MustVocabulary(slice), slice), Budget(budget)) })
		if !errors.As(sliceErr, &over) || slice.made != n {
			t.Errorf("control: %T over a slice made %d facts and answered %v, want all %d and the budget exceeded", ev, slice.made, sliceErr, n)
		}
		if peak*10 > slicePeak {
			t.Errorf("%T held %d bytes streaming and %d reading a slice, want a tenth or less", ev, peak, slicePeak)
		}
	}
}

// A read the budget stopped isn't kept: the next query on the Base reads the relation again, whole,
// and answers. A streamed read isn't work either, so Work and Explain see what reading a slice does.
func TestAReadStoppedAtTheBudgetIsNotKept(t *testing.T) {
	const n = 5_000
	q := mustParse(t, selfLoops)
	w := &wide{n: n}
	b := MustBase(ns.MustVocabulary(streamingWide{w}), streamingWide{w})
	if _, err := (SemiNaive{}).Eval(bg, q, b, Budget(100)); err == nil {
		t.Fatal("control: a budget of 100 answered a read of 5000 facts")
	}
	w.made = 0
	before := b.Work() // the stopped query streamed, so it did some work before the budget stopped it
	var r Report
	rows, err := SemiNaive{}.Eval(bg, q, b, Explain(&r))
	if err != nil || len(rows) != 0 || w.made != n {
		t.Fatalf("after a stopped read: %d rows, %v, made %d facts; want none, no error, all %d read again", len(rows), err, w.made, n)
	}

	var sliced Report
	slice := &wide{n: n}
	if _, err := (SemiNaive{}).Eval(bg, q, MustBase(ns.MustVocabulary(slice), slice), Explain(&sliced)); err != nil {
		t.Fatal(err)
	}
	if r.Work != sliced.Work || b.Work()-before != sliced.Work {
		t.Errorf("a streamed read reported work %d (Base %d), a slice %d: reading isn't work", r.Work, b.Work()-before, sliced.Work)
	}
}

// A goal that alone reads a relation its Source streams solves it as it arrives (#264), so a limit
// without an order by stops the read, and the Source sees its iterator stopped.
func TestALimitStopsAStreamedRead(t *testing.T) {
	const n = 100_000
	limited := mustParse(t, `edge(?a, ?k, ?b) => ?a limit 5`)
	w := &wide{n: n}
	rows, err := SemiNaive{}.Eval(bg, limited, MustBase(ns.MustVocabulary(streamingWide{w}), streamingWide{w}))
	if err != nil || len(rows) != 5 {
		t.Fatalf("limit 5 answered %d rows, %v", len(rows), err)
	}
	if w.made != 5 {
		t.Errorf("limit 5 made %d of %d facts, want the read stopped at the fifth", w.made, n)
	}

	w = &wide{n: n}
	if _, err := (Naive{}).Eval(bg, limited, MustBase(ns.MustVocabulary(streamingWide{w}), streamingWide{w})); err != nil || w.made != n {
		t.Errorf("control: Naive made %d facts (%v), want all %d: it reads a relation whole", w.made, err, n)
	}
	w = &wide{n: n}
	all := mustParse(t, `edge(?a, ?k, ?b) => ?a`)
	if rows, err := (SemiNaive{}).Eval(bg, all, MustBase(ns.MustVocabulary(streamingWide{w}), streamingWide{w})); err != nil || len(rows) != n || w.made != n {
		t.Errorf("control: with no limit, %d rows and %d facts made (%v), want all %d", len(rows), w.made, err, n)
	}
}

// What a goal streams isn't kept, so the next query on the Base reads it again, and the goal holds
// its answer, one group for a count. A Source returning one slice is kept as before.
func TestAStreamedReadIsNotKept(t *testing.T) {
	const n = 5_000
	q := mustParse(t, `edge(?a, ?k, ?b) => count(?a)`)
	w := &wide{n: n}
	b := MustBase(ns.MustVocabulary(streamingWide{w}), streamingWide{w})
	var r Report
	rows, err := SemiNaive{}.Eval(bg, q, b, Explain(&r))
	if err != nil || len(rows) != 1 || rows[0].Bind["count(a)"].S != "5000" {
		t.Fatalf("count answered %v, %v", rows, err)
	}
	if r.Held != 1 || b.edb.holds("edge") {
		t.Errorf("a streamed count held %d groups and kept edge %v, want one group and nothing kept", r.Held, b.edb.holds("edge"))
	}
	if _, err := (SemiNaive{}).Eval(bg, q, b); err != nil || w.made != 2*n {
		t.Errorf("the second count made %d facts in all (%v), want edge read again, %d", w.made, err, 2*n)
	}

	slice := &wide{n: n}
	sb := MustBase(ns.MustVocabulary(slice), slice)
	for range 2 {
		if _, err := (SemiNaive{}).Eval(bg, q, sb); err != nil {
			t.Fatal(err)
		}
	}
	if slice.made != n || !sb.edb.holds("edge") {
		t.Errorf("control: a slice made %d facts over two counts and kept edge %v, want %d, read once and kept", slice.made, sb.edb.holds("edge"), n)
	}
}

// A streamed goal holds its answer and not the relation (#120's discipline, carried down to the
// read): a count over a streamed relation peaks far below the same count over one read whole.
func TestAStreamedGoalHoldsItsAnswerNotTheRelation(t *testing.T) {
	const n = 300_000
	q := mustParse(t, `edge(?a, ?k, ?b) => count(?a)`)
	// Collect often, so the peak is what the Eval holds rather than the per-tuple garbage a stream
	// leaves behind it, which -race leaves longer.
	defer debug.SetGCPercent(debug.SetGCPercent(5))
	peak := func(src ns.Source) uint64 {
		b := MustBase(ns.MustVocabulary(src), src)
		return peakHeapDuring(func() {
			if _, err := (SemiNaive{}).Eval(bg, q, b); err != nil {
				t.Fatal(err)
			}
		})
	}
	w := &wide{n: n}
	streamed := peak(streamingWide{w})
	whole := peak(&wide{n: n})
	t.Logf("peak heap: streamed %d KB, read whole %d KB", streamed>>10, whole>>10)
	if streamed*4 > whole {
		t.Errorf("a streamed count peaked at %d KB, read whole %d KB: want under a quarter", streamed>>10, whole>>10)
	}
}

// streaming is a Source that streams every relation, counting the tuples it handed over by relation.
type streaming struct {
	ns.Source
	mu     sync.Mutex
	handed map[string]int
}

func streamingOver(src ns.Source) *streaming {
	return &streaming{Source: src, handed: map[string]int{}}
}

func (s *streaming) TuplesSeq(_ context.Context, rel string) iter.Seq2[ns.Tuple, error] {
	return func(yield func(ns.Tuple, error) bool) {
		for _, t := range s.Source.Tuples(rel) {
			s.mu.Lock()
			s.handed[rel]++
			s.mu.Unlock()
			if !yield(t, nil) {
				return
			}
		}
	}
}

func (s *streaming) count(rel string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handed[rel]
}

// chain is edge(n0, n1) to edge(n39, n40), label(n3, start), and node(n0) to node(n40): enough
// edges to index (indexMinFacts).
func chain() *ns.MemSource {
	src := ns.NewMemSource().Declare("edge", "from", "to").Declare("node", "name").Declare("label", "node", "text")
	for i := range 40 {
		src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("n%d", i)), ns.S(fmt.Sprintf("n%d", i+1))}})
	}
	for i := range 41 {
		src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("n%d", i))}})
	}
	src.Add("label", ns.Tuple{Vals: []ns.Value{ns.S("n3"), ns.S("start")}})
	return src
}

// Only the goal's first literal streams, and only when nothing else in the Eval reads its relation:
// a relation a rule or a second literal reads, or one the planner puts second, is read whole and
// kept, and one an earlier query kept is read from the cache.
func TestOnlyTheGoalsOwnReadStreams(t *testing.T) {
	const edges = 40
	cases := []struct {
		name, query string
		handed      int
		kept        bool
		opts        []Option
	}{
		{"alone", `edge(?a, ?b) => ?a limit 2`, 2, false, nil},
		{"a rule reads it", `r(?a) :- edge(?a, ?b); r(?a) :- label(?a, _); edge(?a, ?b), r(?a) => ?a limit 2`, edges, true, nil},
		{"read twice", `edge(?a, ?b), edge(?b, ?c) => ?a limit 2`, edges, true, nil},
		{"planned second", `label(?a, "start"), edge(?a, ?b) => ?b limit 2`, edges, true, nil},
		{"negated too", `edge(?a, ?b), not edge(?b, "n9") => ?a limit 2`, edges, true, nil},
		{"demand stores it", `r(?b) :- label(?b, _); r(?b) :- node(?b), label(?b, "start"); edge(?a, ?b), r(?b) => ?a limit 2`, edges, true, nil},
		// With no supplementary relation, a demand rule reads edge in the fixpoint and the goal reads it too.
		{"demand reads it", `r(?b) :- label(?b, _); r(?b) :- node(?b), label(?b, "start"); edge(?a, ?b), r(?b) => ?a limit 2`, edges, true, []Option{CanonicalCites()}},
	}
	for _, c := range cases {
		src := streamingOver(chain())
		b := MustBase(std(src), src)
		rows, err := SemiNaive{}.Eval(bg, mustParse(t, c.query), b, c.opts...)
		if err != nil || len(rows) == 0 {
			t.Errorf("%s: %v, %v", c.name, rows, err)
			continue
		}
		if got := src.count("edge"); got != c.handed || b.edb.holds("edge") != c.kept {
			t.Errorf("%s: edge handed %d tuples, kept %v; want %d, kept %v", c.name, got, b.edb.holds("edge"), c.handed, c.kept)
		}
		if !c.kept {
			continue
		}
		if _, err := (SemiNaive{}).Eval(bg, mustParse(t, `edge(?a, ?b) => ?a limit 2`), b); err != nil || src.count("edge") != c.handed {
			t.Errorf("%s: a goal over the kept edge had it handed %d tuples in all (%v), want the cache read, %d", c.name, src.count("edge"), err, c.handed)
		}
	}
}

// A streamed literal is a candidate wherever the cached read's index would list it, so a bound call
// does the work it does over a held relation, above and below the size it indexes at.
func TestAStreamedReadWorksAsTheCachedOne(t *testing.T) {
	for _, q := range []string{
		`edge("n7", ?b) => ?b`,
		`edge(?a, ?b) => count(?a)`,
		`edge(?a, "n9"), node(?a) => ?a`,
	} {
		for _, edges := range []int{40, 5} {
			mem := chain()
			if edges < 40 {
				mem = ns.NewMemSource().Declare("edge", "from", "to").Declare("node", "name").Declare("label", "node", "text")
				for i := range edges {
					mem.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("n%d", i+5)), ns.S(fmt.Sprintf("n%d", i+6))}})
					mem.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("n%d", i+5))}})
				}
			}
			src := streamingOver(mem)
			var streamed, held Report
			sb := MustBase(std(src), src)
			got, err := SemiNaive{}.Eval(bg, mustParse(t, q), sb, Explain(&streamed))
			if err != nil {
				t.Fatal(err)
			}
			hb := MustBase(std(mem), mem)
			want, err := SemiNaive{}.Eval(bg, mustParse(t, q), hb, Explain(&held))
			if err != nil {
				t.Fatal(err)
			}
			if sb.edb.holds("edge") || !hb.edb.holds("edge") {
				t.Errorf("control: %s over %d edges kept edge streamed %v, held %v; want only the held", q, edges, sb.edb.holds("edge"), hb.edb.holds("edge"))
			}
			if !reflect.DeepEqual(got, want) || streamed.Work != held.Work || sb.Work() != hb.Work() {
				t.Errorf("%s over %d edges: streamed answered %v with work %d, held %v with %d", q, edges, got, streamed.Work, want, held.Work)
			}
			if s, h := sourceTuples(streamed, "edge"), sourceTuples(held, "edge"); s != h || s != edges {
				t.Errorf("%s over %d edges: Explain read %d edge tuples streamed, %d held", q, edges, s, h)
			}
		}
	}
}

// lookingUpLabels is a batching store over chain() that looks up label only and streams the rest.
type lookingUpLabels struct {
	*batching
	stream *streaming
}

func (s lookingUpLabels) LooksUp(rel string) bool { return rel == "label" }

func (s lookingUpLabels) TuplesSeq(ctx context.Context, rel string) iter.Seq2[ns.Tuple, error] {
	return s.stream.TuplesSeq(ctx, rel)
}

// A goal whose prefetch runs a prefix through its first literal (a looked-up call it batches after
// it) reads that literal's relation whole once, rather than streaming it twice: prefetch kept it.
func TestAPrefetchedPrefixIsReadWhole(t *testing.T) {
	mem := chain()
	src := lookingUpLabels{batchingAt(mem), streamingOver(mem)}
	b := MustBase(std(src), src)
	var r Report
	rows, err := SemiNaive{}.Eval(bg, mustParse(t, `edge(?a, ?b), label(?b, ?l) => count(?a)`), b, Explain(&r))
	if err != nil || len(rows) != 1 {
		t.Fatalf("%v, %v", rows, err)
	}
	if len(src.batches) == 0 || !strings.HasPrefix(r.Goal.Literals[0].Literal, "edge") {
		t.Fatalf("control: batches %v, goal solved as %v; want label batched after edge", src.batches, r.Goal.Literals)
	}
	if got := src.stream.count("edge"); got != 40 || !b.edb.holds("edge") {
		t.Errorf("edge handed %d tuples, kept %v; want 40, read whole once and kept", got, b.edb.holds("edge"))
	}
}

// A streamed tuple is read as edbCache reads one: normalized to its argument's type, so a number
// stored in a text column matches the text, and cited by its own witness node.
func TestAStreamedTupleIsReadAsAHeldOne(t *testing.T) {
	src := streamingOver(typedNets())
	b := MustBase(std(src), src)
	rows, err := SemiNaive{}.Eval(bg, mustParse(t, `component.pin(?r, "3") => ?r`), b)
	if err != nil || col(rows, "r") != "U1" || b.edb.holds("component.pin") {
		t.Errorf("a streamed pin stored as a number answered %v (%v), kept %v; want U1, streamed", rows, err, b.edb.holds("component.pin"))
	}

	q := mustParse(t, `edge(?a, "n9") => ?a`)
	ssrc := streamingOver(chain())
	sb := MustBase(std(ssrc), ssrc)
	got, err := SemiNaive{}.Eval(bg, q, sb, Witnesses())
	if err != nil {
		t.Fatal(err)
	}
	mem := chain()
	want, err := SemiNaive{}.Eval(bg, q, MustBase(std(mem), mem), Witnesses())
	if err != nil {
		t.Fatal(err)
	}
	if sb.edb.holds("edge") || len(got) != 1 || got[0].Witness == nil || !reflect.DeepEqual(got, want) {
		t.Errorf("streamed (kept %v) answered %+v, held %+v; want the same rows and witnesses, streamed", sb.edb.holds("edge"), got, want)
	}
}

// The planner can't size a relation the goal may stream without reading it, so a tie it would break
// by size keeps the written order (as for a looked-up relation); relations the Base already holds,
// or that a rule reads anyway, it sizes as before.
func TestAStreamableRelationIsSizedOnlyWhenRead(t *testing.T) {
	mem := chain()
	for i := range 30 {
		mem.Add("label", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("n%d", i)), ns.S("x")}})
	}
	first := func(r *BodyReport) string {
		if r == nil || len(r.Literals) == 0 {
			return ""
		}
		return r.Literals[0].Literal
	}
	goal := mustParse(t, `label(?a, "x"), edge(?a, "n9") => ?a`)
	src := streamingOver(mem)
	b := MustBase(std(src), src)
	var fresh Report
	if _, err := (SemiNaive{}).Eval(bg, goal, b, Explain(&fresh)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first(fresh.Goal), "label") {
		t.Errorf("control: on a fresh Base the goal ran %q first, want label: edge can't be sized unread", first(fresh.Goal))
	}
	if _, err := (SemiNaive{}).Eval(bg, mustParse(t, `r(?a) :- edge(?a, ?b); r(?a) :- label(?a, _); r(?a) => ?a`), b); err != nil || !b.edb.holds("edge") || !b.edb.holds("label") {
		t.Fatalf("control: rules over edge and label didn't keep them (%v)", err)
	}
	var held Report
	if _, err := (SemiNaive{}).Eval(bg, goal, b, Explain(&held)); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(first(held.Goal), "edge") {
		t.Errorf("with edge and label held the goal ran %q first, want edge, its one tuple against label's 30", first(held.Goal))
	}

	src = streamingOver(mem)
	var ruled Report
	q := mustParse(t, `s(?a) :- label(?a, "x"), edge(?a, "n9"); s(?a) :- node(?a), ?a = "n0"; edge(?x, "n1"), s(?a) => ?a`)
	if _, err := (SemiNaive{}).Eval(bg, q, MustBase(std(src), src), Explain(&ruled)); err != nil {
		t.Fatal(err)
	}
	ran := false
	for _, r := range ruled.Rules {
		if strings.Contains(r.Ran, "label") {
			ran = true
			if !strings.HasPrefix(first(r), "edge") {
				t.Errorf("a rule reading edge, which the goal reads too, ran %q first, want edge sized: %s", first(r), r.Ran)
			}
		}
	}
	if !ran {
		t.Errorf("control: no rule body read label: %+v", ruled.Rules)
	}
}

// sourceTuples is how many of rel's tuples r says the Eval read.
func sourceTuples(r Report, rel string) int {
	for _, s := range r.Sources {
		if s.Relation == rel {
			return s.Tuples
		}
	}
	return -1
}

// lookingStreams is a looking store that streams what it reads whole.
type lookingStreams struct {
	*looking
	stream *streaming
}

func (s lookingStreams) TuplesSeq(ctx context.Context, rel string) iter.Seq2[ns.Tuple, error] {
	return s.stream.TuplesSeq(ctx, rel)
}

// A relation the Source looks up streams only for a call binding nothing under a limit without an
// order by, where the read stops early: an unlimited read is kept, so asking again asks the store
// nothing (#212), and a bound call is still looked up. A Federation of such stores streams the same.
func TestALookedUpRelationStreamsOnlyUnderALimit(t *testing.T) {
	cases := []struct {
		name, query    string
		handed         int
		lookedUp, kept bool
	}{
		{"limited", `edge(?a, ?b) => ?a limit 2`, 2, false, false},
		{"unlimited", `edge(?a, ?b) => count(?a)`, 40, false, true},
		{"ordered", `edge(?a, ?b) => ?a order by ?a limit 2`, 40, false, true},
		{"bound", `edge("n7", ?b) => ?b limit 2`, 0, true, false},
	}
	for _, c := range cases {
		for _, federated := range []bool{false, true} {
			mem := chain()
			stream := streamingOver(mem)
			var lookups func() int
			var src ns.Source
			if federated {
				members := splitInto(mem, 2, func(int) bool { return true })
				stores := make([]*looking, len(members))
				for i := range members {
					stores[i] = members[i].Source.(*looking)
				}
				src = federate(t, members...)
				lookups = func() int { return stores[0].lookups["edge"] + stores[1].lookups["edge"] }
			} else {
				l := lookingAt(mem)
				src = lookingStreams{l, stream}
				lookups = func() int { return l.lookups["edge"] }
			}
			b := MustBase(std(src), src)
			var r Report
			if _, err := (SemiNaive{}).Eval(bg, mustParse(t, c.query), b, Explain(&r)); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			handed := stream.count("edge")
			if federated {
				handed = sourceTuples(r, "edge") // a Federation streams its members' slices, uncounted
				if c.lookedUp {
					handed = 0
				}
			}
			if handed != c.handed || (lookups() > 0) != c.lookedUp || b.edb.holds("edge") != c.kept {
				t.Errorf("%s (federated %v): edge handed %d tuples, looked up %d times, kept %v; want %d, looked up %v, kept %v",
					c.name, federated, handed, lookups(), b.edb.holds("edge"), c.handed, c.lookedUp, c.kept)
			}
		}
	}
}
