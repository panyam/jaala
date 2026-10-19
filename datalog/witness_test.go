package datalog

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// render draws a witness as one line per node, indented by depth, so a test compares a whole tree.
func render(ws []*Witness) string {
	var b strings.Builder
	var walk func(w *Witness, depth int)
	walk = func(w *Witness, depth int) {
		b.WriteString(strings.Repeat("  ", depth))
		if w.Negated {
			b.WriteString("not ")
		}
		vals := make([]string, len(w.Values))
		for i, v := range w.Values {
			vals[i] = v.S
		}
		b.WriteString(w.Relation + "(" + strings.Join(vals, ",") + ")")
		if w.Rule != "" {
			b.WriteString("  by " + w.Rule)
		}
		if len(w.Cites) > 0 {
			b.WriteString("  [" + strings.Join(w.Cites, " ") + "]")
		}
		b.WriteString("\n")
		for _, c := range w.Children {
			walk(c, depth+1)
		}
	}
	for _, w := range ws {
		walk(w, 0)
	}
	return b.String()
}

func witnessOf(t *testing.T, ev Evaluator, src ns.Source, text string, row map[Var]string) string {
	t.Helper()
	rows, err := ev.Eval(bg, mustParse(t, text), baseFor(std(src)), Witnesses())
	if err != nil {
		t.Fatalf("%T %s: %v", ev, text, err)
	}
	for _, r := range rows {
		match := true
		for v, s := range row {
			match = match && r.Bind[v].S == s
		}
		if match {
			return render(r.Witness)
		}
	}
	t.Fatalf("%T %s: no row %v in %v", ev, text, row, rows)
	return ""
}

// A derived answer explains itself through its rule, with the body's facts in the order the rule is
// written, though the planner runs this body edge first.
func TestAWitnessFollowsTheRuleAsWritten(t *testing.T) {
	text := `link(?y) :- node(?x), node(?y), edge(?x, ?y), ?x = "b"; link(?y), weight(?y, ?w) => ?y`
	want := `link(c)  by link(?y) :- node(?x), node(?y), edge(?x, ?y), ?x = "b"
  node(b)  [node:b]
  node(c)  [node:c]
  edge(b,c)  [edge:bc]
weight(c,3)
`
	for _, ev := range evaluators {
		if got := witnessOf(t, ev, graph(), text, map[Var]string{"y": "c"}); got != want {
			t.Errorf("%T:\n%s\nwant\n%s", ev, got, want)
		}
	}
	if planned := plan(baseFor(std(graph())), mustParse(t, text)).Rules[0].Body.String(); planned == mustParse(t, text).Rules[0].Body.String() {
		t.Errorf("control: the planner runs this body as written (%s), so written order proves nothing", planned)
	}
}

// A recursive answer under demand (magic sets) explains itself as written: no magic guard, no adorned
// names, and the same tree from every evaluator.
func TestARecursiveWitnessHidesTheRewrites(t *testing.T) {
	text := leftReach + `reach("a", ?x) => ?x`
	want := `reach(a,d)  by reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c)
  reach(a,c)  by reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c)
    reach(a,b)  by reach(?a, ?b) :- edge(?a, ?b)
      edge(a,b)  [edge:ab]
    edge(b,c)  [edge:bc]
  edge(c,d)  [edge:cd]
`
	for _, ev := range evaluators {
		if got := witnessOf(t, ev, graph(), text, map[Var]string{"x": "d"}); got != want {
			t.Errorf("%T:\n%s\nwant\n%s", ev, got, want)
		}
	}
}

// A generator's citations keep the order it gave them: a walk's steps stay in walk order.
func TestAGeneratorsCitationsKeepTheirOrder(t *testing.T) {
	v := std(graph())
	if err := v.AddPredicate("trace", ns.Builtin{Arity: 2, Modes: [][]bool{{true, false}}, Gen: func(_ context.Context, _ ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
		return emit([]ns.Value{args[0].Value, ns.S("d")}, []string{"step9", "step1", "step5"})
	}}); err != nil {
		t.Fatal(err)
	}
	rows, err := SemiNaive{}.Eval(bg, mustParse(t, `trace("a", ?z)`), baseFor(v), Witnesses())
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows = %v, %v", rows, err)
	}
	if got := rows[0].Witness[0].Cites; !reflect.DeepEqual(got, []string{"step9", "step1", "step5"}) {
		t.Errorf("witness cites = %v, want the order the generator gave", got)
	}
	if got := rows[0].Cites; !reflect.DeepEqual(got, []string{"step1", "step5", "step9"}) {
		t.Errorf("control: row cites = %v, want the sorted set, which is what the witness must not be", got)
	}
}

// A `not` that held is part of why an answer holds; a filter is too.
func TestNegationsAndFiltersAppearInTheWitness(t *testing.T) {
	want := `node(d)  [node:d]
not edge(d,)
str.prefix(d,d)
`
	got := witnessOf(t, SemiNaive{}, graph(), `node(?n), not edge(?n, _), str.prefix(?n, "d") => ?n`, map[Var]string{"n": "d"})
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// A relation SemiNaive would inline stays its own node when witnesses are asked for.
func TestAWitnessedEvalKeepsInlinableRelations(t *testing.T) {
	text := `has(?n) :- edge(?x, ?n); has(?n) => ?n`
	want := `has(c)  by has(?n) :- edge(?x, ?n)
  edge(b,c)  [edge:bc]
`
	if got := witnessOf(t, SemiNaive{}, graph(), text, map[Var]string{"n": "c"}); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

// Witnesses are only recorded when asked for, an aggregate row has none, and asking for them changes
// no answer.
func TestWitnessesAreOptInAndChangeNoAnswer(t *testing.T) {
	for _, text := range append([]string{`node(?n), not edge(?n, _) => count(?n)`}, recursivePrograms...) {
		for _, ev := range evaluators {
			plain, err1 := ev.Eval(bg, mustParse(t, text), baseFor(std(graph())))
			witnessed, err2 := ev.Eval(bg, mustParse(t, text), baseFor(std(graph())), Witnesses())
			if err1 != nil || err2 != nil || !reflect.DeepEqual(binds(plain), binds(witnessed)) {
				t.Errorf("%T %s: rows differ with witnesses (%v / %v)", ev, text, err1, err2)
			}
			for _, r := range plain {
				if r.Witness != nil {
					t.Errorf("%T %s: a witness without Witnesses()", ev, text)
					break
				}
			}
			agg := hasAggregate(mustParse(t, text).Select)
			for _, r := range witnessed {
				if agg != (r.Witness == nil) {
					t.Errorf("%T %s: witness %v on a row of an aggregate=%v query", ev, text, r.Witness, agg)
					break
				}
			}
		}
	}
}
