package datalog

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// params is a datasheet's parameters, one relation for every unit. U1's are in three units; U2's
// are all volts; U3 has a volt and a bare number, which takes the volt; U4 has 5 V and 5 A, one
// number in two units, so dedup on the number alone would hide the mix.
func params() *ns.MemSource {
	src := ns.NewMemSource().DeclareSchema("param", ns.Schema{Arity: 3, Labels: []string{"part", "sym", "value"}, Types: []ns.ArgType{{}, {}, {Type: ns.TypeNumber}}})
	for _, p := range []struct {
		part, sym string
		v         ns.Value
	}{
		{"U1", "vmax", ns.NU(5, "V")}, {"U1", "imax", ns.NU(0.5, "A")}, {"U1", "rpull", ns.NU(10000, "ohm")},
		{"U2", "vmax", ns.NU(3, "V")}, {"U2", "vmin", ns.NU(2, "V")},
		{"U3", "vref", ns.NU(1.25, "V")}, {"U3", "gain", ns.N(2)},
		{"U4", "vin", ns.NU(5, "V")}, {"U4", "iin", ns.NU(5, "A")},
	} {
		src.Add("param", ns.Tuple{Vals: []ns.Value{ns.S(p.part), ns.S(p.sym), p.v}})
	}
	return src
}

// withUnits writes each row's columns as text, a number with its unit in brackets, absent as - with
// the row's Why for it, if any, in parentheses.
func withUnits(rows []Row, cols ...string) []string {
	var out []string
	for _, r := range rows {
		var vals []string
		for _, c := range cols {
			v := r.Bind[Var(c)]
			switch {
			case v.Absent && r.Why[Var(c)] != "":
				vals = append(vals, "-("+r.Why[Var(c)]+")")
			case v.Absent:
				vals = append(vals, "-")
			case v.BaseUnit != "":
				vals = append(vals, v.S+"["+v.BaseUnit+"]")
			default:
				vals = append(vals, v.S)
			}
		}
		out = append(out, strings.Join(vals, " "))
	}
	return out
}

func TestAnAggregateOverMixedUnitsIsAbsent(t *testing.T) {
	rows := eval(t, params(), `param(?p, _, ?v) => ?p, max(?v), min(?v), sum(?v), count(?v)`)
	got := withUnits(rows, "p", "max(v)", "min(v)", "sum(v)", "count(v)")
	want := []string{
		"U1 -(mixed units: A, V, ohm) -(mixed units: A, V, ohm) -(mixed units: A, V, ohm) 3",
		"U2 3[V] 2[V] 5[V] 2", // control: one unit reduces, and keeps it
		"U3 2[V] 1.25[V] 3.25[V] 2",
		"U4 -(mixed units: A, V) -(mixed units: A, V) -(mixed units: A, V) 2",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("per part = %v, want %v", got, want)
	}
	rows = eval(t, params(), `param(?p, _, ?v) => ?p, sum(distinct ?v)`)
	if got, want := withUnits(rows, "p", "sum(distinct v)"), []string{"U1 -(mixed units: A, V, ohm)", "U2 5[V]", "U3 3.25[V]", "U4 -(mixed units: A, V)"}; !reflect.DeepEqual(got, want) {
		t.Errorf("sum(distinct) = %v, want %v: U4's 5 V and 5 A are one number in two units", got, want)
	}
}

func TestAnAggregateInARuleOverMixedUnitsIsAbsent(t *testing.T) {
	for _, c := range []struct {
		name, query string
		want        []string
	}{
		{"head", `peak(?p, max(?v)) :- param(?p, _, ?v); peak(?p, ?m) => ?p, ?m`, []string{"U1 -", "U2 3[V]", "U3 2[V]", "U4 -"}},
		{"body", `param(?p, "vmax", _), ?m = max(?v) : { param(?p, _, ?v) } => ?p, ?m`, []string{"U1 -", "U2 3[V]"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := withUnits(eval(t, params(), c.query), "p", "m"); !reflect.DeepEqual(got, c.want) {
				t.Errorf("peaks = %v, want %v", got, c.want)
			}
		})
	}
}

func TestNumberPartsSayWhatAMixedAggregateMeans(t *testing.T) {
	rows := eval(t, params(), `param("U1", _, ?v), num.value(?v, ?n) => max(?n)`)
	if got := withUnits(rows, "max(n)"); !reflect.DeepEqual(got, []string{"10000"}) {
		t.Errorf("U1's max without units = %v, want 10000", got)
	}
	rows = eval(t, params(), `param(?p, _, ?v), num.unit(?v, ?u), ?p != "U2" => ?p, ?u, max(?v)`)
	want := []string{"U1 A 0.5[A]", "U1 V 5[V]", "U1 ohm 10000[ohm]", "U3 - 2", "U3 V 1.25[V]", "U4 A 5[A]", "U4 V 5[V]"}
	if got := withUnits(rows, "p", "u", "max(v)"); !reflect.DeepEqual(got, want) {
		t.Errorf("max per unit = %v, want %v", got, want)
	}
}

// An aggregate a rule stores keeps only its value, so a goal reading it sees absent with no reason.
func TestWhyIsSetOnAGoalsOwnAggregates(t *testing.T) {
	for _, c := range []struct {
		query string
		cols  []string
		want  []string
	}{
		{`param(?p, ?s, _), ?p = "U2" => ?p, min(?s), sum(?s)`, []string{"min(s)", "sum(s)"}, []string{"-(no numbers) 0"}},
		{`param(?p, _, ?v), ?p = "U9" => max(?v), count(?v)`, []string{"max(v)", "count(v)"}, []string{"-(no rows) 0"}},
		{`peak(?p, max(?v)) :- param(?p, _, ?v); peak("U1", ?m) => ?m`, []string{"m"}, []string{"-"}},
	} {
		if got := withUnits(eval(t, params(), c.query), c.cols...); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %v, want %v", c.query, got, c.want)
		}
	}
	rows := eval(t, params(), `param(?p, _, ?v) => ?p having max(?v) != 99`)
	if got := withUnits(rows, "p"); !reflect.DeepEqual(got, []string{"U1", "U2", "U3", "U4"}) {
		t.Fatalf("having = %v, want every part: absent isn't 99", got)
	}
	for _, r := range rows {
		if len(r.Why) != 0 {
			t.Errorf("%v carries Why %v for a column only having read", r.Bind, r.Why)
		}
	}
	rows = eval(t, params(), `param(?p, _, ?v), ?p = "U2" => ?p, max(?v)`)
	if len(rows) != 1 || rows[0].Why != nil {
		t.Errorf("a row with no absent column has Why %v, want nil", rows)
	}
}

// meters has a column in volts, one in amps and one that is a number without a unit, all by net.
func meters() *ns.MemSource {
	num := func(unit string) ns.ArgType { return ns.ArgType{Type: ns.TypeNumber, Unit: unit} }
	src := ns.NewMemSource().
		DeclareSchema("volts", ns.Schema{Arity: 2, Labels: []string{"net", "v"}, Types: []ns.ArgType{{}, num("V")}}).
		DeclareSchema("amps", ns.Schema{Arity: 2, Labels: []string{"net", "i"}, Types: []ns.ArgType{{}, num("A")}}).
		DeclareSchema("plain", ns.Schema{Arity: 2, Labels: []string{"net", "x"}, Types: []ns.ArgType{{}, num("")}})
	src.Add("volts", ns.Tuple{Vals: []ns.Value{ns.S("VBUS"), ns.NU(5, "V")}}).
		Add("amps", ns.Tuple{Vals: []ns.Value{ns.S("VBUS"), ns.NU(2, "A")}}).
		Add("plain", ns.Tuple{Vals: []ns.Value{ns.S("VBUS"), ns.N(5)}})
	return src
}

func TestAComparisonAcrossDeclaredUnitsIsRefused(t *testing.T) {
	bindAmp := Bind(map[Var][]ns.Value{"lim": {ns.NU(1, "A")}})
	for _, c := range []struct {
		q    string
		opts []Option
		want string
	}{
		{`volts(?n, ?v), amps(?n, ?i), ?v < ?i => ?n`, nil, `?v < ?i in the query compares number[V] with number[A], which never holds`},
		{`volts(?n, ?v), amps(?n, ?i), ?i >= ?v => ?n`, nil, `?i >= ?v in the query compares number[A] with number[V]`},
		{`r(?n) :- volts(?n, ?v), amps(?n, ?i), ?v > ?i; r(?n) => ?n`, nil, `?v > ?i in rule "r" compares number[V] with number[A]`},
		{`a(?n, ?i) :- amps(?n, ?i); volts(?n, ?v), a(?n, ?x), ?v < ?x => ?n`, nil, `?v < ?x in the query compares number[V] with number[A]`},
		{`volts(?n, ?v), ?v < ?lim => ?n`, []Option{bindAmp}, `?v < 1A in the query compares number[V] with number[A]`},
		{`volts(?n, ?v), amps(?n, ?i) => ?n, ?i, max(?v) having max(?v) > ?i`, nil, `max(?v) > ?i in having compares number[V] with number[A]`},
		{`volts(?n, ?v), amps(?n, ?i) => ?n, ?i, avg(?v) having avg(?v) > ?i`, nil, `avg(?v) > ?i in having compares number[V] with number[A]`},
	} {
		q, err := Parse(c.q)
		if err != nil {
			t.Fatal(err)
		}
		_, err = both(q, baseFor(std(meters())), c.opts...)
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), "query: ") {
			t.Errorf("%s: err = %v, want it to say %q", c.q, err, c.want)
		}
		if c.opts != nil {
			// Validate doesn't know the host's value, so its unit is unknown until the Eval.
			if err := ValidateBound(q, std(meters()), "lim"); err != nil {
				t.Errorf("ValidateBound(%s, ?lim) = %v, want it accepted", c.q, err)
			}
		} else if err := Validate(q, std(meters())); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Validate(%s) = %v, want it to say %q", c.q, err, c.want)
		}
	}
	v := std(meters())
	if err := v.AddModule("m", LanguageName, `low(?n) :- volts(?n, ?v), amps(?n, ?i), ?v < ?i;`, ""); err != nil {
		t.Fatal(err)
	}
	if err := v.Check(); err == nil || !strings.Contains(err.Error(), `?v < ?i in rule "m.low" compares number[V] with number[A]`) {
		t.Errorf("Check of a module comparing volts with amps: %v, want it refused", err)
	}
}

// Each side the run can't pin to one declared unit stays polymorphic, as comparisons are at run
// time, and equality isn't dimension-checked at all.
func TestAComparisonWithAnUnknownUnitIsAccepted(t *testing.T) {
	for _, q := range []string{
		`volts(?n, ?v), ?v < 6 => ?n`,
		`volts(?n, ?v), plain(?n, ?x), ?v <= ?x => ?n`,
		`volts(?n, ?v), amps(?n, ?i), ?v = ?i => ?n`,
		`volts(?n, ?v), volts(?m, ?w), ?v >= ?w => ?n`,
		`volts(?n, ?v), plain(?n, ?v), amps(?n, ?i), ?v < ?i => ?n`, // ?v may be plain's, with no unit
		`volts(?n, ?v), amps(?n, ?v), volts(?m, ?w), ?v < ?w => ?n`, // ?v is in volts and in amps, so either unit
		`volts(?n, ?v), amps(?n, ?i) => ?n, ?i, count(?v) having count(?v) > ?i`,
		`r(?n, ?v) :- volts(?n, ?v); r(?n, ?v) :- r(?n, ?w), amps(?n, ?v), ?w > ?v; r(?n, ?v) => ?n`, // the body reads its own relation
	} {
		if _, err := both(mustParse(t, q), baseFor(std(meters()))); err != nil {
			t.Errorf("%s: %v, want it accepted", q, err)
		}
	}
	if rows := eval(t, meters(), `volts(?n, ?v), ?v < 6 => ?n`); col(rows, "n") != "VBUS" {
		t.Errorf("control: volts below 6 = %v, want VBUS, so the accepted cases run", binds(rows))
	}
}

// twoNines is weight 9 on v0 with no unit and on v1 in amps, which is one value twice (#201).
func twoNines() *ns.MemSource {
	src := ns.NewMemSource().Declare("node", "name").Declare("weight", "node", "w")
	for _, n := range []string{"v0", "v1"} {
		src.Add("node", ns.Tuple{Vals: []ns.Value{ns.S(n)}, Cites: []string{"node:" + n}})
	}
	src.Add("weight", ns.Tuple{Vals: []ns.Value{ns.S("v0"), ns.N(9)}}).
		Add("weight", ns.Tuple{Vals: []ns.Value{ns.S("v1"), ns.NU(9, "A")}}).
		Add("weight", ns.Tuple{Vals: []ns.Value{ns.S("v2"), ns.NU(9, "V")}})
	return src
}

// Every derivation, and every row, of one value keeps the annotation spelledBefore puts first (a unit
// before none, then A before V), whichever derivation an evaluator found first, so Witnesses and
// CanonicalCites answer as plain evaluation does (#201). both() compares all of them.
func TestOneValueKeepsOneAnnotationWhicheverDerivationCameFirst(t *testing.T) {
	for _, c := range []struct{ q, col, want string }{
		// a derived tuple reached through weight(v0, 9) and weight(v1, 9 A)
		{`r(?w, ?n, ?n) :- weight(_, ?w), node(?n); r(?w, ?n, ?n) :- weight(?n, ?w); r(?w, ?a, ?n) => ?n, ?w`, "w", "9[A],9[A],9[V]"}, // v2's comes from one derivation
		// rows the projection folds into one: A before V decides two units
		{`weight(?n, ?w) => ?w`, "w", "9[A]"},
		// a group's key
		{`weight(?n, ?w) => ?w, count(?n)`, "w", "9[A]"},
		// a distinct list's value
		{`weight(?n, ?w) => list(distinct ?w)`, "list(distinct w)", `[{"num":9,"unit":"A"}]`},
		// a body aggregate's minimum, whose witness names the value
		{`r0(?w, ?n) :- weight(?n, ?w); node(?n), ?m = min(?w) : { r0(?x, ?n), weight(?y, ?w), ?y != "v2" } => ?n, ?m`, "m", "9[A],9[A]"},
	} {
		for _, opts := range [][]Option{nil, {Witnesses()}} {
			rows, err := both(mustParse(t, c.q), baseFor(std(twoNines())), opts...)
			if err != nil {
				t.Fatalf("%s: %v", c.q, err)
			}
			if got := strings.Join(withUnits(rows, c.col), ","); got != c.want {
				t.Errorf("%s (%d options): %s = %s, want %s", c.q, len(opts), c.col, got, c.want)
			}
		}
	}
}

// One value in volts stored before the same value in amps: A still wins, so arrival doesn't decide
// between two units. And inside a recursive component, a tuple whose annotation improves after a
// consumer read it is read again: copy holds 9 until reach learns 9 A from step (#201).
func TestABetterAnnotationReachesWhatWasDerivedBeforeIt(t *testing.T) {
	src := ns.NewMemSource().Declare("weight", "node", "w")
	src.Add("weight", ns.Tuple{Vals: []ns.Value{ns.S("v0"), ns.N(9)}}).
		Add("weight", ns.Tuple{Vals: []ns.Value{ns.S("v1"), ns.NU(9, "V")}}).
		Add("weight", ns.Tuple{Vals: []ns.Value{ns.S("v2"), ns.NU(9, "A")}})
	for _, c := range []struct{ q, col, want string }{
		{`weight(?n, ?w), ?n != "v0" => ?w`, "w", "9[A]"},
		{`reach(?w) :- weight("v0", ?w); reach(?w) :- step(?w); step(?w) :- copy(?x), weight("v2", ?w), ?x = ?w;
		  copy(?w) :- reach(?w); copy(?w) => ?w`, "w", "9[A]"},
	} {
		for _, opts := range [][]Option{nil, {Witnesses()}} {
			rows, err := both(mustParse(t, c.q), baseFor(std(src)), opts...)
			if err != nil {
				t.Fatalf("%s: %v", c.q, err)
			}
			if got := strings.Join(withUnits(rows, c.col), ","); got != c.want {
				t.Errorf("%s (%d options): %s = %s, want %s", c.q, len(opts), c.col, got, c.want)
			}
		}
	}
}

// rails is the voltage on two nets, in base units as a Source keeps them, and a seed for rules whose
// heads state their own values.
func rails() *ns.MemSource {
	src := ns.NewMemSource().Declare("seed", "value").
		DeclareSchema("rail", ns.Schema{Arity: 2, Labels: []string{"net", "v"}, Types: []ns.ArgType{{}, {Type: ns.TypeNumber, Unit: "V"}}})
	src.Add("seed", ns.Tuple{Vals: []ns.Value{ns.S("s")}})
	src.Add("rail", ns.Tuple{Vals: []ns.Value{ns.S("VBUS"), ns.NU(5, "V")}})
	src.Add("rail", ns.Tuple{Vals: []ns.Value{ns.S("HV"), ns.NU(1000, "V")}})
	return src
}

const prefixedHeads = `limit(1kV) :- seed(_); limit(3.3V) :- seed(_);`

// A unit in a rule head is folded as one in a body is (#224), so 1kV is 1000 V wherever it is read.
func TestAUnitInARuleHeadIsFolded(t *testing.T) {
	for _, c := range []struct{ name, query, col string }{
		{"answers in base units", `limit(?v) => ?v`, "3.3[V],1000[V]"},
		{"a goal in the same prefix finds it", `limit(1kV)`, ""},
		{"a goal in base units finds it", `limit(1000V)`, ""},
		{"compares with volts", `limit(?v), ?v > 5V => ?v`, "1000[V]"},
		{"joins with a Source's volts", `limit(?v), rail(?n, ?v) => ?n`, "HV"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rows := eval(t, rails(), prefixedHeads+c.query)
			if len(rows) != 1 && c.col == "" {
				t.Errorf("%s answers %d rows, want one", c.query, len(rows))
			}
			if got := strings.Join(withUnits(rows, rows0(rows)...), ","); got != c.col {
				t.Errorf("%s = %q, want %q", c.query, got, c.col)
			}
		})
	}
}

// rows0 is the first row's one column name, for a single-column answer.
func rows0(rows []Row) []string {
	for _, r := range rows {
		for v := range r.Bind {
			return []string{string(v)}
		}
	}
	return nil
}

// A module's head is folded before its signature is inferred, so the Domain and unit a host reads
// are the values the module answers.
func TestAUnitInAModuleRuleHeadIsFolded(t *testing.T) {
	v := std(rails())
	if err := v.AddModule("lim", LanguageName, prefixedHeads, ""); err != nil {
		t.Fatal(err)
	}
	e, err := v.Lookup("lim.limit")
	if err != nil {
		t.Fatal(err)
	}
	want := ns.ArgSig{Name: "arg0", ArgType: ns.ArgType{Type: ns.TypeNumber, Unit: "V", Domain: []string{"1000", "3.3"}}, Inferred: true}
	if got := e.Args[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("signature = %#v, want %#v", got, want)
	}
	for q, want := range map[string]string{
		`lim.limit(?v) => ?v`:               "3.3[V] 1000[V]",
		`lim.limit(1kV)`:                    "",
		`lim.limit(?v), ?v > 5V => ?v`:      "1000[V]",
		`lim.limit(?v), rail(?n, ?v) => ?n`: "HV",
	} {
		rows, err := both(mustParse(t, q), baseFor(v))
		if err != nil {
			t.Errorf("%s: %v", q, err)
			continue
		}
		if len(rows) == 0 {
			t.Errorf("%s answers nothing", q)
		}
		if got := strings.Join(withUnits(rows, rows0(rows)...), " "); got != want {
			t.Errorf("%s = %q, want %q", q, got, want)
		}
	}
	kinds, err := ColumnKinds(mustParse(t, `limit(1kV) :- seed(_); limit(?v) => ?v`), v)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds[0]; got.Type != ns.TypeNumber || got.Unit != "V" {
		t.Errorf("a query rule's column kind = %+v, want number in V", got)
	}
}

// levels is a rail's level at each net: n1 at 1 V, n2 at a bare 1, the same number, and n3 at 2 V.
// feeds gives the two-rule relations below a second rule, so they're demanded rather than inlined, and
// want asks for 1 A from the data, so demand is seeded by a variable rather than factored.
func levels() *ns.MemSource {
	src := ns.NewMemSource().DeclareSchema("level", ns.Schema{Arity: 2, Types: []ns.ArgType{{}, {Type: ns.TypeNumber}}}).
		DeclareSchema("feeds", ns.Schema{Arity: 2}).
		DeclareSchema("want", ns.Schema{Arity: 1, Types: []ns.ArgType{{Type: ns.TypeNumber}}})
	src.Add("level", ns.Tuple{Vals: []ns.Value{ns.S("n1"), ns.NU(1, "V")}})
	src.Add("level", ns.Tuple{Vals: []ns.Value{ns.S("n2"), ns.N(1)}})
	src.Add("level", ns.Tuple{Vals: []ns.Value{ns.S("n3"), ns.NU(2, "V")}})
	src.Add("feeds", ns.Tuple{Vals: []ns.Value{ns.S("n0"), ns.S("n3")}})
	src.Add("want", ns.Tuple{Vals: []ns.Value{ns.NU(1, "A")}})
	return src
}

// A goal's 1A finds the levels equal to it, and each answers in the unit the data gave it, as Naive
// answers: the planned SemiNaive used to carry the goal's unit through its demand guard (#227).
func TestADemandedNumberAnswersInTheDataUnit(t *testing.T) {
	for _, c := range []struct{ name, q, guard string }{
		{"factored", `at(?v, ?v) :- level(_, ?v); at(1A, ?w) => ?w`, ":- at:from1(?v), level("},
		{"demanded", `at(?n, ?v, ?v) :- level(?n, ?v); at(?n, ?v, ?v) :- feeds(?n, ?m), level(?m, ?v); want(?x), at(?n, ?x, ?w) => ?n, ?w`, ":- demand:at/fbf(?v), level("},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, r := reported(t, levels(), c.q)
			if !strings.Contains(r.String(), c.guard) {
				t.Fatalf("control: the planned SemiNaive ran no body starting %q, so demand never bound first:\n%s", c.guard, r.String())
			}
			cols := []string{"w"}
			want := []string{"1[V]"} // 1 V and 1 are one value, and the unit ranks first
			if c.name == "demanded" {
				cols, want = []string{"n", "w"}, []string{"n1 1[V]", "n2 1"}
			}
			if got := withUnits(eval(t, levels(), c.q), cols...); !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}

// Arithmetic over a demanded or inlined number reads the data's spelling of it, so 1 V doubles to 2 V
// and a bare 1 to a bare 2, never to the goal's 2 A (#227).
func TestArithmeticOverADemandedNumberReadsTheData(t *testing.T) {
	for _, c := range []struct{ name, q string }{
		{"inlined", `twice(?n, ?v, ?y) :- level(?n, ?v), ?y = ?v * 2; twice(?n, 1A, ?y) => ?n, ?y`},
		{"demanded", `twice(?n, ?v, ?y) :- level(?n, ?v), ?y = ?v * 2; twice(?n, ?v, ?y) :- feeds(?n, ?m), level(?m, ?v), ?y = ?v * 2; twice(?n, 1A, ?y) => ?n, ?y`},
	} {
		t.Run(c.name, func(t *testing.T) {
			want := []string{"n1 2[V]", "n2 2"}
			if got := withUnits(eval(t, levels(), c.q), "n", "y"); !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v, want %v", got, want)
			}
		})
	}
}

// An expression waits for every relation that binds what it reads, wherever it's written: n2's bare 1
// meets n1's 1 V before it's doubled, so the pair answers in volts whichever net came first (#227).
func TestArithmeticWaitsForEveryRelationBindingItsInput(t *testing.T) {
	for _, q := range []string{
		`level(?n, ?v), level(?m, ?v), ?y = ?v * 2 => ?n, ?m, ?y`,
		`level(?n, ?v), ?y = ?v * 2, level(?m, ?v) => ?n, ?m, ?y`,
		`pair(?n, ?m, ?v, ?y) :- level(?n, ?v), ?y = ?v * 2, level(?m, ?v); pair(?n, ?m, _, ?y) => ?n, ?m, ?y`, // inlined
	} {
		want := []string{"n1 n1 2[V]", "n1 n2 2[V]", "n2 n1 2[V]", "n2 n2 2", "n3 n3 4[V]"}
		if got := withUnits(eval(t, levels(), q), "n", "m", "y"); !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: got %v, want %v", q, got, want)
		}
	}
}

// A host generator waits as an expression does for every relation binding a number it reads (#244):
// num.mul written as a call, its arguments typed as numbers, doubles n2's bare 1 only once n1's 1 V
// has met it. control: written as an expression, the same body answers the same.
func TestAGeneratorWaitsForEveryRelationBindingANumberItReads(t *testing.T) {
	want := []string{"n1 n1 2[V]", "n1 n2 2[V]", "n2 n1 2[V]", "n2 n2 2", "n3 n3 4[V]"}
	for _, q := range []string{
		`level(?n, ?v), num.mul(?v, 2, ?y), level(?m, ?v) => ?n, ?m, ?y`,
		`pair(?n, ?m, ?v, ?y) :- level(?n, ?v), num.mul(?v, 2, ?y), level(?m, ?v); pair(?n, ?m, _, ?y) => ?n, ?m, ?y`, // inlined
		`level(?n, ?v), ?y = ?v * 2, level(?m, ?v) => ?n, ?m, ?y`,                                                     // control
	} {
		if got := withUnits(eval(t, levels(), q), "n", "m", "y"); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %v, want %v", q, got, want)
		}
	}
	// control: walk types nothing as a number, so it still runs once per node, before the three tags
	// each node has bind ?x again, as #36 plans it.
	src := line(4).Declare("tag", "n", "t")
	for i := 0; i < 4; i++ {
		for _, tag := range []string{"a", "b", "c"} {
			src.Add("tag", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("v%d", i)), ns.S(tag)}})
		}
	}
	v := std(src)
	calls := walker(t, v, [][]bool{{true, false}})
	rows, err := SemiNaive{}.Eval(bg, mustParse(t, `node(?x), walk(?x, ?y), tag(?x, ?t) => ?x, ?y, ?t`), baseFor(v))
	if err != nil || len(rows) != 3*(3+2+1) || len(*calls) != 4 {
		t.Errorf("control: %d rows, %d walks, %v; want 18 rows and a walk per node (4)", len(rows), len(*calls), err)
	}
}

// A goal's 1A isn't inlined into a comparison it would fail against 2 V, where the rule reads the
// data's 1 V and passes it (#227).
func TestAComparisonReadsTheDataUnitOfAGoalsNumber(t *testing.T) {
	q := `under(?n, ?v) :- level(?n, ?v), ?v < 2V; under(?n, 1A) => ?n`
	if got := withUnits(eval(t, levels(), q), "n"); !reflect.DeepEqual(got, []string{"n1", "n2"}) {
		t.Fatalf("got %v, want n1 and n2", got)
	}
}
