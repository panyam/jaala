package datalog

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/panyam/jaala/ns"
)

// ohmsLaw is two resistors: R1 is 1000 ohm carrying 2 mA on a 5 V rail, R2 470 ohm carrying 10 mA on
// a 3.3 V rail.
func ohmsLaw() *ns.MemSource {
	num := func(unit string) []ns.ArgType { return []ns.ArgType{{}, {Type: ns.TypeNumber, Unit: unit}} }
	src := ns.NewMemSource().
		DeclareSchema("res", ns.Schema{Arity: 2, Labels: []string{"part", "r"}, Types: num("ohm")}).
		DeclareSchema("cur", ns.Schema{Arity: 2, Labels: []string{"part", "i"}, Types: num("A")}).
		DeclareSchema("rail", ns.Schema{Arity: 2, Labels: []string{"part", "v"}, Types: num("V")})
	for _, p := range []struct {
		part    string
		r, i, v float64
	}{{"R1", 1000, 0.002, 5}, {"R2", 470, 0.01, 3.3}} {
		src.Add("res", ns.Tuple{Vals: []ns.Value{ns.S(p.part), ns.NU(p.r, "ohm")}}).
			Add("cur", ns.Tuple{Vals: []ns.Value{ns.S(p.part), ns.NU(p.i, "A")}}).
			Add("rail", ns.Tuple{Vals: []ns.Value{ns.S(p.part), ns.NU(p.v, "V")}})
	}
	return src
}

func TestArithmeticComputesAndKeepsUnits(t *testing.T) {
	for _, c := range []struct {
		name, q string
		cols    []string
		want    []string
	}{
		{"a product's unit is named", `res(?p, ?r), cur(?p, ?i), ?v = ?i * ?r => ?p, ?v`, []string{"p", "v"}, []string{"R1 2[V]", "R2 4.7[V]"}},
		{"solved for the missing factor", `rail(?p, ?v), cur(?p, ?i), ?v = ?i * ?r => ?p, ?r`, []string{"p", "r"}, []string{"R1 2500[ohm]", "R2 330[ohm]"}},
		{"written before what binds it", `?v = ?i * ?r, res(?p, ?r), cur(?p, ?i) => ?p, ?v`, []string{"p", "v"}, []string{"R1 2[V]", "R2 4.7[V]"}},
		{"in a comparison", `res(?p, ?r), cur(?p, ?i), rail(?p, ?v), ?i * ?r > ?v / 2 => ?p`, []string{"p"}, []string{"R2"}},
		{"a unit literal folds its prefix", `res(?p, ?r), ?r < 1kohm => ?p`, []string{"p"}, []string{"R2"}},
		{"a sum of one unit", `rail(?p, ?v), ?x = ?v + 1V => ?p, ?x`, []string{"p", "x"}, []string{"R1 6[V]", "R2 4.3[V]"}},
		{"a sum of two units has no result", `rail(?p, ?v), cur(?p, ?i), ?x = ?v + ?i => ?p`, []string{"p"}, nil},
		{"precedence and parentheses", `res(?p, _), ?a = 1 + 2 * 3, ?b = (1 + 2) * 3, ?p = "R1" => ?a, ?b`, []string{"a", "b"}, []string{"7 9"}},
		{"negation and a call", `res(?p, ?r), ?n = -num.add(?r, 10ohm), ?p = "R2" => ?n`, []string{"n"}, []string{"-480[ohm]"}},
		{"division by zero has no result", `res(?p, ?r), ?x = ?r / 0 => ?p`, []string{"p"}, nil},
		{"a quotient nobody named", `res(?p, ?r), ?x = ?r / 2s, ?p = "R1" => ?x`, []string{"x"}, []string{"500[ohm/s]"}},
		{"the same calls bind once", `rail(?p, ?v), cur(?p, ?i), ?v = ?i * ?r, ?r > 1kohm => ?p`, []string{"p"}, []string{"R1"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, extra := range [][]Option{nil, {Witnesses()}} {
				rows, err := both(mustParse(t, c.q), baseFor(std(ohmsLaw())), extra...)
				if err != nil {
					t.Fatal(err)
				}
				if got := withUnits(rows, c.cols...); !reflect.DeepEqual(got, c.want) {
					t.Errorf("%d extra options: %v, want %v", len(extra), got, c.want)
				}
			}
		})
	}
}

func TestArithmeticRefusals(t *testing.T) {
	for _, c := range []struct{ q, want string }{
		{`n(?x) :- res(_, ?x); n(?y) :- n(?x), ?y = ?x + 1; n(?y) => ?y`, `rule "n" computes with arithmetic and reads n, which reaches it again, so it might never finish`},
		{`res(?p, ?r), ?y = ?r + => ?p`, `ends early`},
		{`res(?p, ?r), ?y = (?r + 1 => ?p`, `unclosed (`},
		{`res(?p, ?r), ?y = ?q * 2 => ?p`, `nothing binds what num.mul needs first`},
	} {
		q, err := Parse(c.q)
		if err == nil {
			// A budget, so a recursion the check misses stops rather than hangs.
			_, err = both(q, baseFor(std(ohmsLaw())), Budget(100_000))
		}
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), "query: ") {
			t.Errorf("%s: err = %v, want it to say %q", c.q, err, c.want)
		}
	}
	// A vocabulary without the standard library's functions says which one arithmetic needs.
	v := ns.MustVocabulary(ohmsLaw())
	if err := v.AddLanguage(Language); err != nil {
		t.Fatal(err)
	}
	if err := Validate(mustParse(t, `res(?p, ?r), ?y = ?r * 2 => ?p`), v); err == nil || !strings.Contains(err.Error(), `arithmetic needs num.mul, which stdlib.Arithmetic registers`) {
		t.Errorf("without stdlib: %v", err)
	}
}

// A goal calling a computing relation twice, the second call's demand read from the first, gives the
// demand rewrite a cycle the program doesn't have. The recursion check reads the program as linked,
// so every evaluator answers it, under CanonicalCites (no supplementary relations) too (#266).
func TestDemandDoesNotMakeAComputingRuleRecursive(t *testing.T) {
	q := `hot(?p) :- res(?p, ?r), cur(?p, ?i), ?r * ?i > 1V; hot(?p) :- rail(?p, ?v), ?v > 4V; hot("R1"), hot("R1"), res("R1", ?r) => ?r`
	for _, opts := range [][]Option{nil, {CanonicalCites()}} {
		rows, err := both(mustParse(t, q), baseFor(std(ohmsLaw())), opts...)
		if err != nil || len(rows) != 1 {
			t.Errorf("%d options: %v, %v; want R1's resistance", len(opts), rows, err)
		}
	}
}

// A function runs backwards only for what nothing else in its body binds (#273). Demanded, r's ?z is
// bound when its body runs, and the plan puts num.mul(?e, 0, ?z) first, whose mode from its second
// argument and its result is satisfied: solving ?e * 0 = 0 for ?e answers nothing, where the product
// of the ?e that num.add computes is 0. The call waits for num.add, as Naive's written order has it.
func TestAFunctionWaitsForWhatItWouldSolveFor(t *testing.T) {
	src := ns.NewMemSource().Declare("weight", "n", "w")
	for i, w := range []float64{9, 4, 0} {
		src.Add("weight", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("v%d", i)), ns.N(w)}})
	}
	q := `r(?z, ?n) :- weight(?n, ?w), ?z = (?w + 4) * 0; weight(?m, ?v), r(?v, ?m) => ?m`
	rows, err := both(mustParse(t, q), baseFor(std(src)))
	if err != nil || col(rows, "m") != "v2" {
		t.Fatalf("%v, %v; want v2, whose weight is 0", col(rows, "m"), err)
	}
	_, r := reported(t, src, q)
	var ran string
	for _, b := range r.Rules {
		if b.Relation == "r" {
			ran = b.Ran
		}
	}
	if i, j := strings.Index(ran, "num.mul("), strings.Index(ran, "num.add("); i < 0 || j < 0 || i > j {
		t.Errorf("control: r ran as %q, so num.mul doesn't come first and this proves nothing", ran)
	}
}

// A call waits for what would bind its operand only while that binder could still run (FuzzEval's
// 159b75a6). Solving ?w = ?x * 2 + 1 backwards, num.add(?e, 1, ?w) would wait for the num.mul that
// computes ?e, and num.mul can't run until num.add has given it ?e. Each waited on the other, so
// they ran in the order they were left in: Naive's written order called num.mul first, which errs,
// and the plan called num.add first and answered.
func TestTwoFunctionsWaitingOnEachOtherRunInTheOnlyOrderThatCan(t *testing.T) {
	src := ns.NewMemSource().Declare("weight", "n", "w")
	for i, w := range []float64{9, 4, 0} {
		src.Add("weight", ns.Tuple{Vals: []ns.Value{ns.S(fmt.Sprintf("v%d", i)), ns.N(w)}})
	}
	q := mustParse(t, `weight(?n, ?w), ?w = ?x * 2 + 1 => ?n, ?x`)
	if _, err := (Naive{}).Eval(bg, q, baseFor(std(src))); err != nil {
		t.Fatalf("Naive: %v; want ?x solved after num.add gives num.mul its result", err)
	}
	rows, err := both(q, baseFor(std(src)))
	if err != nil || col(rows, "x") != "-0.5,1.5,4" {
		t.Fatalf("%v, %v; want -0.5,1.5,4", col(rows, "x"), err)
	}
	lowered, err := lowerExpressions(q, std(src))
	if err != nil {
		t.Fatal(err)
	}
	if body := lowered.Goal.Literals; len(body) < 3 || body[1].Pos == nil || body[1].Pos.Relation != "num.mul" {
		t.Errorf("control: the goal lowers to %v, so num.mul isn't written before num.add and this proves nothing", body)
	}
}

func TestAnExpressionPrintsAsItParses(t *testing.T) {
	for _, text := range []string{
		`res(?p, ?r), ?y = (?r + 1) * -?r, ?z = num.add(?r, 2) / 3, ?r < 3.3V => ?y`,
		`res(?p, ?r), ?y = ?r - (1 - 2) => ?y`,
	} {
		q := mustParse(t, text)
		again, err := Parse(q.Goal.String() + " => ?y")
		if err != nil {
			t.Fatalf("%s printed as %s, which doesn't parse: %v", text, q.Goal, err)
		}
		if !reflect.DeepEqual(again.Goal, q.Goal) {
			t.Errorf("%s printed as %s, which parses differently", text, q.Goal)
		}
	}
}

// Arithmetic in a rule head or the projection (#206) computes into a column as ?y = expr in the body
// would. A projected expression's column is its text without the ?s, in the answer and in Columns. A
// head keeps its written text in a witness, whether the relation is inlined (one rule) or not (two).
func TestArithmeticInAHeadOrTheProjection(t *testing.T) {
	for _, c := range []struct {
		name, q string
		cols    []string
		want    []string
	}{
		{"projected", `res(?p, ?r), cur(?p, ?i) => ?p, ?r * ?i`, []string{"p", "r * i"}, []string{"R1 2[V]", "R2 4.7[V]"}},
		{"projected, with a call and a negation", `res(?p, ?r) => ?p, -?r, num.add(?r, 1ohm)`, []string{"p", "-r", "num.add(r, 1ohm)"}, []string{"R1 -1000[ohm] 1001[ohm]", "R2 -470[ohm] 471[ohm]"}},
		{"projected beside an aggregate", `res(?p, ?r) => ?r * 2, count(?p)`, []string{"r * 2", "count(p)"}, []string{"940[ohm] 1", "2000[ohm] 1"}},
		{"in a head", `v(?p, ?r * ?i) :- res(?p, ?r), cur(?p, ?i); v(?p, ?x) => ?p, ?x`, []string{"p", "x"}, []string{"R1 2[V]", "R2 4.7[V]"}},
		{"in one of two rules", `v(?p, ?r * ?i) :- res(?p, ?r), cur(?p, ?i); v(?p, ?x) :- rail(?p, ?x); v(?p, ?x) => ?p, ?x`, []string{"p", "x"}, []string{"R1 2[V]", "R1 5[V]", "R2 3.3[V]", "R2 4.7[V]"}},
		{"in a head and the projection", `v(?p, ?r * ?i) :- res(?p, ?r), cur(?p, ?i); v(?p, ?x) => ?p, ?x / 2`, []string{"p", "x / 2"}, []string{"R1 1[V]", "R2 2.35[V]"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			q := mustParse(t, c.q)
			if got := q.Columns(); !reflect.DeepEqual(got, toVars(c.cols)) {
				t.Errorf("Columns() = %q, want %q", got, c.cols)
			}
			if err := Validate(q, std(ohmsLaw())); err != nil {
				t.Errorf("Validate: %v", err)
			}
			for _, extra := range [][]Option{nil, {Witnesses()}} {
				rows, err := both(q, baseFor(std(ohmsLaw())), extra...)
				if err != nil {
					t.Fatal(err)
				}
				if got := withUnits(rows, c.cols...); !reflect.DeepEqual(got, c.want) {
					t.Errorf("%d extra options: %v, want %v", len(extra), got, c.want)
				}
				if extra != nil && strings.HasPrefix(c.q, "v(") {
					if w := rows[0].Witness[0]; w.Relation != "v" || !strings.Contains(w.Rule, "v(?p, ?r * ?i) :- ") {
						t.Errorf("witness %s by %q, want v's rule as written", w.Relation, w.Rule)
					}
				}
			}
		})
	}
	for _, c := range []struct{ q, want string }{
		{`res(?p, ?r) => ?p, ?r * ?q`, `nothing binds what num.mul needs first`},
		// ?q is unbound, so num.mul solves it from the column: v takes its caller's value (#200).
		{`v(?p, ?r * ?q) :- res(?p, ?r); v(?p, ?x) => ?x`, `v's ?r * q is an input argument, and the call in the query doesn't bind it`},
		{`res(?p, ?r) => ?p, avg(?r)`, `unknown aggregate "avg"`},
	} {
		if _, err := both(mustParse(t, c.q), baseFor(std(ohmsLaw()))); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want it to say %q", c.q, err, c.want)
		}
	}
	// A computing head prints as it parses.
	text := `v(?p, (?r + 1) * num.add(?i, 2)) :- res(?p, ?r), cur(?p, ?i)`
	r := mustParse(t, text+`; v(?p, ?x) => ?x`).Rules[0]
	again := mustParse(t, r.String()+`; v(?p, ?x) => ?x`).Rules[0]
	if !reflect.DeepEqual(again.Head, r.Head) {
		t.Errorf("%s printed as %s, which parses differently", text, r)
	}
}

func toVars(ss []string) []Var {
	out := make([]Var, len(ss))
	for i, s := range ss {
		out[i] = Var(s)
	}
	return out
}

const powerFn = `power(?v, ?i, ?p) :- ?p = ?v * ?i;`

func TestARuleIsAFunctionAnExpressionCalls(t *testing.T) {
	for _, c := range []struct {
		name, q string
		cols    []string
		want    []string
	}{
		{"called in an expression", powerFn + `rail(?x, ?v), cur(?x, ?i), ?w = power(?v, ?i) => ?x, ?w`, []string{"x", "w"}, []string{"R1 0.01[W]", "R2 0.033[W]"}},
		{"called as a relation", powerFn + `rail(?x, ?v), cur(?x, ?i), power(?v, ?i, ?w) => ?x, ?w`, []string{"x", "w"}, []string{"R1 0.01[W]", "R2 0.033[W]"}},
		{"written before what binds it", powerFn + `?w = power(?v, ?i), rail(?x, ?v), cur(?x, ?i) => ?x, ?w`, []string{"x", "w"}, []string{"R1 0.01[W]", "R2 0.033[W]"}},
		{"in a comparison", powerFn + `rail(?x, ?v), cur(?x, ?i), power(?v, ?i) > 0.02W => ?x`, []string{"x"}, []string{"R2"}},
		{"over an expression", powerFn + `res(?x, ?r), cur(?x, ?i), ?w = power(?i * ?r, ?i) => ?x, ?w`, []string{"x", "w"}, []string{"R1 0.004[W]", "R2 0.047[W]"}},
		{"calling another", powerFn + `heat(?i, ?r, ?w) :- ?w = power(?i * ?r, ?i); res(?x, ?r), cur(?x, ?i), ?w = heat(?i, ?r) => ?x, ?w`, []string{"x", "w"}, []string{"R1 0.004[W]", "R2 0.047[W]"}},
		// An expression's call fills the relation's last argument, so one body is two functions.
		{"solved for its last argument", `half(?x, ?h) :- ?x = ?h * 2; rail(?p, ?v), ?h = half(?v) => ?p, ?h`, []string{"p", "h"}, []string{"R1 2.5[V]", "R2 1.65[V]"}},
		{"the same body, its head the other way", `double(?h, ?x) :- ?x = ?h * 2; rail(?p, ?v), ?d = double(?v) => ?p, ?d`, []string{"p", "d"}, []string{"R1 10[V]", "R2 6.6[V]"}},
		{"a filter's argument", `named(?x) :- str.contains(?x, "R"); rail(?x, _), named(?x) => ?x`, []string{"x"}, []string{"R1", "R2"}},
		{"a generator called directly", `dbl(?a, ?b) :- num.mul(?a, 2, ?b); rail(?p, ?v), dbl(?v, ?d) => ?p, ?d`, []string{"p", "d"}, []string{"R1 10[V]", "R2 6.6[V]"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, extra := range [][]Option{nil, {Witnesses()}} {
				rows, err := both(mustParse(t, c.q), baseFor(std(ohmsLaw())), extra...)
				if err != nil {
					t.Fatal(err)
				}
				if got := withUnits(rows, c.cols...); !reflect.DeepEqual(got, c.want) {
					t.Errorf("%d extra options: %v, want %v", len(extra), got, c.want)
				}
			}
		})
	}
}

func TestAFunctionsCallMustBindItsInputs(t *testing.T) {
	for _, c := range []struct{ q, want string }{
		{powerFn + `rail(?x, ?v), ?w = power(?q, ?v) => ?x, ?w`, `power's ?v is an input argument, and the call in the query doesn't bind it`},
		{powerFn + `rail(?x, ?v), ?w = power(?v, ?q) => ?x, ?w`, `power's ?i is an input argument, and the call in the query doesn't bind it`},
		{powerFn + `rail(?x, ?v), not power(?v, 1A, 5W) => ?x`, `power takes an argument from its caller, so the query can't negate it`},
		// A relation of two rules isn't inlined, so it keeps the mode error.
		{powerFn + `power(?v, ?i, ?p) :- rail(_, ?v), cur(_, ?i), ?p = ?v * ?i; rail(?x, ?v), cur(?x, ?i), ?w = power(?v, ?i) => ?x`, `rule "power" calls num.mul(?v, ?i, ?p), and nothing binds what num.mul needs first`},
	} {
		q := mustParse(t, c.q)
		if _, err := both(q, baseFor(std(ohmsLaw()))); err == nil || !strings.Contains(err.Error(), c.want) || !strings.HasPrefix(err.Error(), "query: ") {
			t.Errorf("%s: err = %v, want it to say %q", c.q, err, c.want)
		}
		if err := Validate(q, std(ohmsLaw())); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("Validate(%s) = %v, want it to say %q", c.q, err, c.want)
		}
	}
}

func TestAModuleExportsAFunction(t *testing.T) {
	v := std(ohmsLaw())
	if err := v.AddModule("elec", LanguageName, powerFn, ""); err != nil {
		t.Fatal(err)
	}
	if err := v.Check(); err != nil {
		t.Fatalf("Check: %v", err)
	}
	e, err := v.Lookup("elec.power")
	if err != nil {
		t.Fatal(err)
	}
	var inputs []string
	for _, a := range e.Args {
		if a.Input {
			inputs = append(inputs, a.Name)
		}
	}
	if !reflect.DeepEqual(inputs, []string{"v", "i"}) {
		t.Errorf("elec.power's inputs = %v, want [v i]", inputs)
	}
	rows, err := both(mustParse(t, `rail(?x, ?v), cur(?x, ?i), ?w = elec.power(?v, ?i) => ?x, ?w`), baseFor(v))
	if got := withUnits(rows, "x", "w"); err != nil || !reflect.DeepEqual(got, []string{"R1 0.01[W]", "R2 0.033[W]"}) {
		t.Errorf("elec.power = %v, %v", got, err)
	}
}
