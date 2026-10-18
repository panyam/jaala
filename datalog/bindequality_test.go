package datalog

import (
	"slices"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// An equality to text written after the literals it filters costs what the constant written in place
// does (#370): bound first, the demanded walk starts from it. control: the closure in full costs far
// more, so the fixture can tell the two apart.
func TestAnEqualityWrittenLastCostsWhatTheConstantInPlaceDoes(t *testing.T) {
	late := leftReach + `reach(?a, ?b), ?b = "v40" => ?a`
	early := leftReach + `reach(?a, "v40") => ?a`
	lw, ew := workOf(t, SemiNaive{}, line(60), late), workOf(t, SemiNaive{}, line(60), early)
	if lw > 2*ew+20 {
		t.Errorf("written last: work %d, in place: %d", lw, ew)
	}
	if all := workOf(t, SemiNaive{}, line(60), leftReach+`reach(?a, ?b) => ?a, ?b`); all < 5*ew {
		t.Errorf("control: the whole closure costs %d, in place %d", all, ew)
	}
	src := line(60)
	want, err := both(mustParse(t, early), baseFor(std(src)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := both(mustParse(t, late), baseFor(std(src)))
	if err != nil || col(got, "a") != col(want, "a") || len(got) != 40 {
		t.Errorf("written last: %v, %v; in place: %v", col(got, "a"), err, col(want, "a"))
	}
}

// A generator behind a derived relation is called as it is with the constant in place: once, from the
// bound end, where the equality written last walked out from every node. control: the constant in place.
func TestAnEqualityWrittenLastReachesAGeneratorsMode(t *testing.T) {
	calls := func(goal string) [][2]bool {
		v := std(line(10))
		recorded := walker(t, v, [][]bool{{true, false}, {false, true}})
		if _, err := (SemiNaive{}).Eval(bg, mustParse(t, `covers(?t, ?f) :- node(?t), walk(?t, ?f); `+goal), baseFor(v)); err != nil {
			t.Fatal(err)
		}
		return *recorded
	}
	inPlace := calls(`covers(?t, "v5") => ?t`)
	if !slices.Equal(inPlace, [][2]bool{{false, true}}) {
		t.Fatalf("control: the constant in place called walk %v", inPlace)
	}
	if late := calls(`covers(?t, ?h), ?h = "v5" => ?t`); !slices.Equal(late, inPlace) {
		t.Errorf("written last, walk was called %v, want %v", late, inPlace)
	}
	if late := calls(`covers(?t, ?h), "v5" = ?h => ?t`); !slices.Equal(late, inPlace) {
		t.Errorf("written the other way round, walk was called %v, want %v", late, inPlace)
	}
}

// Binding the variable first changes no answer, citation or witness, beside a host's set-bound
// variable of the same name too, and two equalities to different text answer nothing.
func TestAnEqualityBoundFirstAnswersAsWritten(t *testing.T) {
	b := baseFor(std(graph()))
	q := mustParse(t, `edge(?a, ?b), ?b = "c" => ?a, ?b`)
	for _, opts := range [][]Option{nil, {Witnesses()}, {CanonicalCites()}} {
		rows, err := both(q, b, opts...)
		if err != nil || col(rows, "a") != "b" {
			t.Errorf("%d options: %v, %v", len(opts), col(rows, "a"), err)
		}
	}
	rows, err := both(q, b, Bind(map[Var][]ns.Value{"b": {ns.S("c"), ns.S("d")}}))
	if err != nil || col(rows, "a") != "b" {
		t.Errorf("beside a set-bound ?b: %v, %v", col(rows, "a"), err)
	}
	if rows, err := both(mustParse(t, `edge(?a, ?b), ?b = "c", ?b = "d" => ?a`), b); err != nil || len(rows) != 0 {
		t.Errorf("two equalities: %v, %v", col(rows, "a"), err)
	}
}

// Only an equality between a variable and text binds it first: a number only filters (#227), and two
// variables, or a comparison other than =, are left alone.
func TestOnlyAnEqualityToTextBindsFirst(t *testing.T) {
	for _, c := range []struct {
		goal  string
		binds bool
	}{
		{`edge(?a, ?b), ?b = "c" => ?a`, true},
		{`edge(?a, ?b), "c" = ?b => ?a`, true},
		{`weight(?n, ?w), ?w = 3 => ?n`, false},
		{`edge(?a, ?b), ?a = ?b => ?a`, false},
		{`edge(?a, ?b), ?b != "c" => ?a`, false},
	} {
		q := bindEqualities(mustParse(t, c.goal))
		first := q.Goal.Literals[0]
		got := first.Pos != nil && strings.HasPrefix(first.Pos.Relation, bindPrefix+"=")
		if got != c.binds || got != (len(q.Rules) == 1) {
			t.Errorf("%s: bound first %v with %d rules, want %v", c.goal, got, len(q.Rules), c.binds)
		}
	}
}
