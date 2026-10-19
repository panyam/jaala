package datalog

import (
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// parted is netlist with a part relation that also holds X9, a part with no pins and no value.
func parted() *ns.MemSource {
	src := netlist().Declare("part", "ref")
	for _, r := range []string{"C1", "R1", "R2", "U1", "X9"} {
		src.Add("part", ns.Tuple{Vals: []ns.Value{ns.S(r)}, Cites: []string{"part:" + r}})
	}
	return src
}

// shown renders a column as the answer holds it, so absent, a number and text read apart.
func shown(rows []Row, names ...Var) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		cells := make([]string, len(names))
		for j, n := range names {
			switch v := r.Bind[n]; {
			case v.Absent:
				cells[j] = "absent"
			case v.Num != nil:
				cells[j] = "n:" + v.S
			default:
				cells[j] = "s:" + v.S
			}
		}
		out[i] = strings.Join(cells, " ")
	}
	return out
}

// An aggregate in a rule body reduces per value of what it shares with the rest of the body, so a
// part with no pins counts 0 (#71). A head aggregate has no group for it.
func TestABodyAggregateCountsAGroupWithNoBindings(t *testing.T) {
	got := shown(eval(t, parted(), `nets(?r, ?c) :- part(?r), ?c = count(distinct ?n) : { pin(?r, ?n, _) }; nets(?r, ?c) => ?r, ?c`), "r", "c")
	if want := []string{"s:C1 n:1", "s:R1 n:2", "s:R2 n:2", "s:U1 n:3", "s:X9 n:0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("distinct nets per part %v, want %v", got, want)
	}
	// control: the head aggregate over the same body has no row for X9.
	got = shown(eval(t, parted(), `nets(?r, count(distinct ?n)) :- pin(?r, ?n, _); nets(?r, ?c) => ?r, ?c`), "r", "c")
	if want := []string{"s:C1 n:1", "s:R1 n:2", "s:R2 n:2", "s:U1 n:3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("control: the head aggregate %v, want %v", got, want)
	}
}

// Over no bindings a body aggregate answers what an aggregate over nothing does (#122), and in a goal
// as in a rule. A comparison after it reads the value, and refuses an absent one.
func TestABodyAggregateOverNothing(t *testing.T) {
	for _, c := range []struct {
		text  string
		names []Var
		want  []string
	}{
		{`part(?r), ?r = "X9", ?c = count(?n) : { pin(?r, ?n, _) }, ?s = sum(?k) : { pin(?r, _, ?k) }, ?m = min(?k) : { pin(?r, _, ?k) }, ?l = list(?n) : { pin(?r, ?n, _) } => ?r, ?c, ?s, ?m, ?l`,
			[]Var{"r", "c", "s", "m", "l"}, []string{"s:X9 n:0 n:0 absent s:"}},
		{`part(?r), ?m = max(?v) : { ohms(?r, ?v) } => ?r, ?m`, []Var{"r", "m"},
			[]string{"s:C1 absent", "s:R1 n:10", "s:R2 n:47", "s:U1 absent", "s:X9 absent"}},
		{`part(?r), ?m = max(?v) : { ohms(?r, ?v) }, ?m < 20 => ?r`, []Var{"r"}, []string{"s:R1"}},
		{`part(?r), ?c = count(?n) : { pin(?r, ?n, _) }, ?c < 2 => ?r`, []Var{"r"}, []string{"s:X9"}},
		// With nothing shared there is one group, even over nothing.
		{`part(?r), ?c = count(?n) : { pin(_, ?n, "NOPE") } => ?r, ?c`, []Var{"r", "c"},
			[]string{"s:C1 n:0", "s:R1 n:0", "s:R2 n:0", "s:U1 n:0", "s:X9 n:0"}},
	} {
		if got := shown(eval(t, parted(), c.text), c.names...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n  %v, want %v", c.text, got, c.want)
		}
	}
}

// The braces are asked about what the rest of the body binds without their value, so a filter that
// reads the value runs after it, never in the question.
func TestABodyAggregateValueCanFeedAFilter(t *testing.T) {
	got := shown(eval(t, parted(), `part(?r), ?c = count(?n) : { pin(?r, ?n, _) }, str.contains(?r, ?c) => ?r, ?c`), "r", "c")
	if want := []string{"s:R2 n:2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("parts whose ref holds their pin count %v, want %v", got, want)
	}
}

// Every variable the braces share is a key: two shared, the count is per part and net, and a pair the
// rest of the body makes with no pin counts 0.
func TestABodyAggregateKeysOnEverySharedVariable(t *testing.T) {
	got := shown(eval(t, parted(), `part(?r), pin(_, ?n, _), ?n = "GND", ?c = count(?p) : { pin(?r, ?n, ?p) } => ?r, ?c`), "r", "c")
	if want := []string{"s:C1 n:2", "s:R1 n:1", "s:R2 n:1", "s:U1 n:0", "s:X9 n:0"}; !reflect.DeepEqual(got, want) {
		t.Errorf("GND pins per part %v, want %v", got, want)
	}
}

// A row cites the facts of its own binding and those the braces reduced, never the facts that
// worked out which values the braces were asked about: U1's three pins give three rows over no ohms,
// and each cites its own pin.
func TestABodyAggregateCitesWhatItReduced(t *testing.T) {
	q := `pin(?r, ?n, _), ?r = "U1", ?v = count(?k) : { ohms(?r, ?k) } => ?n, ?v`
	for _, opts := range [][]Option{nil, {CanonicalCites()}} {
		rows, err := both(mustParse(t, q), baseFor(std(parted())), opts...)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, r := range rows {
			got = append(got, r.Bind["n"].S+" "+strings.Join(r.Cites, ","))
		}
		if want := []string{"SCL pin:U1.3", "SDA pin:U1.2", "VCC pin:U1.1"}; !reflect.DeepEqual(got, want) {
			t.Errorf("%v: cites %v, want %v", opts, got, want)
		}
	}
	rows := eval(t, parted(), `part(?r), ?r = "R1", ?v = sum(?k) : { ohms(?r, ?k) } => ?v`)
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].Cites, []string{"part:R1"}) {
		// ohms carries no citations in netlist, so the part's own is all there is.
		t.Errorf("R1's sum cites %v, want only its part", rows)
	}
	rows = eval(t, parted(), `part(?r), ?r = "R1", ?v = count(?n) : { pin(?r, ?n, _) } => ?v`)
	if len(rows) != 1 || !reflect.DeepEqual(rows[0].Cites, []string{"part:R1", "pin:R1.1", "pin:R1.2"}) {
		t.Errorf("R1's count cites %v, want its part and both pins", rows)
	}
}

// A witness shows the literal as written: one node for the aggregate, with its shared values and its
// own, and no children, as an aggregate answer row has none, whether the braces matched (R1) or not
// (X9). The clause's node keeps its written rule.
func TestABodyAggregateWitness(t *testing.T) {
	for _, c := range []struct{ ref, want string }{{"X9", "s:X9 n:0"}, {"R1", "s:R1 n:2"}} {
		testABodyAggregateWitness(t, c.ref, c.want)
	}
}

func testABodyAggregateWitness(t *testing.T, ref, want string) {
	q := mustParse(t, `nets(?r, ?c) :- part(?r), ?c = count(?n) : { pin(?r, ?n, _) }; nets("`+ref+`", ?c) => ?c`)
	for _, opts := range [][]Option{{Witnesses()}, {Witnesses(), CanonicalCites()}} {
		rows, err := both(q, baseFor(std(parted())), opts...)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 1 || len(rows[0].Witness) != 1 {
			t.Fatalf("rows %+v, want one row with one witness", rows)
		}
		w := rows[0].Witness[0]
		if w.Relation != "nets" || w.Rule != `nets(?r, ?c) :- part(?r), ?c = count(?n) : { pin(?r, ?n, _) }` || len(w.Children) != 2 {
			t.Fatalf("nets node %+v, want the rule as written over two children", w)
		}
		agg := w.Children[1]
		if agg.Relation != "count(?n)" || agg.Rule != `?c = count(?n) : { pin(?r, ?n, _) }` || len(agg.Children) != 0 ||
			shown([]Row{{Bind: map[Var]ns.Value{"r": agg.Values[0], "c": agg.Values[1]}}}, "r", "c")[0] != want {
			t.Errorf("aggregate node %+v, want count(?n) over %s, with no children", agg, want)
		}
	}
}

// What a body aggregate can't run is refused, by Validate and by every evaluator, with the literal as
// written in the message.
func TestABodyAggregateRefusals(t *testing.T) {
	v := std(parted())
	for _, c := range []struct{ text, want string }{
		{`part(?r), ?c = count(?n) : { pin(?r, ?n, _), ?c > 1 } => ?r`, `query: ?c = count(?n) : { pin(?r, ?n, _), ?c > 1 } uses ?c inside its own braces`},
		{`part(?r), ?c = count(?n) : { pin(?r, ?n, _), ?d = count(?m) : { pin(?r, ?m, _) } } => ?r`, `holds another aggregate in its braces`},
		{`part(?r), ?c = count(?n) : { pin(?r, ?n, _), ?n != ?q }, ?q = "GND" => ?r`, `shares ?q with the rest of its body, so a relation outside the braces must bind it`},
		{`part(?r), ?c = count(?n) : { pin(_, ?n, _), ?r != ?n } => ?r, ?c`, `shares ?r with the rest of its body, so a relation inside the braces must bind it too`},
		{`part(?r), ?c = count(?z) : { pin(?r, ?n, _) } => ?r`, `aggregates ?z, which no relation in its braces binds`},
		{`part(?r), ?c = median(?n) : { pin(?r, ?n, _) } => ?r`, `unknown aggregate "median"`},
		{`part(?r), ?c = count(?n) : { pin(?r, ?n, _) }, ?c = sum(?n) : { pin(?r, _, ?n) } => ?r`, `two aggregates bind ?c`},
		{`part(?r), ?c = count(?n) : { pni(?r, ?n, _) } => ?r`, `query: the aggregate ?c = count(?n) : { pni(?r, ?n, _) } reads unknown relation "pni"`},
		{`part(?r), ?c = count(?n) : { pin(?r, ?n) } => ?r`, `relation "pin" takes 3 args, got 2`},
		{`r(?x, ?c) :- part(?x), ?c = count(?y) : { r(?x, ?y) }; r(?x, ?c) => ?x`, `not stratifiable (recursion through an aggregate: count(?y), r)`},
	} {
		q, err := Parse(c.text)
		if err != nil {
			t.Fatalf("%s: %v", c.text, err)
		}
		if err := Validate(q, v); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Validate(%s) = %v, want %q", c.text, err, c.want)
		}
		if err := evalErr(parted(), c.text); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Eval(%s) = %v, want %q", c.text, err, c.want)
		}
	}
	for _, c := range []struct{ text, want string }{
		{`part(?r), ?c = count(?n) : { } => ?r`, `query: the aggregate binding ?c has an empty body`},
		{`part(?r), count(?n) : { pin(?r, ?n, _) } => ?r`, `must name the variable an aggregate binds`},
		{`part(?r), ?c = ?n : { pin(?r, ?n, _) } => ?r`, `is not an aggregate`},
		{`part(?r), _ = count(?n) : { pin(?r, ?n, _) } => ?r`, `binds a ?variable, not _`},
	} {
		if _, err := Parse(c.text); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Parse(%s) = %v, want %q", c.text, err, c.want)
		}
	}
}

// A body aggregate prints back as written, so a parsed rule round-trips (FuzzParse's property).
func TestABodyAggregatePrintsAsWritten(t *testing.T) {
	text := `nets(?r, ?c) :- part(?r), ?c = count(distinct ?n) : { pin(?r, ?n, _), not ohms(?r, _) }, ?c > 1`
	rules, err := ParseRules(text)
	if err != nil || len(rules) != 1 || rules[0].String() != text {
		t.Errorf("%q parses to %v, %v", text, rules, err)
	}
}

// A module's rule can hold one, its signature types the value, and a host binding the shared variable
// to one value or several reads only those groups.
func TestABodyAggregateInAModuleAndBound(t *testing.T) {
	r := withModules(t, "g", `outdeg(?a, ?c) :- node(?a), ?c = count(?b) : { edge(?a, ?b) };`)
	if got := cols(evalReg(t, r, `g.outdeg(?a, ?c) => ?a, ?c`), "a", "c"); !reflect.DeepEqual(got, []string{"a 1", "b 1", "c 1", "d 0", "x 0"}) {
		t.Errorf("out-degrees %v", got)
	}
	sigs, err := Language.Check(r)
	if err != nil || len(sigs["g.outdeg"]) != 2 || sigs["g.outdeg"][1].Type != ns.TypeNumber {
		t.Errorf("g.outdeg's signature %v, %v, want ?c a number", sigs["g.outdeg"], err)
	}
	q := mustParse(t, `node(?a), ?c = count(?b) : { edge(?a, ?b) } => ?a, ?c`)
	for _, c := range []struct {
		vals []ns.Value
		want []string
	}{
		{[]ns.Value{ns.S("a")}, []string{"a 1"}},
		{[]ns.Value{ns.S("a"), ns.S("d")}, []string{"a 1", "d 0"}},
	} {
		rows, err := both(q, baseFor(std(graph())), Bind(map[Var][]ns.Value{"a": c.vals}))
		if got := cols(rows, "a", "c"); err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("bound to %v: %v, %v, want %v", c.vals, got, err, c.want)
		}
	}
}
