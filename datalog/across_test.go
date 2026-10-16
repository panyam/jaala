package datalog

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// acrossPrograms counts the corpus programs Across answered with a row holding in some versions and
// not others, the control that agreeAcross's sets tell versions apart; acrossRefused those it refused
// for aggregating.
var acrossPrograms, acrossRefused int

// agreeAcross requires every evaluator's Across answer over b's facts dealt across versions to hold,
// for each version v, exactly the rows At(v) answers, and under CanonicalCites their citations and
// witnesses at v too (#180).
func agreeAcross(q Query, b *Base, opts []Option) string {
	if anyRows(q) {
		q.Limit = 0
	}
	timed, at := timedOver(b.src)
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
		got, err := ev.Eval(bg, q, MustBase(b.reg, timed), append(opts[:len(opts):len(opts)], Across(all))...)
		if err != nil && strings.Contains(err.Error(), "Across can't evaluate an aggregate") {
			acrossRefused++
			return ""
		}
		if (err != nil) != anyErr {
			return fmt.Sprintf("%T across versions errs %v where a version's own answer errs %v on %v", ev, err, anyErr, q)
		}
		if err != nil {
			continue
		}
		for v := range ns.Version(timedVersions) {
			if g := binds(rowsAt(got, v)); !reflect.DeepEqual(g, binds(wants[v])) {
				return fmt.Sprintf("%T across versions holds at %d other rows than a Source holding only its facts on %v\n only v: %v\n Across: %v", ev, v, q, binds(wants[v]), g)
			}
		}
		for _, r := range got {
			if !ns.SameVersions(r.Versions, all) {
				partial = true
			}
		}
	}
	if anyErr {
		return ""
	}
	canon := append(opts[:len(opts):len(opts)], CanonicalCites())
	for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}, SemiNaive{}} {
		got, _ := ev.Eval(bg, q, MustBase(b.reg, timed), append(canon, Across(all))...)
		for v := range ns.Version(timedVersions) {
			want, _ := Naive{}.Eval(bg, q, MustBase(b.reg, at(v)), canon...)
			if g := rowsAt(got, v); (len(g) > 0 || len(want) > 0) && !reflect.DeepEqual(g, want) {
				return fmt.Sprintf("%T across versions cites differently at %d under CanonicalCites on %v\n only v: %v\n Across: %v", ev, v, q, want, g)
			}
		}
	}
	if partial {
		acrossPrograms++
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
		{baseFor(std(versionedGraph())), mustParse(t, `edge(?a, ?b) => count(?a)`), []Option{Across(ns.Span(0, 3))}, "Across can't evaluate an aggregate yet"},
		{baseFor(std(versionedGraph())), mustParse(t, `n(?a, ?c) :- edge(?a, _), ?c = count(?b) : { edge(?a, ?b) }; n(?a, ?c) => ?a, ?c`), []Option{Across(ns.Span(0, 3))}, "Across can't evaluate an aggregate yet"},
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
