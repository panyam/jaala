package datalog

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// factored reports whether the rewrite factored a goal call of text (see factor).
func factored(t *testing.T, b *Base, text string) bool {
	t.Helper()
	for _, r := range magic(b, mustParse(t, text)).Rules {
		if strings.Contains(r.Head.Relation, fromSep) {
			return true
		}
	}
	return false
}

// Asked about one start, right-linear recursion is factored into the set of nodes the start reaches,
// so its work grows with the chain rather than with the chain's square. The control asks the same
// question with the start bound through a variable a relation binds, which plain magic sets serve. An
// equality to text no longer serves (#370): bound first, its value is factored as a constant is.
func TestFactoringMakesRightLinearRecursionLinear(t *testing.T) {
	q := rightReach + `reach("v0", ?x) => ?x`
	ratio := float64(workOf(t, SemiNaive{}, line(400), q)) / float64(workOf(t, SemiNaive{}, line(200), q))
	if ratio > 2.5 {
		t.Errorf("factored closure work grew %.1fx when the chain doubled, want about 2x", ratio)
	}
	ctl := rightReach + `edge(?s, "v1"), reach(?s, ?x) => ?x`
	if factored(t, baseFor(std(line(4))), ctl) {
		t.Fatal("control: a start bound through a variable was factored")
	}
	if full := float64(workOf(t, SemiNaive{}, line(400), ctl)) / float64(workOf(t, SemiNaive{}, line(200), ctl)); full < 3.5 {
		t.Errorf("control: plain magic sets grew only %.1fx, so the chain does not show the quadratic case", full)
	}
}

// The shapes factoring needs, and the ones it must leave to plain magic sets.
func TestFactoringAppliesOnlyToRightLinearRecursionFromConstants(t *testing.T) {
	cases := []struct {
		name, text string
		want       bool
	}{
		{"right-linear from a constant", rightReach + `reach("v0", ?x) => ?x`, true},
		{"two free arguments", `p(?a, ?k, ?b) :- edge(?a, ?b), weight(?a, ?k); p(?a, ?k, ?c) :- edge(?a, ?b), p(?b, ?k, ?c); p("v1", ?k, ?x) => ?k, ?x`, true},
		{"a filter on the way down", `r(?a, ?b) :- edge(?a, ?b); r(?a, ?c) :- edge(?a, ?b), weight(?b, ?w), ?w > 3, r(?b, ?c); r("v0", ?x) => ?x`, true},
		{"start bound through a variable", rightReach + `node(?s), reach(?s, ?x) => ?s, ?x`, false},
		{"left-linear", leftReach + `reach("v0", ?x) => ?x`, false},
		{"answer read on the way down", `r(?a, ?b) :- edge(?a, ?b); r(?a, ?c) :- edge(?a, ?b), r(?b, ?c), node(?c); r("v0", ?x) => ?x`, false},
		{"answer changed on the way up", `r(?a, ?b) :- edge(?a, ?b); r(?a, ?c) :- edge(?a, ?b), r(?b, ?d), edge(?d, ?c); r("v0", ?x) => ?x`, false},
		{"answers swapped on the way up", `p(?a, ?x, ?y) :- edge(?a, ?x), weight(?a, ?y); p(?a, ?x, ?y) :- edge(?a, ?b), p(?b, ?y, ?x); p("v0", ?x, ?y) => ?x, ?y`, false},
		{"answer repeated in the head", `r(?a, ?b, ?b) :- edge(?a, ?b); r(?a, ?c, ?c) :- edge(?a, ?b), r(?b, ?c, ?c); r("v0", ?x, ?y) => ?x, ?y`, false},
		{"start of the recursive call unbound", `r(?a, ?b) :- edge(?a, ?b); r(?a, ?c) :- node(?a), r(?b, ?c); r("v0", ?x) => ?x`, false},
		{"two recursive calls", `reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), reach(?b, ?c); reach("v0", ?x) => ?x`, false},
		{"mutual recursion", `p(?a, ?b) :- edge(?a, ?b); p(?a, ?c) :- edge(?a, ?b), q(?b, ?c); q(?a, ?c) :- p(?a, ?c); p("v0", ?x) => ?x`, false},
		{"recursion under a negation", rightReach + `far(?a, ?b) :- reach(?a, ?b), not edge(?a, ?b); reach("v0", ?x), far("v0", ?x) => ?x`, true},
	}
	for _, c := range cases {
		if got := factored(t, baseFor(std(randomGraph(1, 6, 0.3))), c.text); got != c.want {
			t.Errorf("%s: factored = %v, want %v", c.name, got, c.want)
		}
		want, err := both(mustParse(t, c.text), baseFor(std(randomGraph(1, 8, 0.25))))
		if err != nil || len(want) == 0 {
			t.Errorf("%s: %d rows, %v; want some, agreeing across evaluators", c.name, len(want), err)
		}
	}
	witnessed := baseFor(std(line(4)))
	witnessed.run = &evalRun{witness: true}
	if factored(t, witnessed, rightReach+`reach("v0", ?x) => ?x`) {
		t.Error("a witnessed Eval was factored, so its witnesses would not mirror the rules as written")
	}
}

// The reachable set is an ordinary derived relation, not a magic one, so an answer cites the facts of
// the whole path to it, as Naive's does.
func TestAFactoredAnswerCitesItsPath(t *testing.T) {
	src := ns.NewMemSource().Declare("edge", "from", "to")
	for i := 0; i < 4; i++ {
		src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("v%d", i)), ns.S(fmt.Sprintf("v%d", i+1))}, Cites: []string{fmt.Sprintf("e%d", i)}})
	}
	text := rightReach + `reach("v0", ?x) => ?x`
	if !factored(t, baseFor(std(src)), text) {
		t.Fatal("control: the call was not factored, so this test proves nothing")
	}
	rows, err := SemiNaive{}.Eval(bg, mustParse(t, text), baseFor(std(src)))
	if err != nil || len(rows) != 4 {
		t.Fatalf("rows = %v, %v", rows, err)
	}
	for _, r := range rows {
		var n int
		fmt.Sscanf(r.Bind["x"].S, "v%d", &n)
		var want []string
		for i := 0; i < n; i++ {
			want = append(want, fmt.Sprintf("e%d", i))
		}
		cites := append([]string(nil), r.Cites...)
		sort.Strings(cites)
		if !reflect.DeepEqual(cites, want) {
			t.Errorf("reach(v0, %s) cites %v, want %v", r.Bind["x"].S, cites, want)
		}
	}
}

// Two factored calls in one goal each get their own set.
func TestTwoFactoredCallsKeepTheirOwnSets(t *testing.T) {
	text := rightReach + `reach("v0", ?x), reach("v3", ?y) => ?x, ?y`
	q := magic(baseFor(std(line(6))), mustParse(t, text))
	sets := map[string]bool{}
	for _, r := range q.Rules {
		if strings.Contains(r.Head.Relation, fromSep) {
			sets[r.Head.Relation] = true
		}
	}
	if len(sets) != 2 {
		t.Fatalf("%d sets, want one per call", len(sets))
	}
	if got := len(eval(t, line(6), text)); got != 5*2 {
		t.Errorf("%d rows, want 10: v1..v5 from v0, each with v4..v5 from v3", got)
	}
}
