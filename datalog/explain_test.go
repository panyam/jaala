package datalog

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

// explained answers query over b under ev with Explain, and returns the report.
func explained(t *testing.T, ev Evaluator, query string, b *Base, opts ...Option) *Report {
	t.Helper()
	var r Report
	if _, err := ev.Eval(bg, mustParse(t, query), b, append(opts, Explain(&r))...); err != nil {
		t.Fatalf("%T: %s: %v", ev, query, err)
	}
	return &r
}

func relationIn(r *Report, rel string) *RelationReport {
	for _, rr := range r.Relations {
		if rr.Relation == rel {
			return rr
		}
	}
	return nil
}

func ruleRan(r *Report, prefix string) *BodyReport {
	for _, br := range r.Rules {
		if strings.HasPrefix(br.Ran, prefix) {
			return br
		}
	}
	return nil
}

// On #139's shape, one run says where the work went (#147): has_tp evaluated under demand for the two
// nets C3 is on, with its work, and the order its body ran in, from the demanded net to its pins and
// then their parts; probed_both walked from C3; tt inlined into it. control: asked for every
// capacitor, has_tp is evaluated in full, so the report tells the two apart.
func TestExplainShowsWhereADemandedRelationSpentItsWork(t *testing.T) {
	r := explained(t, SemiNaive{}, probed+`probed_both("C3")`, baseFor(std(probedBoard(50))))
	tp := relationIn(r, "has_tp")
	if tp == nil || tp.How != "demand" || fmt_(tp.Adornments) != "b" || tp.Demanded != 2 || tp.Work == 0 {
		t.Fatalf("has_tp: %+v; want demand [b], 2 demanded, some work\n%s", tp, r)
	}
	if pb, tt := relationIn(r, "probed_both"), relationIn(r, "tt"); pb == nil || pb.How != "factored" || tt == nil || tt.How != "inlined" {
		t.Errorf("probed_both %+v, tt %+v; want factored and inlined\n%s", pb, tt, r)
	}
	body := ruleRan(r, "has_tp/b(?n) :- demand:has_tp/b(?n)")
	if body == nil || body.Rule != `has_tp(?n) :- pin(?tp, ?n), part(?tp, "test_point")` || len(body.Literals) != 3 ||
		body.Literals[1].Literal != "pin(?tp, ?n)" || fmt_(body.Literals[1].Access) != "index (net)" || body.Literals[1].Reached != 2 {
		t.Errorf("has_tp's body: %+v; want it run from its demand, then pin by its net, reached once per net\n%s", body, r)
	}
	full := explained(t, SemiNaive{}, probed+`probed_both(?r) => count(?r)`, baseFor(std(probedBoard(50))))
	if tp := relationIn(full, "has_tp"); tp == nil || tp.How != "full" || tp.Demanded != 0 {
		t.Errorf("control: asked for every capacitor, has_tp is %+v; want full", tp)
	}
}

func fmt_(ss []string) string { return strings.Join(ss, " | ") }

// Every unit of work is counted against a body, and every body's against its relation: the goal's
// and the rules' add up to the total, from every evaluator, through recursion, negation, aggregates,
// a generator and demand. Explaining changes neither the rows nor the work.
func TestExplainAccountsForEveryUnitOfWork(t *testing.T) {
	queries := []struct {
		src   func() *Base
		query string
	}{
		{func() *Base { return baseFor(std(reversedLine(30))) }, closure + `reach("v0", ?b), not edge(?b, "v9") => ?b`},
		{func() *Base { return baseFor(std(reversedLine(30))) }, closure + `reach(?a, ?b) => ?a, count(?b)`},
		{func() *Base { return baseFor(std(probedBoard(30))) }, probed + `probed_both(?r) => count(?r)`},
		{func() *Base { return baseFor(std(graph())) }, `deg(?n, count(?m)) :- edge(?n, ?m); node(?n), not deg(?n, _) => ?n`},
		{func() *Base { return MustBase(std(fanned(40)), batchingAt(fanned(40))) }, fannedQueries[1].text},
	}
	for _, q := range queries {
		for _, ev := range evaluators {
			plainBase := q.src()
			plain, err := ev.Eval(bg, mustParse(t, q.query), plainBase)
			if err != nil {
				t.Fatal(err)
			}
			b := q.src()
			var r Report
			rows, err := ev.Eval(bg, mustParse(t, q.query), b, Explain(&r))
			if err != nil || !reflect.DeepEqual(binds(rows), binds(plain)) || b.Work() != plainBase.Work() {
				t.Errorf("%T %s: explained %v, %v, work %d; want the plain answer and work %d", ev, q.query, binds(rows), err, b.Work(), plainBase.Work())
			}
			sum := r.Goal.Work
			for _, br := range r.Rules {
				sum += br.Work
				lits := int64(0)
				for _, l := range br.Literals {
					lits += l.Work
				}
				if lits > br.Work {
					t.Errorf("%T: %s: literals' work %d over the body's %d", ev, br.Ran, lits, br.Work)
				}
			}
			if sum != r.Work || r.Work != b.Work() || r.Rows != len(rows) {
				t.Errorf("%T %s: bodies' work %d, report %d, Base %d, rows %d of %d\n%s", ev, q.query, sum, r.Work, b.Work(), r.Rows, len(rows), &r)
			}
			var rel int64
			for _, rr := range r.Relations {
				rel += rr.Work
			}
			if rel > r.Work-r.Goal.Work {
				t.Errorf("%T %s: relations' work %d over the rules' %d", ev, q.query, rel, r.Work-r.Goal.Work)
			}
		}
	}
}

// A second query over the Base reads what the first derived and read: the relation reused at no
// work, what only it read left out, the base relations cached, their indexes kept. control: the first
// query derived and read them.
func TestExplainShowsWhatTheBaseKept(t *testing.T) {
	b := baseFor(std(probedBoard(30)))
	q := probed + `probed_both(?r) => count(?r)`
	first := explained(t, SemiNaive{}, q, b)
	second := explained(t, SemiNaive{}, q, b)
	for _, c := range []struct {
		r          *Report
		how        string
		cached     bool
		indexBuilt bool
	}{{first, "full", false, true}, {second, "reused", true, false}} {
		rr := relationIn(c.r, "probed_both")
		if rr == nil || rr.How != c.how || (c.how == "reused" && rr.Work != 0) {
			t.Errorf("probed_both: %+v; want %s", rr, c.how)
		}
		for _, s := range c.r.Sources {
			if s.Cached != c.cached {
				t.Errorf("%s: cached %v, want %v", s.Relation, s.Cached, c.cached)
			}
			for _, x := range s.Indexes {
				if x.Built != c.indexBuilt {
					t.Errorf("%s index %v: built %v, want %v", s.Relation, x.On, x.Built, c.indexBuilt)
				}
			}
		}
	}
	// tt was inlined into probed_both the first time; reading probed_both, the second query needs none of it.
	if tt := relationIn(first, "tt"); tt == nil || tt.How != "inlined" {
		t.Errorf("first query's tt: %+v, want inlined", tt)
	}
	if tt := relationIn(second, "tt"); tt != nil {
		t.Errorf("second query's tt: %+v, want it left out, since what it fed was reused", tt)
	}
	if len(first.Sources) == 0 || len(first.Sources[0].Indexes) == 0 {
		t.Errorf("control: the first query read no source through an index\n%s", first)
	}
}

// A query its Budget stops still reports where its work went, with the error.
func TestExplainReportsAQueryItsBudgetStopped(t *testing.T) {
	var r Report
	_, err := SemiNaive{}.Eval(bg, mustParse(t, closure+`reach(?a, ?b) => ?a, ?b`), baseFor(std(chainOf(60))), Budget(500), Explain(&r))
	var over *BudgetExceeded
	if !errors.As(err, &over) {
		t.Fatalf("err = %v, want the budget stop", err)
	}
	if r.Error != err.Error() || r.Budget != 500 || r.Work <= 500 || len(r.Rules) == 0 || relationIn(&r, "reach") == nil {
		t.Errorf("report: %s; want the error, the budget, work past it, and the rules that spent it", &r)
	}
}

// withoutTimes is r with its wall-clock times zeroed, so it compares and prints the same on every run.
func withoutTimes(r *Report) *Report {
	c := *r
	c.Elapsed = 0
	for _, br := range append([]*BodyReport{c.Goal}, c.Rules...) {
		if br != nil {
			br.Elapsed = 0
		}
	}
	for _, rr := range c.Relations {
		rr.Elapsed = 0
	}
	return &c
}

// The report's text and JSON hold their shape: the text against testdata/explain.golden (go test
// ./datalog -run TestExplainHoldsItsShape -update rewrites it), the JSON by reading back the same.
func TestExplainHoldsItsShape(t *testing.T) {
	var text strings.Builder
	for _, q := range []struct {
		b     *Base
		query string
	}{
		{baseFor(std(probedBoard(20))), probed + `probed_both("C3")`},
		{baseFor(std(reversedLine(10))), closure + `reach("v0", ?b), not edge(?b, "v9") => ?b`},
	} {
		r := withoutTimes(explained(t, SemiNaive{}, q.query, q.b))
		text.WriteString("# " + q.query + "\n" + r.String() + "\n")
		data, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var back Report
		if err := json.Unmarshal(data, &back); err != nil || !reflect.DeepEqual(&back, r) {
			t.Errorf("JSON doesn't read back as the report: %v\n%s", err, data)
		}
		if !strings.Contains(string(data), `"relations":[{"relation":`) {
			t.Errorf("JSON lost its field names: %s", data)
		}
	}
	const golden = "testdata/explain.golden"
	if *updateWork {
		if err := os.WriteFile(golden, []byte(text.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if text.String() != string(want) {
		t.Errorf("the report's text changed; if on purpose, rerun with -update\ngot:\n%s", text.String())
	}
}

// A body written comparison-first runs its comparison after the literal that binds it (#89), and the
// report lists the literals in that order, so each literal's counts are its own: the comparison is
// reached once per edge and passes the one from a, and edge is reached once.
func TestExplainListsADeferredComparisonWhereItRan(t *testing.T) {
	var r Report
	if _, err := (Naive{}).Eval(bg, mustParse(t, `?a = "a", edge(?a, ?b) => ?b`), baseFor(std(graph())), Explain(&r)); err != nil {
		t.Fatal(err)
	}
	lits := r.Goal.Literals
	if len(lits) != 2 || lits[0].Literal != "edge(?a, ?b)" || lits[1].Literal != `?a = "a"` {
		t.Fatalf("goal literals = %v, want edge then the comparison", literalNames(lits))
	}
	if lits[0].Reached != 1 || lits[0].Passed != 3 || lits[1].Reached != 3 || lits[1].Passed != 1 {
		t.Errorf("edge reached %d passed %d, comparison reached %d passed %d; want 1/3 and 3/1",
			lits[0].Reached, lits[0].Passed, lits[1].Reached, lits[1].Passed)
	}
}

func literalNames(lits []*LiteralReport) []string {
	var out []string
	for _, l := range lits {
		out = append(out, l.Literal)
	}
	return out
}

// An empty answer says where it lost its last binding (#343), the same from every evaluator: the
// first literal that passed nothing on, the call that first matched nothing with what the literals
// before it had bound, and through a derived relation that derived nothing, the rule and literal
// that left it empty. control: a count over nothing answers a row though its literal passed nothing
// on, and has no Empty.
func TestExplainSaysWhereAnEmptyAnswerLostItsLastBinding(t *testing.T) {
	cases := []struct{ query, want string }{
		{`edge("a", ?x), edge(?x, "a") => ?x`, `edge(?x, "a") matched nothing, even alone (first called as edge("b", "a"))`},
		{`edge("a", ?x), edge(?x, "d") => ?x`, `edge(?x, "d") matched nothing here, though it matches alone (first called as edge("b", "d"))`},
		{`edge("zz", ?b) => ?b`, `edge("zz", ?b) matched nothing, even alone`},
		{`edge(?a, ?b), ?b = "q" => ?a`, `?b = "q" held for none (first called as "b" = "q")`},
		{`node(?n), not weight(?n, _) => ?n`, `not weight(?n, _) let nothing through (first called as not weight("a", _))`},
		{`far(?a, ?c) :- edge(?a, ?b), edge(?b, ?c), weight(?c, 99); far(?a, ?c) :- edge(?c, ?a), weight(?c, 99); far(?x, ?y) => ?x`,
			"far(?x, ?y) matched nothing, even alone\n" +
				`  because weight(?c, 99) matched nothing, even alone (first called as weight("c", 99)), in far(?a, ?c) :- edge(?a, ?b), edge(?b, ?c), weight(?c, 99)` + "\n" +
				`  because weight(?c, 99) matched nothing, even alone (first called as weight("a", 99)), in far(?a, ?c) :- edge(?c, ?a), weight(?c, 99)`},
	}
	// The planned SemiNaive's own Empty, where its plan differs from the written order's: an equality to
	// text binds its variable first (#370), so the call it empties is the culprit.
	planned := map[string]string{
		`edge(?a, ?b), ?b = "q" => ?a`: `edge(?a, ?b) matched nothing here, though it matches alone (first called as edge(?a, "q"))`,
	}
	for _, c := range cases {
		for _, ev := range []Evaluator{Naive{}, SemiNaive{WrittenOrder: true}} {
			r := explained(t, ev, c.query, baseFor(std(graph())))
			if r.Empty == nil || r.Empty.String() != c.want {
				t.Errorf("%T: %s: empty\n%v\nwant\n%s", ev, c.query, r.Empty, c.want)
			}
		}
		r := explained(t, SemiNaive{}, c.query, baseFor(std(graph())))
		if want, ok := planned[c.query]; ok {
			if r.Empty == nil || r.Empty.String() != want {
				t.Errorf("planned: %s: empty\n%v\nwant\n%s", c.query, r.Empty, want)
			}
			continue
		}
		if r.Empty == nil || r.Empty.Literal == "" {
			t.Errorf("planned: %s: no Empty\n%s", c.query, r)
		} else if probed := r.Empty.Alone != nil; probed != strings.Contains(c.want, "alone") {
			t.Errorf("planned: %s: Alone is %v\n%s", c.query, r.Empty.Alone, r)
		}
	}
	r := explained(t, SemiNaive{}, `edge("zz", ?b) => count(?b)`, baseFor(std(graph())))
	if r.Rows != 1 || r.Goal.Literals[0].Passed != 0 || r.Empty != nil {
		t.Errorf("control: a count over nothing, Empty is %v\n%s", r.Empty, r)
	}
}

// An empty answer's culprit is run alone over a store only when that can't read the store whole: a
// looked-up relation called with no constant isn't, unless the Eval read it whole. control: with a
// constant it is, at one lookup.
func TestExplainRunsAnEmptyAnswersCulpritAloneOnlyWhereTheStoreIsAsked(t *testing.T) {
	for _, c := range []struct {
		query   string
		probed  bool
		lookups int
	}{
		{`edge("a", ?x), edge(?x, ?y), edge(?y, ?x) => ?x`, false, 0},
		{`edge("a", ?x), edge(?x, ?y), edge(?y, "a") => ?x`, true, 1},
		{`edge(?a, ?b), edge(?b, ?a) => ?a`, true, 0}, // edge was read whole, so it is held
	} {
		plain, store := lookingAt(graph()), lookingAt(graph())
		if _, err := (SemiNaive{WrittenOrder: true}).Eval(bg, mustParse(t, c.query), baseFor(std(plain))); err != nil {
			t.Fatal(err)
		}
		r := explained(t, SemiNaive{WrittenOrder: true}, c.query, baseFor(std(store)))
		if r.Empty == nil || (r.Empty.Alone != nil) != c.probed {
			t.Errorf("%s: Empty %v, Alone %v; want probed %v", c.query, r.Empty, r.Empty.Alone, c.probed)
		}
		if store.wholes["edge"] != plain.wholes["edge"] || store.lookups["edge"] != plain.lookups["edge"]+c.lookups {
			t.Errorf("%s: %d whole reads and %d lookups; without Explain %d and %d, want %d more lookups",
				c.query, store.wholes["edge"], store.lookups["edge"], plain.wholes["edge"], plain.lookups["edge"], c.lookups)
		}
	}
}

// The probe is bounded by the Eval's Budget and counts toward neither its Work nor the Base's. The
// second Eval reuses the derived reach the first kept, so only its probe, on a copy keeping nothing,
// derives it again. control: without a budget the same culprit is probed.
func TestExplainBoundsAnEmptyAnswersProbe(t *testing.T) {
	q := closure + `edge("v57", ?x), reach(?x, "v57") => ?x`
	b := baseFor(std(chainOf(60)))
	full := explained(t, SemiNaive{}, q, b)
	if full.Empty == nil || full.Empty.Alone == nil || !*full.Empty.Alone || full.Work != b.Work() {
		t.Fatalf("control: Alone %v, work %d against the Base's %d\n%s", full.Empty.Alone, full.Work, b.Work(), full)
	}
	r := explained(t, SemiNaive{}, q, b, Budget(100))
	if r.Empty == nil || r.Empty.Alone != nil || r.Work > 10 {
		t.Errorf("Alone %v under a budget of 100\n%s", r.Empty.Alone, r)
	}
}

// A literal the budget ran out in is marked, and only that one. control: without a budget none is.
func TestExplainMarksTheLiteralTheBudgetStoppedIn(t *testing.T) {
	stopped := func(r *Report) (out []string) {
		for _, br := range append([]*BodyReport{r.Goal}, r.Rules...) {
			if br == nil {
				continue
			}
			for _, l := range br.Literals {
				if l.Stopped {
					out = append(out, l.Literal)
				}
			}
		}
		return out
	}
	var r Report
	SemiNaive{}.Eval(bg, mustParse(t, closure+`reach(?a, ?b) => ?a, ?b`), baseFor(std(chainOf(60))), Budget(500), Explain(&r))
	if got := stopped(&r); len(got) != 1 || !strings.Contains(r.String(), "stopped by the budget") {
		t.Errorf("stopped in %v; want one literal\n%s", got, &r)
	}
	full := explained(t, SemiNaive{}, closure+`reach(?a, ?b) => ?a, ?b`, baseFor(std(chainOf(60))))
	if got := stopped(full); len(got) != 0 {
		t.Errorf("control: without a budget, stopped in %v", got)
	}
}
