package datalog

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/jaala/ns"
)

// storedVersions is a TimedMemSource that serves each version as a store of its own (ns.SourceAt, #312):
// a pushing store over the version's facts, answering lookups and conjunctions and counting them. It
// counts its TimedTuples reads, which At over it never makes. fail makes SourceAt fail; missing leaves
// a relation out of the version's store.
type storedVersions struct {
	*ns.TimedMemSource
	mu         sync.Mutex
	stores     map[ns.Version][]*pushing
	asked      int
	timedReads int
	fail       error
	missing    string
}

func storing(src *ns.TimedMemSource) *storedVersions {
	return &storedVersions{TimedMemSource: src, stores: map[ns.Version][]*pushing{}}
}

func (s *storedVersions) SourceAt(v ns.Version) (ns.Source, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.asked++
	if s.fail != nil {
		return nil, s.fail
	}
	m := ns.NewMemSource()
	for _, rel := range s.TimedMemSource.Relations() {
		if rel == s.missing {
			continue
		}
		sch, _ := s.TimedMemSource.Schema(rel)
		m.DeclareSchema(rel, sch)
		tuples, _ := s.TimedMemSource.TimedTuples(bg, rel)
		for _, t := range tuples {
			if t.Versions.Contains(v) {
				m.Add(rel, ns.Tuple{Vals: t.Vals, Cites: t.CitesAt(v)})
			}
		}
	}
	p := pushingAt(m)
	s.stores[v] = append(s.stores[v], p)
	return p, nil
}

func (s *storedVersions) TimedTuples(ctx context.Context, rel string) ([]ns.TimedTuple, error) {
	s.mu.Lock()
	s.timedReads++
	s.mu.Unlock()
	return s.TimedMemSource.TimedTuples(ctx, rel)
}

// asking reports whether any version's store was looked up, pushed to or walked.
func (s *storedVersions) asking() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ps := range s.stores {
		for _, p := range ps {
			p.mu.Lock()
			n := len(p.lookups)
			p.mu.Unlock()
			if n+p.pushes()+p.walks() > 0 {
				return true
			}
		}
	}
	return false
}

// timedOnly hides a TimedSource's SourceAt, so the engine reads its versions from TimedTuples.
type timedOnly struct{ ns.TimedSource }

// At(v) over a Source serving each version as a store asks that store for what the query binds (here
// reach walks it), and never reads TimedTuples, answering as the same versions read from TimedTuples do. control: hiding
// SourceAt reads TimedTuples.
func TestAtAsksAVersionsStoreRatherThanReadingItWhole(t *testing.T) {
	src := storing(versionedGraph())
	q := mustParse(t, reachRules+`reach("a", ?x) => ?x`)
	for v, want := range []string{"b", "b,c", "b,c"} {
		for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
			rows, err := ev.Eval(bg, q, baseFor(std(src)), At(ns.Version(v)))
			if err != nil {
				t.Fatal(err)
			}
			if got := col(rows, "x"); got != want {
				t.Errorf("%T At(%d) = %v, want %v", ev, v, got, want)
			}
		}
	}
	if src.timedReads != 0 {
		t.Errorf("At read TimedTuples %d times from a Source serving versions as stores", src.timedReads)
	}
	planned := src.stores[1][len(src.stores[1])-1] // the planned SemiNaive's, which walks
	if planned.walks() == 0 {
		t.Errorf("version 1's store walked %d times, want reach walked there", planned.walks())
	}
	hidden := storing(versionedGraph())
	if _, err := (SemiNaive{}).Eval(bg, q, baseFor(std(timedOnly{hidden})), At(1)); err != nil {
		t.Fatal(err)
	}
	if hidden.timedReads == 0 || hidden.asked != 0 {
		t.Errorf("control: without SourceAt, At read TimedTuples %d times and asked SourceAt %d", hidden.timedReads, hidden.asked)
	}
}

// A join at a version goes to the version's store as one conjunction, as it does over a plain store.
func TestAJoinAtAVersionIsPushedToItsStore(t *testing.T) {
	src := storing(versionedGraph())
	q := mustParse(t, `edge(?x, ?y), edge(?y, ?z) => ?x, ?z`)
	rows, err := SemiNaive{}.Eval(bg, q, baseFor(std(src)), At(1))
	if err != nil {
		t.Fatal(err)
	}
	if got := col(rows, "x") + " " + col(rows, "z"); got != "a c" {
		t.Errorf("At(1) = %s, want a c", got)
	}
	p := src.stores[1][0]
	if !slices.ContainsFunc(p.pushed, func(c ns.Conjunction) bool { return len(c.Atoms) == 2 }) {
		t.Errorf("version 1's store was pushed %v, want the two-atom join", p.pushed)
	}
}

// SourceAt is asked once per version whose Base the cache keeps, and again once it's dropped.
func TestSourceAtIsAskedOncePerKeptVersion(t *testing.T) {
	src := storing(versionedGraph())
	b := baseFor(std(src))
	q := mustParse(t, `edge("a", ?y) => ?y`)
	for range 3 {
		for _, v := range []ns.Version{0, 1} {
			if _, err := (SemiNaive{}).Eval(bg, q, b, At(v)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if src.asked != 2 {
		t.Errorf("SourceAt asked %d times for two kept versions, want 2", src.asked)
	}
	b.LimitVersions(0)
	for range 2 {
		if _, err := (SemiNaive{}).Eval(bg, q, b, At(0)); err != nil {
			t.Fatal(err)
		}
	}
	if src.asked != 4 {
		t.Errorf("SourceAt asked %d times with no versions kept, want 4", src.asked)
	}
}

// A SourceAt that fails fails the Eval, naming the version, and is asked again by the next Eval; one
// whose Source lacks a relation is refused as NewBase refuses it.
func TestAVersionsSourceThatFailsFailsTheEval(t *testing.T) {
	src := storing(versionedGraph())
	src.fail = errors.New("store down")
	b := baseFor(std(src))
	q := mustParse(t, `edge("a", ?y) => ?y`)
	for range 2 {
		_, err := (SemiNaive{}).Eval(bg, q, b, At(1))
		if err == nil || err.Error() != "query: version 1: store down" || !errors.Is(err, src.fail) {
			t.Fatalf("err = %v, want query: version 1: store down", err)
		}
	}
	if src.asked != 2 {
		t.Errorf("SourceAt asked %d times after failing, want 2", src.asked)
	}
	src.fail, src.missing = nil, "node"
	_, err := (SemiNaive{}).Eval(bg, q, b, At(1))
	if want := "query: version 1: the source does not serve node"; err == nil || err.Error() != want {
		t.Errorf("err = %v, want %s", err, want)
	}
}

// What a version's Base keeps in a DerivedStore is filed under its version, so a fresh Base at
// another version derives its own rather than reading it (#220, #312), over a TimedSource read from
// TimedTuples and over one serving versions as stores alike, including stores whose own versions are
// the same in every version. control: the second Base at the same
// version reads the store.
func TestAVersionsKeptRelationsAreFiledUnderTheVersion(t *testing.T) {
	q := mustParse(t, reachRules+`reach(?a, ?b) => ?a, ?b`)
	for stores, src := range map[string]ns.TimedSource{
		"read from TimedTuples":                 &versionedTimed{versionedGraph(), "h1"},
		"served as stores":                      &versionedStored{storedVersions: storing(versionedGraph()), v: "h1"},
		"served as stores Versioned themselves": &movingStores{TimedMemSource: versionedGraph(), stores: map[ns.Version]*versioned{}},
	} {
		v := std(src)
		store := &counted{}
		want := map[ns.Version]string{}
		for _, at := range []ns.Version{0, 2} {
			rows, err := SemiNaive{}.Eval(bg, q, storedBase(v, src, store), At(at))
			if err != nil {
				t.Fatal(err)
			}
			want[at] = col(rows, "b")
		}
		if want[0] == want[2] {
			t.Fatalf("control: versions 0 and 2 answer alike (%s)", want[0])
		}
		writes := store.writes.Load()
		if writes == 0 {
			t.Fatalf("%s: nothing was kept", stores)
		}
		for _, at := range []ns.Version{2, 0} {
			rows, err := SemiNaive{}.Eval(bg, q, storedBase(v, src, store), At(at))
			if err != nil {
				t.Fatal(err)
			}
			if got := col(rows, "b"); got != want[at] {
				t.Errorf("%s: a fresh Base at %d answers %s, want %s", stores, at, got, want[at])
			}
		}
		if store.writes.Load() != writes {
			t.Errorf("%s: a fresh Base derived again (%d writes, was %d), so it didn't read the store", stores, store.writes.Load(), writes)
		}
	}
}

type versionedStored struct {
	*storedVersions
	v string
}

func (s *versionedStored) Version() string { return s.v }

// labels is a Source that doesn't change between versions: a label per node.
func labels() *ns.MemSource {
	m := ns.NewMemSource()
	m.Declare("label", "name", "text")
	for _, n := range []string{"a", "b", "c", "d"} {
		m.Add("label", ns.Tuple{Vals: []ns.Value{ns.S(n), ns.S(strings.ToUpper(n))}, Cites: []string{"label:" + n}})
	}
	return m
}

// A Federation of a TimedSource and a Source that doesn't change answers At(v) as one Source holding
// v's facts and the fixed ones (#312), through the timed member's store when it serves one, and Across
// as one TimedSource holding the fixed facts in every version.
func TestATimedFederationAnswersAtEachVersionWithItsFixedMembers(t *testing.T) {
	q := mustParse(t, reachRules+`reach("a", ?x), label(?x, ?l) => ?l`)
	for _, stores := range []bool{false, true} {
		var timed ns.Source = versionedGraph()
		if stores {
			timed = storing(versionedGraph())
		}
		f, err := ns.NewFederation(ns.Member{Source: timed}, ns.Member{Source: labels()})
		if err != nil {
			t.Fatal(err)
		}
		tf, err := f.Timed()
		if err != nil {
			t.Fatal(err)
		}
		b := baseFor(std(tf))
		for v, want := range []string{"B", "B,C", "B,C"} {
			rows, err := both(q, b, At(ns.Version(v)))
			if err != nil {
				t.Fatal(err)
			}
			if got := col(rows, "l"); got != want {
				t.Errorf("stores %v: At(%d) = %s, want %s", stores, v, got, want)
			}
		}
		if s, ok := timed.(*storedVersions); ok {
			// Naive read edge whole on b, so the planned SemiNaive asks the store on a fresh Base.
			if _, err := (SemiNaive{}).Eval(bg, q, baseFor(std(tf)), At(1)); err != nil {
				t.Fatal(err)
			}
			if s.timedReads != 0 || !s.asking() {
				t.Errorf("At through the federation read TimedTuples %d times, asked the stores %v", s.timedReads, s.asking())
			}
		}
		one := versionedGraph()
		one.DeclareSchema("label", ns.Schema{Arity: 2, Labels: []string{"name", "text"}})
		for _, l := range labels().Tuples("label") {
			one.Add("label", ns.Span(0, 3), l.Cites, l.Vals...)
		}
		across := Across(ns.Span(0, 3))
		got, err := SemiNaive{}.Eval(bg, q, b, across)
		if err != nil {
			t.Fatal(err)
		}
		want, err := SemiNaive{}.Eval(bg, q, baseFor(std(one)), across)
		if err != nil {
			t.Fatal(err)
		}
		if versionRows(got) != versionRows(want) {
			t.Errorf("stores %v: Across over the federation = %s, want %s", stores, versionRows(got), versionRows(want))
		}
	}
}

// versionRows writes rows' ?l with the versions each holds in.
func versionRows(rows []Row) string {
	var out []string
	for _, r := range rows {
		out = append(out, r.Bind["l"].S+" @"+versionsText(r.Versions))
	}
	return strings.Join(out, "; ")
}

// A federation is timed only when a member serves versions, and a member's Name prefixes its timed
// facts' citations as it does its plain ones.
func TestATimedFederationNamesItsMembersCitations(t *testing.T) {
	plain, err := ns.NewFederation(ns.Member{Source: labels()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.Timed(); err == nil || err.Error() != "query: federation: no member serves versions (ns.TimedSource)" {
		t.Errorf("Timed over no TimedSource = %v", err)
	}
	f, err := ns.NewFederation(ns.Member{Name: "rev", Source: versionedGraph()}, ns.Member{Name: "lib", Source: labels()})
	if err != nil {
		t.Fatal(err)
	}
	tf, err := f.Timed()
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := tf.TimedTuples(bg, "node")
	if err != nil {
		t.Fatal(err)
	}
	if got := nodes[0].CitesAt(2); !slices.Equal(got, []string{"rev:node:a@new"}) {
		t.Errorf("node a's citations at 2 = %v, want rev:node:a@new", got)
	}
	labelled, err := tf.TimedTuples(bg, "label")
	if err != nil {
		t.Fatal(err)
	}
	if got := labelled[0]; !ns.SameVersions(got.Versions, ns.Span(0, 3)) || !slices.Equal(got.CitesAt(1), []string{"lib:label:a"}) {
		t.Errorf("label a holds in %v citing %v, want every version citing lib:label:a", versionsText(got.Versions), got.CitesAt(1))
	}
}

// movingStores serves each version as a Versioned MemSource it keeps, so a test can change a version's
// store and move its version.
type movingStores struct {
	*ns.TimedMemSource
	stores map[ns.Version]*versioned
}

func (s *movingStores) SourceAt(v ns.Version) (ns.Source, error) {
	if st, ok := s.stores[v]; ok {
		return st, nil
	}
	m := ns.NewMemSource()
	for _, rel := range s.TimedMemSource.Relations() {
		sch, _ := s.TimedMemSource.Schema(rel)
		m.DeclareSchema(rel, sch)
		tuples, _ := s.TimedMemSource.TimedTuples(bg, rel)
		for _, t := range tuples {
			if t.Versions.Contains(v) {
				m.Add(rel, ns.Tuple{Vals: t.Vals, Cites: t.CitesAt(v)})
			}
		}
	}
	s.stores[v] = &versioned{MemSource: m, v: "1"}
	return s.stores[v], nil
}

// A version's Source that is Versioned drops that version's caches when it moves, as a plain Base's
// Source does. control: unmoved, the version still answers what it read.
func TestAVersionsOwnSourceMovingDropsItsCaches(t *testing.T) {
	src := &movingStores{TimedMemSource: versionedGraph(), stores: map[ns.Version]*versioned{}}
	b := baseFor(std(src))
	q := mustParse(t, `edge(?a, ?b) => ?a, ?b`)
	if rows, _ := (SemiNaive{}).Eval(bg, q, b, At(1)); len(rows) != 2 {
		t.Fatalf("before = %v", rows)
	}
	src.stores[1].Add("edge", ns.Tuple{Vals: []ns.Value{ns.S("d"), ns.S("e")}})
	if rows, _ := (SemiNaive{}).Eval(bg, q, b, At(1)); len(rows) != 2 {
		t.Fatalf("control: unmoved = %v", rows)
	}
	src.stores[1].v = "2"
	if rows, _ := (SemiNaive{}).Eval(bg, q, b, At(1)); len(rows) != 3 {
		t.Errorf("after version 1's store moved = %v, want its new edge", rows)
	}
}
