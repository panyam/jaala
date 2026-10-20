package datalog

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// An equality of a variable nothing else binds with a constant binds it (#457), either way round, in
// the goal and in a rule, a number reading as one through arithmetic, and a constant projection column
// is the same value on every row, under an aggregate and a page too. A selected constant groups an
// aggregate, as Soufflé reads it: over nothing there is no row, where a count with no column answers
// 0. Two constants for one variable that differ answer nothing. control: != over an unbound variable
// is still refused, and an equality over a variable an atom binds still only filters it.
func TestAnEqualityToAConstantBindsItsVariable(t *testing.T) {
	for _, c := range []struct{ q, col, want string }{
		{`edge(?x, ?y), ?k = "tag" => ?x, ?k`, "k", "tag,tag,tag"},
		{`edge(?x, ?y), "tag" = ?k => ?x, ?k`, "k", "tag,tag,tag"},
		{`r(?x, ?k) :- edge(?x, _), ?k = "tag"; r(?x, ?t) => ?x, ?t`, "t", "tag,tag,tag"},
		{`r(?x, ?k) :- edge(?x, _), ?k = "tag"; r(?x, "tag") => ?x`, "x", "a,b,c"},
		{`edge(?x, ?y), ?k = 3, ?z = ?k + 1 => ?x, ?z`, "z", "4,4,4"},
		{`edge(?x, ?y), ?k = "a", ?k = "b" => ?x`, "x", ""},
		{`edge(?x, ?y), ?k = "a", ?k = "a" => ?x`, "x", "a,b,c"},
		{`?k = "tag" => ?k`, "k", "tag"},
		{`edge(?x, ?y), ?x = "b" => ?y`, "y", "c"},
		{`edge(?x, ?y) => ?x, "tag"`, `"tag"`, "tag,tag,tag"},
		{`edge(?x, ?y) => 3, count(?y)`, "count(y)", "3"},
		{`edge(?x, ?y) => ?x, "tag" order by ?x desc limit 1`, "x", "c"},
		{`edge("z", ?y), ?k = "tag" => ?k, count(?y)`, "k", ""},
		{`edge("z", ?y), ?k = "tag" => count(?y)`, "count(y)", "0"},
		{`edge("z", ?y) => "tag", count(?y)`, "count(y)", ""},
	} {
		if got := col(eval(t, graph(), c.q), c.col); got != c.want {
			t.Errorf("%s: %s = %q, want %q", c.q, c.col, got, c.want)
		}
	}
	for _, c := range []struct{ q, want string }{
		{`edge(?x, ?y), ?k != "tag" => ?x`, "comparison operand is unbound"},
		{`edge(?x, ?y) => ?x order by "x"`, `order by: projection column "\"x\"" must be a ?variable or an aggregate`},
	} {
		if err := evalErr(graph(), c.q); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error containing %q", c.q, err, c.want)
		}
	}
}

// A module's rule binds by an equality as the goal's rules do, and Validate takes what Eval answers.
func TestAModuleRuleAndValidateTakeAConstantEquality(t *testing.T) {
	r := withModules(t, "tags", `tagged(?x, ?k) :- edge(?x, _), ?k = "t";`)
	if got := col(evalReg(t, r, `tags.tagged(?x, ?k) => ?x, ?k`), "k"); got != "t,t,t" {
		t.Errorf("tags.tagged: k = %q, want t,t,t", got)
	}
	for _, q := range []string{`edge(?x, ?y), ?k = "tag" => ?x, ?k`, `edge(?x, ?y) => ?x, "tag"`} {
		if err := Validate(mustParse(t, q), std(graph())); err != nil {
			t.Errorf("Validate(%s) = %v", q, err)
		}
	}
}

// Across versions a selected constant groups too: the versions no binding holds in get no row.
// control: with no column, those versions count 0.
func TestAcrossAConstantColumnGroupsItsAggregate(t *testing.T) {
	for _, c := range []struct{ q, want string }{
		{`edge("c", ?y), ?k = "tag" => ?k, count(?y)`, "1 [{0 1}]"},
		{`edge("c", ?y) => count(?y)`, "0 [{1 3}],1 [{0 1}]"},
	} {
		rows, err := SemiNaive{}.Eval(bg, mustParse(t, c.q), baseFor(std(versionedGraph())), Across(ns.Span(0, 3)))
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range rows {
			got = append(got, fmt.Sprintf("%s %v", r.Bind["count(y)"].S, slices.Collect(r.Versions.Ranges())))
		}
		if s := strings.Join(got, ","); s != c.want {
			t.Errorf("%s across = %s, want %s", c.q, s, c.want)
		}
	}
}
