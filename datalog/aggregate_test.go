package datalog

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// netlist is four parts' pins on four nets. C1 has both pins on GND, so counting bindings and
// counting distinct values disagree on it: two pins, one net. Parts by distinct nets: C1 1, R1 2,
// R2 2, U1 3. Pins by net: GND 4 (from 3 parts), VCC 2, SDA 2, SCL 1. Resistors carry a value in ohms.
func netlist() *ns.MemSource {
	src := ns.NewMemSource().Declare("pin", "ref", "net", "n").
		DeclareSchema("ohms", ns.Schema{Arity: 2, Labels: []string{"ref", "value"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber, Unit: "ohm"}}})
	for _, p := range []struct {
		ref, net string
		n        float64
	}{{"R1", "GND", 1}, {"R1", "VCC", 2}, {"R2", "GND", 1}, {"R2", "SDA", 2}, {"C1", "GND", 1}, {"C1", "GND", 2},
		{"U1", "VCC", 1}, {"U1", "SDA", 2}, {"U1", "SCL", 3}} {
		src.Add("pin", ns.Tuple{Vals: []ns.Value{ns.S(p.ref), ns.S(p.net), ns.N(p.n)}, Cites: []string{"pin:" + p.ref + "." + ftoa(p.n)}})
	}
	src.Add("ohms", ns.Tuple{Vals: []ns.Value{ns.S("R1"), ns.N(10)}})
	src.Add("ohms", ns.Tuple{Vals: []ns.Value{ns.S("R2"), ns.N(47)}})
	return src
}

func TestARuleCountsPerGroup(t *testing.T) {
	got := cols(eval(t, netlist(), `nets(?r, count(distinct ?n)) :- pin(?r, ?n, _); nets(?r, ?c) => ?r, ?c`), "r", "c")
	if want := []string{"C1 1", "R1 2", "R2 2", "U1 3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("distinct nets per part %v, want %v", got, want)
	}
	rows := eval(t, netlist(), `nets(?r, count(distinct ?n)) :- pin(?r, ?n, _); nets("C1", ?c) => ?c`)
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].Cites, []string{"pin:C1.1", "pin:C1.2"}) {
		t.Errorf("C1's count cites %v, want both of its pins", rows)
	}
	got = cols(eval(t, netlist(), `pins(?r, count(?n)) :- pin(?r, ?n, _); pins(?r, ?c) => ?r, ?c`), "r", "c")
	if want := []string{"C1 2", "R1 2", "R2 2", "U1 3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("pins per part %v, want %v", got, want)
	}
}

// A rule's aggregate reduces what the goal's would: the same functions over the same body give the same
// rows, whether the rule or the projection groups them.
func TestEveryAggregateFunctionInARuleHead(t *testing.T) {
	const fns = `min(?p), max(?p), sum(?p), list(?p), count(distinct ?p)`
	rule := cols(eval(t, netlist(), `pins(?r, `+fns+`) :- pin(?r, _, ?p); pins(?r, ?a, ?b, ?c, ?d, ?e) => ?r, ?a, ?b, ?c, ?d, ?e`), "r", "a", "b", "c", "d", "e")
	goal := cols(eval(t, netlist(), `pin(?r, _, ?p) => ?r, `+fns), "r", "min(p)", "max(p)", "sum(p)", "list(p)", "count(distinct p)")
	if !reflect.DeepEqual(rule, goal) {
		t.Errorf("rule %v, goal %v; want the same rows", rule, goal)
	}
	if want := "U1 1 3 6 1 2 3 3"; len(rule) != 4 || rule[3] != want {
		t.Errorf("U1's row %v, want %q", rule, want)
	}
}

// agni's "exactly two" was "two and not three" with a three-way self-join; a rule that counts says it
// directly, and a constant written as text is read as the count column's number (#65).
func TestExactlyTwoIsACount(t *testing.T) {
	const nets = `nets(?r, count(distinct ?n)) :- pin(?r, ?n, _); `
	selfJoin := col(eval(t, netlist(), `three(?r) :- pin(?r, ?a, _), pin(?r, ?b, _), pin(?r, ?c, _), ?a < ?b, ?b < ?c; `+
		`two(?r) :- pin(?r, ?a, _), pin(?r, ?b, _), ?a < ?b, not three(?r); two(?r) => ?r`), "r")
	if selfJoin != "R1,R2" {
		t.Fatalf("control: the self-join answers %s, want R1,R2", selfJoin)
	}
	for _, q := range []string{nets + `nets(?r, 2) => ?r`, nets + `nets(?r, "2") => ?r`, nets + `two(?r) :- nets(?r, 2); two(?r) => ?r`} {
		if got := col(eval(t, netlist(), q), "r"); got != selfJoin {
			t.Errorf("%s: %s, want %s", q, got, selfJoin)
		}
	}
}

// With no group key there is one group even over nothing, so a count answers 0, as the goal's does
// (agni issue 726). With a group key there is nothing to name a group by, so there are no rows: a
// relation that counts per part has no row for a part with nothing to count.
func TestAnAggregateRuleOverNothing(t *testing.T) {
	rows := eval(t, netlist(), `total(count(?r)) :- pin(?r, "NOPE", _); total(?c) => ?c`)
	if got := cols(rows, "c"); !reflect.DeepEqual(got, []string{"0"}) {
		t.Errorf("an ungrouped count over nothing: %v, want one row of 0", got)
	}
	if rows := eval(t, netlist(), `per(?r, count(?n)) :- pin(?r, ?n, _), ?n = "NOPE"; per(?r, ?c) => ?r, ?c`); len(rows) != 0 {
		t.Errorf("a grouped count over nothing: %v, want no rows", binds(rows))
	}
}

// min, max and sum reduce numbers. With none, over a text column or over nothing, sum is 0 and min
// and max are absent, never the empty text they used to make up (#122).
func TestAnAggregateWithNoNumbers(t *testing.T) {
	render := func(rows []Row, labels ...Var) []string {
		var out []string
		for _, r := range rows {
			var cells []string
			for _, l := range labels {
				switch v := r.Bind[l]; {
				case v.Absent:
					cells = append(cells, "absent")
				case v.Num != nil:
					cells = append(cells, "n:"+v.S)
				default:
					cells = append(cells, "s:"+v.S)
				}
			}
			out = append(out, strings.Join(cells, " "))
		}
		return out
	}
	for _, c := range []struct {
		text   string
		labels []Var
		want   []string
	}{
		// control: over the pin numbers each part has a min.
		{`pin(?r, _, ?n) => ?r, min(?n)`, []Var{"r", "min(n)"}, []string{"s:C1 n:1", "s:R1 n:1", "s:R2 n:1", "s:U1 n:1"}},
		{`pin(?r, ?net, _), ?r = "U1" => ?r, min(?net), max(?net), sum(?net)`, []Var{"r", "min(net)", "max(net)", "sum(net)"}, []string{"s:U1 absent absent n:0"}},
		{`pin(_, "NOPE", ?n) => min(?n), max(?n), sum(?n)`, []Var{"min(n)", "max(n)", "sum(n)"}, []string{"absent absent n:0"}},
		// In a rule head too, and a reader sees absent: it unifies only with absent, and has no order.
		{`lo(min(?n)) :- pin(_, "NOPE", ?n); lo(?m) => ?m`, []Var{"m"}, []string{"absent"}},
		{`lo(min(?n)) :- pin(_, "NOPE", ?n); lo(?m), ?m < 5 => ?m`, []Var{"m"}, nil},
		{`lo(min(?n)) :- pin(_, "NOPE", ?n); w(?x) :- lo(?x); w("") :- pin("R1", "GND", _); w(?x) => ?x`, []Var{"x"}, []string{"absent", "s:"}},
		{`tot(sum(?n)) :- pin(_, "NOPE", ?n); tot(?s), ?s < 5 => ?s`, []Var{"s"}, []string{"n:0"}},
		// having reads the same values: a sum of nothing is under 5, a min of nothing is not.
		{`pin(_, "NOPE", ?n) => sum(?n) having sum(?n) < 5`, []Var{"sum(n)"}, []string{"n:0"}},
		{`pin(_, "NOPE", ?n) => min(?n) having min(?n) < 5`, []Var{"min(n)"}, nil},
	} {
		if got := render(eval(t, netlist(), c.text), c.labels...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %v, want %v", c.text, got, c.want)
		}
	}
}

func TestAConstantInAnAggregatingHead(t *testing.T) {
	got := cols(eval(t, netlist(), `tally(?r, "nets", count(distinct ?n)) :- pin(?r, ?n, _); tally("U1", ?k, ?c) => ?k, ?c`), "k", "c")
	if want := []string{"nets 3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("%v, want %v", got, want)
	}
}

func TestAnAggregateNeedsItsInputComplete(t *testing.T) {
	const recursive = `reach(?a, ?b) :- edge(?a, ?b); reach(?a, ?c) :- reach(?a, ?b), edge(?b, ?c); `
	got := cols(eval(t, graph(), recursive+`far(?a, count(?b)) :- reach(?a, ?b); far(?a, ?n) => ?a, ?n`), "a", "n")
	if want := []string{"a 3", "b 2", "c 1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("a count over a recursive relation: %v, want %v", got, want)
	}
	for _, q := range []string{
		`deg(?n, count(?m)) :- edge(?n, ?m), deg(?m, _); deg(?n, ?d) => ?n`,
		`p(?n, count(?m)) :- q(?n, ?m); q(?n, ?m) :- edge(?n, ?m); q(?n, ?c) :- p(?n, ?c); p(?n, ?c) => ?n`,
	} {
		err := evalErr(graph(), q)
		if err == nil || !strings.Contains(err.Error(), "not stratifiable (recursion through an aggregate: ") {
			t.Errorf("%s: %v, want the recursion-through-an-aggregate refusal", q, err)
		}
		if verr := Validate(mustParse(t, q), std(graph())); fmt.Sprint(verr) != fmt.Sprint(err) {
			t.Errorf("%s: Validate says %v, Eval says %v", q, verr, err)
		}
	}
	if err := evalErr(graph(), `deg(?n, ?m) :- edge(?n, ?m), deg(?m, _); deg(?n, ?d) => ?n`); err != nil {
		t.Errorf("control: the same recursion without the aggregate: %v, want it accepted", err)
	}
	err := evalErr(graph(), `r(?n) :- node(?n), not s(?n); s(?n) :- r(?n); r(?n) => ?n`)
	if err == nil || !strings.Contains(err.Error(), "recursion through negation: ") {
		t.Errorf("recursion through negation: %v, want its own wording kept", err)
	}
}

func TestAggregatingRuleRefusals(t *testing.T) {
	for _, c := range []struct{ q, want string }{
		{`deg(?n, count(?m)) :- edge(?n, ?m); deg(?n, count(?m)) :- edge(?m, ?n); deg(?n, ?d) => ?n`, `rule "deg" aggregates, so it must be the relation's only rule (2 define it)`},
		{`deg(?n, count(?m)) :- edge(?n, ?m); deg(?n, ?m) :- edge(?m, ?n); deg(?n, ?d) => ?n`, `rule "deg" aggregates, so it must be the relation's only rule`},
		{`deg(?n, count(?q)) :- edge(?n, ?m); deg(?n, ?d) => ?n`, `count aggregates ?q, which no relation binds`},
		{`deg(?n, median(?m)) :- edge(?n, ?m); deg(?n, ?d) => ?n`, `unknown aggregate "median"`},
		{`deg(?n, count(?q)) :- edge(?n, ?m), not edge(?m, ?q); deg(?n, ?d) => ?n`, `count aggregates ?q, which no relation binds`},
	} {
		err := evalErr(graph(), c.q)
		if err == nil || !strings.HasPrefix(err.Error(), "query: ") || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error containing %q", c.q, err, c.want)
		}
		if verr := Validate(mustParse(t, c.q), std(graph())); fmt.Sprint(verr) != fmt.Sprint(err) {
			t.Errorf("%s: Validate says %v, Eval says %v", c.q, verr, err)
		}
	}
	if _, err := Parse(`edge(?a, count(?b)) => ?a`); err == nil {
		t.Error("an aggregate in a goal atom parsed, want a parse error")
	}
	count := Term{Agg: &Aggregate{Func: "count", Var: "b"}}
	for _, q := range []Query{
		Build([]Rule{Def(Rel("r", V("a")), Pos(Rel("edge", V("a"), count)))}, []Literal{Pos(Rel("r", V("a")))}),
		Build(nil, []Literal{Pos(Rel("edge", V("a"), V("b"))), Cmp(V("a"), "=", count)}),
	} {
		_, err := both(q, baseFor(std(graph())))
		if err == nil || !strings.Contains(err.Error(), "uses count(?b) in a literal; an aggregate can only stand in a rule head or the answer") {
			t.Errorf("%v: %v, want the aggregate-in-a-literal refusal", q, err)
		}
		if verr := Validate(q, std(graph())); fmt.Sprint(verr) != fmt.Sprint(err) {
			t.Errorf("%v: Validate says %v, Eval says %v", q, verr, err)
		}
	}
}

// on_net is a set, so a part with two pins on one net is on it once, and size counts parts. Inlined, its
// body would yield that part once per pin (#4, as for the goal in TestInliningLeavesABindingCountAlone).
func TestInliningLeavesARuleCountAlone(t *testing.T) {
	if got := cols(eval(t, netlist(), `pin(?r, "GND", _) => count(?r)`), "count(r)"); !reflect.DeepEqual(got, []string{"4"}) {
		t.Fatalf("control: the body yields %v bindings on GND, want 4, so inlining it would change the count", got)
	}
	got := cols(eval(t, netlist(), `on_net(?r, ?n) :- pin(?r, ?n, _); size(?n, count(?r)) :- on_net(?r, ?n); size("GND", ?c) => ?c`), "c")
	if !reflect.DeepEqual(got, []string{"3"}) {
		t.Errorf("parts on GND %v, want 3", got)
	}
}

// Demand stops at an aggregating relation, which is read in full: a caller binding the count column
// names no value of the body to demand.
func TestACallerBindingAnAggregateColumn(t *testing.T) {
	const nets = `nets(?r, count(distinct ?n)) :- pin(?r, ?n, _); `
	for _, c := range []struct{ q, col, want string }{
		{nets + `nets(?r, 3) => ?r`, "r", "U1"},
		{nets + `nets("R2", ?c) => ?c`, "c", "2"},
		{nets + `pin(?r, "SCL", _), nets(?r, ?c) => ?c`, "c", "3"},
	} {
		if got := col(eval(t, netlist(), c.q), c.col); got != c.want {
			t.Errorf("%s: %s, want %s", c.q, got, c.want)
		}
	}
}

func TestAnAggregateColumnsType(t *testing.T) {
	q := mustParse(t, `stats(?r, count(?n), list(?n), sum(?v), min(?n)) :- pin(?r, ?n, _), ohms(?r, ?v); `+
		`stats(?r, ?c, ?l, ?s, ?m) => ?c, ?l, ?s, ?m`)
	got, err := ColumnKinds(q, std(netlist()))
	want := []ColumnKind{{Type: ns.TypeNumber}, {Type: ns.TypeString}, {Type: ns.TypeNumber, Unit: "ohm"}, {Type: ns.TypeNumber}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("%+v, %v; want %+v", got, err, want)
	}
	r := withModules(t, "g", `degree(?n, count(?m)) :- edge(?n, ?m);`)
	e, err := r.Lookup("g.degree")
	if err != nil || e.Signature() != "g.degree(n, arg1: number)" {
		t.Errorf("a module's aggregating member: %v, %v; want g.degree(n, arg1: number)", e.Signature(), err)
	}
	if e.Args[1].Inferred {
		t.Error("g.degree's count is marked inferred, though count fixes its type")
	}
	if rows := evalReg(t, r, `g.degree(?n, 1) => ?n`); col(rows, "n") != "a,b,c" {
		t.Errorf("a module's aggregating member answers %v, want a,b,c", col(rows, "n"))
	}
}

// An aggregate tuple's witness names the rule as written, with no children: like an aggregate answer
// row, it reduces many bindings and proves none of them.
func TestAnAggregateTuplesWitness(t *testing.T) {
	text := `nets(?r, count(distinct ?n)) :- pin(?r, ?n, _); nets(?r, ?c) => ?r, ?c`
	for _, ev := range evaluators {
		got := witnessOf(t, ev, netlist(), text, map[Var]string{"r": "U1"})
		if want := "nets(U1,3)  by nets(?r, count(distinct ?n)) :- pin(?r, ?n, _)\n"; got != want {
			t.Errorf("%T: %q, want %q", ev, got, want)
		}
	}
}

func TestAnAggregatingRulePrintsAsWritten(t *testing.T) {
	const text = `nets(?r, count(distinct ?n), list(?p)) :- pin(?r, ?n, ?p)`
	if got := mustParse(t, text+`; nets(?r, ?c, ?l)`).Rules[0].String(); got != text {
		t.Errorf("%q, want %q", got, text)
	}
}

func TestHeadAggregateSignature(t *testing.T) {
	r := circuit()
	if err := r.AddModule("net", LanguageName, `
covered(?n: net, count(distinct ?c)) :- component.net(?c, ?n);
classes(?n: net, list(?k)) :- component.net(?c, ?n), component.class(?c, ?k);
peak(?n: net, max(?v)) :- net.max_voltage(?n, ?v);
mpns(?n: net, sum(?m)) :- component.net(?c, ?n), component.mpn(?c, ?m);
mean(?n: net, avg(?v)) :- net.max_voltage(?n, ?v);
mpnmean(?n: net, avg(?m)) :- component.net(?c, ?n), component.mpn(?c, ?m);
`, ""); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]struct {
		sig      string
		inferred bool
	}{
		"net.covered": {"net.covered(n: net, arg1: number)", false},
		"net.classes": {"net.classes(n: net, arg1: string)", false},
		"net.peak":    {"net.peak(n: net, arg1: number[V])", false},
		// control: sum over an untyped column is still a number, but in a unit nobody stated.
		"net.mpns":    {"net.mpns(n: net, arg1: number)", true},
		"net.mean":    {"net.mean(n: net, arg1: number[V])", false},
		"net.mpnmean": {"net.mpnmean(n: net, arg1: number)", true},
	} {
		e, err := r.Lookup(path)
		if err != nil {
			t.Fatal(err)
		}
		if e.Signature() != want.sig || e.Args[1].Inferred != want.inferred {
			t.Errorf("%s = %s, inferred %v; want %s, inferred %v", path, e.Signature(), e.Args[1].Inferred, want.sig, want.inferred)
		}
		if e.Args[0].Inferred {
			t.Errorf("%s's declared argument is marked inferred", path)
		}
	}
}
