package datalog

import (
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// rated is three parts' ratings, some left out: R1 states both, R2 only its typical, C1 neither.
func rated() *ns.MemSource {
	src := ns.NewMemSource().Declare("rating", "ref", "max", "typ")
	for _, r := range []struct {
		ref      string
		max, typ ns.Value
	}{{"R1", ns.N(5), ns.N(3)}, {"R2", ns.Absent(), ns.N(2)}, {"C1", ns.Absent(), ns.Absent()}} {
		src.Add("rating", ns.Tuple{Vals: []ns.Value{ns.S(r.ref), r.max, r.typ}})
	}
	return src
}

func TestCoalesceFallsBackFromAnAbsentValue(t *testing.T) {
	for _, c := range []struct {
		query string
		cols  []string
		want  []string
	}{
		{`rating(?r, ?max, ?typ), coalesce(?max, ?typ, ?v) => ?r, ?v`, []string{"r", "v"}, []string{"C1 -", "R1 5", "R2 2"}},
		{`rating(?r, ?max, ?typ), ?v = coalesce(?max, coalesce(?typ, 0)) => ?r, ?v`, []string{"r", "v"}, []string{"C1 0", "R1 5", "R2 2"}},
		{`rating(?r, ?max, _), ?v = coalesce(?max, "unrated") => ?r, ?v`, []string{"r", "v"}, []string{"C1 unrated", "R1 5", "R2 unrated"}},
		// A min over nothing is absent, and coalesce gives it a value a comparison can read.
		{`lo(min(?m)) :- rating(_, ?m, _), ?m > 9; lo(?l), ?v = coalesce(?l, 9) => ?v`, []string{"v"}, []string{"9"}},
		// control: with the value present coalesce passes it through, unchanged.
		{`lo(min(?m)) :- rating(_, ?m, _), ?m > 1; lo(?l), ?v = coalesce(?l, 9) => ?v`, []string{"v"}, []string{"5"}},
	} {
		if got := withUnits(eval(t, rated(), c.query), c.cols...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %v, want %v", c.query, got, c.want)
		}
	}
}

func TestCoalesceNeedsBothInputsBound(t *testing.T) {
	err := evalErr(rated(), `rating(?r, _, ?typ), coalesce(?max, ?typ, ?v) => ?r, ?v`)
	if err == nil || !strings.Contains(err.Error(), "coalesce") {
		t.Errorf("an unbound value = %v, want coalesce refused", err)
	}
	b, _ := std(nil).Predicate("coalesce")
	err = b.Gen(bg, nil, []ns.Arg{{}, {Value: ns.N(1), Bound: true}, {}}, func([]ns.Value, []string) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "query: coalesce needs its value and its fallback bound") {
		t.Errorf("called unbound = %v, want it to say what must be bound", err)
	}
}
