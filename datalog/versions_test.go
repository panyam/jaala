package datalog

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// timedVersions is how many versions timedOver deals a Source's facts across.
const timedVersions = 4

// timedOver deals src's facts across timedVersions versions, each fact to a set drawn from its own
// values (so a shrunk program keeps its facts' versions), half as intervals and half as bits, some up
// to Open. A fact's citations in an odd version carry "@v", so a version's witnesses show whose they
// are. at(v) is a plain Source holding exactly version v's facts, as the engine should read them.
func timedOver(src ns.Source) (timed *ns.TimedMemSource, at func(ns.Version) *ns.MemSource) {
	timed = ns.NewTimedMemSource(ns.Span(0, timedVersions))
	for _, rel := range src.Relations() {
		s, _ := src.Schema(rel)
		timed.DeclareSchema(rel, s)
		for i, t := range src.Tuples(rel) {
			h := fnv.New64a()
			fmt.Fprintf(h, "%s/%d/%s", rel, i, tupleKey(t.Vals))
			draw := h.Sum64()
			var in []ns.Version
			for v := range ns.Version(timedVersions) {
				if draw&(1<<v) != 0 {
					in = append(in, v)
				}
			}
			var vs ns.VersionSet = ns.Bits(in...)
			if draw&(1<<8) != 0 {
				vs = ns.IntervalsOf(vs)
			}
			if vs.Contains(timedVersions-1) && draw&(1<<9) != 0 {
				vs = vs.Union(ns.Since(timedVersions - 1))
			}
			tt := ns.TimedTuple{Vals: t.Vals, Versions: vs}
			for v := range ns.Version(timedVersions) {
				cites := t.Cites
				if v%2 == 1 && len(cites) > 0 {
					cites = append(slices.Clone(cites), "@"+strconv.Itoa(int(v)))
				}
				tt.Cites = append(tt.Cites, ns.TimedCites{Versions: ns.Bits(v), Cites: cites})
			}
			timed.AddTimed(rel, tt)
		}
	}
	return timed, func(v ns.Version) *ns.MemSource {
		m := ns.NewMemSource()
		for _, rel := range timed.Relations() {
			s, _ := timed.Schema(rel)
			m.DeclareSchema(rel, s)
			tuples, _ := timed.TimedTuples(bg, rel)
			for _, t := range tuples {
				if t.Versions.Contains(v) {
					m.Add(rel, ns.Tuple{Vals: t.Vals, Cites: t.CitesAt(v)})
				}
			}
		}
		return m
	}
}

// atPrograms counts the corpus programs whose answer differs between two versions, the control that
// agreeAt's versions tell anything apart, and atStoredPrograms those whose versions' stores were looked
// up, pushed to or walked (#312).
var atPrograms, atStoredPrograms int

// agreeAt requires, for every version v of b's facts dealt across versions, every evaluator under
// At(v) to answer q as Naive does over a Base holding only v's facts, and to cite and witness as it
// does under CanonicalCites (#179), both reading the versions from TimedTuples and served as a store
// per version (ns.SourceAt, #312; through a timed Federation every third seed). The versions share one
// Base each, which keeps a single version's caches on every other seed, so a version's answer never
// reads another's.
func agreeAt(q Query, b *Base, opts []Option, seed int) string {
	if anyRows(q) {
		q.Limit = 0
	}
	timed, at := timedOver(b.src)
	tb := MustBase(b.reg, timed)
	stored := storing(timed)
	var storedSrc ns.Source = stored
	if seed%3 == 0 {
		f, err := ns.NewFederation(ns.Member{Source: stored})
		if err != nil {
			return err.Error()
		}
		if storedSrc, err = f.Timed(); err != nil {
			return err.Error()
		}
	}
	sb := MustBase(b.reg, storedSrc)
	if seed%2 == 1 {
		tb.LimitVersions(1)
		sb.LimitVersions(1)
	}
	var answers []string
	for v := range ns.Version(timedVersions) {
		mb := MustBase(b.reg, at(v))
		want, werr := Naive{}.Eval(bg, q, mb, opts...)
		answers = append(answers, fmt.Sprint(errText(werr), len(want), binds(want)))
		atV := append(opts[:len(opts):len(opts)], At(v))
		for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
			for how, base := range map[string]*Base{"read from TimedTuples": tb, "served as a store": sb} {
				got, err := ev.Eval(bg, q, base, atV...)
				if errText(err) != errText(werr) || !reflect.DeepEqual(binds(got), binds(want)) {
					return fmt.Sprintf("%T at version %d %s answers differently from a Source holding only its facts on %v\n only v: %v %v\n At(v):  %v %v", ev, v, how, q, binds(want), werr, binds(got), err)
				}
			}
		}
		if werr != nil {
			continue
		}
		canon := append(opts[:len(opts):len(opts)], CanonicalCites())
		w, _ := Naive{}.Eval(bg, q, mb, canon...)
		for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
			for how, base := range map[string]*Base{"read from TimedTuples": tb, "served as a store": sb} {
				g, _ := ev.Eval(bg, q, base, append(canon, At(v))...)
				if !reflect.DeepEqual(w, g) {
					return fmt.Sprintf("%T at version %d %s cites differently under CanonicalCites on %v\n only v: %v\n At(v):  %v", ev, v, how, q, w, g)
				}
			}
		}
	}
	if stored.timedReads > 0 {
		return fmt.Sprintf("At read TimedTuples %d times from a Source serving its versions as stores, on %v", stored.timedReads, q)
	}
	if stored.asking() {
		atStoredPrograms++
	}
	for _, a := range answers[1:] {
		if a != answers[0] {
			atPrograms++
			break
		}
	}
	return ""
}

// versioned is graph() dealt across three versions: a->b holds throughout, b->c from 1, c->d only in
// 0, and node a's citation changes at 2.
func versionedGraph() *ns.TimedMemSource {
	src := ns.NewTimedMemSource(ns.Span(0, 3))
	src.Declare("edge", "from", "to").Declare("node", "name")
	src.Add("edge", ns.Since(0), []string{"edge:ab"}, ns.S("a"), ns.S("b"))
	src.Add("edge", ns.Since(1), []string{"edge:bc"}, ns.S("b"), ns.S("c"))
	src.Add("edge", ns.Bits(0), []string{"edge:cd"}, ns.S("c"), ns.S("d"))
	src.AddTimed("node", ns.TimedTuple{Vals: []ns.Value{ns.S("a")}, Versions: ns.Since(0), Cites: []ns.TimedCites{
		{Versions: ns.Span(0, 2), Cites: []string{"node:a@old"}},
		{Versions: ns.Since(2), Cites: []string{"node:a@new"}},
	}})
	return src
}

const reachRules = `reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c); `

func TestAtAnswersOverItsVersionsFacts(t *testing.T) {
	b := baseFor(std(versionedGraph()))
	q := mustParse(t, reachRules+`reach("a", ?x) => ?x`)
	for v, want := range []string{"b", "b,c", "b,c"} {
		rows, err := both(q, b, At(ns.Version(v)))
		if err != nil {
			t.Fatal(err)
		}
		if got := col(rows, "x"); got != want {
			t.Errorf("At(%d) = %v, want %v", v, got, want)
		}
	}
	// control: without At the Source is read as a plain one, its last version.
	rows, _ := both(q, b)
	if got := col(rows, "x"); got != "b,c" {
		t.Errorf("plain = %v", got)
	}
}

func TestAtWitnessesCiteTheirVersionsCitations(t *testing.T) {
	b := baseFor(std(versionedGraph()))
	q := mustParse(t, `node(?n), edge(?n, ?m) => ?n, ?m`)
	for v, want := range map[ns.Version]string{0: "node:a@old", 2: "node:a@new"} {
		rows, err := both(q, b, At(v), Witnesses())
		if err != nil || len(rows) != 1 {
			t.Fatalf("At(%d) = %v, %v", v, rows, err)
		}
		if got := rows[0].Witness[0].Cites; !slices.Equal(got, []string{want}) {
			t.Errorf("At(%d) witness cites %v, want %s", v, got, want)
		}
		if !slices.Contains(rows[0].Cites, want) {
			t.Errorf("At(%d) row cites %v, want %s among them", v, rows[0].Cites, want)
		}
	}
}

// A version's derived relations are kept for that version only. control: the second version asked
// answers its own facts after the first's reach was kept.
func TestAtKeepsEachVersionsCachesApart(t *testing.T) {
	b := baseFor(std(versionedGraph()))
	q := mustParse(t, reachRules+`reach(?a, ?b) => ?a, ?b`)
	counts := map[ns.Version]int{}
	for _, v := range []ns.Version{0, 1, 0, 1} {
		rows, err := SemiNaive{}.Eval(bg, q, b, At(v))
		if err != nil {
			t.Fatal(err)
		}
		if n, ok := counts[v]; ok && n != len(rows) {
			t.Fatalf("At(%d) answered %d rows, then %d", v, n, len(rows))
		}
		counts[v] = len(rows)
	}
	if counts[0] == counts[1] {
		t.Fatalf("control: versions 0 and 1 both answer %d rows", counts[0])
	}
}

func TestLimitVersionsDropsTheLeastRecent(t *testing.T) {
	b := baseFor(std(versionedGraph()))
	b.LimitVersions(2)
	q := mustParse(t, `edge(?a, ?b) => ?a, ?b`)
	for _, v := range []ns.Version{0, 1, 2} {
		if _, err := (SemiNaive{}).Eval(bg, q, b, At(v)); err != nil {
			t.Fatal(err)
		}
	}
	held := func() []ns.Version {
		var out []ns.Version
		b.versions.each(func(vb *Base) { out = append(out, *vb.at) })
		return out
	}
	if got := held(); !slices.Equal(got, []ns.Version{2, 1}) {
		t.Fatalf("held %v, want [2 1]", got)
	}
	b.LimitVersions(0)
	if got := held(); len(got) != 0 {
		t.Fatalf("LimitVersions(0) still holds %v", got)
	}
	// control: with nothing held, a version still answers, read again from the shared timed read.
	rows, err := SemiNaive{}.Eval(bg, q, b, At(0))
	if err != nil || len(rows) != 2 {
		t.Fatalf("At(0) with nothing held = %v, %v", rows, err)
	}
}

// A generator is handed the version's own Source, and a context carrying the version: b->c holds from
// version 1.
func TestAtHandsAGeneratorItsVersion(t *testing.T) {
	v := std(versionedGraph())
	var seen []string
	err := v.AddPredicate("edges_seen", ns.Builtin{Arity: 1, Modes: [][]bool{{false}}, Gen: func(ctx context.Context, src ns.Source, _ []ns.Arg, emit func([]ns.Value, []string) error) error {
		at, ok := ns.VersionOf(ctx)
		seen = append(seen, fmt.Sprint(at, ok))
		n := 0
		for _, e := range src.Tuples("edge") {
			if e.Vals[0].S == "b" {
				n++
			}
		}
		return emit([]ns.Value{ns.N(float64(n))}, nil)
	}})
	if err != nil {
		t.Fatal(err)
	}
	b := baseFor(v)
	q := mustParse(t, `edges_seen(?n) => ?n`)
	for at, want := range map[ns.Version]string{0: "0", 2: "1", 1: "1"} {
		rows, err := SemiNaive{}.Eval(bg, q, b, At(at))
		if err != nil {
			t.Fatal(err)
		}
		if got := col(rows, "n"); got != want {
			t.Errorf("At(%d) = %v, want %s", at, got, want)
		}
	}
	if !slices.Contains(seen, "2 true") {
		t.Errorf("the generator saw versions %v, want 2 among them", seen)
	}
	seen = nil
	if _, err := (SemiNaive{}).Eval(bg, q, b); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(seen, []string{"0 false"}) {
		t.Errorf("without At the generator saw %v, want no version", seen)
	}
}

func TestAtRefusesWhatItCannotRead(t *testing.T) {
	q := mustParse(t, `edge(?a, ?b) => ?a`)
	_, err := both(q, baseFor(std(graph())), At(0))
	if err == nil || !strings.Contains(err.Error(), "At needs a Source that serves versions") {
		t.Errorf("At over a plain Source: %v", err)
	}
	_, err = both(q, baseFor(std(versionedGraph())), At(3))
	if err == nil || !strings.Contains(err.Error(), "version 3 is not one the Source serves") {
		t.Errorf("At(3) over versions 0 to 2: %v", err)
	}
}

// failingTimed is a TimedSource whose reads fail until told otherwise.
type failingTimed struct {
	*ns.TimedMemSource
	fail bool
}

func (f *failingTimed) TimedTuples(ctx context.Context, rel string) ([]ns.TimedTuple, error) {
	if f.fail {
		return nil, errors.New("store down")
	}
	return f.TimedMemSource.TimedTuples(ctx, rel)
}

func TestAFailedTimedReadIsNotKept(t *testing.T) {
	src := &failingTimed{versionedGraph(), true}
	b := MustBase(std(src), src)
	q := mustParse(t, `edge(?a, ?b) => ?a`)
	if _, err := (SemiNaive{}).Eval(bg, q, b, At(0)); err == nil || !strings.Contains(err.Error(), "reading edge: store down") {
		t.Fatalf("a failing read: %v", err)
	}
	src.fail = false
	if rows, err := (SemiNaive{}).Eval(bg, q, b, At(0)); err != nil || len(rows) != 2 {
		t.Fatalf("after the store recovered: %v, %v", rows, err)
	}
}

// versionedTimed is a TimedSource whose facts move, as a Versioned Source's do.
type versionedTimed struct {
	*ns.TimedMemSource
	version string
}

func (v *versionedTimed) Version() string { return v.version }

func TestAVersionedTimedSourceDropsEveryVersionWhenItMoves(t *testing.T) {
	src := &versionedTimed{versionedGraph(), "1"}
	b := MustBase(std(src), src)
	q := mustParse(t, `edge(?a, ?b) => ?a, ?b`)
	if rows, _ := (SemiNaive{}).Eval(bg, q, b, At(1)); len(rows) != 2 {
		t.Fatalf("before = %v", rows)
	}
	src.Add("edge", ns.Since(1), nil, ns.S("d"), ns.S("e"))
	// control: unmoved, the version's Base still answers what it read.
	if rows, _ := (SemiNaive{}).Eval(bg, q, b, At(1)); len(rows) != 2 {
		t.Fatalf("unmoved = %v", rows)
	}
	src.version = "2"
	if rows, _ := (SemiNaive{}).Eval(bg, q, b, At(1)); len(rows) != 3 {
		t.Fatalf("after the version moved = %v", rows)
	}
}

func TestDiffIsTheSetDifferenceOfTwoVersions(t *testing.T) {
	src := versionedGraph()
	// A fact spelled differently in two versions is one fact, read as its declared type.
	src.DeclareSchema("weight", ns.Schema{Arity: 2, Labels: []string{"node", "w"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
	src.Add("weight", ns.Bits(0), []string{"w:old"}, ns.S("a"), ns.S("1.0"))
	src.Add("weight", ns.Bits(2), []string{"w:new"}, ns.S("a"), ns.N(1))
	src.Add("weight", ns.Bits(2), nil, ns.S("b"), ns.N(2))
	b := baseFor(std(src))
	d, err := b.Diff(bg, "edge", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := tupleTexts(d.Added); !slices.Equal(got, []string{"b c [edge:bc]"}) {
		t.Errorf("added %v", got)
	}
	if got := tupleTexts(d.Removed); !slices.Equal(got, []string{"c d [edge:cd]"}) {
		t.Errorf("removed %v", got)
	}
	d, _ = b.Diff(bg, "weight", 0, 2)
	if got := tupleTexts(d.Added); !slices.Equal(got, []string{"b 2 []"}) || len(d.Removed) != 0 {
		t.Errorf("weight: added %v, removed %v", got, tupleTexts(d.Removed))
	}
	// A fact whose citations changed and nothing else isn't in the diff.
	if d, _ := b.Diff(bg, "node", 0, 2); len(d.Added)+len(d.Removed) != 0 {
		t.Errorf("node: %+v", d)
	}
	// The diff is the set difference of the two versions' facts, both ways, for every pair.
	for from := range ns.Version(3) {
		for to := range ns.Version(3) {
			d, _ := b.Diff(bg, "edge", from, to)
			f, g := factsAt(t, b, from), factsAt(t, b, to)
			if added := tupleTexts(d.Added); !slices.Equal(added, minus(g, f)) {
				t.Errorf("Diff(%d, %d) added %v, want %v", from, to, added, minus(g, f))
			}
			if removed := tupleTexts(d.Removed); !slices.Equal(removed, minus(f, g)) {
				t.Errorf("Diff(%d, %d) removed %v, want %v", from, to, removed, minus(f, g))
			}
		}
	}
}

func TestADiffAppliedToTheFirstVersionGivesTheSecond(t *testing.T) {
	src := versionedGraph()
	b := baseFor(std(src))
	timed, _ := src.TimedTuples(bg, "edge")
	changed := 0
	for from := range ns.Version(3) {
		var base []ns.Tuple
		for _, tt := range timed {
			if tt.Versions.Contains(from) {
				base = append(base, ns.Tuple{Vals: tt.Vals, Cites: tt.CitesAt(from)})
			}
		}
		for to := range ns.Version(3) {
			d, err := b.Diff(bg, "edge", from, to)
			if err != nil {
				t.Fatal(err)
			}
			changed += len(d.Added) + len(d.Removed)
			got := tupleTexts(d.Changes("edge").Apply("edge", base))
			slices.Sort(got)
			want := factsAt(t, b, to)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("Diff(%d, %d) applied to %d gives %v, want %v", from, to, from, got, want)
			}
		}
	}
	if changed == 0 {
		t.Fatal("control: no pair of versions differs")
	}
}

func TestDiffRefusesWhatItCannotRead(t *testing.T) {
	for _, tc := range []struct {
		b        *Base
		rel      string
		from, to ns.Version
		want     string
	}{
		{baseFor(std(graph())), "edge", 0, 1, "Diff needs a Source that serves versions"},
		{baseFor(std(versionedGraph())), "edge", 0, 7, "version 7 is not one the Source serves"},
		{baseFor(std(versionedGraph())), "egde", 0, 1, `unknown relation "egde"; did you mean "edge"?`},
	} {
		if _, err := tc.b.Diff(bg, tc.rel, tc.from, tc.to); err == nil || !strings.HasPrefix(err.Error(), "query: ") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("Diff(%s, %d, %d) = %v, want %q", tc.rel, tc.from, tc.to, err, tc.want)
		}
	}
}

// factsAt is edge's facts at v, as text with their citations, through At.
func factsAt(t *testing.T, b *Base, v ns.Version) []string {
	t.Helper()
	rows, err := Naive{}.Eval(bg, mustParse(t, `edge(?a, ?b) => ?a, ?b`), b, At(v))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, fmt.Sprintf("%s %s %v", r.Bind["a"].S, r.Bind["b"].S, r.Cites))
	}
	return out
}

func minus(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

func tupleTexts(ts []ns.Tuple) []string {
	var out []string
	for _, t := range ts {
		var vals []string
		for _, v := range t.Vals {
			vals = append(vals, v.S)
		}
		out = append(out, fmt.Sprintf("%s %v", strings.Join(vals, " "), t.Cites))
	}
	return out
}
