package datalog

import (
	"context"
	"fmt"
	"math/rand"
	"testing"

	"github.com/panyam/jaala/ns"
)

// randomGraph is a directed graph on n nodes with each edge present at probability p, so seeds give
// cycles, self-loops, disconnected parts and dead ends without anyone choosing them. Every node also
// carries a numeric weight for the aggregate programs.
func randomGraph(seed int64, n int, p float64) *ns.MemSource {
	rnd := rand.New(rand.NewSource(seed))
	src := ns.NewMemSource().Declare("edge", "from", "to").Declare("node", "name").Declare("weight", "node", "w")
	name := func(i int) ns.Value { return ns.S(fmt.Sprintf("v%d", i)) }
	for i := 0; i < n; i++ {
		src.Add("node", ns.Tuple{Vals: []ns.Value{name(i)}, Cites: []string{fmt.Sprintf("node:%d", i)}})
		src.Add("weight", ns.Tuple{Vals: []ns.Value{name(i), ns.N(float64(rnd.Intn(10)))}})
		for j := 0; j < n; j++ {
			if rnd.Float64() < p {
				src.Add("edge", ns.Tuple{Vals: []ns.Value{name(i), name(j)}, Cites: []string{fmt.Sprintf("edge:%d-%d", i, j)}})
			}
		}
	}
	return src
}

// recursivePrograms are the shapes a fixpoint has to get right: left, right and non-linear recursion,
// mutual recursion, two recursive atoms in one body, a recursive relation read under negation and by
// an aggregate in later strata, a chain of strata, and aggregating rules.
var recursivePrograms = []string{
	`reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c); reach(?a, ?b) => ?a, ?b`,
	`reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- edge(?a, ?b), reach(?b, ?c); reach("v0", ?b) => ?b`,
	`reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), reach(?b, ?c); reach(?a, ?b) => ?a, ?b`,
	`even(?x) :- node(?x), ?x = "v0"; odd(?y) :- even(?x), edge(?x, ?y); even(?y) :- odd(?x), edge(?x, ?y); even(?x), odd(?x) => ?x`,
	// A fact only the second recursive atom's delta can find: even(x) is old by the round odd(x) is new.
	`even(?x) :- node(?x), ?x = "v0"; odd(?y) :- even(?x), edge(?x, ?y); even(?y) :- odd(?x), edge(?x, ?y); ` +
		`both(?x) :- even(?x), odd(?x); both(?x) => ?x`,
	`sg(?x, ?y) :- edge(?p, ?x), edge(?p, ?y); sg(?x, ?y) :- edge(?p, ?x), sg(?p, ?q), edge(?q, ?y); sg(?x, ?y) => ?x, ?y`,
	`reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c); cyclic(?a) :- reach(?a, ?a); ` +
		`stuck(?a) :- node(?a), not cyclic(?a); stuck(?a) => ?a`,
	`reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c); reach(?a, ?b), weight(?b, ?w) => ?a, count(distinct ?b), sum(?w)`,
	`r1(?a, ?b) :- edge(?a, ?b); r2(?a, ?c) :- r1(?a, ?b), r1(?b, ?c); r2(?a, ?c) :- r2(?a, ?b), r1(?b, ?c); ` +
		`far(?a, ?b) :- r2(?a, ?b), not r1(?a, ?b); far(?a, ?b) => ?a, ?b`,
	`t(?a, ?b) :- edge(?a, ?b); t(?a, ?c) :- t(?a, ?b), t(?b, ?c), ?a != ?c; t(?a, ?b), not edge(?a, ?b) => ?a, ?b`,
	// Aggregating rules (#4): one over a recursive relation, read by a later recursion; one counting
	// bindings of a join, which the planner reorders and nothing may inline into or demand.
	`reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c); out(?a, count(distinct ?b), max(?w)) :- reach(?a, ?b), weight(?b, ?w); ` +
		`hub(?a, ?b) :- out(?a, ?n, _), ?n >= 2, edge(?a, ?b); hub(?a, ?c) :- hub(?a, ?b), hub(?b, ?c); hub(?a, ?b) => ?a, ?b`,
	`two(?a, ?c) :- edge(?a, ?b), edge(?b, ?c); deg(?a, count(?c), sum(?w)) :- two(?a, ?c), edge(?c, ?d), weight(?d, ?w); ` +
		`deg(?a, ?n, ?s), deg("v0", ?m, _) => ?a, ?n, ?s, ?m`,
}

// SemiNaive must answer exactly as Naive does, rows and citations, on every recursive shape over many
// random graphs. both panics on the first disagreement and prints the program.
func TestSemiNaiveAgreesWithNaiveOnRandomGraphs(t *testing.T) {
	compared := 0
	for seed := int64(1); seed <= 40; seed++ {
		src := randomGraph(seed, 4+int(seed%9), 0.08+float64(seed%5)*0.06)
		b := baseFor(std(src))
		for _, text := range recursivePrograms {
			if _, err := both(mustParse(t, text), b); err != nil {
				t.Fatalf("seed %d: %s: %v", seed, text, err)
			}
			compared++
		}
	}
	if compared != 40*len(recursivePrograms) {
		t.Errorf("compared %d programs, want %d", compared, 40*len(recursivePrograms))
	}
}

// chainOf is a path v0 -> v1 -> ... -> v(n-1).
func chainOf(n int) *ns.MemSource {
	src := ns.NewMemSource().Declare("edge", "from", "to")
	for i := 0; i+1 < n; i++ {
		src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("v%d", i)), ns.S(fmt.Sprintf("v%d", i+1))}})
	}
	return src
}

// The point of semi-naive evaluation, as a ratio rather than a timing: doubling a chain under a
// transitive closure multiplies Naive's work by about 8 (n rounds of n² derivations) and SemiNaive's
// by about 4 (each of the n² facts joined once).
func TestSemiNaiveClosureGrowsQuadratically(t *testing.T) {
	q := mustParse(t, `reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c); reach(?a, ?b) => ?a, ?b`)
	work := func(ev Evaluator, n int) int64 {
		b := baseFor(std(chainOf(n)))
		rows, err := ev.Eval(bg, q, b)
		if err != nil || len(rows) != n*(n-1)/2 {
			t.Fatalf("n=%d: %d rows, %v", n, len(rows), err)
		}
		return b.Work()
	}
	naive := float64(work(Naive{}, 80)) / float64(work(Naive{}, 40))
	semi := float64(work(SemiNaive{}, 80)) / float64(work(SemiNaive{}, 40))
	if semi > 4.5 {
		t.Errorf("SemiNaive work grew %.1fx when the chain doubled, want about 4x", semi)
	}
	if naive < 7 {
		t.Errorf("control: Naive work grew only %.1fx, so this chain is not exercising the fixpoint", naive)
	}
}

// In the round q(x) is new, p is new somewhere else too, and p(x) has been known since round zero. Only
// the variant reading q's delta, the second recursive atom, can derive both(x), and it must run even
// though the first atom's delta is not empty.
func TestEveryRecursiveAtomGetsItsDeltaVariant(t *testing.T) {
	src := ns.NewMemSource().Declare("base1", "n").Declare("base2", "n").Declare("edge1", "a", "b").Declare("edge2", "a", "b")
	src.Add("base1", ns.Tuple{Vals: []ns.Value{ns.S("x")}}).Add("base1", ns.Tuple{Vals: []ns.Value{ns.S("a0")}}).Add("base2", ns.Tuple{Vals: []ns.Value{ns.S("b0")}})
	for _, e := range [][2]string{{"a0", "a1"}, {"a1", "a2"}, {"a2", "a3"}, {"a3", "a4"}, {"a4", "a5"}} {
		src.Add("edge1", ns.Tuple{Vals: []ns.Value{ns.S(e[0]), ns.S(e[1])}})
	}
	for _, e := range [][2]string{{"b0", "b1"}, {"b1", "b2"}, {"b2", "x"}} {
		src.Add("edge2", ns.Tuple{Vals: []ns.Value{ns.S(e[0]), ns.S(e[1])}})
	}
	rows := eval(t, src, `p(?x) :- base1(?x); p(?y) :- p(?x), edge1(?x, ?y); q(?x) :- base2(?x); q(?y) :- q(?x), edge2(?x, ?y); `+
		`both(?x) :- p(?x), q(?x); both(?x) => ?x`)
	if got := col(rows, "x"); got != "x" {
		t.Errorf("both = %q, want x", got)
	}
}

// reversedLine is line(n) with its edges stored last-first, so no single pass over them follows the
// path: a recursion reaching along it takes a round per node.
func reversedLine(n int) *ns.MemSource {
	src := ns.NewMemSource().Declare("edge", "from", "to").Declare("node", "name")
	for i := 0; i < n; i++ {
		src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("v%d", i))}})
	}
	for i := n - 2; i >= 0; i-- {
		src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("v%d", i)), ns.S(fmt.Sprintf("v%d", i+1))}})
	}
	return src
}

// A round starts from its delta (#27): written edge-first, a reach still costs each round only its new
// nodes, not a scan of every edge, so its work grows with the path. The control runs the written
// order, which scans the edges every round.
func TestARoundStartsFromItsDelta(t *testing.T) {
	q := `r(?y) :- node(?y), ?y = "v0"; r(?y) :- edge(?x, ?y), r(?x); r(?y) => ?y`
	if ratio := float64(workOf(t, SemiNaive{}, reversedLine(400), q)) / float64(workOf(t, SemiNaive{}, reversedLine(200), q)); ratio > 2.5 {
		t.Errorf("work grew %.1fx when the path doubled, want about 2x", ratio)
	}
	if full := float64(workOf(t, SemiNaive{WrittenOrder: true}, reversedLine(400), q)) / float64(workOf(t, SemiNaive{WrittenOrder: true}, reversedLine(200), q)); full < 3.5 {
		t.Errorf("control: the written order grew only %.1fx, so the path is not taking a round per node", full)
	}
}

// A delta is not always small. With the edges stored in walk order, the first round derives every
// pair, and the next round's delta is all of them: then probing the delta from the edges is cheaper
// than scanning it, and the round must do that rather than start from the delta regardless. On the
// reversed path the deltas are small and starting from them wins.
func TestARoundStartsFromTheSmallerSide(t *testing.T) {
	q := `reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- edge(?b, ?c), reach(?a, ?b); reach(?a, ?b) => ?a, ?b`
	if got, written := workOf(t, SemiNaive{}, line(100), q), workOf(t, SemiNaive{WrittenOrder: true}, line(100), q); got > written {
		t.Errorf("walk order: work %d, written order %d; want no more, since the big delta should be probed", got, written)
	}
	if got, written := workOf(t, SemiNaive{}, reversedLine(100), q), workOf(t, SemiNaive{WrittenOrder: true}, reversedLine(100), q); got*3 > written*2 {
		t.Errorf("reversed: work %d, written order %d; want under two thirds, since the small deltas should go first", got, written)
	}
}

// counter registers probe(?x), a generator that echoes its bound argument and counts its calls, so a
// test can see how often a rule's body ran.
func counter(t *testing.T, v *ns.Vocabulary) *int {
	t.Helper()
	n := 0
	if err := v.AddPredicate("probe", ns.Builtin{Arity: 1, Modes: [][]bool{{true}}, Gen: func(_ context.Context, _ ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
		n++
		return emit([]ns.Value{args[0].Value}, nil)
	}}); err != nil {
		t.Fatal(err)
	}
	return &n
}

// A relation that only reads others is derived once, after them, however its name sorts (#51): a
// stratum holds every relation no negation separates, but only the ones reading one another need
// rounds. Here b reads a and probes each row, and c joins b with a recursive closure. Before, round
// zero ran b over a whenever a's name sorted first, and round one counted a's tuples as new and ran b
// over them again. Planned, c stands in for the goal (#270), so r("v0", ?x) demands b for the 7 nodes
// it reaches; as written, nothing is demanded and b probes all 8.
func TestAPlainDependencyIsDerivedOnce(t *testing.T) {
	for _, names := range [][3]string{{"a", "b", "c"}, {"z", "y", "x"}} {
		a, b, c := names[0], names[1], names[2]
		text := `r(?x, ?y) :- edge(?x, ?y); r(?x, ?z) :- r(?x, ?y), edge(?y, ?z); ` +
			a + `(?x) :- node(?x); ` + b + `(?x) :- ` + a + `(?x), probe(?x); ` + c + `(?x) :- ` + b + `(?x), r("v0", ?x); ` + c + `(?x) => ?x`
		for _, ev := range []Evaluator{SemiNaive{}, SemiNaive{WrittenOrder: true}} {
			v := std(line(8))
			n := counter(t, v)
			rows, err := ev.Eval(bg, mustParse(t, text), baseFor(v), Witnesses())
			if err != nil || len(rows) != 7 {
				t.Fatalf("%T %v: %d rows, %v", ev, names, len(rows), err)
			}
			want := 7
			if ev.(SemiNaive).WrittenOrder {
				want = 8
			}
			if *n != want {
				t.Errorf("%T %+v, relations named %v: probe ran %d times, want once per node (%d)", ev, ev, names, *n, want)
			}
		}
	}
}

// The shapes #36 left to this issue: two rules, or a witnessed Eval (nothing inlined), walk from the
// bound end once per rule. In the witnessed shape demand passes on into test after the walk, which a
// supplementary relation stores once (#54) rather than the walk running again in test's magic rule.
func TestAGeneratorInADerivedRelationRunsOncePerRule(t *testing.T) {
	const test = `test(?f) :- attr(?f, "role", "test"); `
	for _, c := range []struct {
		name, rules string
		opts        []Option
		walks       int
	}{
		{"two rules", test + `covers(?t, ?f) :- test(?t), walk(?t, ?f); covers(?t, ?f) :- test(?t), walk(?t, ?f), node(?f); `, nil, 2},
		{"witnessed", test + `covers(?t, ?f) :- test(?t), walk(?t, ?f); `, []Option{Witnesses()}, 1},
	} {
		v := std(tested())
		calls := walker(t, v, [][]bool{{true, false}, {false, true}})
		rows, err := SemiNaive{}.Eval(bg, mustParse(t, c.rules+`covers(?t, "v5") => ?t`), baseFor(v), c.opts...)
		if err != nil || col(rows, "t") != "v0,v1,v2" {
			t.Fatalf("%s: %v, %v", c.name, rows, err)
		}
		if len(*calls) != c.walks {
			t.Errorf("%s: walks %v, want %d, each from the bound end", c.name, *calls, c.walks)
		}
		for _, w := range *calls {
			if w != [2]bool{false, true} {
				t.Errorf("%s: a walk with %v bound, want the end", c.name, w)
			}
		}
	}
}

// Components come out in dependency order, each sorted, a recursive one whole.
func TestComponentsAreInDependencyOrder(t *testing.T) {
	q := mustParse(t, `p(?x) :- q(?x); q(?x) :- r(?x); r(?x) :- q(?x), edge(?x, ?y); r(?x) :- node(?x); s(?x) :- p(?x), r(?x); s(?x)`)
	byHead := map[string][]Rule{}
	for _, r := range q.Rules {
		byHead[r.Head.Relation] = append(byHead[r.Head.Relation], r)
	}
	got := fmt.Sprint(components([]string{"p", "q", "r", "s"}, byHead))
	if want := "[[q r] [p] [s]]"; got != want {
		t.Errorf("components = %s, want %s", got, want)
	}
}

// Round zero runs a component's rules one after another, so a rule has read what the rules before it
// added, and round one doesn't join it again (#149). Under demand, has_tp/b's rule reads the two nets
// C3 is on in round zero; a variant reading them again as a delta derived nothing, for as much work.
// control: the round-zero body still runs, and derives both tuples.
func TestRoundOneSkipsWhatARuleReadInRoundZero(t *testing.T) {
	r := explained(t, SemiNaive{}, probed+`probed_both("C3")`, baseFor(std(probedBoard(20))))
	if again := ruleRan(r, "has_tp/b(?n) :- demand:has_tp/b+("); again != nil {
		t.Errorf("has_tp/b's rule ran again over its demand's delta: %d runs, work %d", again.Runs, again.Work)
	}
	if once := ruleRan(r, "has_tp/b(?n) :- demand:has_tp/b("); once == nil || once.Tuples != 2 {
		t.Errorf("control: has_tp/b's round-zero body: %+v; want it to derive the 2 demanded tuples", once)
	}
}

// What a rule hasn't read yet in round zero, because a rule after it added it, is still its delta in
// round one: a reads b, and b's rules run after a's. control: a's variant does run.
func TestRoundOneReadsWhatARuleRanBefore(t *testing.T) {
	text := `a(?x) :- b(?y), edge(?y, ?x); b(?x) :- node(?x), ?x = "a"; b(?x) :- a(?x); a(?x) => ?x`
	rows := eval(t, graph(), text)
	if got := col(rows, "x"); got != "b,c,d" {
		t.Errorf("a = %q, want b c d", got)
	}
	r := explained(t, SemiNaive{}, text, baseFor(std(graph())))
	if v := ruleRan(r, "a(?x) :- b+("); v == nil || v.Tuples == 0 {
		t.Errorf("control: a's variant reading b's delta: %+v; want it to derive", v)
	}
}

// Under CanonicalCites a tuple a rule read in round zero, and a later rule then gave a shorter
// derivation, is still in that rule's round-one delta, so what it derived from the tuple shortens too.
// b's first rule derives b("a") three steps up, its second extends it to b("b"), and its third
// derives b("a") in one step after the second has run, revising it. The corpus doesn't reach this.
func TestRoundOneRereadsWhatRoundZeroRevisedAfterARuleRan(t *testing.T) {
	text := `d(?x) :- node(?x), ?x = "a"; c(?x) :- d(?x); ` +
		`b(?x) :- c(?x); b(?y) :- b(?x), edge(?x, ?y); b(?x) :- node(?x), ?x = "a"; b(?x) => ?x`
	q := mustParse(t, text)
	rows, err := both(q, baseFor(std(graph())), CanonicalCites(), Witnesses())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.Bind["x"].S == "b" && (len(r.Witness) != 1 || r.Witness[0].height != 2) {
			t.Errorf("b(\"b\")'s witness: %v; want two steps, from the one-step b(\"a\")", r.Witness)
		}
	}
}
