package datalog

import (
	"context"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

const (
	leftReach  = `reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c); `
	rightReach = `reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- edge(?a, ?b), reach(?b, ?c); `
)

func workOf(t *testing.T, ev Evaluator, src ns.Source, q string) int64 {
	t.Helper()
	b := baseFor(std(src))
	if _, err := ev.Eval(bg, mustParse(t, q), b); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return b.Work()
}

// Asked about one start, a left-linear closure derives only what that start reaches: its work grows
// with the chain, not with the chain's square, and asked about one end it walks back only as far as
// that end.
func TestDemandMakesABoundClosureLinear(t *testing.T) {
	q := leftReach + `reach("v0", ?x) => ?x`
	ratio := float64(workOf(t, SemiNaive{}, line(400), q)) / float64(workOf(t, SemiNaive{}, line(200), q))
	if ratio > 2.5 {
		t.Errorf("bound-start closure work grew %.1fx when the chain doubled, want about 2x", ratio)
	}
	full := float64(workOf(t, SemiNaive{WrittenOrder: true}, line(400), q)) / float64(workOf(t, SemiNaive{WrittenOrder: true}, line(200), q))
	if full < 3.5 {
		t.Errorf("control: without demand the work grew only %.1fx, so the closure is not being built in full", full)
	}
	q = leftReach + `reach(?x, "v5") => ?x`
	if near, far := workOf(t, SemiNaive{}, line(200), q), workOf(t, SemiNaive{}, line(400), q); near != far {
		t.Errorf("bound-end closure work = %d on 200 nodes and %d on 400, want the same (it walks back 5)", near, far)
	}
}

// Asked about one chain of twenty, right-linear recursion does the work of that chain alone.
func TestDemandHelpsRightLinearRecursionOnAPartOfTheGraph(t *testing.T) {
	src := ns.NewMemSource().Declare("edge", "from", "to").Declare("node", "name")
	for c := 0; c < 20; c++ {
		for i := 0; i < 30; i++ {
			src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("c%d_%d", c, i))}})
			if i+1 < 30 {
				src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("c%d_%d", c, i)), ns.S(fmt.Sprintf("c%d_%d", c, i+1))}})
			}
		}
	}
	q := rightReach + `reach("c0_0", ?x) => ?x`
	if with, without := workOf(t, SemiNaive{}, src, q), workOf(t, SemiNaive{WrittenOrder: true}, src, q); with*20 > without {
		t.Errorf("work with demand %d, without %d; want under a twentieth, since only one chain of twenty is asked about", with, without)
	}
}

// A magic tuple says what was asked, not what produced an answer. Here s1 and s2 both reach z, and z
// is demanded first on s1's behalf; s2's answer must still cite only s2's own facts.
func TestDemandLeavesNoCitationsOnTheAnswer(t *testing.T) {
	src := ns.NewMemSource().Declare("src", "s").Declare("q", "s", "z").Declare("base", "z", "y").Declare("alt", "z", "y")
	for _, s := range []string{"s1", "s2"} {
		src.Add("src", ns.Tuple{Vals: []ns.Value{ns.S(s)}, Cites: []string{"src:" + s}})
		src.Add("q", ns.Tuple{Vals: []ns.Value{ns.S(s), ns.S("z")}, Cites: []string{"q:" + s}})
	}
	src.Add("base", ns.Tuple{Vals: []ns.Value{ns.S("z"), ns.S("y")}, Cites: []string{"base:zy"}})
	text := `r(?z, ?y) :- base(?z, ?y); r(?z, ?y) :- alt(?z, ?y); src(?x), q(?x, ?z), r(?z, ?y) => ?x, ?y`
	if !strings.Contains(fmt.Sprint(magic(baseFor(std(src)), unfold(baseFor(std(src)), mustParse(t, text))).Rules), magicPrefix) {
		t.Fatal("control: r is not called for demand, so this test proves nothing")
	}
	rows, err := SemiNaive{}.Eval(bg, mustParse(t, text), baseFor(std(src)))
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows = %v, %v", rows, err)
	}
	for _, r := range rows {
		x := r.Bind["x"].S
		cites := append([]string(nil), r.Cites...)
		sort.Strings(cites)
		if want := []string{"base:zy", "q:" + x, "src:" + x}; !reflect.DeepEqual(cites, want) {
			t.Errorf("row %s cites %v, want %v", x, cites, want)
		}
	}
}

// Demand passes through a relation that uses negation (#34): asked about one start, far derives
// only what that start reaches, so its work grows with the chain. far has two rules so that it is
// not inlined, which would hand the demand to reach without passing through far.
func TestDemandPassesThroughANegatingRelation(t *testing.T) {
	text := leftReach + `far(?a, ?b) :- reach(?a, ?b), not edge(?a, ?b); far(?a, ?b) :- reach(?a, ?b), node(?b), not edge(?a, ?b); far("v0", ?x) => ?x`
	if got := col(eval(t, line(6), text), "x"); got != "v2,v3,v4,v5" {
		t.Errorf("far(v0) = %s, want v2..v5", got)
	}
	if ratio := float64(workOf(t, SemiNaive{}, line(400), text)) / float64(workOf(t, SemiNaive{}, line(200), text)); ratio > 2.5 {
		t.Errorf("far(v0) work grew %.1fx when the chain doubled, want about 2x", ratio)
	}
	if full := float64(workOf(t, SemiNaive{WrittenOrder: true}, line(400), text)) / float64(workOf(t, SemiNaive{WrittenOrder: true}, line(200), text)); full < 3.5 {
		t.Errorf("control: without demand far's work grew only %.1fx, so reach is not being built in full", full)
	}
}

// Demand goes into a negated call: agni's nocov("C12") :- p(?c), not cov(?c) derives cov only at C12.
// Here cov is "reaches anything", over a left-linear closure, so in full it is every pair.
func TestDemandGoesIntoANegatedCall(t *testing.T) {
	const rules = leftReach + `cov(?a) :- reach(?a, ?b); nocov(?c) :- node(?c), not cov(?c); `
	if got := col(eval(t, line(6), rules+`nocov(?c) => ?c`), "c"); got != "v5" {
		t.Errorf("nocov = %s, want v5 (the end of the line reaches nothing)", got)
	}
	if got := len(eval(t, line(6), rules+`nocov("v5")`)) + len(eval(t, line(6), rules+`nocov("v2")`)); got != 1 {
		t.Errorf("nocov(v5) and nocov(v2): %d rows, want 1", got)
	}
	q := magic(baseFor(std(line(6))), mustParse(t, rules+`nocov("v0")`))
	into := false
	for _, r := range q.Rules {
		into = into || r.Head.Relation == magicName("cov", "b")
	}
	if !into {
		t.Fatal("no demand rule for cov: the negated call was not rewritten")
	}
	goal := rules + `nocov("v0")`
	if ratio := float64(workOf(t, SemiNaive{}, line(400), goal)) / float64(workOf(t, SemiNaive{}, line(200), goal)); ratio > 2.5 {
		t.Errorf("nocov(v0) work grew %.1fx when the chain doubled, want about 2x", ratio)
	}
	if full := float64(workOf(t, SemiNaive{WrittenOrder: true}, line(400), goal)) / float64(workOf(t, SemiNaive{WrittenOrder: true}, line(200), goal)); full < 3.5 {
		t.Errorf("control: without demand the work grew only %.1fx, so cov is not being built in full", full)
	}
}

// Demand into a negation would make this program unstratifiable: p is recursive, so the demand for q
// at ?z depends on p, and p negates q. The rewrite is made again with q read in full, and answers as
// Naive does.
func TestDemandIntoANegationFallsBackWhenItWouldNotStratify(t *testing.T) {
	text := `q(?y) :- weight(?y, ?w), ?w > 5; p(?x, ?y) :- edge(?x, ?y), not q(?y); p(?x, ?z) :- p(?x, ?y), edge(?y, ?z), not q(?z); p("v0", ?z) => ?z`
	b := baseFor(std(randomGraph(3, 10, 0.3)))
	if _, err := stratify(magicWith(b, mustParse(t, text), true).Rules, derivedArity(magicWith(b, mustParse(t, text), true).Rules)); err == nil {
		t.Fatal("control: demand into the negation stratifies here, so the fallback is not exercised")
	}
	q := magic(b, mustParse(t, text))
	if _, err := stratify(q.Rules, derivedArity(q.Rules)); err != nil {
		t.Fatalf("the fallback does not stratify: %v", err)
	}
	for _, r := range q.Rules {
		if r.Head.Relation == magicName("q", "b") {
			t.Errorf("the fallback still demands q: %s", r)
		}
	}
	for seed := int64(1); seed <= 10; seed++ {
		if _, err := both(mustParse(t, text), baseFor(std(randomGraph(seed, 10, 0.3)))); err != nil {
			t.Errorf("seed %d: %v", seed, err)
		}
	}
}

// Bound calls over random graphs, through every recursive shape, answer as Naive does: through both()
// as written, and planned from a shuffled body against Naive on the written one.
func TestDemandAgreesWithNaiveOnRandomGraphs(t *testing.T) {
	programs := []string{
		leftReach + `reach("v0", ?x) => ?x`,
		rightReach + `reach("v1", ?x) => ?x`,
		rightReach + `reach("v0", ?x), reach("v2", ?x) => ?x`,
		`p(?a, ?k, ?b) :- edge(?a, ?b), weight(?a, ?k); p(?a, ?k, ?c) :- edge(?a, ?b), p(?b, ?k, ?c); p("v1", ?k, ?x) => ?k, ?x`,
		`r(?a, ?b) :- edge(?a, ?b); r(?a, ?a) :- node(?a); r(?a, ?c) :- edge(?a, ?b), weight(?b, ?w), ?w > 3, r(?b, ?c); r("v0", ?x) => ?x`,
		`r(?a) :- node(?a), ?a = "v3"; r(?a) :- edge(?a, ?b), r(?b); r("v0")`,
		leftReach + `reach(?x, "v2") => ?x`,
		`reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), reach(?b, ?c); reach("v0", ?x) => ?x`,
		`sg(?x, ?y) :- edge(?p, ?x), edge(?p, ?y); sg(?x, ?y) :- edge(?p, ?x), sg(?p, ?q), edge(?q, ?y); sg("v1", ?y) => ?y`,
		`even(?x) :- node(?x), ?x = "v0"; odd(?y) :- even(?x), edge(?x, ?y); even(?y) :- odd(?x), edge(?x, ?y); odd("v3")`,
		leftReach + `node(?s), weight(?s, ?w), ?w > 6, reach(?s, ?x) => ?s, ?x`,
		leftReach + `two(?a, ?c) :- reach(?a, ?b), reach(?b, ?c); two("v0", ?c), weight(?c, ?w) => ?c, max(?w)`,
		leftReach + `far(?a, ?b) :- reach(?a, ?b), not edge(?a, ?b); far("v0", ?x) => ?x`,
		leftReach + `from0(?x) :- reach("v0", ?x); from0(?x) :- reach("v0", ?x), node(?x); from0(?x), weight(?x, ?w) => ?x, ?w`,
		leftReach + `lone(?x) :- node(?x), not reach("v1", ?x); lone(?x) :- node(?x), weight(?x, ?w), not reach("v1", ?x); lone(?x) => ?x`,
		leftReach + `two(?x, ?y) :- reach(?x, ?y); two(?x, ?y) :- reach(?x, ?y), node(?y); via(?y) :- two("v2", ?y); via(?y) => ?y`,
		rightReach + `from1(?x) :- reach("v1", ?x); from1(?x) :- reach("v1", ?x), node(?x); from1(?x) => ?x`,
		leftReach + `cov(?a) :- reach(?a, ?b), weight(?b, ?w), ?w > 6; nocov(?c) :- node(?c), not cov(?c); nocov("v1")`,
		leftReach + `cov(?a) :- reach(?a, ?b), weight(?b, ?w), ?w > 6; node(?c), not cov(?c) => ?c`,
		`q(?y) :- weight(?y, ?w), ?w > 5; p(?x, ?y) :- edge(?x, ?y), not q(?y); p(?x, ?z) :- p(?x, ?y), edge(?y, ?z), not q(?z); p("v0", ?z) => ?z`,
		leftReach + `node(?s), weight(?s, ?w), reach(?s, ?x) => ?s, count(?x)`,
		roles + `role(?n, "sink") => ?n`,
		roles + `role(?n, "source") => ?n`,
		roles + `role(?n, "source"), reach(?x, _) => ?n, ?x`,
		`deg(?n, count(?w)) :- edge(?n, ?m), weight(?m, ?w); hub(?n, "a") :- deg(?n, ?k), ?k > 0; ` +
			`hub(?n, "b") :- edge(_, ?n); hub(?n, "a") => ?n`,
		rightReach + `far(?a, ?b) :- reach(?a, ?b); far(?a, ?b) :- reach(?a, ?b), node(?b); ` +
			`lone(?n, "a") :- node(?n), not far(_, _); lone(?n, "b") :- edge(?n, _); lone(?n, "a") => ?n`,
	}
	for seed := int64(1); seed <= 25; seed++ {
		b := baseFor(std(randomGraph(seed, 5+int(seed%8), 0.1+float64(seed%4)*0.07)))
		rnd := rand.New(rand.NewSource(seed))
		for _, text := range programs {
			want, err := both(mustParse(t, text), b)
			if err != nil {
				t.Fatalf("seed %d: %s: %v", seed, text, err)
			}
			got, err := SemiNaive{}.Eval(bg, shuffle(mustParse(t, text), rnd), b)
			if err != nil || !reflect.DeepEqual(rowSet(got), rowSet(want)) {
				t.Errorf("seed %d, shuffled %s:\n got  %v %v\n want %v", seed, text, rowSet(got), err, rowSet(want))
			}
		}
	}
}

// An error in a rule names the rule as written, not the relation a rewrite renamed it to: adorned for
// demand (left-linear here) or factored (right-linear).
func TestARewrittenRuleErrsUnderItsOwnName(t *testing.T) {
	want := `query: rule "r" head variable ?b is not bound by a positive body relation`
	for _, rec := range []string{`r(?a, ?b), edge(?b, ?c)`, `edge(?a, ?b), r(?b, ?c)`} {
		text := `r(?a, ?b) :- edge(?a, ?b); r(?a, ?b) :- node(?a), ?a = ?b; r(?a, ?c) :- ` + rec + `; r("v0", ?x) => ?x`
		for _, ev := range evaluators {
			if _, err := ev.Eval(bg, mustParse(t, text), baseFor(std(line(5)))); err == nil || err.Error() != want {
				t.Errorf("%T, recursing %s: err = %v, want %s", ev, rec, err, want)
			}
		}
	}
}

// A prefix that passes demand on runs once (#54): the walk before r is stored in a supplementary
// relation that r's demand and the rest of the goal both read. A goal whose aggregate counts bindings
// keeps its own prefix (a supplementary relation is a set), so there the walk runs twice.
func TestAPrefixThatPassesDemandRunsOnce(t *testing.T) {
	const rules = `r(?a, ?b) :- edge(?a, ?b); r(?a, ?b) :- edge(?a, ?c), edge(?c, ?b); `
	for _, c := range []struct {
		goal  string
		walks int
	}{
		{`walk("v1", ?y), r(?y, ?z) => ?y, ?z`, 1},
		{`walk("v1", ?y), r(?y, ?z) => count(?z)`, 2},
	} {
		v := std(line(6))
		calls := walker(t, v, [][]bool{{true, false}, {false, true}})
		if _, err := (SemiNaive{}).Eval(bg, mustParse(t, rules+c.goal), baseFor(v)); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != c.walks {
			t.Errorf("%s: %d walks, want %d", c.goal, len(*calls), c.walks)
		}
	}
	v := std(line(6))
	walker(t, v, [][]bool{{true, false}, {false, true}})
	if got, want := rowSet(evalReg(t, v, rules+`walk("v1", ?y), r(?y, ?z) => ?y, ?z`)), 3+2; len(got) != want {
		t.Errorf("%d rows, want %d: %v", len(got), want, got)
	}
}

// A supplementary tuple is a prefix's result, not demand, so it keeps the prefix's citations: each
// answer of a right-linear closure from a start bound through a variable cites its whole path.
func TestASupplementaryTupleKeepsItsCitations(t *testing.T) {
	src := ns.NewMemSource().Declare("edge", "from", "to").Declare("node", "name")
	for i := 0; i < 5; i++ {
		src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("v%d", i))}})
		if i < 4 {
			src.Add("edge", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("v%d", i)), ns.S(fmt.Sprintf("v%d", i+1))}, Cites: []string{fmt.Sprintf("e%d", i)}})
		}
	}
	text := rightReach + `node(?s), ?s = "v0", reach(?s, ?x) => ?x`
	if !strings.Contains(fmt.Sprint(magic(baseFor(std(src)), mustParse(t, text)).Rules), supPrefix) {
		t.Fatal("control: no supplementary relation, so this test proves nothing")
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

// A witnessed Eval keeps the demand a bound call carries (#53): Declaire's go.covers, whose test uses
// a negation, walks once, answers as an unwitnessed Eval does, and its witness still shows covers' own
// node and rule, with the walk's citations in walk order.
func TestAWitnessedBoundCallKeepsItsDemand(t *testing.T) {
	const text = `test(?f) :- attr(?f, "role", "test"), not attr(?f, "receiver", _); ` +
		`covers(?t, ?f) :- test(?t), hop(?t, ?f); covers(?t, "v5") => ?t`
	run := func(opts ...Option) ([]Row, int) {
		v := std(tested())
		walks := hopper(t, v)
		rows, err := SemiNaive{}.Eval(bg, mustParse(t, text), baseFor(v), opts...)
		if err != nil {
			t.Fatal(err)
		}
		return rows, *walks
	}
	plain, _ := run()
	rows, walks := run(Witnesses())
	if walks != 1 {
		t.Errorf("witnessed: %d walks, want 1", walks)
	}
	if !reflect.DeepEqual(rowSet(rows), rowSet(plain)) || col(rows, "t") != "v0,v1,v2" {
		t.Errorf("witnessed rows %v, unwitnessed %v; want the same, v0..v2", rowSet(rows), rowSet(plain))
	}
	for _, r := range rows {
		if len(r.Witness) != 1 || r.Witness[0].Relation != "covers" || r.Witness[0].Rule != `covers(?t, ?f) :- test(?t), hop(?t, ?f)` {
			t.Fatalf("row %s: witness %+v, want covers' own node with its rule as written", r.Bind["t"].S, r.Witness)
		}
		kids := r.Witness[0].Children
		if len(kids) != 2 || kids[0].Relation != "test" || kids[1].Relation != "hop" {
			t.Fatalf("row %s: covers' children %+v, want test then hop, as written", r.Bind["t"].S, kids)
		}
		var start int
		fmt.Sscanf(r.Bind["t"].S, "v%d", &start)
		var want []string
		for i := start; i < 5; i++ {
			want = append(want, fmt.Sprintf("e%d-%d", i, i+1))
		}
		if !reflect.DeepEqual(kids[1].Cites, want) {
			t.Errorf("row %s: hop cites %v, want %v in walk order", r.Bind["t"].S, kids[1].Cites, want)
		}
	}
}

// hopper registers hop(?from, ?to), which walks back from a bound ?to along line()'s edges, citing each
// answer's path in walk order, and counts its walks. A bound ?from alone yields nothing.
func hopper(t *testing.T, v *ns.Vocabulary) *int {
	t.Helper()
	walks := 0
	if err := v.AddPredicate("hop", ns.Builtin{Arity: 2, Modes: [][]bool{{true, false}, {false, true}}, Gen: func(_ context.Context, _ ns.Source, args []ns.Arg, emit func([]ns.Value, []string) error) error {
		walks++
		if !args[1].Bound {
			return nil
		}
		var end int
		fmt.Sscanf(args[1].Value.S, "v%d", &end)
		for start := end - 1; start >= 0; start-- {
			var path []string
			for i := start; i < end; i++ {
				path = append(path, fmt.Sprintf("e%d-%d", i, i+1))
			}
			if err := emit([]ns.Value{ns.S(fmt.Sprintf("v%d", start)), args[1].Value}, path); err != nil {
				return err
			}
		}
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	return &walks
}

// A constant in a rule the query defines is demand too (#57): the goal asks uses(?t) with nothing
// bound, so no demand starts there, but covers is still walked once, directly or through mid, with
// or without witnesses. The witness keeps a node for each derived relation on the way.
func TestAConstantInARuleCarriesDemand(t *testing.T) {
	const test = `test(?f) :- attr(?f, "role", "test"), not attr(?f, "receiver", _); covers(?t, ?f) :- test(?t), hop(?t, ?f); `
	for _, c := range []struct {
		name, rules string
		chain       []string
	}{
		{"direct", `uses(?t) :- covers(?t, "v5"); `, []string{"uses", "covers"}},
		{"through mid", `mid(?t, ?f) :- covers(?t, ?f); uses(?t) :- mid(?t, "v5"); `, []string{"uses", "mid", "covers"}},
	} {
		text := test + c.rules + `uses(?t) => ?t`
		run := func(ev Evaluator, opts ...Option) ([]Row, int) {
			v := std(tested())
			walks := hopper(t, v)
			rows, err := ev.Eval(bg, mustParse(t, text), baseFor(v), opts...)
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			return rows, *walks
		}
		plain, plainWalks := run(SemiNaive{})
		rows, walks := run(SemiNaive{}, Witnesses())
		if walks != 1 || plainWalks != 1 {
			t.Errorf("%s: %d walks witnessed, %d not; want 1 each", c.name, walks, plainWalks)
		}
		if _, written := run(SemiNaive{WrittenOrder: true}); written != 3 {
			t.Errorf("%s: control: as written, %d walks, want one per test (3)", c.name, written)
		}
		if !reflect.DeepEqual(rowSet(rows), rowSet(plain)) || col(rows, "t") != "v0,v1,v2" {
			t.Errorf("%s: witnessed rows %v, unwitnessed %v; want the same, v0..v2", c.name, rowSet(rows), rowSet(plain))
		}
		for _, r := range rows {
			w := r.Witness[0]
			for i, rel := range c.chain {
				if w == nil || w.Relation != rel {
					t.Fatalf("%s, row %s: witness level %d is %+v, want %s", c.name, r.Bind["t"].S, i, w, rel)
				}
				if i+1 < len(c.chain) {
					w = w.Children[0]
				}
			}
			if len(w.Children) != 2 || w.Children[0].Relation != "test" || w.Children[1].Relation != "hop" {
				t.Errorf("%s, row %s: covers' children %+v, want test then hop", c.name, r.Bind["t"].S, w.Children)
			}
		}
	}
}

// A rule evaluated in full that calls a relation with a constant, positively or under a negation,
// derives only that constant's part, so its work grows with what the constant reaches. Each relation
// has two rules so that it is not inlined.
func TestARuleCallWithAConstantDerivesOnlyItsPart(t *testing.T) {
	for _, text := range []string{
		leftReach + `from0(?x) :- reach("v0", ?x); from0(?x) :- reach("v0", ?x), node(?x); from0(?x) => ?x`,
		leftReach + `lone(?x) :- node(?x), not reach("v1", ?x); lone(?x) :- edge(?x, _), not reach("v1", ?x); lone(?x) => ?x`,
	} {
		if ratio := float64(workOf(t, SemiNaive{}, line(400), text)) / float64(workOf(t, SemiNaive{}, line(200), text)); ratio > 2.5 {
			t.Errorf("%s: work grew %.1fx when the chain doubled, want about 2x", text[len(leftReach):], ratio)
		}
		if full := float64(workOf(t, SemiNaive{WrittenOrder: true}, line(400), text)) / float64(workOf(t, SemiNaive{WrittenOrder: true}, line(200), text)); full < 3.5 {
			t.Errorf("%s: control: as written the work grew only %.1fx, so reach is not being built in full", text[len(leftReach):], full)
		}
	}
}

// A call into the rule's own recursion keeps reading the relation: it is evaluated in full already,
// and an adorned copy would derive part of it twice.
func TestACallIntoItsOwnRecursionIsLeftAlone(t *testing.T) {
	text := `reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c), reach(?c, "v5"); reach(?a, ?b) => ?a, ?b`
	for _, r := range magic(baseFor(std(line(6))), mustParse(t, text)).Rules {
		if strings.HasPrefix(r.Head.Relation, adornedName("reach", "")) {
			t.Errorf("the rewrite made %s", displayName(r.Head.Relation))
		}
	}
	if got := rowSet(eval(t, line(6), text)); len(got) != 6 {
		t.Errorf("rows %v, want the 5 edges and v2-v4 (only v4 reaches v5 in one step)", got)
	}
}

// role's two clauses, each called with its kind bound: a "source" clause reading reach with nothing
// bound, and a "sink" clause that doesn't read it. Asked for sinks, the source clause's guard never
// holds.
const roles = leftReach + `role(?n, "source") :- reach(?n, _); role(?n, "sink") :- edge(_, ?n); `

// A call with nothing bound, from a clause no caller demands, derives nothing (#60): asked for sinks,
// reach is never built, so the work grows with the chain rather than its square.
func TestDemandReachesACallWithNothingBound(t *testing.T) {
	sinks := roles + `role(?n, "sink") => ?n`
	ratio := float64(workOf(t, SemiNaive{}, line(400), sinks)) / float64(workOf(t, SemiNaive{}, line(200), sinks))
	if ratio > 2.5 {
		t.Errorf("a dead clause's closure: work grew %.1fx when the chain doubled, want about 2x", ratio)
	}
	full := float64(workOf(t, SemiNaive{WrittenOrder: true}, line(400), sinks)) / float64(workOf(t, SemiNaive{WrittenOrder: true}, line(200), sinks))
	if full < 3.5 {
		t.Errorf("control: without demand the work grew only %.1fx, so reach is not being built in full", full)
	}
	lone := leftReach + `far(?a, ?b) :- reach(?a, ?b); far(?a, ?b) :- reach(?a, ?b), node(?b); ` +
		`lone(?n, "a") :- node(?n), not far(_, _); lone(?n, "b") :- edge(?n, _); lone(?n, "b") => ?n`
	if ratio := float64(workOf(t, SemiNaive{}, line(400), lone)) / float64(workOf(t, SemiNaive{}, line(200), lone)); ratio > 2.5 {
		t.Errorf("a dead clause's negated closure: work grew %.1fx when the chain doubled, want about 2x", ratio)
	}
	if got := col(eval(t, line(6), sinks), "n"); got != "v1,v2,v3,v4,v5" {
		t.Errorf("sinks = %s, want v1..v5", got)
	}
	if got := col(eval(t, line(6), roles+`role(?n, "source") => ?n`), "n"); got != "v0,v1,v2,v3,v4" {
		t.Errorf("sources = %s, want v0..v4: a demanded clause still reads reach in full", got)
	}
}

// A clause no caller demands never calls its generator, even in a mode with nothing bound.
func TestADeadClauseNeverCallsAnAllFreeGenerator(t *testing.T) {
	const text = `role(?n, "source") :- walk(?n, _); role(?n, "sink") :- edge(_, ?n); `
	run := func(goal string) int {
		v := std(line(6))
		calls := walker(t, v, [][]bool{{false, false}})
		if _, err := (SemiNaive{}).Eval(bg, mustParse(t, text+goal), baseFor(v)); err != nil {
			t.Fatal(err)
		}
		return len(*calls)
	}
	if n := run(`role(?n, "sink") => ?n`); n != 0 {
		t.Errorf("asked for sinks, walk ran %d times, want 0", n)
	}
	if n := run(`role(?n, "source") => ?n`); n == 0 {
		t.Error("control: asked for sources, walk never ran")
	}
}

// When something reads a relation in full anyway, its all-free calls read the original too, rather
// than deriving it a second time behind a guard.
func TestAllFreeCallsFoldBackWhenTheOriginalIsRead(t *testing.T) {
	free := adornedName("reach", "ff")
	rewritten := func(goal string) string {
		b := baseFor(std(line(6)))
		return fmt.Sprint(magic(b, unfold(b, mustParse(t, roles+goal))).Rules)
	}
	if !strings.Contains(rewritten(`role(?n, "source") => ?n`), free) {
		t.Fatal("control: the source clause's call is not rewritten all-free, so this test proves nothing")
	}
	if got := rewritten(`role(?n, "source"), reach(?x, _) => ?n, ?x`); strings.Contains(got, free) {
		t.Errorf("reach is read in full and still derived all-free:\n%s", got)
	}
	rows := eval(t, line(4), roles+`role(?n, "source"), reach(?x, _) => ?n, ?x`)
	if len(rows) != 9 {
		t.Errorf("%d rows, want 9 (three sources by three starts)", len(rows))
	}
}

// A recursive call passing on the demand its rule received adds no magic rule restating the guard.
func TestARecursiveAllFreeCallAddsNoTautology(t *testing.T) {
	b := baseFor(std(line(6)))
	for _, r := range magic(b, unfold(b, mustParse(t, roles+`role(?n, "source") => ?n`))).Rules {
		if len(r.Body.Literals) == 1 && r.Body.Literals[0].Pos != nil && sameAtom(*r.Body.Literals[0].Pos, r.Head) {
			t.Errorf("rule %v restates its own guard", r)
		}
	}
}

// Witnessed, the demanded clause answers as unwitnessed (factoring is off, demand is not).
func TestAWitnessedAllFreeCallAnswersAsUnwitnessed(t *testing.T) {
	text := roles + `role(?n, "source") => ?n`
	plain, err := SemiNaive{}.Eval(bg, mustParse(t, text), baseFor(std(line(6))))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := SemiNaive{}.Eval(bg, mustParse(t, text), baseFor(std(line(6))), Witnesses())
	if err != nil || !reflect.DeepEqual(rowSet(rows), rowSet(plain)) || len(rows) != 5 {
		t.Errorf("witnessed %v %v, unwitnessed %v; want the same five sources", rowSet(rows), err, rowSet(plain))
	}
	for _, r := range rows {
		for _, c := range r.Cites {
			if strings.Contains(c, magicPrefix) {
				t.Errorf("row %v cites a magic tuple: %s", r.Bind, c)
			}
		}
	}
}

// An aggregating relation is read in full (#4), even from a demanded clause that calls it with nothing
// bound.
func TestAnAggregatingRelationCalledAllFreeIsReadInFull(t *testing.T) {
	const rest = `hub(?n, "a") :- deg(?n, ?k), ?k > 0; hub(?n, "b") :- edge(_, ?n); hub(?n, "a") => ?n`
	rewritten := func(deg string) string {
		b := baseFor(std(line(6)))
		return fmt.Sprint(magic(b, unfold(b, mustParse(t, deg+rest))).Rules)
	}
	free := adornedName("deg", "ff")
	if !strings.Contains(rewritten(`deg(?n, ?m) :- edge(?n, ?m); deg(?n, ?m) :- edge(?m, ?n); `), free) {
		t.Fatal("control: a plain deg is not called all-free, so this test proves nothing")
	}
	if got := rewritten(`deg(?n, count(?m)) :- edge(?n, ?m); `); strings.Contains(got, free) {
		t.Errorf("the aggregating deg is demanded all-free:\n%s", got)
	}
}

// A rule the goal never reaches is still the query's rule, so a rewrite of what it reads must not
// strand it (#90): demand for a bound call, inlining a single-rule relation, and demand into a
// negated call each used to leave it reading a relation that no longer had rules.
func TestAnUnreachedRuleDoesNotBreakTheRewrites(t *testing.T) {
	for _, c := range []struct {
		text string
		opts []Option
		want string
	}{
		{`r0(?x) :- node(?x); r1(?y, ?y) :- r0(?y); node(?x), r0(?x) => ?x`, []Option{Witnesses()}, "a,b,c,d,x"},
		{`r0(?x) :- edge(?x, _); r1(?y) :- node(?y), not r0(?y); r0(?x) => ?x`, nil, "a,b,c"},
		{`r0(?x, ?x) :- edge(?x, _); r0(?x, ?x) :- edge(_, ?x); r1(?y) :- r0(?y, _); node(?x), not r0(_, ?x) => ?x`, nil, "x"},
		{`r0(?x, ?y) :- edge(?x, ?y); r0(?x, ?y) :- edge(?y, ?x); r1(?x) :- r0(?x, _); r0("b", ?x) => ?x`, nil, "a,c"},
	} {
		rows, diff, err := agree(mustParse(t, c.text), baseFor(std(graph())), c.opts...)
		if diff != "" || err != nil {
			t.Errorf("%s: %v\n%s", c.text, err, diff)
			continue
		}
		if got := col(rows, "x"); got != c.want {
			t.Errorf("%s = %s, want %s", c.text, got, c.want)
		}
	}
	// control: the unreached rule is still checked, under its written name, by every evaluator.
	_, diff, err := agree(mustParse(t, `r0(?x) :- node(?x); r1(?y, ?z) :- r0(?y); r0("a") => `), baseFor(std(graph())))
	if diff != "" || err == nil || !strings.Contains(err.Error(), `rule "r1"`) {
		t.Errorf("an unreached rule with an unbound head variable: %v\n%s", err, diff)
	}
}

// When demand makes a stratified program unstratifiable even with negated calls read in full, the
// program runs without demand (#93): through an aggregate whose body a demanded relation feeds, or
// through a negation the #34 fallback still leaves in a cycle. control: a program whose rewrite
// stratifies keeps it.
func TestDemandFallsBackWhenNoRewriteStratifies(t *testing.T) {
	for _, c := range []struct {
		why, text string
		opts      []Option
		want      string
	}{
		{"through an aggregate", `r1(?a, "a", ?c) :- r1(?a, ?b, ?c); r1(?a, ?a, 3) :- r1(?a, _, ?d); ` +
			`r2(sum(?w), min(?w)) :- r1(?p, ?n, 5), weight(?n, ?w); ` +
			`r3(?x) :- r3("d"), r1(_, ?x, ?y); r3(?x) :- r2(_, ?v), node(?x); r3("b"), node(?z) => ?z`, []Option{Witnesses()}, "a,b,c,d,x"},
		{"through a negation", `r0(?x, ?x) :- edge(_, ?x); ` +
			`r1(?w, ?y) :- node(?y), weight("c", ?w), not r0(?y, "b"); r1(?w, ?y) :- r0(?n, ?y), weight(?n, ?w); ` +
			`r2(?y, ?y) :- r1(?v, ?y); r1(?w, ?y), r2(_, ?y) => min(?w)`, nil, "2"},
	} {
		rows, diff, err := agree(mustParse(t, c.text), baseFor(std(graph())), c.opts...)
		if diff != "" || err != nil {
			t.Errorf("%s: %v\n%s", c.why, err, diff)
			continue
		}
		var got []string
		for _, r := range rows {
			for _, v := range r.Bind {
				got = append(got, v.S)
			}
		}
		if strings.Join(got, ",") != c.want {
			t.Errorf("%s = %v, want %q", c.why, got, c.want)
		}
	}
	b := baseFor(std(graph()))
	q := mustParse(t, leftReach+`reach("a", ?y) => ?y`)
	if out := magic(b, q); fmt.Sprint(out.Rules) == fmt.Sprint(q.Rules) {
		t.Errorf("control: a program whose rewrite stratifies should keep it")
	}
}

// A relation whose every rule is recursive has no base rule to seed its answers, so it is empty and
// isn't factored: factoring wrote no rule for the answer relation, and the goal read a relation with
// none (#91). control: a closure with a base rule, called from a constant, is still factored.
func TestARelationWithNoBaseRuleIsNotFactored(t *testing.T) {
	for _, text := range []string{
		`r(?x) :- r(?x); r("a"), node(?y) => ?y`,
		`r(0) :- r(7); r(4), node(?y) => ?y`,
		`r(?a, ?c) :- r(?a, ?b), edge(?b, ?c); r("a", ?y) => ?y`,
	} {
		if rows, diff, err := agree(mustParse(t, text), baseFor(std(graph()))); diff != "" || err != nil || len(rows) != 0 {
			t.Errorf("%s: %d rows, %v\n%s", text, len(rows), err, diff)
		}
	}
	b := baseFor(std(graph()))
	if out := magic(b, mustParse(t, rightReach+`reach("a", ?y) => ?y`)); !strings.Contains(fmt.Sprint(out.Rules), "\x00answer") {
		t.Errorf("control: a right-linear closure from a constant should be factored: %v", out.Rules)
	}
}

// Demand that would derive one relation under two adornments is skipped: for one points-to variable,
// pt/bf and pt/fb between them covered all of pt, twice, at three times the whole analysis's work
// (#96). control: a closure demanded one way keeps its rewrite.
func TestDemandIsSkippedWhenARelationNeedsTwoAdornments(t *testing.T) {
	b := baseFor(std(pointerProgram(1)))
	q := mustParse(t, pointsTo+`pt("p7", ?o) => ?o`)
	if out := magic(b, q); fmt.Sprint(out.Rules) != fmt.Sprint(q.Rules) {
		t.Errorf("pt is demanded bf and fb, so the program should run as written: %d rules", len(out.Rules))
	}
	if _, diff, err := agree(q, b); diff != "" || err != nil {
		t.Errorf("%v\n%s", err, diff)
	}
	if out := magic(b, mustParse(t, leftReach+`reach("a", ?y) => ?y`)); !strings.Contains(fmt.Sprint(out.Rules), magicPrefix) {
		t.Errorf("control: a closure demanded one way should keep its rewrite")
	}
}

// volts holds numbers the way agni does (#148): with display text that isn't the number's canonical
// spelling, "3.3V" for 3.3. spare holds one more, so that the relation reading both has two rules and
// is not inlined away from the demand rewrite.
func volts() *ns.MemSource {
	src := ns.NewMemSource().Declare("volt", "net", "v").Declare("spare", "net", "v")
	for n, v := range map[string]float64{"VDD": 3.3, "VBUS": 5} {
		f := v
		src.Add("volt", ns.Tuple{Vals: []ns.Value{ns.S(n), {S: ftoa(v) + "V", Num: &f, BaseUnit: "V"}}})
	}
	f := 1.8
	src.Add("spare", ns.Tuple{Vals: []ns.Value{ns.S("VIO"), {S: "1.8V", Num: &f, BaseUnit: "V"}}})
	return src
}

// A number the goal spells one way and the data another answers in the data's spelling under every
// evaluator. The planned SemiNaive copies the goal's constant into the relations the demand rewrite
// adds, so before #148 it answered 3.30 where Naive, reading the fact, answered 3.3V. In the first
// query the constant meets volt in the rule the goal calls (factor.go's from relation); in the second
// it passes through a variable into the demand for e, so it meets volt one rule further down.
func TestAnswerKeepsTheDatasSpellingOfANumberTheGoalBinds(t *testing.T) {
	for _, c := range []struct{ text, control string }{
		{`same(?n, ?v, ?v) :- volt(?n, ?v); same(?n, ?v, ?v) :- spare(?n, ?v); same(?n, 3.30, ?out) => ?n, ?out`, "same:from1"},
		{`e(?n, ?v) :- volt(?n, ?v); e(?n, ?v) :- spare(?n, ?v);
		  same(?n, ?v, ?v) :- e(?n, ?v); same(?n, ?v, ?v) :- spare(?n, ?v); same(?n, 3.30, ?out) => ?n, ?out`, "demand:e/fb(?v) :- same:from1(?v)"},
	} {
		rows := eval(t, volts(), c.text)
		if len(rows) != 1 || rows[0].Bind["n"].S != "VDD" || rows[0].Bind["out"].S != "3.3V" {
			t.Errorf("%s:\n rows = %v, want VDD with 3.3V as the data spells it", c.text, binds(rows))
		}
		var r Report
		if _, err := (SemiNaive{}).Eval(bg, mustParse(t, c.text), baseFor(std(volts())), Explain(&r)); err != nil || !strings.Contains(r.String(), c.control) {
			t.Errorf("control: the planned evaluator didn't run %q (%v), so the goal's constant took another path:\n%s", c.control, err, r.String())
		}
	}
}

// The fuzzer's repro (#148): ?x7 is seeded from the goal's 01 and then joined with weight's 1.
func TestANumberSpelledTwoWaysAnswersAlike(t *testing.T) {
	q := mustParse(t, `r0(0, ?x7, ?x7) :- edge(?01, ?00), weight(?0, ?x7); r0(0, 01, ?0)`)
	rows, err := both(q, baseFor(fuzzVocabulary(t)))
	if err != nil || len(rows) != 1 || rows[0].Bind["0"].S != "1" {
		t.Errorf("rows = %v, %v, want ?0 = 1 as weight holds it", binds(rows), err)
	}
}
