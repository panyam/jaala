package datalog

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// line is a path v0 -> ... -> v(n-1), with every node listed.
func line(n int) *ns.MemSource {
	src := ns.NewMemSource().Declare("edge", "from", "to").Declare("node", "name")
	for i := 0; i < n; i++ {
		src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("v%d", i))}})
		if i+1 < n {
			src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("v%d", i)), ns.S(fmt.Sprintf("v%d", i+1))}})
		}
	}
	return src
}

// walker registers walk(from, to), which follows edges from a bound start or back from a bound end,
// in the modes given, and records which ends each call had bound.
func walker(t *testing.T, v *ns.Vocabulary, modes [][]bool) *[][2]bool {
	t.Helper()
	var calls [][2]bool
	err := v.AddPredicate("walk", ns.Builtin{Arity: 2, Modes: modes, Gen: func(_ context.Context, src ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
		calls = append(calls, [2]bool{args[0].Bound, args[1].Bound})
		next, prev := map[string]string{}, map[string]string{}
		for _, e := range src.Tuples("edge") {
			next[e.Vals[0].S], prev[e.Vals[1].S] = e.Vals[1].S, e.Vals[0].S
		}
		var starts []string
		switch {
		case args[0].Bound:
			starts = []string{args[0].Value.S}
		case args[1].Bound:
			for cur, ok := prev[args[1].Value.S]; ok; cur, ok = prev[cur] {
				if err := emit([]ns.Value{ns.S(cur), args[1].Value}, nil); err != nil {
					return err
				}
			}
			return nil
		default:
			for _, n := range src.Tuples("node") {
				starts = append(starts, n.Vals[0].S)
			}
		}
		for _, s := range starts {
			for cur, ok := next[s]; ok; cur, ok = next[cur] {
				if err := emit([]ns.Value{ns.S(s), ns.S(cur)}, nil); err != nil {
					return err
				}
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return &calls
}

// Written in the order that forms a cross product, the body costs n² under Naive and n planned.
func TestPlanningRemovesTheCrossProduct(t *testing.T) {
	q := mustParse(t, `node(?a), node(?b), edge(?a, ?b), ?a = "v7" => ?b`)
	work := func(ev Evaluator) int64 {
		b := baseFor(std(line(400)))
		rows, err := ev.Eval(bg, q, b)
		if err != nil || col(rows, "b") != "v8" {
			t.Fatalf("%T: %v, %v", ev, rows, err)
		}
		return b.Work()
	}
	if w := work(SemiNaive{}); w > 800 {
		t.Errorf("planned work = %d over 400 nodes, want about one comparison per node (under 800)", w)
	}
	if w := work(Naive{}); w < 100000 {
		t.Errorf("control: Naive's work = %d, so the written order is not the cross product it should be", w)
	}
}

// A generator that needs its start bound waits until the body binds it, however it is written.
func TestAGeneratorWaitsForItsInput(t *testing.T) {
	v := std(line(50))
	calls := walker(t, v, [][]bool{{true, false}})
	rows, err := SemiNaive{}.Eval(bg, mustParse(t, `near(?m) :- walk(?n, ?m), node(?n), ?n = "v45"; near(?m) => ?m`), baseFor(v))
	if err != nil || col(rows, "m") != "v46,v47,v48,v49" {
		t.Fatalf("near = %v, %v", rows, err)
	}
	if len(*calls) != 1 || !(*calls)[0][0] {
		t.Errorf("walk calls (from bound, to bound) = %v, want one call from the bound start", *calls)
	}
}

// A generator with a mode for each end runs from whichever end the body binds.
func TestATwoModeGeneratorRunsFromTheBoundEnd(t *testing.T) {
	for _, c := range []struct {
		query, want string
		end         [2]bool
	}{
		{`walk(?a, ?z), node(?a), ?a = "v2" => ?z`, "v3,v4,v5", [2]bool{true, false}},
		{`walk(?z, ?a), node(?a), ?a = "v3" => ?z`, "v0,v1,v2", [2]bool{false, true}},
	} {
		v := std(line(6))
		calls := walker(t, v, [][]bool{{true, false}, {false, true}})
		rows, err := SemiNaive{}.Eval(bg, mustParse(t, c.query), baseFor(v))
		if err != nil || col(rows, "z") != c.want {
			t.Errorf("%s = %v, %v; want %s", c.query, rows, err, c.want)
		}
		if len(*calls) != 1 || (*calls)[0] != c.end {
			t.Errorf("%s: walk calls = %v, want one with %v bound", c.query, *calls, c.end)
		}
	}
}

// A body that can never satisfy a generator's modes is refused before evaluation, with the same
// message from every entry point.
func TestABodyThatNeverSatisfiesAGeneratorIsRefused(t *testing.T) {
	for _, c := range []struct {
		modes [][]bool
		query string
		want  string
	}{
		{[][]bool{{true, false}}, `near(?m) :- walk(?n, ?m); near(?m)`,
			`query: rule "near" calls walk(?n, ?m), and nothing binds what walk needs first: it needs ?n bound`},
		{[][]bool{{true, false}, {false, true}}, `walk(?n, ?m), ?n = "v1" => ?m`,
			`query: the query calls walk(?n, ?m), and nothing binds what walk needs first: it needs ?n bound, or ?m bound`},
		{[][]bool{{true, false}}, `node(?x), not walk(?y, ?x) => ?x`,
			`query: the query calls walk(?y, ?x), and nothing binds what walk needs first: it needs ?y bound`},
	} {
		v := std(line(3))
		walker(t, v, c.modes)
		q := mustParse(t, c.query)
		for name, err := range map[string]error{
			"Validate":  Validate(q, v),
			"Naive":     second(Naive{}.Eval(bg, q, baseFor(v))),
			"SemiNaive": second(SemiNaive{}.Eval(bg, q, baseFor(v))),
		} {
			if err == nil || err.Error() != c.want {
				t.Errorf("%s(%s): err = %v\n want %s", name, c.query, err, c.want)
			}
		}
	}
}

func second(_ []Row, err error) error { return err }

// The planner's whole claim: an answer does not depend on how a body is written. Every program is run
// with its rule bodies and goal shuffled, and the planned answer must equal Naive's on the original.
func TestPlannedAnswersDoNotDependOnClauseOrder(t *testing.T) {
	programs := append([]string{
		`node(?a), node(?b), edge(?a, ?b), ?a != ?b, str.prefix(?b, "v") => ?a, ?b`,
		`two(?a, ?c) :- edge(?a, ?b), edge(?b, ?c), node(?b); two(?a, ?c), not edge(?a, ?c) => ?a, ?c`,
		`heavy(?n) :- weight(?n, ?w), node(?n), ?w > 4; heavy(?n), edge(?m, ?n) => ?m, count(?n)`,
	}, recursivePrograms...)
	for seed := int64(1); seed <= 12; seed++ {
		src := randomGraph(seed, 6+int(seed%5), 0.2)
		b := baseFor(std(src))
		for _, text := range programs {
			q := mustParse(t, text)
			want, err := Naive{}.Eval(bg, q, b)
			if err != nil {
				t.Fatalf("%s: %v", text, err)
			}
			shuffled := shuffle(q, rand.New(rand.NewSource(seed)))
			got, err := SemiNaive{}.Eval(bg, shuffled, b)
			if err != nil {
				t.Fatalf("seed %d, shuffled %s: %v", seed, text, err)
			}
			if !reflect.DeepEqual(rowSet(want), rowSet(got)) {
				t.Errorf("seed %d: %s\n written:  %v\n shuffled: %v", seed, text, rowSet(want), rowSet(got))
			}
		}
	}
}

// shuffle permutes every rule body and the goal, keeping the answer's columns as the written goal
// gives them.
func shuffle(q Query, rnd *rand.Rand) Query {
	out := q
	if len(out.Select) == 0 {
		out.Select = defaultSelect(q.Goal)
	}
	perm := func(b Body) Body {
		lits := append([]Literal(nil), b.Literals...)
		rnd.Shuffle(len(lits), func(i, j int) { lits[i], lits[j] = lits[j], lits[i] })
		return Body{Literals: lits}
	}
	out.Rules = make([]Rule, len(q.Rules))
	for i, r := range q.Rules {
		r.Body = perm(r.Body)
		out.Rules[i] = r
	}
	out.Goal = perm(q.Goal)
	return out
}

// rowSet is an answer's bindings as sorted strings, independent of row order.
func rowSet(rows []Row) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		keys := make([]string, 0, len(r.Bind))
		for k, v := range r.Bind {
			keys = append(keys, string(k)+"="+v.S)
		}
		sort.Strings(keys)
		out[i] = strings.Join(keys, " ")
	}
	sort.Strings(out)
	return out
}

// A check nothing can bind is not dropped by planning, which would answer with more rows than the
// query allows; it still fails as it does unplanned.
func TestAnUnbindableCheckStillFailsWhenPlanned(t *testing.T) {
	for _, q := range []string{`str.contains(?n, "x") => ?n`, `node(?n), ?m > 3 => ?n`, `node(?n), str.prefix(?m, "v") => ?n`} {
		_, want := Naive{}.Eval(bg, mustParse(t, q), baseFor(std(graph())))
		_, got := SemiNaive{}.Eval(bg, mustParse(t, q), baseFor(std(graph())))
		if want == nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: planned err = %v, want Naive's %v", q, got, want)
		}
	}
}

// Planning may start the goal with a later literal, but the answer keeps the columns, and so the row
// order, its written goal gives it.
func TestPlanningKeepsTheWrittenColumnOrder(t *testing.T) {
	src := ns.NewMemSource().Declare("node", "name").Declare("pair", "k", "v")
	for _, n := range []string{"a", "b"} {
		src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(n)}})
	}
	for _, v := range []string{"1", "2"} {
		src.Add("pair", ns.Tuple{Vals: []ns.Value{ns.S("k"), ns.S(v)}})
	}
	q := mustParse(t, `node(?x), pair("k", ?y)`)
	want, err := Naive{}.Eval(bg, q, baseFor(std(src)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := SemiNaive{}.Eval(bg, q, baseFor(std(src)))
	if err != nil || !reflect.DeepEqual(binds(got), binds(want)) {
		t.Errorf("planned rows = %v, %v\n want, in Naive's order, %v", binds(got), err, binds(want))
	}
	if p := plan(baseFor(std(src)), q).Goal.Literals[0].Pos; p == nil || p.Relation != "pair" {
		t.Errorf("control: the plan starts with %v, so this goal is not reordered and the test proves nothing", p)
	}
}

// tested is line(6) whose first three nodes are tests, stated as attributes, the way Declaire's
// go.test is a rule over constants.
func tested() *ns.MemSource {
	src := line(6).Declare("attr", "n", "k", "v").Declare("blocked", "n")
	for _, n := range []string{"v0", "v1", "v2"} {
		src.Add("attr", ns.Tuple{Vals: []ns.Value{ns.S(n), ns.S("role"), ns.S("test")}})
	}
	return src.Add("blocked", ns.Tuple{Vals: []ns.Value{ns.S("v0")}})
}

// A generator whose mode is satisfied runs before a relation that only constants bind (#36): such a
// relation is a scan, and a generator after it runs once per row. Declaire's go.covers walked once per
// test when written test-first; go.test is a rule over constants, with a negation, inlined here.
func TestASatisfiedGeneratorRunsBeforeAScan(t *testing.T) {
	const test = `test(?f) :- attr(?f, "role", "test"), not attr(?f, "receiver", _); `
	goals := []struct {
		goal, col, want string
		end             [2]bool
	}{
		{`covers(?t, "v5") => ?t`, "t", "v0,v1,v2", [2]bool{false, true}},
		{`covers("v1", ?f) => ?f`, "f", "v2,v3,v4,v5", [2]bool{true, false}},
	}
	for _, body := range []string{`test(?t), walk(?t, ?f)`, `walk(?t, ?f), test(?t)`} {
		for _, g := range goals {
			v := std(tested())
			calls := walker(t, v, [][]bool{{true, false}, {false, true}})
			rows, err := SemiNaive{}.Eval(bg, mustParse(t, test+`covers(?t, ?f) :- `+body+`; `+g.goal), baseFor(v))
			if err != nil || col(rows, g.col) != g.want {
				t.Fatalf("%s, %s: %v, %v; want %s", body, g.goal, rows, err, g.want)
			}
			if len(*calls) != 1 || (*calls)[0] != g.end {
				t.Errorf("%s, %s: walks %v, want one with %v bound", body, g.goal, *calls, g.end)
			}
		}
	}
	v := std(tested())
	calls := walker(t, v, [][]bool{{true, false}, {false, true}})
	written := test + `covers(?t, ?f) :- test(?t), walk(?t, ?f); covers(?t, "v5") => ?t`
	if _, err := (SemiNaive{WrittenOrder: true}).Eval(bg, mustParse(t, written), baseFor(v)); err != nil || len(*calls) != 3 {
		t.Errorf("control: written test-first, %d walks (%v), want one per test (3)", len(*calls), err)
	}
	v = std(tested())
	calls = walker(t, v, [][]bool{{true, false}, {false, true}})
	if rows, err := (SemiNaive{}).Eval(bg, mustParse(t, test+`covers(?t, ?f) :- walk(?t, ?f), test(?t); covers(?t, ?f) => ?t, ?f`), baseFor(v)); err != nil || len(rows) != 5+4+3 || len(*calls) != 3 {
		t.Errorf("nothing bound, written walk-first: %d rows, %d walks, %v; want 12 rows and a walk per test", len(rows), len(*calls), err)
	}
}

// A relation whose arguments are all bound only asks whether a tuple exists, so it runs before a
// generator: a binding it rejects needs no walk at all.
func TestAnExistenceProbeRunsBeforeAGenerator(t *testing.T) {
	for _, c := range []struct {
		start string
		rows  int
		walks int
	}{{"v0", 5, 1}, {"v1", 0, 0}} {
		v := std(tested())
		calls := walker(t, v, [][]bool{{true, false}, {false, true}})
		text := `stuck(?t, ?f) :- walk(?t, ?f), blocked(?t); stuck("` + c.start + `", ?f) => ?f`
		rows, err := SemiNaive{}.Eval(bg, mustParse(t, text), baseFor(v))
		if err != nil || len(rows) != c.rows {
			t.Fatalf("from %s: %d rows, %v; want %d", c.start, len(rows), err, c.rows)
		}
		if len(*calls) != c.walks {
			t.Errorf("from %s: %d walks, want %d (the probe decides first)", c.start, len(*calls), c.walks)
		}
	}
}

// A generator called with nothing bound lists every value, so it runs after a relation the engine
// holds or derives, and its calls then have an argument bound (#293: Declaire's loc.home listed all
// 136,181 homes ahead of http.pattern's 100). Before a relation the Source looks up it keeps its
// place, since that relation read with nothing bound is a whole read from the store. control: a
// generator with an argument bound still runs first, as #36 has it.
func TestAGeneratorWithNothingBoundRunsAfterAHeldRelation(t *testing.T) {
	small := func() *ns.MemSource {
		src := line(10).Declare("small", "n")
		for _, n := range []string{"v7", "v8"} {
			src.Add("small", ns.Tuple{Vals: []ns.Value{ns.S(n)}})
		}
		return src
	}
	const derived = `sm(?x) :- small(?x); sm(?x) :- small(?x), node(?x); `
	for _, c := range []struct {
		name, q  string
		lookedUp bool
		walks    [][2]bool
	}{
		{"held", `walk(?x, ?y), small(?x) => ?x, ?y`, false, [][2]bool{{true, false}, {true, false}}},
		{"derived", derived + `walk(?x, ?y), sm(?x) => ?x, ?y`, false, [][2]bool{{true, false}, {true, false}}},
		{"control: looked up", `walk(?x, ?y), small(?x) => ?x, ?y`, true, [][2]bool{{false, false}}},
		{"control: bound", `walk("v7", ?y), small(?y) => ?y`, false, [][2]bool{{true, false}}},
	} {
		v := std(small())
		calls := walker(t, v, [][]bool{{false, false}, {true, false}})
		q := mustParse(t, c.q)
		want, err := both(q, baseFor(v))
		if err != nil || len(want) == 0 {
			t.Fatalf("%s: %d rows, %v", c.name, len(want), err)
		}
		*calls = nil
		var src ns.Source = small()
		if c.lookedUp {
			src = lookingAt(src)
		}
		rows, err := SemiNaive{}.Eval(bg, q, MustBase(v, src))
		if err != nil || len(rows) != len(want) {
			t.Fatalf("%s: %d rows, %v; want %d", c.name, len(rows), err, len(want))
		}
		if !reflect.DeepEqual(*calls, c.walks) {
			t.Errorf("%s: walks %v, want %v", c.name, *calls, c.walks)
		}
	}
}

// A generator bound only where its all-free mode would take it is a full walk per call, so it ranks
// as nothing bound and waits for what binds its input (#304: agni's esd_ok started from the TVS parts
// and walked every net from each of their 220 pins, where its derived needs_esd, empty, would have
// ended the clause). control: with the guard holding a value, every walk runs from a bound start, once
// per TVS pin (the guard is derived, so planning can't tell it is smaller than the index).
func TestAGeneratorBoundOnlyOutsideItsModesWaitsForItsInput(t *testing.T) {
	tvs := func() *ns.MemSource {
		src := line(10).Declare("part", "ref", "net").Declare("class", "ref", "kind").Declare("small", "n")
		src.Add("small", ns.Tuple{Vals: []ns.Value{ns.S("v2")}})
		for i := 0; i < 8; i++ {
			ref := ns.S(fmt.Sprintf("D%d", i))
			src.Add("class", ns.Tuple{Vals: []ns.Value{ref, ns.S("tvs")}})
			src.Add("part", ns.Tuple{Vals: []ns.Value{ref, ns.S(fmt.Sprintf("v%d", i+1))}})
		}
		return src
	}
	const body = `ok(?n) :- guard(?n), walk(?n, ?r), part(?t, ?r), class(?t, "tvs"); ok(?n) => ?n`
	for _, c := range []struct {
		name, guard string
		rows        int
		walks       [][2]bool
	}{
		{"empty guard", `guard(?n) :- small(?n), ?n = "v9"; guard(?n) :- node(?n), small(?n), ?n = "v9"; `, 0, nil},
		{"control: guard holds v2", `guard(?n) :- small(?n); guard(?n) :- node(?n), small(?n); `, 1, slices.Repeat([][2]bool{{true, true}}, 8)},
	} {
		v := std(tvs())
		calls := walker(t, v, [][]bool{{true, false}, {false, false}})
		q := mustParse(t, c.guard+body)
		if want, err := both(q, baseFor(v)); err != nil || len(want) != c.rows {
			t.Fatalf("%s: %d rows, %v; want %d", c.name, len(want), err, c.rows)
		}
		*calls = nil
		if rows, err := (SemiNaive{}).Eval(bg, q, baseFor(v)); err != nil || len(rows) != c.rows {
			t.Fatalf("%s: planned %d rows, %v; want %d", c.name, len(rows), err, c.rows)
		}
		if !reflect.DeepEqual(*calls, c.walks) {
			t.Errorf("%s: walks %v, want %v", c.name, *calls, c.walks)
		}
	}
	v := std(tvs())
	walker(t, v, [][]bool{{true, false}, {false, false}})
	q := mustParse(t, `guard(?n), walk(?n, ?r), part(?t, ?r), class(?t, "tvs")`)
	var order []string
	for _, l := range planBody(baseFor(v), q.Goal, nil).Literals {
		order = append(order, l.Pos.Relation)
	}
	if got := strings.Join(order, " "); got != "class part guard walk" {
		t.Errorf("planned %s, want class part guard walk (walk once guard binds its start)", got)
	}
}

// The same ranking orders a body planned from what its head binds, as demand plans an adorned rule.
func TestABodyPlannedFromItsHeadRanksTheSameWay(t *testing.T) {
	v := std(tested())
	walker(t, v, [][]bool{{true, false}, {false, true}})
	b := baseFor(v)
	first := func(body string, entry ...Var) string {
		q := mustParse(t, body)
		in := map[Var]bool{}
		for _, e := range entry {
			in[e] = true
		}
		return planBody(b, q.Goal, in).Literals[0].Pos.Relation
	}
	if got := first(`attr(?t, "role", "test"), walk(?t, ?f)`, "f"); got != "walk" {
		t.Errorf("?f bound on entry: %s runs first, want walk", got)
	}
	if got := first(`attr(?t, "role", "test"), walk(?t, ?f)`); got != "attr" {
		t.Errorf("control: nothing bound on entry, %s runs first, want attr (walk cannot run yet)", got)
	}
	if got := first(`walk(?t, ?f), blocked(?t)`, "t"); got != "blocked" {
		t.Errorf("?t bound on entry: %s runs first, want the probe blocked", got)
	}
}
