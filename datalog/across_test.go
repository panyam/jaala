package datalog

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/panyam/jaala/ns"
)

// acrossPrograms counts the corpus programs Across answered with a row holding in some versions and
// not others, the control that agreeAcross's sets tell versions apart, and acrossAggregates those of
// them that aggregate (#181). acrossLookedPrograms counts those whose Across asked a lookingTimed store
// for a bound call (#317).
var acrossPrograms, acrossAggregates, acrossLookedPrograms int

// lookingTimed is a TimedMemSource that answers LookupTimed (#317), counting its lookups and its
// TimedTuples reads by relation. sloppy answers each lookup with the relation's every fact, and fail
// fails each lookup.
type lookingTimed struct {
	*ns.TimedMemSource
	mu      sync.Mutex
	lookups map[string]int
	wholes  map[string]int
	sloppy  bool
	fail    error
}

func lookingTimedAt(src *ns.TimedMemSource) *lookingTimed {
	return &lookingTimed{TimedMemSource: src, lookups: map[string]int{}, wholes: map[string]int{}}
}

func (l *lookingTimed) TimedTuples(ctx context.Context, rel string) ([]ns.TimedTuple, error) {
	l.mu.Lock()
	l.wholes[rel]++
	l.mu.Unlock()
	return l.TimedMemSource.TimedTuples(ctx, rel)
}

func (l *lookingTimed) LookupTimed(ctx context.Context, rel string, bound map[int]ns.Value) ([]ns.TimedTuple, error) {
	l.mu.Lock()
	l.lookups[rel]++
	fail := l.fail
	l.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	all, err := l.TimedMemSource.TimedTuples(ctx, rel)
	if err != nil || l.sloppy {
		return all, err
	}
	s, _ := l.Schema(rel)
	var out []ns.TimedTuple
	for _, t := range all {
		vals, _ := normalizeValues(stdUnits, t.Vals, s.Types)
		ok := true
		for i, v := range bound {
			ok = ok && valueEq(vals[i], v)
		}
		if ok {
			out = append(out, t)
		}
	}
	return out, nil
}

func (l *lookingTimed) looked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, c := range l.lookups {
		n += c
	}
	return n
}

// lookingTimedOver is a lookingTimed over timed, sloppy on every other seed and behind a timed
// Federation on every third.
func lookingTimedOver(timed *ns.TimedMemSource, seed int) (*lookingTimed, ns.Source) {
	lt := lookingTimedAt(timed)
	lt.sloppy = seed%2 == 1
	if seed%3 != 0 {
		return lt, lt
	}
	f, err := ns.NewFederation(ns.Member{Source: lt})
	if err != nil {
		panic(err)
	}
	tf, err := f.Timed()
	if err != nil {
		panic(err)
	}
	return lt, tf
}

// callsClosure reports whether q calls graph.closure (#281) or graph.paths (#280) in its goal or a rule.
func callsClosure(q Query) bool {
	bodies := []Body{q.Goal}
	for _, r := range q.Rules {
		bodies = append(bodies, r.Body)
	}
	for _, b := range bodies {
		for _, a := range ruleAtoms(b) {
			if a.Relation == closureName || a.Relation == pathsName {
				return true
			}
		}
	}
	return false
}

// agreeAcross requires every evaluator's Across answer over b's facts dealt across versions to hold,
// for each version v, exactly the rows At(v) answers, and under CanonicalCites their citations and
// witnesses at v too (#180).
func agreeAcross(q Query, b *Base, opts []Option, seed int) string {
	if callsClosure(q) {
		return "" // refused across versions (#281), which TestClosureIsRefusedAcrossVersions checks
	}
	if anyRows(q) {
		q.Limit = 0
	}
	timed, at := timedOver(b.src)
	lt, looked := lookingTimedOver(timed, seed)
	sources := map[string]ns.Source{"read from TimedTuples": timed, "looked up": looked}
	all := ns.Span(0, timedVersions)
	var wants [][]Row
	anyErr := false
	for v := range ns.Version(timedVersions) {
		w, err := Naive{}.Eval(bg, q, MustBase(b.reg, at(v)), opts...)
		wants = append(wants, w)
		anyErr = anyErr || err != nil
	}
	partial := false
	for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
		for how, src := range sources {
			got, err := ev.Eval(bg, q, MustBase(b.reg, src), append(opts[:len(opts):len(opts)], Across(all))...)
			if (err != nil) != anyErr {
				return fmt.Sprintf("%T across versions %s errs %v where a version's own answer errs %v on %v", ev, how, err, anyErr, q)
			}
			if err != nil {
				continue
			}
			for v := range ns.Version(timedVersions) {
				if g := binds(rowsAt(got, v)); !reflect.DeepEqual(g, binds(wants[v])) {
					return fmt.Sprintf("%T across versions %s holds at %d other rows than a Source holding only its facts on %v\n only v: %v\n Across: %v", ev, how, v, q, binds(wants[v]), g)
				}
			}
			for _, r := range got {
				if !ns.SameVersions(r.Versions, all) {
					partial = true
				}
			}
		}
	}
	if anyErr {
		return ""
	}
	canon := append(opts[:len(opts):len(opts)], CanonicalCites())
	for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
		for how, src := range sources {
			got, _ := ev.Eval(bg, q, MustBase(b.reg, src), append(canon, Across(all))...)
			for v := range ns.Version(timedVersions) {
				want, _ := Naive{}.Eval(bg, q, MustBase(b.reg, at(v)), canon...)
				if g := rowsAt(got, v); (len(g) > 0 || len(want) > 0) && !reflect.DeepEqual(g, want) {
					return fmt.Sprintf("%T across versions %s cites differently at %d under CanonicalCites on %v\n only v: %v\n Across: %v", ev, how, v, q, want, g)
				}
			}
		}
	}
	if lt.looked() > 0 {
		acrossLookedPrograms++
	}
	if partial {
		acrossPrograms++
		if aggregates(q) {
			acrossAggregates++
		}
	}
	return ""
}

// rowsAt are an Across answer's rows holding in v, as At(v) answers them: v's citations and witnesses,
// and no versions.
func rowsAt(rows []Row, v ns.Version) []Row {
	var out []Row
	for _, r := range rows {
		if !r.Versions.Contains(v) {
			continue
		}
		at := Row{Bind: r.Bind, Cites: r.CitesAt(v), Why: r.Why}
		for _, w := range r.Witness {
			at.Witness = append(at.Witness, w.At(v))
		}
		out = append(out, at)
	}
	return out
}

func TestAcrossCarriesTheVersionsEachRowHoldsIn(t *testing.T) {
	b := baseFor(std(versionedGraph()))
	q := mustParse(t, reachRules+`reach("a", ?x) => ?x`)
	rows, err := SemiNaive{}.Eval(bg, q, b, Across(ns.Span(0, 3)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%s %v", r.Bind["x"].S, slices.Collect(r.Versions.Ranges())))
	}
	// b holds from 0; c from 1, through b->c; d only in 0, where c->d held but b->c didn't.
	if want := []string{"b [{0 3}]", "c [{1 3}]"}; !slices.Equal(got, want) {
		t.Errorf("Across = %v, want %v", got, want)
	}
}

// A `not` holds in the versions the negated relation doesn't.
func TestAcrossNegationHoldsWhereTheNegatedRelationDoesnt(t *testing.T) {
	b := baseFor(std(versionedGraph()))
	q := mustParse(t, `edge(?a, ?b), not edge(?b, _) => ?a, ?b`)
	rows, err := SemiNaive{}.Eval(bg, q, b, Across(ns.Span(0, 3)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%s%s %v", r.Bind["a"].S, r.Bind["b"].S, slices.Collect(r.Versions.Ranges())))
	}
	// b leads on from 1 (b->c), so a->b ends at a sink only in 0; c leads on only in 0 (c->d), so b->c,
	// from 1, always does; d never leads on.
	if want := []string{"ab [{0 1}]", "bc [{1 3}]", "cd [{0 1}]"}; !slices.Equal(got, want) {
		t.Errorf("Across = %v, want %v", got, want)
	}
}

func TestAcrossRefusesWhatItCannotAnswer(t *testing.T) {
	q := mustParse(t, `edge(?a, ?b) => ?a`)
	for _, tc := range []struct {
		b    *Base
		q    Query
		opts []Option
		want string
	}{
		{baseFor(std(graph())), q, []Option{Across(ns.Span(0, 1))}, "Across needs a Source that serves versions"},
		{baseFor(std(versionedGraph())), q, []Option{Across(ns.IntervalSet{})}, "Across needs at least one version"},
		{baseFor(std(versionedGraph())), q, []Option{Across(ns.Bits(1, 4, 5, 6))}, "Across names versions the Source doesn't serve: 4-6"},
		{baseFor(std(versionedGraph())), q, []Option{Across(ns.Span(0, 3)), At(1)}, "At and Across can't both be given"},
	} {
		if _, err := both(tc.q, tc.b, tc.opts...); err == nil || !strings.HasPrefix(err.Error(), "query: ") || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want %q", tc.q, err, tc.want)
		}
	}
}

// A row's citations are its facts' in the latest version it holds in, and CitesAt gives another's.
func TestAcrossRowsCiteEachVersion(t *testing.T) {
	b := baseFor(std(versionedGraph()))
	q := mustParse(t, `node(?n), edge(?n, ?m) => ?n, ?m`)
	rows, err := SemiNaive{}.Eval(bg, q, b, Across(ns.Span(0, 3)), Witnesses())
	if err != nil || len(rows) != 1 {
		t.Fatalf("Across = %v, %v", rows, err)
	}
	r := rows[0]
	if !slices.Equal(r.Cites, []string{"edge:ab", "node:a@new"}) {
		t.Errorf("Cites = %v", r.Cites)
	}
	if got := r.CitesAt(0); !slices.Equal(got, []string{"edge:ab", "node:a@old"}) {
		t.Errorf("CitesAt(0) = %v", got)
	}
	if got := r.Witness[0].At(1).Cites; !slices.Equal(got, []string{"node:a@old"}) {
		t.Errorf("witness At(1) cites %v", got)
	}
	if got := r.Witness[0].Cites; !slices.Equal(got, []string{"node:a@new"}) {
		t.Errorf("witness cites %v", got)
	}
	if !ns.SameVersions(r.Witness[0].Versions, ns.Span(0, 3)) {
		t.Errorf("witness versions %v", slices.Collect(r.Witness[0].Versions.Ranges()))
	}
}

// A row derived differently in two versions is answered once per derivation, each with the versions
// At keeps it in. r holds through q in both versions and through p in version 1; plain evaluation keeps
// whichever rule ran first (q's, written first), and CanonicalCites the rule whose text comes first.
func TestAcrossAnswersARowOncePerDerivation(t *testing.T) {
	src := ns.NewTimedMemSource(ns.Span(0, 2))
	src.Declare("p", "x").Declare("q", "x")
	src.Add("q", ns.Since(0), []string{"q"}, ns.S("a"))
	src.Add("p", ns.Bits(1), []string{"p"}, ns.S("a"))
	b := baseFor(std(src))
	q := mustParse(t, `r(?x) :- q(?x); r(?x) :- p(?x); r(?x) => ?x`)
	for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
		for _, canonical := range []bool{true, false} {
			opts := []Option{Across(ns.Span(0, 2)), Witnesses()}
			want := []string{"[{0 1}] [q]", "[{1 2}] [p]"}
			if canonical {
				opts = append(opts, CanonicalCites())
			} else {
				want = []string{"[{0 2}] [q]"} // control: the fixture tells the two apart
			}
			rows, err := ev.Eval(bg, q, b, opts...)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, r := range rows {
				got = append(got, fmt.Sprintf("%v %v", slices.Collect(r.Versions.Ranges()), r.Cites))
			}
			if !slices.Equal(got, want) {
				t.Errorf("%T canonical %v: %v, want %v", ev, canonical, got, want)
			}
		}
	}
}

// A limit and offset page each version's rows: the second row of one version can be another's first.
func TestAcrossPagesEachVersion(t *testing.T) {
	b := baseFor(std(versionedGraph()))
	q := mustParse(t, `edge(?a, ?b) => ?a order by ?a limit 1 offset 1`)
	rows, err := both(q, b, Across(ns.Span(0, 3)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%s %v", r.Bind["a"].S, slices.Collect(r.Versions.Ranges())))
	}
	// Version 0 holds a->b and c->d, the later two b->c too.
	if want := []string{"b [{1 3}]", "c [{0 1}]"}; !slices.Equal(got, want) {
		t.Errorf("Across = %v, want %v", got, want)
	}
}

// Where two derivations of a tuple spell its number differently, a version keeps the better spelling of
// those holding there (#201), so a row spelled one way in some versions is another row in the rest.
func TestAcrossKeepsEachVersionsSpelling(t *testing.T) {
	src := ns.NewTimedMemSource(ns.Span(0, 2))
	src.DeclareSchema("n", ns.Schema{Arity: 2, Labels: []string{"x", "v"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
	src.DeclareSchema("m", ns.Schema{Arity: 2, Labels: []string{"x", "v"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
	src.Add("n", ns.Since(0), nil, ns.S("a"), ns.N(5))
	src.Add("m", ns.Bits(1), nil, ns.S("a"), ns.NU(5, "V"))
	b := baseFor(std(src))
	q := mustParse(t, `r(?x, ?v) :- n(?x, ?v); r(?x, ?v) :- m(?x, ?v); s(?x, ?v) :- r(?x, ?v), n(?x, _); s(?x, ?v) => ?x, ?v`)
	for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
		rows, err := ev.Eval(bg, q, b, Across(ns.Span(0, 2)))
		if err != nil {
			t.Fatal(err)
		}
		for v := range ns.Version(2) {
			want, _ := ev.Eval(bg, q, b, At(v))
			if got := binds(rowsAt(rows, v)); !reflect.DeepEqual(got, binds(want)) {
				t.Errorf("%T at %d: Across %v, At %v", ev, v, got, binds(want))
			}
		}
		if len(rows) != 2 {
			t.Errorf("control: %T answered %d rows, want one per spelling", ev, len(rows))
		}
	}
}

// aggregates reports whether q reduces anything, in its goal, a rule head or a rule body.
func aggregates(q Query) bool {
	if hasAggregate(q.Select) || len(q.Having) > 0 {
		return true
	}
	for _, r := range q.Rules {
		if r.aggregates() || strings.Contains(r.String(), ": {") {
			return true
		}
	}
	return false
}

// An aggregate across versions is cut where its inputs change: a count per version, segments that
// reduce alike joined, and the versions nothing holds in answered as over nothing.
func TestAcrossAggregatesPerSegment(t *testing.T) {
	src := ns.NewTimedMemSource(ns.Span(0, 5))
	src.Declare("pin", "part", "net")
	src.Add("pin", ns.Span(1, 5), nil, ns.S("R1"), ns.S("GND"))
	src.Add("pin", ns.Span(2, 4), nil, ns.S("R2"), ns.S("GND"))
	src.Add("pin", ns.Span(3, 5), nil, ns.S("C1"), ns.S("GND"))
	src.Add("pin", ns.Span(1, 3), nil, ns.S("C1"), ns.S("VCC"))
	b := baseFor(std(src))
	for _, tc := range []struct {
		q    string
		want []string
	}{
		// GND: 1 part in 1, 2 in 2, 3 in 3, 2 in 4. Nothing in 0, so no GND row there.
		{`pin(?p, ?n) => ?n, count(?p)`, []string{"GND 1 [{1 2}]", "GND 2 [{2 3} {4 5}]", "GND 3 [{3 4}]", "VCC 1 [{1 3}]"}},
		// With no group column, version 0 counts nothing, as At(0) answers.
		{`pin(?p, "GND") => count(?p)`, []string{"0 [{0 1}]", "1 [{1 2}]", "2 [{2 3} {4 5}]", "3 [{3 4}]"}},
		// Distinct counts keep their own seen values per segment: C1 counts once in 3, where it holds twice.
		{`pin(?p, _) => count(distinct ?p)`, []string{"0 [{0 1}]", "2 [{1 2} {4 5}]", "3 [{2 4}]"}},
		// A list keeps its own values per segment too.
		{`pin(?p, "GND") => list(?p)`, []string{`["C1","R1","R2"] [{3 4}]`, `["C1","R1"] [{4 5}]`, `["R1","R2"] [{2 3}]`, `["R1"] [{1 2}]`, "[] [{0 1}]"}},
		{`n(?n, count(?p)) :- pin(?p, ?n); n(?n, ?c), ?c > 1 => ?n, ?c`, []string{"GND 2 [{2 3} {4 5}]", "GND 3 [{3 4}]"}},
		{`m(?n, ?c) :- pin(_, ?n), ?c = count(?p) : { pin(?p, ?n) }; m(?n, ?c), ?c > 1 => ?n, ?c`, []string{"GND 2 [{2 3} {4 5}]", "GND 3 [{3 4}]"}},
	} {
		q := mustParse(t, tc.q)
		for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
			rows, err := ev.Eval(bg, q, b, Across(ns.Span(0, 5)))
			if err != nil {
				t.Fatalf("%s: %v", tc.q, err)
			}
			var got []string
			for _, r := range rows {
				var vals []string
				for _, c := range q.Select {
					vals = append(vals, r.Bind[colLabel(c)].S)
				}
				got = append(got, fmt.Sprintf("%s %v", strings.Join(vals, " "), slices.Collect(r.Versions.Ranges())))
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("%T %s:\n got %v\nwant %v", ev, tc.q, got, tc.want)
			}
			for v := range ns.Version(5) {
				want, _ := Naive{}.Eval(bg, q, b, At(v))
				if g := binds(rowsAt(rows, v)); !reflect.DeepEqual(g, binds(want)) {
					t.Errorf("%s at %d: Across %v, At %v", tc.q, v, g, binds(want))
				}
			}
		}
	}
}

// A segment split off keeps its own list: three values held in every version, then one each side of a
// split, each appended where the other's would share spare room.
func TestAcrossSplitsAListApart(t *testing.T) {
	src := ns.NewTimedMemSource(ns.Span(0, 2))
	src.Declare("v", "x")
	for _, x := range []string{"a", "b", "c"} {
		src.Add("v", ns.Since(0), nil, ns.S(x))
	}
	src.Add("v", ns.Bits(0), nil, ns.S("d"))
	src.Add("v", ns.Bits(1), nil, ns.S("e"))
	b := baseFor(std(src))
	rows, err := Naive{}.Eval(bg, mustParse(t, `v(?x) => list(?x)`), b, Across(ns.Span(0, 2)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%s %v", r.Bind["list(x)"].S, slices.Collect(r.Versions.Ranges())))
	}
	if want := []string{`["a","b","c","d"] [{0 1}]`, `["a","b","c","e"] [{1 2}]`}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// A segment split off keeps its own distinct values: 5 counted in version 0 is still new in version 1.
func TestAcrossSplitsADistinctSumApart(t *testing.T) {
	src := ns.NewTimedMemSource(ns.Span(0, 2))
	src.DeclareSchema("w", ns.Schema{Arity: 2, Labels: []string{"x", "w"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
	for i, w := range []float64{1, 2, 3} {
		src.Add("w", ns.Since(0), nil, ns.S(fmt.Sprint(i)), ns.N(w))
	}
	src.Add("w", ns.Bits(0), nil, ns.S("d"), ns.N(5))
	src.Add("w", ns.Bits(1), nil, ns.S("e"), ns.N(5))
	b := baseFor(std(src))
	rows, err := Naive{}.Eval(bg, mustParse(t, `w(_, ?w) => sum(distinct ?w)`), b, Across(ns.Span(0, 2)))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range rows {
		got = append(got, fmt.Sprintf("%s %v", r.Bind["sum(distinct w)"].S, slices.Collect(r.Versions.Ranges())))
	}
	if want := []string{"11 [{0 2}]"}; !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// Across over a TimedLookupSource asks it for what a query binds rather than reading TimedTuples whole
// (#317), and holds the rows in each version, with their citations and witnesses, that Across reading
// TimedTuples does. s's second rule finds its lookup kept by the first, so a kept lookup holds its
// versions too: c->d holds only in 0. control: hiding LookupTimed reads TimedTuples and looks nothing up.
func TestAcrossLooksUpATimedLookupSource(t *testing.T) {
	all := ns.Span(0, 3)
	for _, text := range []string{
		reachRules + `reach("a", ?x) => ?x`,
		`edge("a", ?m), node(?n), edge(?n, ?m) => ?n, ?m`,
		`s(?y) :- edge("c", ?y); s(?y) :- node(?n), edge("c", ?y); s(?y) => ?y`,
	} {
		q := mustParse(t, text)
		for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
			for _, opts := range [][]Option{nil, {Witnesses()}, {CanonicalCites()}} {
				want, err := ev.Eval(bg, q, baseFor(std(versionedGraph())), append(opts, Across(all))...)
				if err != nil || len(want) == 0 {
					t.Fatalf("%T reading TimedTuples: %v %v", ev, want, err)
				}
				lt := lookingTimedAt(versionedGraph())
				got, err := ev.Eval(bg, q, baseFor(std(lt)), append(opts, Across(all))...)
				if err != nil {
					t.Fatal(err)
				}
				for v := range ns.Version(3) {
					if g, w := rowsAt(got, v), rowsAt(want, v); !reflect.DeepEqual(g, w) {
						t.Errorf("%T %v with %d options at %d: looked up %v, read whole %v", ev, q, len(opts), v, g, w)
					}
				}
				if ev == (SemiNaive{}) && (lt.looked() == 0 || lt.wholes["edge"] != 0) {
					t.Errorf("%v: the planned SemiNaive looked up %d times and read edge's TimedTuples %d times", q, lt.looked(), lt.wholes["edge"])
				}
			}
		}
		hidden := lookingTimedAt(versionedGraph())
		if _, err := (SemiNaive{}).Eval(bg, q, baseFor(std(timedOnly{hidden})), Across(all)); err != nil || hidden.looked() != 0 || hidden.wholes["edge"] == 0 {
			t.Errorf("control: without LookupTimed, looked up %d times and read edge whole %d: %v", hidden.looked(), hidden.wholes["edge"], err)
		}
	}
}

// A failed LookupTimed fails the Across Eval, wrapped with the relation's name, and isn't kept: the
// next Eval asks again.
func TestAFailedTimedLookupFailsAcrossAndIsntKept(t *testing.T) {
	lt := lookingTimedAt(versionedGraph())
	lt.fail = errors.New("store down")
	b := baseFor(std(lt))
	q := mustParse(t, `edge("a", ?x) => ?x`)
	_, err := SemiNaive{}.Eval(bg, q, b, Across(ns.Span(0, 3)))
	if err == nil || !strings.HasPrefix(err.Error(), "query: ") || !strings.Contains(err.Error(), "edge") || !errors.Is(err, lt.fail) {
		t.Fatalf("a failing lookup: %v", err)
	}
	lt.fail = nil
	rows, err := SemiNaive{}.Eval(bg, q, b, Across(ns.Span(0, 3)))
	if err != nil || col(rows, "x") != "b" || lt.looked() != 2 {
		t.Errorf("after the store came back: %v %v, looked up %d times, want b and a second lookup", col(rows, "x"), err, lt.looked())
	}
}

// A derived witness node holds in the versions its copy of the tuple does (#317): r(a, 5) is derived
// from n in both versions and kept as 5 V from m in 1, so the copy spelled 5 holds in 0 alone, and its
// node says so rather than the versions it was first derived for.
func TestAcrossWitnessHoldsInItsCopysVersions(t *testing.T) {
	src := ns.NewTimedMemSource(ns.Span(0, 2))
	src.DeclareSchema("n", ns.Schema{Arity: 2, Labels: []string{"x", "v"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
	src.DeclareSchema("m", ns.Schema{Arity: 2, Labels: []string{"x", "v"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}})
	src.Add("n", ns.Since(0), nil, ns.S("a"), ns.N(5))
	src.Add("m", ns.Bits(1), nil, ns.S("a"), ns.NU(5, "V"))
	q := mustParse(t, `r(?x, ?v) :- n(?x, ?v); r(?x, ?v) :- m(?x, ?v); r(?x, ?v), n(?x, _) => ?x, ?v`)
	for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
		rows, err := ev.Eval(bg, q, baseFor(std(src)), Across(ns.Span(0, 2)), Witnesses())
		if err != nil || len(rows) != 2 {
			t.Fatalf("%T: %d rows, %v, want one per spelling", ev, len(rows), err)
		}
		for _, r := range rows {
			if w := r.Witness[0]; !ns.SameVersions(w.Versions, r.Versions) {
				t.Errorf("%T: the row %v holding in %v has r's node holding in %v", ev, r.Bind["v"], slices.Collect(r.Versions.Ranges()), slices.Collect(w.Versions.Ranges()))
			}
		}
	}
}
