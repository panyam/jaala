package datalog

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"math/bits"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/jaala/ns"
)

// The store-federation mission's exercise (#218, #219): a federation of stores shaped like a host's
// (Declaire's one store per repo fragment), each answering lookups from the patterns it indexes and
// scanning for the rest, asked questions shaped like a host's. BenchmarkStores reports what each
// question costs the stores as their number grows; TestStoresExercise is its smoke test in CI.

// storeNodes is each repo's node count. A node n<k> calls n<2k+1> and n<2k+2> in its own repo, a
// tree, and every twentieth calls a leaf of the next repo, so a walk from one node reaches its repo
// and a few leaves, however many repos there are.
const storeNodes = 200

// storeChain is how many nodes each repo's next chain (n0, n1, ...) links, a walk as deep as it is
// long, so a recursive question's cost per level shows (#231).
const storeChain = 60

// storeLatencyMs is the round trip a store call stands for, SQLite over OPFS or a network, which
// the benchmark reports from the calls rather than sleeping.
const storeLatencyMs = 2

// A store is one repo's facts behind a LookupSource with an index catalog: a lookup binding every
// position of an indexed pattern reads that pattern's bucket, and any other scans the relation. A
// relation read whole streams (ns.StreamSource, #233), or, sliced, is read whole before the first
// tuple goes, as a store returning one slice is. It counts the calls made to it, the calls that
// scanned, the tuples it scanned and the tuples it shipped to the engine.
type store struct {
	*ns.MemSource
	indexed map[string][][]int
	buckets map[string][]ns.Tuple
	// uncatalogued hides the catalog from the planner: Indexed says every pattern is indexed, as a
	// LookupSource that doesn't implement ns.IndexedLookups is taken to.
	uncatalogued bool
	sliced       bool
	// noWalk hides the store's Walk, as a store that doesn't implement ns.WalkSource.
	noWalk bool
	// noGoal refuses a page or a summary, as a store before #240 and #241.
	noGoal bool

	mu                                 sync.Mutex
	calls, unindexed, scanned, shipped int
}

// storeCatalog is what each store indexes: node, attr and edge by their first argument (an id, and
// an edge's source). Edges by their target are deliberately missing, as a real store's might be.
var storeCatalog = map[string][][]int{"node": {{0}}, "attr": {{0}}, "edge": {{0}}, "next": {{0}}}

func repoStore(repo, repos int) *store {
	m := ns.NewMemSource().
		Declare("node", "id", "kind").
		Declare("attr", "id", "key", "value").
		Declare("edge", "src", "kind", "dst").
		Declare("next", "src", "dst")
	id := func(r, k int) ns.Value { return ns.S(fmt.Sprintf("repo%d/n%d", r, k)) }
	for k := range storeNodes {
		m.Add("node", ns.Tuple{Vals: []ns.Value{id(repo, k), ns.S("func")}})
		m.Add("attr", ns.Tuple{Vals: []ns.Value{id(repo, k), ns.S("name"), ns.S("n" + strconv.Itoa(k))}})
		m.Add("attr", ns.Tuple{Vals: []ns.Value{id(repo, k), ns.S("lines"), ns.S(strconv.Itoa(k % 50))}})
		for _, c := range []int{2*k + 1, 2*k + 2} {
			if c < storeNodes {
				m.Add("edge", ns.Tuple{Vals: []ns.Value{id(repo, k), ns.S("calls"), id(repo, c)}})
			}
		}
		if k+1 < storeChain {
			m.Add("next", ns.Tuple{Vals: []ns.Value{id(repo, k), id(repo, k+1)}})
		}
		if k%50 == 0 {
			m.Add("edge", ns.Tuple{Vals: []ns.Value{id(repo, k), ns.S("recurses"), id(repo, k)}})
		}
		if k%20 == 0 {
			m.Add("edge", ns.Tuple{Vals: []ns.Value{id(repo, k), ns.S("calls"), id((repo+1)%repos, storeNodes-1-k/20)}})
		}
	}
	s := &store{MemSource: m, indexed: storeCatalog, buckets: map[string][]ns.Tuple{}}
	for rel, patterns := range storeCatalog {
		for _, p := range patterns {
			for _, t := range m.Tuples(rel) {
				k := bucketKey(rel, p, func(i int) ns.Value { return t.Vals[i] })
				s.buckets[k] = append(s.buckets[k], t)
			}
		}
	}
	return s
}

func bucketKey(rel string, pattern []int, val func(int) ns.Value) string {
	parts := []string{rel, fmt.Sprint(pattern)}
	for _, i := range pattern {
		parts = append(parts, val(i).S)
	}
	return strings.Join(parts, "\x00")
}

// indexes reports the catalog pattern a lookup binding bound can read, if any.
func (s *store) indexes(rel string, bound func(int) bool) ([]int, bool) {
	for _, p := range s.indexed[rel] {
		if !slices.ContainsFunc(p, func(i int) bool { return !bound(i) }) {
			return p, true
		}
	}
	return nil, false
}

// Indexed implements ns.IndexedLookups from the catalog.
func (s *store) Indexed(rel string, bound []int) bool {
	_, ok := s.indexes(rel, func(i int) bool { return slices.Contains(bound, i) })
	return ok || s.uncatalogued
}

// Tuples counts a whole read as a call that scanned.
func (s *store) Tuples(rel string) []ns.Tuple {
	ts := s.MemSource.Tuples(rel)
	s.mu.Lock()
	s.calls++
	s.unindexed++
	s.scanned += len(ts)
	s.shipped += len(ts)
	s.mu.Unlock()
	return ts
}

// TuplesSeq counts a streamed read as a call that scanned what it shipped, the tuples the engine
// took before it stopped.
func (s *store) TuplesSeq(_ context.Context, rel string) iter.Seq2[ns.Tuple, error] {
	return func(yield func(ns.Tuple, error) bool) {
		ts := s.MemSource.Tuples(rel)
		s.mu.Lock()
		s.calls++
		s.unindexed++
		if s.sliced {
			s.scanned += len(ts)
			s.shipped += len(ts)
		}
		s.mu.Unlock()
		for _, t := range ts {
			if !s.sliced {
				s.mu.Lock()
				s.scanned++
				s.shipped++
				s.mu.Unlock()
			}
			if !yield(t, nil) {
				return
			}
		}
	}
}

func (s *store) Lookup(_ context.Context, rel string, bound map[int]ns.Value) ([]ns.Tuple, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	out, scan := s.match(rel, bound)
	s.shipped += len(out)
	if scan {
		s.scanned += len(s.MemSource.Tuples(rel))
		s.unindexed++
	}
	return out, nil
}

// match is rel's facts matching bound, from an index when the catalog has one, and whether it scanned.
func (s *store) match(rel string, bound map[int]ns.Value) ([]ns.Tuple, bool) {
	if p, ok := s.indexes(rel, func(i int) bool { _, ok := bound[i]; return ok }); ok {
		return s.buckets[bucketKey(rel, p, func(i int) ns.Value { return bound[i] })], false
	}
	var out []ns.Tuple
	for _, t := range s.MemSource.Tuples(rel) {
		match := true
		for i, v := range bound {
			match = match && t.Vals[i].S == v.S
		}
		if match {
			out = append(out, t)
		}
	}
	return out, true
}

// Pushes implements ns.PushSource: a store evaluates any conjunction of its relations, as one SQL
// statement over its tables would (#237).
func (s *store) Pushes(c ns.Conjunction) bool { return c.Page == nil || !s.noGoal }

// Push evaluates c as one call, joining through the store's indexes where the bound values allow and
// scanning where they don't, and ships the rows' tuples.
func (s *store) Push(_ context.Context, c ns.Conjunction) iter.Seq2[[]ns.Tuple, error] {
	return func(yield func([]ns.Tuple, error) bool) {
		s.mu.Lock()
		s.calls++
		scanned := false
		match := func(rel string, bound map[int]ns.Value) []ns.Tuple {
			out, scan := s.match(rel, bound)
			if scan {
				s.scanned += len(s.MemSource.Tuples(rel))
				scanned = true
			}
			return out
		}
		schema := func(rel string) ns.Schema { sc, _ := s.Schema(rel); return sc }
		rows := forBindings(c, schema, evalConjunction(c, schema, match, false))
		if c.Page != nil {
			rows = pageOf(c, schema, rows)
		}
		if scanned {
			s.unindexed++
		}
		s.mu.Unlock()
		for _, row := range rows {
			s.mu.Lock()
			s.shipped += len(row)
			s.mu.Unlock()
			if !yield(row, nil) {
				return
			}
		}
	}
}

// Summarizes implements ns.SummarySource: a store reduces any conjunction itself, as GROUP BY would
// (#240).
func (s *store) Summarizes(ns.Conjunction, ns.Summary) bool { return !s.noGoal }

// Summarize reduces c's matches as sm asks in one call, shipping a tuple per row.
func (s *store) Summarize(_ context.Context, c ns.Conjunction, sm ns.Summary) iter.Seq2[ns.SummaryRow, error] {
	return func(yield func(ns.SummaryRow, error) bool) {
		s.mu.Lock()
		s.calls++
		scanned := false
		match := func(rel string, bound map[int]ns.Value) []ns.Tuple {
			out, scan := s.match(rel, bound)
			if scan {
				s.scanned += len(s.MemSource.Tuples(rel))
				scanned = true
			}
			return out
		}
		schema := func(rel string) ns.Schema { sc, _ := s.Schema(rel); return sc }
		rows := summaryOf(c, sm, schema, evalConjunction(c, schema, match, false), false)
		if scanned {
			s.unindexed++
		}
		s.shipped += len(rows)
		s.mu.Unlock()
		for _, r := range rows {
			if !yield(r, nil) {
				return
			}
		}
	}
}

// Walks implements ns.WalkSource: a store follows any relation itself, as WITH RECURSIVE over its
// tables would (#239).
func (s *store) Walks(string, int, int, []int) bool { return !s.noWalk }

// Walk follows w in one call, reading each reached value's facts through the store's index where the
// catalog has one and scanning where it doesn't.
func (s *store) Walk(_ context.Context, w ns.Walk) iter.Seq2[ns.Tuple, error] {
	return func(yield func(ns.Tuple, error) bool) {
		s.mu.Lock()
		s.calls++
		facts := walkFacts(w, func(bound map[int]ns.Value) []ns.Tuple {
			out, scan := s.match(w.Relation, bound)
			if scan {
				s.scanned += len(s.MemSource.Tuples(w.Relation))
				s.unindexed++
			}
			return out
		})
		s.shipped += len(facts)
		s.mu.Unlock()
		for _, t := range facts {
			if !yield(t, nil) {
				return
			}
		}
	}
}

// A batchingStore is a store answering a batch of lookups in one call (ns.BatchLookupSource, #153),
// as a database asked `WHERE id IN (...)` does; an unindexed batch scans the relation once.
type batchingStore struct{ *store }

func (s batchingStore) LookupBatch(_ context.Context, rel string, positions []int, values [][]ns.Value) ([][]ns.Tuple, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	out := make([][]ns.Tuple, len(values))
	scanned := false
	for k, vals := range values {
		bound := map[int]ns.Value{}
		for j, p := range positions {
			bound[p] = vals[j]
		}
		var scan bool
		out[k], scan = s.match(rel, bound)
		s.shipped += len(out[k])
		scanned = scanned || scan
	}
	if scanned {
		s.scanned += len(s.MemSource.Tuples(rel))
		s.unindexed++
	}
	return out, nil
}

func (s *store) reset() {
	s.mu.Lock()
	s.calls, s.unindexed, s.scanned, s.shipped = 0, 0, 0, 0
	s.mu.Unlock()
}

// repoStores is n repos' stores, each held by the repo's id prefix wherever an argument is a
// repo's own id, and answering a batch of lookups in one call when batching, in a partitioned
// federation, which pushes a conjunction across them (#238). An edge lives in its
// source's repo, so its target names no store.
func repoStores(n int, batching bool) ([]*store, *ns.Federation) {
	stores := make([]*store, n)
	members := make([]ns.Member, n)
	for i := range n {
		stores[i] = repoStore(i, n)
		var src ns.Source = stores[i]
		if batching {
			src = batchingStore{stores[i]}
		}
		members[i] = ns.Member{
			Name:   fmt.Sprintf("repo%d", i),
			Source: src,
			Holds:  ns.HoldsPrefix(fmt.Sprintf("repo%d/", i), map[string][]int{"node": {0}, "attr": {0}, "edge": {0}, "next": {0}}),
		}
	}
	f, err := ns.NewPartitionedFederation(members...)
	if err != nil {
		panic(err)
	}
	return stores, f
}

// storeQuestions are the exercise's questions, about repo0, which every N has. callees-with-attr
// binds attr's key and value, more than edge's source, but no store indexes attr by them, so a
// planner that knows the catalog reads edge first (#154). The last four are mission #235's: a join
// whose facts are all in one store (n5's callees and theirs stay in repo0), a count and a page over
// every store, and an unbound call read whole (#233).
var storeQuestions = []struct{ name, text string }{
	{"attrs-by-id", `attr("repo0/n3", ?k, ?v) => ?k, ?v`},
	{"two-hops", `edge("repo0/n20", _, ?m), edge(?m, _, ?o) => ?o`},
	{"callers", `edge(?s, _, "repo0/n3") => ?s`},
	{"callees-with-attr", `attr(?m, "lines", "7"), edge("repo0/n3", _, ?m) => ?m`},
	{"reach", `reach(?a, ?b) :- edge(?a, _, ?b); reach(?a, ?c) :- reach(?a, ?b), edge(?b, _, ?c); reach("repo0/n0", ?x) => count(?x)`},
	{"deep-walk", `walk(?a, ?b) :- next(?a, ?b); walk(?a, ?c) :- walk(?a, ?b), next(?b, ?c); walk("repo0/n0", ?x) => count(?x)`},
	{"two-hops-in-repo", `edge("repo0/n5", _, ?m), edge(?m, _, ?o) => ?o`},
	{"count-lines", `attr(?id, "lines", "7") => count(?id)`},
	{"page", `attr(?id, "lines", ?n) => ?id, ?n order by ?id limit 10`},
	{"self-loops", `edge(?a, ?k, ?a) => ?a, ?k`},
}

// storeBudget is the budget self-loops is asked under too, less than one store's edges, to show a
// whole read stopping at it.
const storeBudget = 100

var selfLoopsQuestion = storeQuestions[len(storeQuestions)-1].text

// storeCost is what one ask of a question cost the stores.
type storeCost struct {
	asked, calls, unindexed, scanned, shipped int
}

func costOf(stores []*store) storeCost {
	var c storeCost
	for _, s := range stores {
		s.mu.Lock()
		if s.calls > 0 {
			c.asked++
		}
		c.calls += s.calls
		c.unindexed += s.unindexed
		c.scanned += s.scanned
		c.shipped += s.shipped
		s.mu.Unlock()
		s.reset()
	}
	return c
}

// A sliced storeSet's stores read a relation whole before shipping any of it.
func (s storeSet) sliced() storeSet {
	for _, st := range s.stores {
		st.sliced = true
	}
	return s
}

// askBudgeted asks q once on a fresh Base under storeBudget, which it must exceed, and returns
// what the ask cost the stores.
func (s storeSet) askBudgeted(q Query) (storeCost, error) {
	costOf(s.stores)
	_, err := (SemiNaive{}).Eval(bg, q, MustBase(s.v, s.fed), Budget(storeBudget))
	if over := (*BudgetExceeded)(nil); !errors.As(err, &over) {
		return storeCost{}, fmt.Errorf("under a budget of %d: %v, want it exceeded", storeBudget, err)
	}
	return costOf(s.stores), nil
}

// An uncatalogued storeSet's stores hide their catalog from the planner, as stores that don't say
// what they index.
func (s storeSet) uncatalogued() storeSet {
	for _, st := range s.stores {
		st.uncatalogued = true
	}
	return s
}

// A storeSet is n repos' stores, their federation and its vocabulary, built once so that what a
// question costs is measured apart from the fixture.
type storeSet struct {
	stores []*store
	fed    *ns.Federation
	v      *ns.Vocabulary
}

func newStoreSet(n int) storeSet {
	stores, f := repoStores(n, true)
	return storeSet{stores: stores, fed: f, v: std(f)}
}

// unbatchedStoreSet is newStoreSet over stores asked one lookup at a time, as before #153, and that
// don't walk.
func unbatchedStoreSet(n int) storeSet {
	stores, f := repoStores(n, false)
	return storeSet{stores: stores, fed: f, v: std(f)}.unwalked()
}

// An ungoaled storeSet's stores neither page nor summarize, as before #240 and #241.
func (s storeSet) ungoaled() storeSet {
	for _, st := range s.stores {
		st.noGoal = true
	}
	return s
}

// An unwalked storeSet's stores don't walk, as before #239.
func (s storeSet) unwalked() storeSet {
	for _, st := range s.stores {
		st.noWalk = true
	}
	return s
}

// askTwice asks q on a fresh Base over the stores, set up by each of setup, then again on the same
// Base, and returns each ask's cost and the first ask's answer.
func (s storeSet) askTwice(q Query, setup ...func(*Base)) (first, second storeCost, rows []Row, err error) {
	costOf(s.stores)
	b := MustBase(s.v, s.fed)
	for _, f := range setup {
		f(b)
	}
	if rows, err = (SemiNaive{}).Eval(bg, q, b); err != nil {
		return
	}
	first = costOf(s.stores)
	if _, err = (SemiNaive{}).Eval(bg, q, b); err != nil {
		return
	}
	return first, costOf(s.stores), rows, nil
}

// BenchmarkStores is the exercise of missions #219 and #235: each question over 1, 10 and 50 stores,
// reporting the stores it asked, its calls to them (round trips, and the latency they stand for), the
// calls an index couldn't answer and the tuples they scanned, the tuples they shipped to the engine,
// what asking it again on the same Base called, and the peak heap in use. self-loops-budgeted is
// self-loops under storeBudget, which a streamed read stops at (#233).
func BenchmarkStores(b *testing.B) {
	for _, question := range storeQuestions {
		q := MustParse(question.text)
		for _, n := range []int{1, 10, 50} {
			b.Run(fmt.Sprintf("%s/stores=%d", question.name, n), func(b *testing.B) {
				set := newStoreSet(n)
				var first, second storeCost
				var peak uint64
				for i := 0; i < b.N; i++ {
					var err error
					peak = max(peak, peakHeapDuring(func() { first, second, _, err = set.askTwice(q) }))
					if err != nil {
						b.Fatal(err)
					}
				}
				b.ReportMetric(float64(first.asked), "stores-asked")
				b.ReportMetric(float64(first.calls), "store-calls")
				b.ReportMetric(float64(first.calls*storeLatencyMs), "latency-ms")
				b.ReportMetric(float64(first.unindexed), "unindexed-calls")
				b.ReportMetric(float64(first.scanned), "scanned")
				b.ReportMetric(float64(first.shipped), "shipped")
				b.ReportMetric(float64(second.calls), "calls-again")
				b.ReportMetric(float64(peak)/1e6, "peak-MB")
			})
		}
	}
	q := MustParse(selfLoopsQuestion)
	for _, n := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("self-loops-budgeted/stores=%d", n), func(b *testing.B) {
			set := newStoreSet(n)
			var c storeCost
			var peak uint64
			for i := 0; i < b.N; i++ {
				var err error
				peak = max(peak, peakHeapDuring(func() { c, err = set.askBudgeted(q) }))
				if err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(c.asked), "stores-asked")
			b.ReportMetric(float64(c.calls), "store-calls")
			b.ReportMetric(float64(c.shipped), "shipped")
			b.ReportMetric(float64(peak)/1e6, "peak-MB")
		})
	}
}

// The exercise's fixture can tell its cases apart: over three stores each question answers as one
// Source of the same facts does, and the costs the mission sets out to cut show (the checks marked
// control). The rest are the mission's targets met so far: an indexed pattern first (#154), a batch
// per call (#153), and nothing fetched for a repeated question (#212).
func TestStoresExercise(t *testing.T) {
	const n = 3
	set := newStoreSet(n)
	f := set.fed
	whole := ns.NewMemSource()
	for _, rel := range f.Relations() {
		s, _ := f.Schema(rel)
		whole.DeclareSchema(rel, s)
		for _, tu := range f.Tuples(rel) {
			whole.Add(rel, tu)
		}
	}
	costs := map[string]storeCost{}
	for _, question := range storeQuestions {
		q := mustParse(t, question.text)
		first, second, got, err := set.askTwice(q)
		if err != nil {
			t.Fatalf("%s: %v", question.name, err)
		}
		want, err := both(q, MustBase(std(whole), whole))
		if err != nil || !reflect.DeepEqual(binds(got), binds(want)) {
			t.Errorf("%s over %d stores: %v, over one source: %v %v", question.name, n, binds(got), binds(want), err)
		}
		if len(want) == 0 {
			t.Errorf("control: %s answers nothing", question.name)
		}
		if second.calls != 0 {
			t.Errorf("asking %s again made %d store calls, want none: the Base keeps what it looked up (#212)", question.name, second.calls)
		}
		// A relation read whole is kept apart from the lookup cache, so only a lookup is asked again.
		uncached := func(b *Base) { b.LimitLookupCache(0) }
		if _, again, _, err := set.askTwice(q, uncached); question.name != "self-loops" && (err != nil || again.calls == 0) {
			t.Errorf("control: with the lookup cache off, asking %s again called no store: %v", question.name, err)
		}
		costs[question.name] = first
	}
	if c := costs["attrs-by-id"]; c.asked != 1 || c.unindexed != 0 {
		t.Errorf("control: a lookup by id asked %d stores with %d unindexed calls, want 1 and 0", c.asked, c.unindexed)
	}
	if c := costs["callers"]; c.asked != n || c.unindexed != n {
		t.Errorf("control: callers asked %d stores with %d unindexed calls, want %d and %d (#154)", c.asked, c.unindexed, n, n)
	}
	reach := mustParse(t, storeQuestions[4].text)
	if c, _, _, err := unbatchedStoreSet(n).askTwice(reach); err != nil || c.calls <= storeNodes {
		t.Errorf("control: over stores that don't batch, reach made %d store calls, want more than its repo's %d nodes (a call per demanded value): %v", c.calls, storeNodes, err)
	}
	deepWalk := mustParse(t, storeQuestions[5].text)
	if c, _, _, err := newStoreSet(n).unwalked().askTwice(deepWalk); err != nil || c.calls < storeChain-1 {
		t.Errorf("control: over stores that don't walk, a walk %d deep made %d calls, want one per level: batching can't go below that (#231): %v", storeChain, c.calls, err)
	}
	if c := costs["deep-walk"]; c.calls != 1 {
		t.Errorf("a walk %d deep made %d calls, want one: the store walks it (#239)", storeChain, c.calls)
	}
	if c := costs["reach"]; c.calls != c.asked || c.asked != 2 {
		t.Errorf("reach made %d calls to %d stores, want one to each of repo0 and repo1, whose leaves repo0 calls: each store walks its part (#239)", c.calls, c.asked)
	}
	if c, depth := costs["reach"], bits.Len(storeNodes); c.calls > c.asked*depth {
		t.Errorf("reach made %d calls to %d stores, want at most a batch per store per level of its %d-level tree (#153)", c.calls, c.asked, depth)
	}
	blind, _, _, err := newStoreSet(n).uncatalogued().askTwice(mustParse(t, storeQuestions[3].text))
	if err != nil || blind.unindexed != n {
		t.Errorf("control: without the catalog, callees with an attribute made %d unindexed calls, want %d (one scan per store): %v", blind.unindexed, n, err)
	}
	if c := costs["callees-with-attr"]; c.asked != 1 || c.unindexed != 0 {
		t.Errorf("callees with an attribute asked %d stores with %d unindexed calls, want 1 and 0: the planner should read edge by its source first (#154)", c.asked, c.unindexed)
	}
	if c := costs["two-hops"]; c.asked != 2 {
		t.Errorf("control: two hops from a node calling across repos asked %d stores, want 2", c.asked)
	}
	if c := costs["two-hops-in-repo"]; c.asked != 1 || c.calls != 1 {
		t.Errorf("two hops within repo0 asked %d stores in %d calls, want one pushed call to one store (#238)", c.asked, c.calls)
	}
	if c := costs["two-hops"]; c.calls != c.asked {
		t.Errorf("two hops from a node calling across repos made %d calls to %d stores, want one per store asked: the prefix leaving repo0 continued in the store holding it (#238)", c.calls, c.asked)
	}
	unpartitioned := func(set storeSet) storeSet {
		members := make([]ns.Member, len(set.stores))
		for i, st := range set.stores {
			members[i] = ns.Member{Name: fmt.Sprintf("repo%d", i), Source: batchingStore{st}, Holds: ns.HoldsPrefix(fmt.Sprintf("repo%d/", i), map[string][]int{"node": {0}, "attr": {0}, "edge": {0}, "next": {0}})}
		}
		f, err := ns.NewFederation(members...)
		if err != nil {
			t.Fatal(err)
		}
		set.fed = f
		return set
	}
	if c, _, _, err := unpartitioned(newStoreSet(n)).askTwice(mustParse(t, storeQuestions[6].text)); err != nil || c.calls < 2 {
		t.Errorf("control: over a federation not declared partitioned, two hops within repo0 made %d calls, want a call per hop: %v", c.calls, err)
	}
	twoHops := mustParse(t, `edge("repo0/n5", _, ?m), edge(?m, _, ?o) => ?o`)
	if c, _, rows, err := newStoreSet(1).askTwice(twoHops); err != nil || c.calls != 1 || c.shipped != 2*len(rows) {
		t.Errorf("two hops within one store made %d calls shipping %d tuples for %d answers, want one pushed call shipping two tuples an answer (#237): %v", c.calls, c.shipped, len(rows), err)
	}
	// A count ships a row per store and a page its limit per store (#240, #241); a store that only
	// looks up ships every match.
	for name, pushed := range map[string]int{"count-lines": n, "page": 10 * n} {
		q := mustParse(t, storeQuestions[slices.IndexFunc(storeQuestions, func(sq struct{ name, text string }) bool { return sq.name == name })].text)
		if c := costs[name]; c.asked != n || c.shipped > pushed {
			t.Errorf("%s asked %d stores, which shipped %d tuples, want %d stores shipping at most %d (#240, #241)", name, c.asked, c.shipped, n, pushed)
		}
		if c, _, _, err := newStoreSet(n).ungoaled().askTwice(q); err != nil || c.shipped <= pushed {
			t.Errorf("control: from stores that don't page or summarize, %s shipped %d tuples, want more than %d: %v", name, c.shipped, pushed, err)
		}
	}
	edges := len(whole.Tuples("edge"))
	if c := costs["self-loops"]; c.shipped != edges {
		t.Errorf("control: an unbound call shipped %d tuples, want all %d edges read whole", c.shipped, edges)
	}
	q := mustParse(t, selfLoopsQuestion)
	if c, err := newStoreSet(n).askBudgeted(q); err != nil || c.shipped > storeBudget+1 {
		t.Errorf("self-loops under a budget of %d shipped %d tuples, want the streamed read stopped at the budget (#233): %v", storeBudget, c.shipped, err)
	}
	if c, err := newStoreSet(n).sliced().askBudgeted(q); err != nil || c.shipped <= storeBudget+1 {
		t.Errorf("control: from stores returning slices, self-loops under a budget of %d shipped %d tuples, want a whole store's edges: %v", storeBudget, c.shipped, err)
	}
}
