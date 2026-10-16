package datalog

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/jaala/ns"
)

// counting is a Source that counts the relations read whole.
type counting struct {
	ns.Source
	mu     sync.Mutex
	wholes map[string]int
}

func (c *counting) Tuples(rel string) []ns.Tuple {
	c.mu.Lock()
	c.wholes[rel]++
	c.mu.Unlock()
	return c.Source.Tuples(rel)
}

func (c *counting) whole(rel string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wholes[rel]
}

// looking is a counting Source that also answers Lookup, by scanning with the engine's equality over
// each value read as its argument's declared type, as the LookupSource contract asks. sloppy
// returns the whole relation from every lookup, which the contract allows; fail makes every lookup
// fail with it. It compares values folded by stdlib's units, as an engine folding a Source's units
// asks (#205); asStored compares them as it holds them, for one that doesn't (#303).
type looking struct {
	*counting
	sloppy   bool
	asStored bool
	fail     error
	lookups  map[string]int
}

func lookingAt(src ns.Source) *looking {
	return &looking{counting: &counting{Source: src, wholes: map[string]int{}}, lookups: map[string]int{}}
}

func (l *looking) Lookup(ctx context.Context, rel string, bound map[int]ns.Value) ([]ns.Tuple, error) {
	l.mu.Lock()
	l.lookups[rel]++
	fail := l.fail
	l.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	return l.answer(rel, bound), nil
}

// answer is what a lookup of rel's bound values returns, uncounted.
func (l *looking) answer(rel string, bound map[int]ns.Value) []ns.Tuple {
	all := l.Source.Tuples(rel)
	if l.sloppy {
		return all
	}
	s, _ := l.Source.Schema(rel)
	units := stdUnits
	if l.asStored {
		units = nil
	}
	read := normalizeTuples(units, all, s.Types)
	var out []ns.Tuple
	for k, t := range all {
		ok := true
		for i, v := range bound {
			ok = ok && valueEq(read[k].Vals[i], v)
		}
		if ok {
			out = append(out, t)
		}
	}
	return out
}

func (l *looking) looked(rel string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lookups[rel]
}

var lookupQueries = []string{
	`edge("a", ?y) => ?y`,
	`edge(?x, ?y), edge(?y, ?z) => ?x, ?z`,
	closure + `reach("a", ?b) => ?b`,
	closure + `reach(?a, ?b), not edge(?a, ?b) => ?a, ?b`,
	closure + `node(?n), reach(?n, "d") => count(?n)`,
	`weight(?n, ?w), ?w > 2, edge(?n, ?m) => ?n, ?m`,
	`two(?a, ?c) :- edge(?a, ?b), edge(?b, ?c); two(?a, ?c) :- edge(?a, ?c), node(?c); node(?n), two(?n, ?c) => ?n, ?c`,
}

// A Source answering lookups gives every query the rows, citations and witnesses reading it whole
// does, in all three evaluators, and so does one whose lookups return more than matches (#126).
// control: the lookups are made.
func TestALookupSourceAnswersAsReadingItWhole(t *testing.T) {
	v := std(graph())
	for _, sloppy := range []bool{false, true} {
		src := lookingAt(graph())
		src.sloppy = sloppy
		b := MustBase(v, src)
		for _, text := range lookupQueries {
			q := mustParse(t, text)
			want, werr := both(q, baseFor(v))
			got, gerr := both(q, b)
			if !reflect.DeepEqual(binds(got), binds(want)) || errText(gerr) != errText(werr) {
				t.Errorf("sloppy %v: %s\n looked up: %v %v\n whole:     %v %v", sloppy, text, binds(got), gerr, binds(want), werr)
			}
			opts := []Option{CanonicalCites(), Witnesses()}
			if strings.Contains(text, "?y") {
				opts = append(opts, Bind(map[Var][]ns.Value{"y": {ns.S("b"), ns.S("c")}}))
			}
			for i, opt := range opts {
				w, werr := Naive{}.Eval(bg, q, baseFor(v), opt)
				g, gerr := Naive{}.Eval(bg, q, b, opt)
				if !reflect.DeepEqual(g, w) || errText(gerr) != errText(werr) {
					t.Errorf("sloppy %v: %s with option %d\n looked up: %v %v\n whole:     %v %v", sloppy, text, i, g, gerr, w, werr)
				}
			}
		}
		if src.looked("edge") == 0 {
			t.Errorf("control: sloppy %v made no lookups", sloppy)
		}
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// A call with an argument bound is looked up, never read whole, in every evaluator, the planned
// SemiNaive's estimates (fanOut, and a delta round's scanSize) included. Under demand that covers rule
// bodies too, which Naive and the written order derive in full, reading what they call whole.
// control: the same queries over a Source without Lookup read edge whole.
func TestABoundCallNeverReadsItsRelationWhole(t *testing.T) {
	v := std(reversedLine(40))
	cases := []struct {
		text string
		evs  []Evaluator
	}{
		{`edge("v3", ?y), edge(?y, ?z) => ?z`, []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}}},
		{closure + `reach("v3", ?b) => count(?b)`, []Evaluator{SemiNaive{}}},
		{`two(?a, ?c) :- edge(?a, ?b), edge(?b, ?c); two(?a, ?c) :- edge(?a, ?c), edge(?c, "v9"); two("v7", ?c) => ?c`, []Evaluator{SemiNaive{}}},
		// Evaluated in full, its recursive rule planned to start from edge("v1", ?y), which a round sizes
		// against its delta.
		{`lab(?y) :- edge("v3", ?y); lab(?z) :- edge("v1", ?y), lab(?x), edge(?x, ?z); lab(?y) => ?y`, []Evaluator{SemiNaive{}}},
	}
	for _, c := range cases {
		for _, ev := range c.evs {
			src := lookingAt(reversedLine(40))
			if _, err := ev.Eval(bg, mustParse(t, c.text), MustBase(v, src)); err != nil {
				t.Fatal(err)
			}
			if src.whole("edge") != 0 || src.looked("edge") == 0 {
				t.Errorf("%T: %s read edge whole %d times, with %d lookups; want none, and lookups", ev, c.text, src.whole("edge"), src.looked("edge"))
			}
			plain := &counting{Source: reversedLine(40), wholes: map[string]int{}}
			if _, err := ev.Eval(bg, mustParse(t, c.text), MustBase(v, plain)); err != nil || plain.whole("edge") == 0 {
				t.Errorf("control: %T: %s over a plain source read edge whole %d times (%v)", ev, c.text, plain.whole("edge"), err)
			}
		}
	}
}

// A call with nothing bound reads the relation whole, and the Base then answers bound calls from its
// own index, in that Eval and later ones, rather than asking the Source. Unindexed never looks up.
func TestAWholeReadAnswersLaterCalls(t *testing.T) {
	v := std(reversedLine(40))
	src := lookingAt(reversedLine(40))
	b := MustBase(v, src)
	if _, err := (Naive{}).Eval(bg, mustParse(t, `edge("v1", ?y) => ?y`), b); err != nil {
		t.Fatal(err)
	}
	if src.looked("edge") != 1 || src.whole("edge") != 0 {
		t.Fatalf("control: a bound call made %d lookups and %d whole reads; want one lookup", src.looked("edge"), src.whole("edge"))
	}
	if _, err := (Naive{}).Eval(bg, mustParse(t, `edge(?x, ?y), edge(?y, ?z) => ?x, ?z`), b); err != nil {
		t.Fatal(err)
	}
	if _, err := (Naive{}).Eval(bg, mustParse(t, `edge("v2", ?y) => ?y`), b); err != nil {
		t.Fatal(err)
	}
	if src.looked("edge") != 1 || src.whole("edge") != 1 {
		t.Errorf("after a whole read: %d lookups, %d whole reads; want the one lookup from before, and one read", src.looked("edge"), src.whole("edge"))
	}
	un := lookingAt(reversedLine(40))
	if _, err := (SemiNaive{}).Eval(bg, mustParse(t, `edge("v1", ?y) => ?y`), MustBase(v, un).Unindexed()); err != nil {
		t.Fatal(err)
	}
	if un.looked("edge") != 0 || un.whole("edge") != 1 {
		t.Errorf("Unindexed: %d lookups, %d whole reads; want it to read the relation, as the oracle", un.looked("edge"), un.whole("edge"))
	}
}

// What a lookup returns is kept for its Eval: Naive, in written order, calls edge("a", ?y) once per
// node, and asks the Source once. The next Eval asks again, so the Base never holds the relation.
// Each lookup counts one unit of work, and a call answered again counts none.
func TestALookupIsKeptForItsEvalOnly(t *testing.T) {
	v := std(graph())
	src := lookingAt(graph())
	b := MustBase(v, src)
	q := `node(?n), edge("a", ?y) => ?n, ?y`
	r := explained(t, Naive{}, q, b)
	if src.looked("edge") != 1 {
		t.Errorf("one Eval made %d lookups of edge(\"a\", ?y); want 1", src.looked("edge"))
	}
	edge := sourceIn(r, "edge")
	if edge == nil || edge.Lookups != 1 || edge.Hits != 4 || edge.Fetched != 1 || edge.Tuples != 0 {
		t.Errorf("edge's report: %+v; want 1 lookup fetching 1 tuple, answered again 4 times", edge)
	}
	if r.Lookups != 1 || r.Fetched != 1+5 {
		t.Errorf("report totals: %d lookups, %d fetched; want 1, and edge's tuple with node's 5", r.Lookups, r.Fetched)
	}
	// node is scanned (5) and each of the 5 calls reads edge's one tuple, plus the one lookup.
	if r.Work != 5+5+1 {
		t.Errorf("work %d; want 11: node's 5, edge's tuple 5 times, one lookup", r.Work)
	}
	if lr := r.Goal.Literals[1]; fmt_(lr.Access) != "lookup (from)" {
		t.Errorf("edge's access: %q", lr.Access)
	}
	if !strings.Contains(r.String(), "edge: looked up, 1 lookups fetching 1 tuples, 4 answered from earlier lookups") {
		t.Errorf("text:\n%s", r)
	}
	explained(t, Naive{}, q, b)
	if src.looked("edge") != 2 || b.edb.holds("edge") {
		t.Errorf("a second Eval: %d lookups in all, edge held %v; want 2, not held", src.looked("edge"), b.edb.holds("edge"))
	}
}

func sourceIn(r *Report, rel string) *SourceReport {
	for _, s := range r.Sources {
		if s.Relation == rel {
			return s
		}
	}
	return nil
}

// A failed lookup stops the query that asked with a query: error naming the relation, and isn't kept:
// the next Eval looks up again and answers. A cancelled one says the evaluation stopped.
func TestAFailedLookupIsReportedAndNotKept(t *testing.T) {
	v := std(graph())
	src := lookingAt(graph())
	b := MustBase(v, src)
	q := mustParse(t, `edge("a", ?y) => ?y`)
	src.fail = errors.New("disk on fire")
	_, err := (SemiNaive{}).Eval(bg, q, b)
	if err == nil || err.Error() != `query: looking up edge: disk on fire` {
		t.Errorf("failed lookup: %v", err)
	}
	src.fail = nil
	if rows, err := (SemiNaive{}).Eval(bg, q, b); err != nil || col(rows, "y") != "b" {
		t.Errorf("after the failure: %v, %v; want b", col(rows, "y"), err)
	}
	ctx, cancel := context.WithCancel(bg)
	cancel()
	src.fail = ctx.Err()
	nb := *MustBase(v, src)
	nb.run = &evalRun{ctx: ctx}
	_, _, _, err = nb.lookup(q.Goal.Literals[0].Pos, newBinding())
	if err == nil || !strings.HasPrefix(err.Error(), "query: evaluation stopped looking up edge") || !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled lookup: %v", err)
	}
}

// Evals sharing a Base over a lookup Source run concurrently; -race checks the per-Eval cache.
func TestConcurrentEvalsLookUpOnTheirOwn(t *testing.T) {
	v := std(reversedLine(40))
	src := lookingAt(reversedLine(40))
	b := MustBase(v, src)
	want, _ := fresh(t, v, SemiNaive{}, closure+`reach("v3", ?b) => ?b`)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rows, err := (SemiNaive{}).Eval(bg, mustParse(t, closure+`reach("v3", ?b) => ?b`), b)
			if err != nil || !sameBinds(rows, want) {
				t.Errorf("concurrent Eval: %d rows, %v", len(rows), err)
			}
		}()
	}
	wg.Wait()
}

// A query that reused a relation an earlier query kept (#140) reports its cold cost: what a Base
// keeping nothing spends on it, as a fresh Base's Work shows. The run that finds it doesn't count
// toward the Base's Work. control: the reusing query's own Work is lower; and a query that reused
// nothing reports its own Work as cold.
func TestColdCostIsWhatAFreshBaseSpends(t *testing.T) {
	v := std(reversedLine(60))
	b := baseFor(v)
	first := explained(t, SemiNaive{}, allReach, b)
	if first.Cold == nil || first.Cold.Work != first.Work || first.Cold.Fetched != first.Fetched {
		t.Errorf("first query: cold %+v, work %d, fetched %d; want its own numbers", first.Cold, first.Work, first.Fetched)
	}
	bound := closure + `reach("v7", ?b) => ?b`
	before := b.Work()
	second := explained(t, SemiNaive{}, bound, b)
	_, cold := fresh(t, v, SemiNaive{}, bound)
	if rr := relationIn(second, "reach"); rr == nil || rr.How != "reused" {
		t.Fatalf("control: reach in the second query: %+v; want reused", rr)
	}
	if second.Cold == nil || second.Cold.Work != cold || second.Work >= cold {
		t.Errorf("second query: work %d, cold %+v; want cold work %d, above its own", second.Work, second.Cold, cold)
	}
	if got := b.Work() - before; got != second.Work {
		t.Errorf("the Base counted %d for the second query; want only its own %d", got, second.Work)
	}
	if !strings.Contains(second.String(), "cold: work ") {
		t.Errorf("text:\n%s", second)
	}
}

// A bound call answered from a relation the Base read whole, where a fresh Base would have looked it
// up, reports the lookup in its cold cost.
func TestColdCostCountsTheLookupsAHeldRelationSaved(t *testing.T) {
	v := std(reversedLine(40))
	b := MustBase(v, lookingAt(reversedLine(40)))
	explained(t, SemiNaive{}, `edge(?x, ?y) => ?x`, b)
	r := explained(t, SemiNaive{}, `edge("v1", ?y) => ?y`, b)
	if r.Lookups != 0 || r.Cold == nil || r.Cold.Lookups != 1 || r.Cold.Fetched != 1 {
		t.Errorf("lookups %d, cold %+v; want none, and cold one lookup fetching one tuple", r.Lookups, r.Cold)
	}
}

// batching is a looking Source that also answers a batch of lookups in one call (#153), each value's
// result being looking's, and records each batch's relation and size. short answers one result too
// few, and failBatch fails every batch with it.
type batching struct {
	*looking
	short     bool
	failBatch error
	batches   []string
}

func batchingAt(src ns.Source) *batching { return &batching{looking: lookingAt(src)} }

func (s *batching) LookupBatch(_ context.Context, rel string, positions []int, values [][]ns.Value) ([][]ns.Tuple, error) {
	s.mu.Lock()
	s.batches = append(s.batches, fmt.Sprintf("%s×%d", rel, len(values)))
	fail := s.failBatch
	s.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	out := make([][]ns.Tuple, len(values))
	for k, vals := range values {
		bound := map[int]ns.Value{}
		for j, p := range positions {
			bound[p] = vals[j]
		}
		out[k] = s.answer(rel, bound)
	}
	if s.short {
		out = out[1:]
	}
	return out, nil
}

// fanned is a hub calling n nodes, each with one attribute, and a node the hub doesn't call.
func fanned(n int) *ns.MemSource {
	m := ns.NewMemSource().Declare("edge", "src", "dst").Declare("attr", "id", "value")
	for i := range n {
		id := ns.S(fmt.Sprintf("b%d", i))
		m.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("a"), id}})
		m.Add("attr", ns.Tuple{Vals: []ns.Value{id, ns.S(fmt.Sprintf("v%d", i%3))}})
	}
	m.Add("attr", ns.Tuple{Vals: []ns.Value{ns.S("lone"), ns.S("v0")}})
	return m
}

var fannedQueries = []struct {
	text    string
	batches []string // with fanned(40)
}{
	{`edge("a", ?x), attr(?x, ?v) => ?x, ?v`, []string{"attr×40"}},
	{`has(?x, ?v) :- attr(?x, ?v); has(?x, ?v) :- edge(?x, ?v); edge("a", ?x), has(?x, ?v) => count(?v)`, []string{"attr×40", "edge×40"}},
}

// The planned SemiNaive asks a batching Source for the values a body binds a looked-up call to in one
// batch, rather than one lookup per value, and answers as reading it whole does; so it does when every
// batch result holds the whole relation, which the contract allows. Naive, the reference, never batches.
// control: a Source that doesn't batch is asked once per value.
func TestABatchingSourceIsAskedOncePerCallNotPerValue(t *testing.T) {
	const n = 40
	v := std(fanned(n))
	for _, c := range fannedQueries {
		text := c.text
		q := mustParse(t, text)
		want, werr := both(q, baseFor(v))
		for _, sloppy := range []bool{false, true} {
			src := batchingAt(fanned(n))
			src.sloppy = sloppy
			got, err := both(q, MustBase(v, src))
			if err != nil || werr != nil || !reflect.DeepEqual(binds(got), binds(want)) {
				t.Errorf("sloppy %v: %s\n batched: %v %v\n whole:   %v %v", sloppy, text, binds(got), err, binds(want), werr)
			}
			src = batchingAt(fanned(n))
			src.sloppy = sloppy
			if _, err := (SemiNaive{}).Eval(bg, q, MustBase(v, src)); err != nil {
				t.Fatal(err)
			}
			if got := src.looked("attr"); got != 0 || !reflect.DeepEqual(src.batches, c.batches) {
				t.Errorf("sloppy %v: %s made %d attr lookups and batches %v, want none and %v", sloppy, text, got, src.batches, c.batches)
			}
		}
		src := batchingAt(fanned(n))
		if _, err := (Naive{}).Eval(bg, q, MustBase(v, src)); err != nil || len(src.batches) != 0 {
			t.Errorf("Naive batched %v: %v", src.batches, err)
		}
		plain := lookingAt(fanned(n))
		if _, err := (SemiNaive{}).Eval(bg, q, MustBase(v, plain)); err != nil || plain.looked("attr") != n {
			t.Errorf("control: %s over a Source that doesn't batch looked attr up %d times, want %d: %v", text, plain.looked("attr"), n, err)
		}
	}
}

// A batch that fails, or answers a result count other than the values asked, fails the query with
// the relation's name, and a failed batch isn't kept: the same Base asks again.
func TestABatchThatFailsFailsTheQuery(t *testing.T) {
	q := mustParse(t, fannedQueries[0].text)
	src := batchingAt(fanned(5))
	src.failBatch = errors.New("store unreachable")
	b := MustBase(std(fanned(5)), src)
	if _, err := (SemiNaive{}).Eval(bg, q, b); err == nil || err.Error() != "query: looking up attr: store unreachable" {
		t.Errorf("err %v", err)
	}
	src.failBatch = nil
	if rows, err := (SemiNaive{}).Eval(bg, q, b); err != nil || len(rows) != 5 || len(src.batches) != 2 {
		t.Errorf("asked again: %d rows, batches %v, %v", len(rows), src.batches, err)
	}
	short := batchingAt(fanned(5))
	short.short = true
	if _, err := (SemiNaive{}).Eval(bg, q, MustBase(std(fanned(5)), short)); err == nil || err.Error() != "query: looking up attr: a batch of 5 lookups answered 4" {
		t.Errorf("short: %v", err)
	}
}

// Explain counts a batch apart from the lookups, on the report and on its relation, and names the
// batched pattern on the literal that reads it.
func TestExplainCountsBatches(t *testing.T) {
	var r Report
	q := mustParse(t, fannedQueries[0].text)
	if _, err := (SemiNaive{}).Eval(bg, q, MustBase(std(fanned(40)), batchingAt(fanned(40))), Explain(&r)); err != nil {
		t.Fatal(err)
	}
	var attr *SourceReport
	for _, s := range r.Sources {
		if s.Relation == "attr" {
			attr = s
		}
	}
	if r.Lookups != 1 || r.Batches != 1 || attr == nil || attr.Batches != 1 || attr.Lookups != 0 || attr.Fetched != 40 {
		t.Fatalf("lookups %d, batches %d, attr %+v\n%s", r.Lookups, r.Batches, attr, &r)
	}
	text := r.String()
	if !strings.Contains(text, "in 1 lookups and 1 batches") || !strings.Contains(text, "batched lookup (id)") {
		t.Errorf("text doesn't show the batch:\n%s", text)
	}
	var plain Report
	if _, err := (SemiNaive{}).Eval(bg, q, MustBase(std(fanned(40)), lookingAt(fanned(40))), Explain(&plain)); err != nil {
		t.Fatal(err)
	}
	for i, l := range r.Goal.Literals {
		if p := plain.Goal.Literals[i]; l.Reached != p.Reached || l.Passed != p.Passed {
			t.Errorf("%s reached %d and passed %d, %d and %d unbatched: the prefetch's rerun counted", l.Literal, l.Reached, l.Passed, p.Reached, p.Passed)
		}
	}
}

// Prefetching runs a body's prefix again, so it stops at a Volatile predicate, which could answer
// differently the second time, and leaves a limit without an order by, which stops at its first rows,
// to look up as it goes. control: the same predicate not Volatile is run twice and the call batched.
func TestPrefetchLeavesVolatilePrefixesAndEarlyStops(t *testing.T) {
	q := mustParse(t, `step(?h, ?x), attr(?x, ?v) => ?x, ?v`)
	for _, volatile := range []bool{true, false} {
		v := std(fanned(10))
		calls := stepper(t, v, volatile)
		src := batchingAt(fanned(10))
		rows, err := (SemiNaive{}).Eval(bg, q, MustBase(v, src))
		if err != nil || len(rows) != 10 {
			t.Fatalf("volatile %v: %d rows, %v", volatile, len(rows), err)
		}
		if batched := len(src.batches) > 0; batched == volatile || *calls != map[bool]int{true: 1, false: 2}[volatile] {
			t.Errorf("volatile %v: batches %v, step called %d times", volatile, src.batches, *calls)
		}
	}
	src := batchingAt(fanned(10))
	limited := mustParse(t, fannedQueries[0].text+` limit 2`)
	if rows, err := (SemiNaive{}).Eval(bg, limited, MustBase(std(fanned(10)), src)); err != nil || len(rows) != 2 || len(src.batches) != 0 || src.looked("attr") > 2 {
		t.Errorf("limit 2: %d rows, batches %v, %d attr lookups, %v", len(rows), src.batches, src.looked("attr"), err)
	}
	ordered := mustParse(t, fannedQueries[0].text+` order by ?x limit 2`)
	src = batchingAt(fanned(10))
	if _, err := (SemiNaive{}).Eval(bg, ordered, MustBase(std(fanned(10)), src)); err != nil || len(src.batches) != 1 {
		t.Errorf("control: ordered limit 2: batches %v, %v", src.batches, err)
	}
}

// A batch counts a unit of work per value it asks, as a lookup of each would.
func TestABatchCountsWorkPerValue(t *testing.T) {
	q := mustParse(t, `attr(?x, ?v) => ?x`)
	src := batchingAt(fanned(3))
	nb := *MustBase(std(fanned(3)), src)
	nb.run = &evalRun{ctx: bg}
	vals := [][]ns.Value{{ns.S("b0")}, {ns.S("b1")}, {ns.S("b2")}}
	if err := nb.lookupBatch(src, q.Goal.Literals[0].Pos, 1, vals); err != nil || nb.run.used != 3 || len(nb.run.looked) != 3 {
		t.Errorf("work %d, kept %d, %v", nb.run.used, len(nb.run.looked), err)
	}
}
